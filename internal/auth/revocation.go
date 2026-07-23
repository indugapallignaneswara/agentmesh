package auth

// Resource-server side of token revocation (P3). The mesh consults a
// RevocationChecker for every JWT it validates; a token whose jti has been
// revoked at the authorization server is rejected even though its signature and
// expiry are still valid. The concrete PollingRevocationChecker keeps a cached
// denylist fresh by polling the authorization server's /revocations feed, so
// checking is a local map lookup — no per-request network call.
//
// SPINE NOTE: the RevocationChecker interface is locked. PollingRevocationChecker
// is implemented by the revocation build (revocation_poll.go).

// RevocationChecker reports whether a token (by jti) has been revoked. A nil
// checker means revocation is not configured — tokens are bounded by their TTL
// alone, which is the JIT default.
type RevocationChecker interface {
	IsRevoked(jti string) bool
}
