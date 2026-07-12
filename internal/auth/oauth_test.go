package auth_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

const cryptoSHA256 = crypto.SHA256

func memStore() *store.Memory { return store.NewMemory() }

// seedAgentToken issues an opaque agent credential and returns the secret.
func seedAgentToken(t *testing.T, st *store.Memory, ws, name string) string {
	t.Helper()
	secret, id, hash, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAuthToken(context.Background(), model.AuthToken{
		ID: id, TokenHash: hash, Workspace: ws, Member: name, Kind: model.KindAgent,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return secret
}

const (
	testIssuer   = "https://idp.example.com"
	testAudience = "https://agentmesh.example.com/mcp"
	testKid      = "test-key-1"
)

// idp is a minimal fake authorization server: it holds a signing key, serves a
// JWKS, and can mint tokens with arbitrary claims.
type idp struct {
	key *rsa.PrivateKey
	srv *httptest.Server
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i := &idp{key: key}
	i.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pub := key.Public().(*rsa.PublicKey)
		doc := map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": testKid,
			"n":   b64u(pub.N.Bytes()),
			"e":   b64u(big.NewInt(int64(pub.E)).Bytes()),
		}}}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(i.srv.Close)
	return i
}

// mint signs a JWT with the given claims (RS256).
func (i *idp) mint(t *testing.T, claims map[string]any) string {
	t.Helper()
	hdr := map[string]string{"alg": "RS256", "typ": "JWT", "kid": testKid}
	hb, _ := json.Marshal(hdr)
	cb, _ := json.Marshal(claims)
	signing := b64u(hb) + "." + b64u(cb)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, cryptoSHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64u(sig)
}

func (i *idp) authenticator(t *testing.T) *auth.JWTAuthenticator {
	t.Helper()
	a, err := auth.NewJWTAuthenticator(auth.OAuthConfig{
		Issuer:   testIssuer,
		Audience: testAudience,
		JWKSURL:  i.srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func validClaims() map[string]any {
	return map[string]any{
		"iss":       testIssuer,
		"aud":       testAudience,
		"sub":       "alice",
		"workspace": "team",
		"exp":       float64(time.Now().Add(time.Hour).Unix()),
	}
}

func TestJWTHappyPath(t *testing.T) {
	i := newIDP(t)
	a := i.authenticator(t)
	p, err := a.Authenticate(context.Background(), i.mint(t, validClaims()))
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	// sub -> member, workspace claim -> room, and an IdP token is a human.
	if p.Member != "alice" || p.Workspace != "team" || p.Kind != model.KindHuman {
		t.Fatalf("principal = %+v", p)
	}
}

// TestJWTAudienceBinding is the RFC 8707 guarantee: a token minted for another
// resource must not work here. Without it, a token stolen from (or issued for)
// a different service would be accepted — the token-passthrough attack.
func TestJWTAudienceBinding(t *testing.T) {
	i := newIDP(t)
	a := i.authenticator(t)

	c := validClaims()
	c["aud"] = "https://some-other-service.example.com"
	if _, err := a.Authenticate(context.Background(), i.mint(t, c)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("token for another audience accepted! err = %v", err)
	}
	// An aud array containing us is fine.
	c["aud"] = []any{"https://other", testAudience}
	if _, err := a.Authenticate(context.Background(), i.mint(t, c)); err != nil {
		t.Fatalf("aud array containing this resource rejected: %v", err)
	}
}

func TestJWTRejectsUntrustedIssuerExpiredAndUnsigned(t *testing.T) {
	i := newIDP(t)
	a := i.authenticator(t)
	ctx := context.Background()

	// Wrong issuer.
	c := validClaims()
	c["iss"] = "https://evil.example.com"
	if _, err := a.Authenticate(ctx, i.mint(t, c)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("untrusted issuer accepted: %v", err)
	}
	// Expired.
	c = validClaims()
	c["exp"] = float64(time.Now().Add(-2 * time.Hour).Unix())
	if _, err := a.Authenticate(ctx, i.mint(t, c)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("expired token accepted: %v", err)
	}
	// Missing exp entirely.
	c = validClaims()
	delete(c, "exp")
	if _, err := a.Authenticate(ctx, i.mint(t, c)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("token without exp accepted: %v", err)
	}
	// No workspace claim -> cannot act in any room.
	c = validClaims()
	delete(c, "workspace")
	if _, err := a.Authenticate(ctx, i.mint(t, c)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("token without workspace accepted: %v", err)
	}
	// Garbage / not a JWS.
	if _, err := a.Authenticate(ctx, "not.a.jwt"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("garbage accepted: %v", err)
	}
}

// TestJWTRejectsAlgConfusion covers the classic JWT attacks: alg=none and
// symmetric algs (which would let an attacker sign with the public key).
func TestJWTRejectsAlgConfusion(t *testing.T) {
	i := newIDP(t)
	a := i.authenticator(t)
	cb, _ := json.Marshal(validClaims())

	for _, alg := range []string{"none", "HS256", "hs256", ""} {
		hb, _ := json.Marshal(map[string]string{"alg": alg, "kid": testKid})
		tok := b64u(hb) + "." + b64u(cb) + "." + b64u([]byte("sig"))
		if _, err := a.Authenticate(context.Background(), tok); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("alg %q accepted! err = %v", alg, err)
		}
	}
}

func TestJWTRejectsTamperedPayload(t *testing.T) {
	i := newIDP(t)
	a := i.authenticator(t)
	tok := i.mint(t, validClaims())

	// Swap the payload for one claiming to be someone else, keeping the signature.
	evil, _ := json.Marshal(map[string]any{
		"iss": testIssuer, "aud": testAudience, "sub": "admin",
		"workspace": "team", "exp": float64(time.Now().Add(time.Hour).Unix()),
	})
	parts := splitJWT(tok)
	tampered := parts[0] + "." + b64u(evil) + "." + parts[2]
	if _, err := a.Authenticate(context.Background(), tampered); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("tampered payload accepted! err = %v", err)
	}
}

// TestChainAuthenticatorHumansAndAgents proves the coexistence design: a human
// with an IdP JWT and an agent with an opaque token both authenticate against
// the same endpoint.
func TestChainAuthenticatorHumansAndAgents(t *testing.T) {
	i := newIDP(t)
	st := memStore()
	agentSecret := seedAgentToken(t, st, "team", "bot")

	chain := &auth.ChainAuthenticator{Authenticators: []auth.Authenticator{
		i.authenticator(t),
		&auth.TokenAuthenticator{Store: st},
	}}
	ctx := context.Background()

	human, err := chain.Authenticate(ctx, i.mint(t, validClaims()))
	if err != nil {
		t.Fatalf("human JWT rejected by chain: %v", err)
	}
	if human.Member != "alice" || human.Kind != model.KindHuman {
		t.Fatalf("human principal = %+v", human)
	}

	agent, err := chain.Authenticate(ctx, agentSecret)
	if err != nil {
		t.Fatalf("agent opaque token rejected by chain: %v", err)
	}
	if agent.Member != "bot" || agent.Kind != model.KindAgent {
		t.Fatalf("agent principal = %+v", agent)
	}

	if _, err := chain.Authenticate(ctx, "amt_bogus"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("bogus credential accepted by chain: %v", err)
	}
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func splitJWT(s string) [3]string {
	var out [3]string
	i, start := 0, 0
	for j := 0; j < len(s) && i < 3; j++ {
		if s[j] == '.' {
			out[i] = s[start:j]
			i++
			start = j + 1
		}
	}
	if i < 3 {
		out[i] = s[start:]
	}
	return out
}
