package iam_test

// The crown-jewel proof: tokens minted by the Agent-IAM authorization server
// are accepted by the UNCHANGED AgentMesh resource server (internal/auth), and
// the security properties hold — audience binding (RFC 8707), expiry, and
// signature integrity.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/iam"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

const iopResource = "https://mesh.example/mcp"

// iopFixture is a live Agent-IAM server with a registered client, ready to
// issue tokens.
type iopFixture struct {
	ts     *httptest.Server
	store  iam.Store
	keys   *iam.KeySet
	id     string
	secret string
}

func iopNew(t *testing.T) *iopFixture {
	t.Helper()
	key, err := iam.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	keys := iam.NewKeySet(key)
	store := iam.NewMemStore()

	var srv *iam.Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	srv, err = iam.NewServer(iam.Config{Issuer: ts.URL}, keys, store)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	id, secret, err := iam.RegisterClient(context.Background(), store, iam.Client{
		Workspace:     "team",
		Subject:       "deployer",
		Kind:          "agent",
		AllowedScopes: []string{"mesh:send", "mesh:read"},
	})
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	return &iopFixture{ts: ts, store: store, keys: keys, id: id, secret: secret}
}

// iopToken fetches an access token from /token for the given resource.
func iopToken(t *testing.T, f *iopFixture, resource string) string {
	t.Helper()
	res, err := f.ts.Client().PostForm(f.ts.URL+"/token", url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {f.id},
		"client_secret": {f.secret},
		"resource":      {resource},
	})
	if err != nil {
		t.Fatalf("POST /token: %v", err)
	}
	defer res.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if res.StatusCode != http.StatusOK || body.AccessToken == "" {
		t.Fatalf("token request failed: status %d, error %q", res.StatusCode, body.Error)
	}
	return body.AccessToken
}

// iopAuthenticator builds the resource-server side, pointed at the iam server's
// issuer and JWKS, guarding the given audience.
func iopAuthenticator(t *testing.T, f *iopFixture, audience string) *auth.JWTAuthenticator {
	t.Helper()
	a, err := auth.NewJWTAuthenticator(auth.OAuthConfig{
		Issuer:   f.ts.URL,
		Audience: audience,
		JWKSURL:  f.ts.URL + "/.well-known/jwks.json",
	})
	if err != nil {
		t.Fatalf("NewJWTAuthenticator: %v", err)
	}
	return a
}

// TestInteropIAMTokenAcceptedByResourceServer is the end-to-end contract: an
// agent registered in Agent-IAM exchanges its credentials for a token, and the
// unmodified internal/auth resource server maps it to the right Principal.
func TestInteropIAMTokenAcceptedByResourceServer(t *testing.T) {
	f := iopNew(t)
	token := iopToken(t, f, iopResource)
	authn := iopAuthenticator(t, f, iopResource)

	p, err := authn.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("resource server rejected an iam-issued token: %v", err)
	}
	want := auth.Principal{Workspace: "team", Member: "deployer", Kind: model.KindAgent}
	if p != want {
		t.Fatalf("Principal = %+v, want %+v", p, want)
	}
}

// TestInteropAudienceBinding proves RFC 8707 resource binding: a token minted
// for another resource must be rejected here, killing token passthrough.
func TestInteropAudienceBinding(t *testing.T) {
	f := iopNew(t)
	tokenForOther := iopToken(t, f, "https://other.example")
	authn := iopAuthenticator(t, f, iopResource)

	if _, err := authn.Authenticate(context.Background(), tokenForOther); err == nil {
		t.Fatal("token minted for https://other.example was accepted by a different resource")
	} else if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("want ErrUnauthenticated, got %v", err)
	}
}

// TestInteropExpiredTokenRejected mints a token from a server whose clock is
// 30 minutes in the past (TTL 1 minute), then authenticates with the real
// clock: well past exp plus the authenticator's leeway.
func TestInteropExpiredTokenRejected(t *testing.T) {
	f := iopNew(t)

	// A second server sharing the SAME key set and issuer string, but with a
	// clock in the past. Its tokens verify cryptographically; only exp differs.
	past := func() time.Time { return time.Now().UTC().Add(-30 * time.Minute) }
	pastSrv, err := iam.NewServer(iam.Config{
		Issuer:     f.ts.URL, // same issuer so only expiry can fail
		DefaultTTL: time.Minute,
		Now:        past,
	}, f.keys, f.store)
	if err != nil {
		t.Fatalf("NewServer(past): %v", err)
	}
	pastTS := httptest.NewServer(pastSrv.Handler())
	defer pastTS.Close()

	res, err := pastTS.Client().PostForm(pastTS.URL+"/token", url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {f.id},
		"client_secret": {f.secret},
		"resource":      {iopResource},
	})
	if err != nil {
		t.Fatalf("POST /token (past server): %v", err)
	}
	defer res.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil || body.AccessToken == "" {
		t.Fatalf("past server issued no token: status %d, err %v", res.StatusCode, err)
	}

	authn := iopAuthenticator(t, f, iopResource)
	if _, err := authn.Authenticate(context.Background(), body.AccessToken); err == nil {
		t.Fatal("expired token was accepted")
	} else if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("want ErrUnauthenticated, got %v", err)
	}
}

// TestInteropTamperedTokenRejected flips the payload (claims) while keeping the
// original signature: the resource server must reject it.
func TestInteropTamperedTokenRejected(t *testing.T) {
	f := iopNew(t)
	token := iopToken(t, f, iopResource)
	authn := iopAuthenticator(t, f, iopResource)

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	// Privilege escalation attempt: rewrite the workspace claim.
	tampered := strings.Replace(string(payload), `"workspace":"team"`, `"workspace":"admins"`, 1)
	if tampered == string(payload) {
		t.Fatal("test setup: workspace claim not found in payload")
	}
	forged := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(tampered)) + "." + parts[2]

	if _, err := authn.Authenticate(context.Background(), forged); err == nil {
		t.Fatal("tampered token was accepted")
	} else if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("want ErrUnauthenticated, got %v", err)
	}
}
