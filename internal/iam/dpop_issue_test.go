package iam_test

// DPoP at token ISSUANCE (RFC 9449 §5, authorization-server side): a client
// that sends a DPoP proof header on /token gets a sender-constrained token
// (cnf.jkt bound, token_type "DPoP"); a client that sends none gets exactly
// the bearer token it always got. The happy path depends on the dpop core
// (internal/dpop proof.go/verify.go) and SKIPS while that core is stubbed;
// the backward-compat, metadata, and bad-proof tests do not.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/dpop"
	"github.com/indugapallignaneswara/agentmesh/internal/iam"
)

// jwtPayload base64url-decodes the payload segment of a compact JWT.
func jwtPayload(t *testing.T, token string) []byte {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("access_token is not a 3-segment JWT: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	return raw
}

// TestTokenWithoutDPoPStaysBearer is the backward-compat contract: no DPoP
// header → token_type "Bearer" and no cnf claim anywhere in the token. This
// must hold regardless of the dpop core.
func TestTokenWithoutDPoPStaysBearer(t *testing.T) {
	f := srvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{
		Workspace:     "team",
		Subject:       "deployer",
		Kind:          "agent",
		AllowedScopes: []string{"mesh:send"},
	})

	res, body := srvPostToken(t, f, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {secret},
		"resource":      {"https://mesh.example/mcp"},
	}, nil)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", res.StatusCode, body)
	}
	if tt, _ := body["token_type"].(string); tt != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", tt)
	}
	tok, _ := body["access_token"].(string)
	payload := jwtPayload(t, tok)
	if strings.Contains(string(payload), `"cnf"`) {
		t.Errorf("unbound token payload contains a cnf claim: %s", payload)
	}
}

// TestMetadataAdvertisesDPoP checks the RFC 9449 §5.1 AS metadata field.
func TestMetadataAdvertisesDPoP(t *testing.T) {
	f := srvNew(t)
	res, err := f.ts.Client().Get(f.ts.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatalf("GET metadata: %v", err)
	}
	defer res.Body.Close()
	var md struct {
		Algs []string `json:"dpop_signing_alg_values_supported"`
	}
	if err := json.NewDecoder(res.Body).Decode(&md); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	found := false
	for _, a := range md.Algs {
		if a == "ES256" {
			found = true
		}
	}
	if !found {
		t.Errorf("dpop_signing_alg_values_supported = %v, want to include ES256", md.Algs)
	}
}

// TestTokenWithDPoPIsBound is the happy path: a valid proof on /token yields
// token_type "DPoP" and a cnf.jkt thumbprint in the token. Gated: skips while
// the dpop core (NewProof/Verify) is stubbed.
func TestTokenWithDPoPIsBound(t *testing.T) {
	f := srvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{
		Workspace:     "team",
		Subject:       "deployer",
		Kind:          "agent",
		AllowedScopes: []string{"mesh:send"},
	})

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-256 key: %v", err)
	}
	htu := f.ts.URL + "/token"
	proof, err := dpop.NewProof(key, http.MethodPost, htu, "", time.Now())
	if err != nil {
		t.Skipf("dpop core not implemented yet (NewProof: %v) — rerun once internal/dpop lands", err)
	}

	res, body := srvPostToken(t, f, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {secret},
		"resource":      {"https://mesh.example/mcp"},
	}, func(r *http.Request) {
		r.Header.Set("DPoP", proof)
	})

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", res.StatusCode, body)
	}
	if tt, _ := body["token_type"].(string); tt != "DPoP" {
		t.Errorf("token_type = %q, want DPoP", tt)
	}
	tok, _ := body["access_token"].(string)
	var claims struct {
		Cnf *struct {
			JKT string `json:"jkt"`
		} `json:"cnf"`
	}
	if err := json.Unmarshal(jwtPayload(t, tok), &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if claims.Cnf == nil || claims.Cnf.JKT == "" {
		t.Fatalf("bound token missing cnf.jkt (payload %s)", jwtPayload(t, tok))
	}
	// base64url(SHA-256) without padding is always 43 chars.
	if len(claims.Cnf.JKT) != 43 {
		t.Errorf("cnf.jkt = %q (%d chars), want a 43-char base64url SHA-256 thumbprint",
			claims.Cnf.JKT, len(claims.Cnf.JKT))
	}
}

// TestTokenWithBadDPoPProofRejected: a present-but-garbage DPoP header must be
// refused with 400 invalid_dpop_proof (RFC 9449 §5.2) — never silently issued
// as a bearer token. Works against the stubbed core too (stubs reject all).
func TestTokenWithBadDPoPProofRejected(t *testing.T) {
	f := srvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{
		Workspace:     "team",
		Subject:       "deployer",
		Kind:          "agent",
		AllowedScopes: []string{"mesh:send"},
	})

	res, body := srvPostToken(t, f, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {secret},
		"resource":      {"https://mesh.example/mcp"},
	}, func(r *http.Request) {
		r.Header.Set("DPoP", "not.a.jwt")
	})
	srvAssertOAuthError(t, res, body, http.StatusBadRequest, "invalid_dpop_proof")
}
