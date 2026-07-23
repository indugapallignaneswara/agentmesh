package iam_test

// The P3 crown proof: a token whose signature and expiry are still perfectly
// valid is rejected by the mesh once it has been revoked at Agent-IAM and the
// mesh has polled the /revocations feed. This closes the one gap of stateless
// JWTs — you can kill an individual token before it expires.

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/iam"
)

// revPost posts a form to a path with HTTP Basic client auth and returns the
// status code.
func revPost(t *testing.T, f *srvFixture, path string, form url.Values, clientID, secret string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.ts.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, secret)
	res, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

func TestRevokedTokenRejectedByMeshAfterPoll(t *testing.T) {
	f := srvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{
		Workspace: "team", Subject: "deployer", Kind: "agent",
		AllowedScopes: []string{"mesh:send"},
	})

	// Agent obtains a token.
	_, body := srvPostToken(t, f, url.Values{
		"grant_type": {"client_credentials"}, "client_id": {id},
		"client_secret": {secret}, "resource": {"https://mesh.example/mcp"},
	}, nil)
	token, _ := body["access_token"].(string)
	if token == "" {
		t.Fatal("no access_token")
	}

	// The mesh resource server, pointed at this Agent-IAM, polling its feed.
	poller := auth.NewPollingRevocationChecker(f.ts.URL+"/revocations", time.Hour)
	authn, err := auth.NewJWTAuthenticator(auth.OAuthConfig{
		Issuer:      f.issuer,
		Audience:    "https://mesh.example/mcp",
		JWKSURL:     f.ts.URL + "/.well-known/jwks.json",
		Revocations: poller,
	})
	if err != nil {
		t.Fatalf("NewJWTAuthenticator: %v", err)
	}

	// Before revocation: the token is accepted.
	if err := poller.Refresh(context.Background()); err != nil {
		t.Fatalf("initial feed refresh: %v", err)
	}
	if _, err := authn.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("valid token rejected before revocation: %v", err)
	}

	// The client revokes its own token (RFC 7009 /revoke).
	if status := revPost(t, f, "/revoke", url.Values{"token": {token}}, id, secret); status != 200 {
		t.Fatalf("/revoke status = %d, want 200", status)
	}

	// The mesh polls the feed and now rejects the SAME token — signature and
	// expiry are still valid; only the revocation changed the answer.
	if err := poller.Refresh(context.Background()); err != nil {
		t.Fatalf("post-revoke feed refresh: %v", err)
	}
	if _, err := authn.Authenticate(context.Background(), token); err == nil {
		t.Fatal("a revoked token was still accepted after the mesh polled the feed")
	}
}
