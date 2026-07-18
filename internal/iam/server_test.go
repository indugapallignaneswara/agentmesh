package iam_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/iam"
)

// srvFixture is a running Agent-IAM server plus everything a test needs to talk
// to it.
type srvFixture struct {
	ts     *httptest.Server
	store  iam.Store
	keys   *iam.KeySet
	issuer string
}

// srvNew stands up a Server on a MemStore with a fresh signing key. The
// httptest URL doubles as the issuer, matching real deployment where the issuer
// is the server's public URL.
func srvNew(t *testing.T) *srvFixture {
	t.Helper()
	key, err := iam.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	keys := iam.NewKeySet(key)
	store := iam.NewMemStore()

	// Issuer must be known before the server URL exists; use a placeholder
	// listener first. Simpler: create the httptest server around a mux we swap
	// in after constructing the Server with its final URL.
	var srv *iam.Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	srv, err = iam.NewServer(iam.Config{Issuer: ts.URL}, keys, store)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return &srvFixture{ts: ts, store: store, keys: keys, issuer: ts.URL}
}

// srvRegister registers a client and returns its id and one-time secret.
func srvRegister(t *testing.T, store iam.Store, c iam.Client) (id, secret string) {
	t.Helper()
	id, secret, err := iam.RegisterClient(context.Background(), store, c)
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	return id, secret
}

// srvPostToken POSTs a form to /token and decodes the JSON body.
func srvPostToken(t *testing.T, f *srvFixture, form url.Values, mutate func(*http.Request)) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.ts.URL+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if mutate != nil {
		mutate(req)
	}
	res, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /token: %v", err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode /token response: %v", err)
	}
	return res, body
}

// srvAssertOAuthError checks status and the RFC 6749 error code in the body.
func srvAssertOAuthError(t *testing.T, res *http.Response, body map[string]any, wantStatus int, wantCode string) {
	t.Helper()
	if res.StatusCode != wantStatus {
		t.Errorf("status = %d, want %d (body %v)", res.StatusCode, wantStatus, body)
	}
	if got, _ := body["error"].(string); got != wantCode {
		t.Errorf("error = %q, want %q (body %v)", got, wantCode, body)
	}
}

func TestTokenClientCredentialsHappyPath(t *testing.T) {
	f := srvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{
		Workspace:     "team",
		Subject:       "deployer",
		Kind:          "agent",
		AllowedScopes: []string{"mesh:send", "mesh:read"},
	})

	res, body := srvPostToken(t, f, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {secret},
		"resource":      {"https://mesh.example/mcp"},
		"scope":         {"mesh:send"},
	}, nil)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", res.StatusCode, body)
	}
	tok, _ := body["access_token"].(string)
	if parts := strings.Split(tok, "."); len(parts) != 3 {
		t.Fatalf("access_token is not a 3-segment JWT: %q", tok)
	}
	if tt, _ := body["token_type"].(string); tt != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", tt)
	}
	if exp, _ := body["expires_in"].(float64); exp <= 0 {
		t.Errorf("expires_in = %v, want > 0", body["expires_in"])
	}
	if sc, _ := body["scope"].(string); sc != "mesh:send" {
		t.Errorf("scope = %q, want mesh:send", sc)
	}
}

func TestTokenUnsupportedGrantType(t *testing.T) {
	f := srvNew(t)
	res, body := srvPostToken(t, f, url.Values{"grant_type": {"foo"}}, nil)
	srvAssertOAuthError(t, res, body, http.StatusBadRequest, "unsupported_grant_type")
}

func TestTokenUnknownClient(t *testing.T) {
	f := srvNew(t)
	res, body := srvPostToken(t, f, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"agt_does-not-exist"},
		"client_secret": {"ags_whatever"},
		"resource":      {"https://mesh.example/mcp"},
	}, nil)
	srvAssertOAuthError(t, res, body, http.StatusBadRequest, "invalid_client")
}

func TestTokenWrongSecret(t *testing.T) {
	f := srvNew(t)
	id, _ := srvRegister(t, f.store, iam.Client{Workspace: "team", Subject: "deployer", Kind: "agent"})
	res, body := srvPostToken(t, f, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {"ags_wrong"},
		"resource":      {"https://mesh.example/mcp"},
	}, nil)
	srvAssertOAuthError(t, res, body, http.StatusBadRequest, "invalid_client")
}

