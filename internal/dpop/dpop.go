// Package dpop implements RFC 9449 DPoP (Demonstrating Proof of Possession):
// sender-constrained tokens for agents. A DPoP-bound access token carries a
// cnf.jkt thumbprint of the client's public key; every use of the token must
// be accompanied by a fresh DPoP proof JWT signed by the matching private key.
//
// Why this matters for AGENTS specifically: an agent's bearer token lives in
// its context window and environment, where a prompt-injection attack can make
// the agent leak it. A leaked DPoP-bound token is INERT — the attacker does not
// hold the private key, which never leaves the agent's runtime, so it cannot
// mint the proof the resource server demands. This is the one threat no
// workforce IdP even has a name for (docs/agentiam-standards.md T1).
//
// The package is shared by both sides of the loop: the authorization server
// (internal/iam) calls Verify to compute the cnf.jkt it binds into a token, and
// the resource server (internal/auth) calls Verify per request to prove the
// caller still holds the key. One verification path, no drift.
//
// SPINE NOTE: the API below is locked. verify.go / proof.go fill in the bodies.
package dpop

import (
	"crypto"
	"errors"
	"sync"
	"time"
)

// ErrInvalidProof is returned for any bad proof: malformed, wrong typ, weak or
// disallowed alg, bad signature, stale iat, method/URI mismatch, ath mismatch,
// or replay. One sentinel — no oracle about which check failed.
var ErrInvalidProof = errors.New("invalid DPoP proof")

// Proof is a validated DPoP proof.
type Proof struct {
	// JKT is the RFC 7638 SHA-256 thumbprint (base64url) of the proof's public
	// key. The issuer binds it into the token as cnf.jkt; the resource server
	// checks the token's cnf.jkt equals this.
	JKT string
	// JTI is the proof's unique id (replay key).
	JTI string
	// HTM / HTU are the HTTP method and URI the proof was bound to.
	HTM string
	HTU string
	// IAT is the proof's issued-at.
	IAT time.Time
	// ATH is the access-token hash claim, present on resource-access proofs.
	ATH string
}

// ReplayGuard records proof jti values to reject replays within the acceptance
// window. Implementations must be safe for concurrent use.
type ReplayGuard interface {
	// SeenBefore reports whether jti was already recorded; otherwise it records
	// jti (expiring at now+window) and returns false. A true result means replay.
	SeenBefore(jti string, now time.Time) bool
}

// Params configures a single Verify call.
type Params struct {
	// HTM and HTU are the expected HTTP method and URI. HTU is compared after
	// normalization (scheme+host lowercased, query and fragment stripped).
	HTM string
	HTU string
	// Now overrides the clock (tests). Defaults to time.Now.
	Now func() time.Time
	// Leeway is the accepted age window for the proof's iat, in both
	// directions. Defaults to DefaultLeeway.
	Leeway time.Duration
	// Replay, when set, rejects a proof whose jti was already seen.
	Replay ReplayGuard
	// ExpectedATH, when non-empty, requires the proof's ath claim to equal it
	// (resource-server access-token binding). Empty at the token endpoint,
	// where no access token exists yet.
	ExpectedATH string
}

// DefaultLeeway is the iat acceptance window when Params.Leeway is unset.
// DPoP proofs are meant to be freshly minted per request; a minute tolerates
// clock skew without opening a meaningful replay window (a Replay guard closes
// the rest).
const DefaultLeeway = 60 * time.Second

// Verify parses and validates a DPoP proof JWT against p, returning the
// validated Proof (notably its JKT) or ErrInvalidProof. Checks: header
// typ=="dpop+jwt"; alg in the asymmetric allowlist (ES256/384/512, RS256/384/512
// — never none/HMAC); an embedded public "jwk"; signature by that jwk; htm/htu
// match; iat within Leeway; ath == ExpectedATH when ExpectedATH set; jti unseen
// when Replay set.
func Verify(proofJWT string, p Params) (Proof, error) {
	return verifyProof(proofJWT, p) // implemented in verify.go
}

// AccessTokenHash computes the RFC 9449 `ath` value for an access token:
// base64url(SHA-256(token)), no padding.
func AccessTokenHash(accessToken string) string {
	return accessTokenHash(accessToken) // implemented in verify.go
}

// NewProof builds and signs a DPoP proof JWT for (htm, htu), optionally binding
// an access token via its hash (ath). key is the client's PRIVATE key
// (*ecdsa.PrivateKey or *rsa.PrivateKey); the matching public key is embedded
// as the proof's jwk. Used by DPoP clients and by tests.
func NewProof(key crypto.Signer, htm, htu, accessToken string, iat time.Time) (string, error) {
	return newProof(key, htm, htu, accessToken, iat) // implemented in proof.go
}

// MemReplayGuard is an in-memory ReplayGuard: a map of jti→expiry with lazy
// sweeping. Adequate for a single process; a shared store backs a multi-replica
// deployment later.
type MemReplayGuard struct {
	mu     sync.Mutex
	window time.Duration
	seen   map[string]time.Time
	nextGC time.Time
}

// NewMemReplayGuard returns a guard that remembers a jti for window (use the
// same value as, or a little longer than, Params.Leeway).
func NewMemReplayGuard(window time.Duration) *MemReplayGuard {
	return &MemReplayGuard{window: window, seen: map[string]time.Time{}}
}

// SeenBefore implements ReplayGuard.
func (g *MemReplayGuard) SeenBefore(jti string, now time.Time) bool {
	return memReplaySeenBefore(g, jti, now) // implemented in verify.go
}
