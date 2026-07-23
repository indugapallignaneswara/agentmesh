package iam

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Claims are the registered + AgentMesh-specific claims carried by an access
// token. The field set is dictated by what internal/auth/oauth.go reads:
// iss, aud, sub, the workspace claim, the kind claim, and exp/nbf. Everything
// here maps straight onto a Principal on the resource-server side.
type Claims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Audience  string `json:"aud"`       // single resource URI (RFC 8707)
	Workspace string `json:"workspace"` // → Principal.Workspace
	Kind      string `json:"kind"`      // → Principal.Kind (agent/human)
	// ClientID identifies WHICH registered client obtained the token — required
	// by RFC 9068 §2.2 and the audit answer to "which credential minted this".
	ClientID string `json:"client_id"`
	Scope    string `json:"scope,omitempty"`
	// BudgetDailyBytes is the Agent-IAM budget claim: a per-principal daily
	// coordination-byte cap the resource server enforces (0 = omitted).
	BudgetDailyBytes int64 `json:"budget_daily_bytes,omitempty"`
	// Act is the RFC 8693 §4.1 actor claim, present only on delegated tokens
	// (token-exchange grant). `sub` stays the AGENT doing the work; Act names
	// the HUMAN (and their IdP) on whose behalf it acts — the audit answer to
	// "which human authorized this".
	Act *Actor `json:"act,omitempty"`
	// Cnf is the RFC 7800 confirmation claim, present only on DPoP-bound
	// (sender-constrained, RFC 9449) tokens. Its jkt member is the thumbprint of
	// the client's DPoP public key; the resource server requires a matching DPoP
	// proof on every request that presents the token. Omitted (omitempty) on
	// plain bearer tokens, which stay byte-identical to pre-DPoP issuance.
	Cnf       *Confirmation `json:"cnf,omitempty"`
	IssuedAt  int64         `json:"iat"`
	NotBefore int64         `json:"nbf"`
	Expiry    int64         `json:"exp"`
	JTI       string        `json:"jti"`
}

// Actor is the RFC 8693 §4.1 `act` claim value: the delegating party a
// token-exchange grant proved via its subject_token.
type Actor struct {
	// Subject is the delegating human's stable identifier at their IdP.
	Subject string `json:"sub"`
	// Issuer is the trusted IdP that attested the human (subject token `iss`).
	Issuer string `json:"iss,omitempty"`
}

// Confirmation is the RFC 7800 `cnf` claim value. For DPoP (RFC 9449 §6.1)
// the only member is jkt: the base64url RFC 7638 SHA-256 thumbprint of the
// JWK the client proved possession of at the token endpoint.
type Confirmation struct {
	JKT string `json:"jkt"`
}

// jwtHeader is the JOSE header. typ "at+jwt" (RFC 9068) marks this an OAuth 2.0
// access token; alg/kid let the resource server select the verification key.
type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// Sign produces a compact RS256 JWS for the claims, signed with the key set's
// active key. The output validates against internal/auth's JWTAuthenticator
// when it is configured with this issuer, this audience, and this JWKS.
func (ks *KeySet) Sign(c Claims) (string, error) {
	key := ks.active
	hdr := jwtHeader{Alg: "RS256", Typ: "at+jwt", Kid: key.Kid}

	hb, err := json.Marshal(hdr)
	if err != nil {
		return "", fmt.Errorf("marshal header: %w", err)
	}
	cb, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}
	signingInput := b64u(hb) + "." + b64u(cb)

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key.private, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signingInput + "." + b64u(sig), nil
}

// newJTI returns a random 128-bit token id (base64url), so every issued token is
// individually identifiable for future revocation/audit.
func newJTI() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("jti entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// normalizeScopes trims and de-duplicates a space-delimited scope string,
// preserving order of first appearance.
func normalizeScopes(scope string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range strings.Fields(scope) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
