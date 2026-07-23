package auth

import (
	"context"
	"fmt"

	"github.com/indugapallignaneswara/agentmesh/internal/dpop"
)

// DPoP (RFC 9449) resource-server enforcement. A sender-constrained token
// carries a cnf.jkt thumbprint; every request presenting it must also carry a
// fresh DPoP proof signed by the matching private key. This file is the glue
// between the HTTP layer (which has the request's method/URI and the DPoP proof
// header) and the JWT authenticator (which has the token and its cnf claim).

// dpopRequest carries the per-request DPoP material the middleware extracts and
// the authenticator consumes. htm/htu are the request's method and absolute
// URI, which the proof must be bound to.
type dpopRequest struct {
	proof string
	htm   string
	htu   string
}

type dpopCtxKey struct{}

// WithDPoP stashes the request's DPoP proof and its method/URI in the context
// so the authenticator can verify a sender-constrained token. The middleware
// sets this; a request with no DPoP proof simply doesn't.
func WithDPoP(ctx context.Context, proof, htm, htu string) context.Context {
	return context.WithValue(ctx, dpopCtxKey{}, dpopRequest{proof: proof, htm: htm, htu: htu})
}

func dpopFromContext(ctx context.Context) (dpopRequest, bool) {
	d, ok := ctx.Value(dpopCtxKey{}).(dpopRequest)
	return d, ok
}

// confirmationJKT extracts cnf.jkt from a claims map, or "" if the token is not
// sender-constrained.
func confirmationJKT(claims map[string]any) string {
	cnf, ok := claims["cnf"].(map[string]any)
	if !ok {
		return ""
	}
	jkt, _ := cnf["jkt"].(string)
	return jkt
}

// verifyConfirmation enforces the cnf.jkt binding. For a non-DPoP token
// (no cnf.jkt) it is a no-op. For a bound token it requires a DPoP proof in
// context, verifies it (method/URI/iat/replay + ath bound to THIS token), and
// checks the proof's key thumbprint equals the token's cnf.jkt.
func (a *JWTAuthenticator) verifyConfirmation(ctx context.Context, token string, claims map[string]any) error {
	jkt := confirmationJKT(claims)
	if jkt == "" {
		return nil // not sender-constrained
	}
	d, ok := dpopFromContext(ctx)
	if !ok || d.proof == "" {
		// The token demands proof of possession and none was presented.
		return fmt.Errorf("%w: DPoP-bound token requires a DPoP proof", ErrUnauthenticated)
	}
	now := a.cfg.Now
	proof, err := dpop.Verify(d.proof, dpop.Params{
		HTM:         d.htm,
		HTU:         d.htu,
		Now:         now,
		Replay:      a.cfg.DPoPReplay,
		ExpectedATH: dpop.AccessTokenHash(token),
	})
	if err != nil {
		return fmt.Errorf("%w: DPoP proof rejected", ErrUnauthenticated)
	}
	// The proof must be signed by the exact key the token was bound to.
	if !Equal(proof.JKT, jkt) {
		return fmt.Errorf("%w: DPoP key does not match token cnf.jkt", ErrUnauthenticated)
	}
	return nil
}
