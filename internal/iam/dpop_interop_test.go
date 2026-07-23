package iam_test

// The DPoP crown proof (RFC 9449): a sender-constrained token issued by
// Agent-IAM is accepted by the UNCHANGED AgentMesh resource server ONLY when
// the caller proves possession of the key with a fresh proof — and a token
// exfiltrated WITHOUT the private key is inert. This is the attack DPoP exists
// to stop: an agent's bearer token leaks from its context window via prompt
// injection; with DPoP the thief holds a useless string.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/dpop"
	"github.com/indugapallignaneswara/agentmesh/internal/iam"
)

const dpiResource = "https://mesh.example/mcp"

// dpiIssueBound registers an agent client and exchanges its credentials for a
// DPoP-bound token, returning the token and the agent's private key.
func dpiIssueBound(t *testing.T, f *srvFixture) (token string, key *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	id, secret := srvRegister(t, f.store, iam.Client{
		Workspace: "team", Subject: "deployer", Kind: "agent",
		AllowedScopes: []string{"mesh:send"},
	})

	// The proof for the TOKEN endpoint: htm=POST, htu=the token endpoint, no ath.
	proof, err := dpop.NewProof(key, "POST", f.ts.URL+"/token", "", time.Now())
	if err != nil {
		t.Fatalf("NewProof(token endpoint): %v", err)
	}
	res, body := srvPostToken(t, f, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {secret},
		"resource":      {dpiResource},
		"scope":         {"mesh:send"},
	}, func(r *http.Request) { r.Header.Set("DPoP", proof) })
	if res.StatusCode != 200 {
		t.Fatalf("bound token issue failed: %d %v", res.StatusCode, body)
	}
	if tt, _ := body["token_type"].(string); tt != "DPoP" {
		t.Fatalf("token_type = %q, want DPoP", tt)
	}
	token, _ = body["access_token"].(string)
	if token == "" {
		t.Fatal("no access_token")
	}
	return token, key
}

// dpiAuthenticator builds the mesh resource server pointed at this Agent-IAM.
func dpiAuthenticator(t *testing.T, f *srvFixture, replay dpop.ReplayGuard) *auth.JWTAuthenticator {
	t.Helper()
	a, err := auth.NewJWTAuthenticator(auth.OAuthConfig{
		Issuer:     f.issuer,
		Audience:   dpiResource,
		JWKSURL:    f.ts.URL + "/.well-known/jwks.json",
		DPoPReplay: replay,
	})
	if err != nil {
		t.Fatalf("NewJWTAuthenticator: %v", err)
	}
	return a
}

func TestDPoPBoundTokenRequiresProof(t *testing.T) {
	f := srvNew(t)
	token, key := dpiIssueBound(t, f)
	authn := dpiAuthenticator(t, f, nil)

	// (1) THE ATTACK: the token alone, no proof — as a thief who scraped it from
	// the agent's context would have. Must be rejected.
	if _, err := authn.Authenticate(context.Background(), token); err == nil {
		t.Fatal("a DPoP-bound token was accepted WITHOUT a proof — exfiltration not defended")
	}

	// (2) The legitimate holder: a fresh proof for THIS request, bound to the
	// token via ath. Must be accepted and map to the agent Principal.
	proof, err := dpop.NewProof(key, "POST", dpiResource, token, time.Now())
	if err != nil {
		t.Fatalf("NewProof(resource): %v", err)
	}
	ctx := auth.WithDPoP(context.Background(), proof, "POST", dpiResource)
	p, err := authn.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("legitimate DPoP request rejected: %v", err)
	}
	if p.Member != "deployer" || p.Workspace != "team" {
		t.Fatalf("principal = %+v", p)
	}
}

func TestDPoPWrongKeyRejected(t *testing.T) {
	f := srvNew(t)
	token, _ := dpiIssueBound(t, f)
	authn := dpiAuthenticator(t, f, nil)

	// A thief who steals the token AND mints their own proof with THEIR key:
	// the proof is internally valid but its thumbprint != the token's cnf.jkt.
	thief, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	proof, err := dpop.NewProof(thief, "POST", dpiResource, token, time.Now())
	if err != nil {
		t.Fatalf("NewProof: %v", err)
	}
	ctx := auth.WithDPoP(context.Background(), proof, "POST", dpiResource)
	if _, err := authn.Authenticate(ctx, token); err == nil {
		t.Fatal("a proof signed by the wrong key was accepted — cnf.jkt not enforced")
	}
}

func TestDPoPProofWrongMethodRejected(t *testing.T) {
	f := srvNew(t)
	token, key := dpiIssueBound(t, f)
	authn := dpiAuthenticator(t, f, nil)

	// Proof minted for GET, presented on a POST request → htm mismatch.
	proof, err := dpop.NewProof(key, "GET", dpiResource, token, time.Now())
	if err != nil {
		t.Fatalf("NewProof: %v", err)
	}
	ctx := auth.WithDPoP(context.Background(), proof, "POST", dpiResource)
	if _, err := authn.Authenticate(ctx, token); err == nil {
		t.Fatal("a proof bound to a different method was accepted")
	}
}

func TestDPoPReplayRejected(t *testing.T) {
	f := srvNew(t)
	token, key := dpiIssueBound(t, f)
	authn := dpiAuthenticator(t, f, dpop.NewMemReplayGuard(2*time.Minute))

	proof, err := dpop.NewProof(key, "POST", dpiResource, token, time.Now())
	if err != nil {
		t.Fatalf("NewProof: %v", err)
	}
	ctx := auth.WithDPoP(context.Background(), proof, "POST", dpiResource)

	// First use: accepted.
	if _, err := authn.Authenticate(ctx, token); err != nil {
		t.Fatalf("first use rejected: %v", err)
	}
	// Same proof replayed: rejected (a captured proof cannot be reused).
	if _, err := authn.Authenticate(ctx, token); err == nil {
		t.Fatal("a replayed DPoP proof was accepted")
	}
}

// Sanity: an ordinary (unbound) token still works with no proof — DPoP is
// opt-in and does not break the bearer path.
func TestUnboundTokenStillBearer(t *testing.T) {
	f := srvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{
		Workspace: "team", Subject: "plain", Kind: "agent", AllowedScopes: []string{"mesh:send"},
	})
	_, body := srvPostToken(t, f, url.Values{
		"grant_type": {"client_credentials"}, "client_id": {id},
		"client_secret": {secret}, "resource": {dpiResource},
	}, nil)
	token, _ := body["access_token"].(string)

	authn := dpiAuthenticator(t, f, nil)
	if _, err := authn.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("unbound token rejected without a proof: %v", err)
	}
}
