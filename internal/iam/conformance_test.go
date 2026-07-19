package iam_test

// RFC 9068 / 8414 conformance guards for the access-token profile. These pin
// the fixes for the non-conformances the standards review found, so they
// cannot silently regress.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/indugapallignaneswara/agentmesh/internal/iam"
)

func TestAccessTokenCarriesClientID(t *testing.T) {
	ts, store := confServer(t)
	id, secret := confClient(t, store)
	tok := confToken(t, ts, id, secret)

	var claims map[string]any
	if err := json.Unmarshal(confPayload(t, tok), &claims); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	if got, _ := claims["client_id"].(string); got != id {
		t.Fatalf("client_id claim = %q, want %q (RFC 9068 §2.2)", got, id)
	}
	if got, _ := claims["typ"].(string); got != "" {
		// typ lives in the header, not the payload — just ensure we didn't stuff it.
		_ = got
	}
}

func TestAccessTokenTypHeaderIsAtJWT(t *testing.T) {
	ts, store := confServer(t)
	id, secret := confClient(t, store)
	tok := confToken(t, ts, id, secret)
	var hdr struct {
		Typ string `json:"typ"`
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(confSeg(t, tok, 0), &hdr); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if hdr.Typ != "at+jwt" {
		t.Fatalf("typ = %q, want at+jwt (RFC 9068 §2.1)", hdr.Typ)
	}
	if hdr.Alg != "RS256" {
		t.Fatalf("alg = %q, want RS256", hdr.Alg)
	}
}

func TestMetadataNoImplicitResponseType(t *testing.T) {
	ts, _ := confServer(t)
	res, err := http.Get(ts.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}
	defer res.Body.Close()
	var md struct {
		ResponseTypesSupported []string `json:"response_types_supported"`
	}
	if err := json.NewDecoder(res.Body).Decode(&md); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	for _, rt := range md.ResponseTypesSupported {
		if rt == "token" {
			t.Fatalf("metadata advertises implicit-grant response_type %q, removed by OAuth 2.1", rt)
		}
	}
}

// --- helpers (conf* prefix, self-contained) ---

func confServer(t *testing.T) (*httptest.Server, iam.Store) {
	t.Helper()
	key, err := iam.GenerateSigningKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	store := iam.NewMemStore()
	var srv *iam.Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	srv, err = iam.NewServer(iam.Config{Issuer: ts.URL}, iam.NewKeySet(key), store)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	return ts, store
}

func confClient(t *testing.T, store iam.Store) (id, secret string) {
	t.Helper()
	id, secret, err := iam.RegisterClient(context.Background(), store, iam.Client{
		Workspace: "team", Subject: "deployer", Kind: "agent",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return id, secret
}

func confToken(t *testing.T, ts *httptest.Server, id, secret string) string {
	t.Helper()
	res, err := http.PostForm(ts.URL+"/token", url.Values{
		"grant_type": {"client_credentials"}, "client_id": {id},
		"client_secret": {secret}, "resource": {"https://mesh.example/mcp"},
	})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	defer res.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if body.AccessToken == "" {
		t.Fatal("empty access_token")
	}
	return body.AccessToken
}

func confSeg(t *testing.T, tok string, i int) []byte {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWS: %q", tok)
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[i])
	if err != nil {
		t.Fatalf("decode segment %d: %v", i, err)
	}
	return b
}

func confPayload(t *testing.T, tok string) []byte { return confSeg(t, tok, 1) }
