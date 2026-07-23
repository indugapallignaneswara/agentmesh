package iam

// Token revocation (P3): self-contained JWTs cannot be un-issued, so revocation
// is a jti denylist. The default posture is JIT — short-lived tokens fetched
// per task, so most credentials expire before anyone would revoke them — with
// this denylist for the tail: "kill THIS token now" and "this token's expiry is
// still minutes away".
//
// Cross-process design: the authorization server (this package) owns the
// denylist and exposes it two ways — RFC 7009 POST /revoke to add to it, and
// GET /revocations to publish the currently-active entries. A resource server
// (the mesh, internal/auth) polls /revocations and caches it, so a revoked
// token stops working within one poll interval without a per-request network
// call. The wire format of the feed is LOCKED (see RevocationFeed).
//
// SPINE NOTE: the RevocationStore interface, the Revocation type, and the
// RevocationFeed wire shape below are locked. Implementations and the HTTP
// handlers are filled in by the revocation build.

import (
	"context"
	"time"
)

// Revocation is one revoked token: its jti and the token's own expiry. An
// entry need only be remembered until Expiry — after that the token is invalid
// on its own and the denylist can forget it.
type Revocation struct {
	JTI    string    `json:"jti"`
	Expiry time.Time `json:"exp"`
}

// RevocationStore persists the jti denylist. Two implementations (memory +
// Postgres) behind one contract, like every other store in the project.
type RevocationStore interface {
	// Revoke records jti as revoked until exp. Idempotent: revoking the same
	// jti twice is not an error.
	Revoke(ctx context.Context, jti string, exp time.Time) error
	// IsRevoked reports whether jti is currently revoked (and not past exp).
	IsRevoked(ctx context.Context, jti string, now time.Time) (bool, error)
	// ListActive returns every revocation not yet past its expiry at now —
	// the payload of the /revocations feed. Expired entries are omitted (and
	// may be pruned).
	ListActive(ctx context.Context, now time.Time) ([]Revocation, error)
}

// RevocationFeed is the LOCKED JSON body of GET /revocations. A resource server
// polls it and denies any token whose jti appears here. `as_of` lets a poller
// reason about staleness; `entries` are the currently-active revocations.
type RevocationFeed struct {
	AsOf    time.Time    `json:"as_of"`
	Entries []Revocation `json:"entries"`
}
