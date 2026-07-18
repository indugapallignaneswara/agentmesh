// Package iam is AgentMesh's Agent-IAM: an OAuth 2.1 authorization server that
// issues access tokens for agents. Its access tokens are ordinary RS256 JWTs
// validated by the UNCHANGED AgentMesh resource server (internal/auth), so the
// verification path is shared and there is exactly one place that trusts a
// token. See docs/agentiam.md for the design.
//
// This file is the signing-key layer: an RSA keypair with a stable key id
// (kid), plus the JWKS document the resource server fetches to validate
// signatures. The JWKS shape is dictated by internal/auth/oauth.go's jwk
// parser: {kty:"RSA", kid, n, e} with base64url n/e.
package iam

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
)

// SigningKey is an RSA keypair used to sign access tokens. Kid is derived from
// the public key, so the same key always advertises the same id across restarts.
type SigningKey struct {
	Kid     string
	private *rsa.PrivateKey
}

// GenerateSigningKey creates a fresh RSA-2048 signing key. Used for the
// zero-config demo; production loads a key from PEM so the kid is stable and the
// private key is managed as a secret.
func GenerateSigningKey() (*SigningKey, error) {
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	return newSigningKey(pk), nil
}

// LoadSigningKeyPEM parses a PKCS#1 or PKCS#8 RSA private key from PEM bytes.
func LoadSigningKeyPEM(pemBytes []byte) (*SigningKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found in signing key")
	}
	var pk *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		var err error
		if pk, err = x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
			return nil, fmt.Errorf("parse PKCS#1 key: %w", err)
		}
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS#8 key: %w", err)
		}
		var ok bool
		if pk, ok = parsed.(*rsa.PrivateKey); !ok {
			return nil, fmt.Errorf("signing key is %T, want RSA", parsed)
		}
	default:
		return nil, fmt.Errorf("unexpected PEM block type %q", block.Type)
	}
	if err := pk.Validate(); err != nil {
		return nil, fmt.Errorf("invalid RSA key: %w", err)
	}
	return newSigningKey(pk), nil
}

func newSigningKey(pk *rsa.PrivateKey) *SigningKey {
	return &SigningKey{Kid: deriveKid(&pk.PublicKey), private: pk}
}

// deriveKid is a stable, collision-resistant id for a public key: the first 16
// bytes of the SHA-256 of its DER encoding, base64url. Same key → same kid, so
// tokens remain verifiable across restarts and the JWKS lookup is stable.
func deriveKid(pub *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		// MarshalPKIXPublicKey only errors on unsupported key types; RSA is
		// always supported, so this is unreachable in practice.
		sum := sha256.Sum256(pub.N.Bytes())
		return base64.RawURLEncoding.EncodeToString(sum[:16])
	}
	sum := sha256.Sum256(der)
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// jwk is one JSON Web Key in the exact shape internal/auth/oauth.go parses.
type jwk struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// jwks is a JSON Web Key Set.
type jwks struct {
	Keys []jwk `json:"keys"`
}

// publicJWK renders the key's PUBLIC half as a JWK. Only n and e are exported;
// the private key never leaves the process.
func (k *SigningKey) publicJWK() jwk {
	pub := k.private.PublicKey
	eBytes := big.NewInt(int64(pub.E)).Bytes()
	return jwk{
		Kty: "RSA",
		Use: "sig",
		Alg: "RS256",
		Kid: k.Kid,
		N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(eBytes),
	}
}

// KeySet holds the active signing key plus any retired-but-still-valid public
// keys. It signs with the active key and publishes every key in the JWKS, so a
// token stays verifiable until it expires even after the key that signed it is
// rotated out. Concurrency: the set is built at startup and read-only
// thereafter (rotation replaces the whole set), so no locking is needed.
type KeySet struct {
	active  *SigningKey
	retired []*SigningKey // public keys kept in the JWKS during rotation
}

// NewKeySet builds a key set with a single active key.
func NewKeySet(active *SigningKey) *KeySet {
	return &KeySet{active: active}
}

// Active returns the key new tokens are signed with.
func (ks *KeySet) Active() *SigningKey { return ks.active }

// JWKS renders the public JWK set: the active key plus every retired key still
// being published for verification.
func (ks *KeySet) JWKS() jwks {
	out := jwks{Keys: []jwk{ks.active.publicJWK()}}
	for _, k := range ks.retired {
		out.Keys = append(out.Keys, k.publicJWK())
	}
	return out
}

// JWKSHandler serves GET /.well-known/jwks.json.
func (ks *KeySet) JWKSHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_ = json.NewEncoder(w).Encode(ks.JWKS())
	}
}
