package iam_test

// The Agent-IAM budget claim: a client registered with a daily byte budget
// mints tokens carrying budget_daily_bytes, and the unchanged AgentMesh
// resource server surfaces it as Principal.BudgetDailyBytes — identity and
// spend control in one credential.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/iam"
)

func TestBudgetClaimFlowsToPrincipal(t *testing.T) {
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

	const resource = "https://mesh.example/mcp"
	id, secret, err := iam.RegisterClient(context.Background(), store, iam.Client{
		Workspace: "team", Subject: "capped", Kind: "agent",
		BudgetDailyBytes: 5000,
	})
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}

	res, err := http.PostForm(ts.URL+"/token", url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {secret},
		"resource":      {resource},
	})
	if err != nil {
		t.Fatalf("token request: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("token endpoint = %d: %s", res.StatusCode, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		t.Fatalf("decode token response: %v", err)
	}

	// The raw claim is present in the JWT payload.
	parts := strings.Split(tok.AccessToken, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWS: %q", tok.AccessToken)
	}
	if !strings.Contains(payloadOf(t, parts[1]), `"budget_daily_bytes":5000`) {
		t.Fatalf("payload missing budget claim: %s", payloadOf(t, parts[1]))
	}

	// And the unchanged resource server maps it onto the Principal.
	authn, err := auth.NewJWTAuthenticator(auth.OAuthConfig{
		Issuer: ts.URL, Audience: resource,
		JWKSURL: ts.URL + "/.well-known/jwks.json",
	})
	if err != nil {
		t.Fatalf("NewJWTAuthenticator: %v", err)
	}
	p, err := authn.Authenticate(context.Background(), tok.AccessToken)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.BudgetDailyBytes != 5000 {
		t.Fatalf("Principal.BudgetDailyBytes = %d, want 5000", p.BudgetDailyBytes)
	}
	if p.Member != "capped" || p.Workspace != "team" {
		t.Fatalf("principal = %+v", p)
	}
}

func payloadOf(t *testing.T, seg string) string {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return string(b)
}