func TestTokenBasicAuthWrongSecretIs401(t *testing.T) {
	f := srvNew(t)
	id, _ := srvRegister(t, f.store, iam.Client{Workspace: "team", Subject: "deployer", Kind: "agent"})
	res, body := srvPostToken(t, f, url.Values{
		"grant_type": {"client_credentials"},
		"resource":   {"https://mesh.example/mcp"},
	}, func(r *http.Request) {
		r.SetBasicAuth(id, "ags_wrong")
	})
	srvAssertOAuthError(t, res, body, http.StatusUnauthorized, "invalid_client")
	if got := res.Header.Get("WWW-Authenticate"); got == "" {
		t.Error("401 response is missing the WWW-Authenticate challenge")
	}
}

func TestTokenDisabledClient(t *testing.T) {
	f := srvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{Workspace: "team", Subject: "deployer", Kind: "agent"})
	if err := f.store.SetClientDisabled(context.Background(), id, true); err != nil {
		t.Fatalf("SetClientDisabled: %v", err)
	}
	res, body := srvPostToken(t, f, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {secret},
		"resource":      {"https://mesh.example/mcp"},
	}, nil)
	srvAssertOAuthError(t, res, body, http.StatusBadRequest, "invalid_client")
}

func TestTokenMissingResource(t *testing.T) {
	f := srvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{Workspace: "team", Subject: "deployer", Kind: "agent"})
	res, body := srvPostToken(t, f, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {secret},
	}, nil)
	srvAssertOAuthError(t, res, body, http.StatusBadRequest, "invalid_target")
}

func TestTokenNonAllowedScope(t *testing.T) {
	f := srvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{
		Workspace: "team", Subject: "deployer", Kind: "agent",
		AllowedScopes: []string{"mesh:read"},
	})
	res, body := srvPostToken(t, f, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {secret},
		"resource":      {"https://mesh.example/mcp"},
		"scope":         {"mesh:admin"},
	}, nil)
	srvAssertOAuthError(t, res, body, http.StatusBadRequest, "invalid_scope")
}

func TestJWKSEndpoint(t *testing.T) {
	f := srvNew(t)
	res, err := f.ts.Client().Get(f.ts.URL + "/.well-known/jwks.json")
	if err != nil {
		t.Fatalf("GET jwks: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatalf("decode jwks: %v", err)
	}
	if len(doc.Keys) != 1 {
		t.Fatalf("jwks has %d keys, want 1", len(doc.Keys))
	}
	k := doc.Keys[0]
	if k.Kty != "RSA" {
		t.Errorf("kty = %q, want RSA", k.Kty)
	}
	if k.Kid != f.keys.Active().Kid {
		t.Errorf("kid = %q, want %q", k.Kid, f.keys.Active().Kid)
	}
	if k.N == "" || k.E == "" {
		t.Error("jwks key is missing n or e")
	}
}

func TestAuthorizationServerMetadata(t *testing.T) {
	f := srvNew(t)
	res, err := f.ts.Client().Get(f.ts.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatalf("GET metadata: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var md struct {
		Issuer              string   `json:"issuer"`
		TokenEndpoint       string   `json:"token_endpoint"`
		JWKSURI             string   `json:"jwks_uri"`
		GrantTypesSupported []string `json:"grant_types_supported"`
	}
	if err := json.NewDecoder(res.Body).Decode(&md); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if md.Issuer != f.issuer {
		t.Errorf("issuer = %q, want %q", md.Issuer, f.issuer)
	}
	if md.TokenEndpoint != f.issuer+"/token" {
		t.Errorf("token_endpoint = %q, want %q", md.TokenEndpoint, f.issuer+"/token")
	}
	if md.JWKSURI != f.issuer+"/.well-known/jwks.json" {
		t.Errorf("jwks_uri = %q, want %q", md.JWKSURI, f.issuer+"/.well-known/jwks.json")
	}
	found := false
	for _, g := range md.GrantTypesSupported {
		if g == "client_credentials" {
			found = true
		}
	}
	if !found {
		t.Errorf("grant_types_supported = %v, want to contain client_credentials", md.GrantTypesSupported)
	}
}

// srvSanityDefaultTTL guards the documented 15-minute default indirectly via
// expires_in when the client sets no TTL.
func TestTokenDefaultTTL(t *testing.T) {
	f := srvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{Workspace: "team", Subject: "deployer", Kind: "agent"})
	res, body := srvPostToken(t, f, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {secret},
		"resource":      {"https://mesh.example/mcp"},
	}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", res.StatusCode, body)
	}
	if exp, _ := body["expires_in"].(float64); exp != (15 * time.Minute).Seconds() {
		t.Errorf("expires_in = %v, want %v", body["expires_in"], (15 * time.Minute).Seconds())
	}
}
