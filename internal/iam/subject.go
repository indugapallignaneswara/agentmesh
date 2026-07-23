package iam

// Subject-token verification for RFC 8693 token exchange (delegation).
//
// A delegation exchange presents a subject_token: a JWT from a TRUSTED HUMAN
// IdP (Okta, Entra, Keycloak, ...) proving which human is delegating. The
// trust registry says which external issuers may act as subject sources and
// where their JWKS lives. Verification here is deliberately independent of
// internal/auth (the mesh's resource server): Agent-IAM must stand alone.
//
// SPINE NOTE: the API below is locked; the implementation is filled in by the
// delegation build (see docs/agentiam-standards.md §3).

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// TrustedIssuer is one external human IdP allowed as a delegation subject
// source.
type TrustedIssuer struct {
	// Issuer is the exact `iss` value the subject token must carry.
	Issuer string
	// JWKSURL is where that issuer publishes its signing keys.
	JWKSURL string
}

// SubjectClaims is the verified identity a subject token proved.
type SubjectClaims struct {
	Issuer  string
	Subject string
	Expiry  time.Time
	// Raw carries all claims for policy use (audit, future may_act checks).
	Raw map[string]any
}

// ErrSubjectRejected is returned for any invalid subject token: untrusted
// issuer, bad signature, expired, malformed. One sentinel — no oracle.
var ErrSubjectRejected = errors.New("subject token rejected")

// TrustRegistry validates subject tokens against the configured issuers.
type TrustRegistry struct {
	issuers map[string]TrustedIssuer
	client  *http.Client
	now     func() time.Time

	keys keyCache
}

// NewTrustRegistry builds a registry. A nil httpClient gets a 10s-timeout
// default. An empty issuer list is valid: every Verify fails (delegation off).
func NewTrustRegistry(issuers []TrustedIssuer, httpClient *http.Client) *TrustRegistry {
	m := make(map[string]TrustedIssuer, len(issuers))
	for _, is := range issuers {
		m[is.Issuer] = is
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &TrustRegistry{issuers: m, client: httpClient, now: func() time.Time { return time.Now().UTC() }}
}

// Verify checks a subject token: trusted issuer, valid signature against that
// issuer's JWKS (RS256/ES256 family only), exp/nbf with 60s leeway, non-empty
// sub. Returns the proven claims or ErrSubjectRejected.
func (r *TrustRegistry) Verify(ctx context.Context, token string) (SubjectClaims, error) {
	return verifySubject(ctx, r, token) // implemented in subject_verify.go
}

// ParseTrustedIssuers parses the AGENTIAM_SUBJECT_ISSUERS format: a
// comma-separated list of issuer=jwks_url pairs, e.g.
// "https://idp.corp=https://idp.corp/jwks,https://sso.x=https://sso.x/keys".
func ParseTrustedIssuers(s string) ([]TrustedIssuer, error) {
	return parseTrustedIssuers(s) // implemented in subject_verify.go
}

// keyCache is the per-issuer JWKS cache; shape owned by the implementation.
type keyCache struct {
	byIssuer map[string]issuerKeys
}

type issuerKeys struct {
	keys      map[string]any // kid -> crypto.PublicKey
	fetchedAt time.Time
}
