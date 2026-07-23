package iam_test

// P5 entitlements: arbitrary policy assertions registered on a client are
// stamped into every issued token's `ent` claim, generalising the
// budget_daily_bytes claim. The credential asserts them; a relying party keys
// authorization off them and the audit trail records what was asserted.

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/indugapallignaneswara/agentmesh/internal/iam"
)

func TestEntitlementsStampedIntoToken(t *testing.T) {
	f := srvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{
		Workspace: "team", Subject: "deployer", Kind: "agent",
		AllowedScopes: []string{"mesh:send"},
		Entitlements:  map[string]string{"tier": "gold", "region": "eu"},
	})

	_, body := srvPostToken(t, f, url.Values{
		"grant_type": {"client_credentials"}, "client_id": {id},
		"client_secret": {secret}, "resource": {"https://mesh.example/mcp"},
	}, nil)
	token, _ := body["access_token"].(string)
	if token == "" {
		t.Fatal("no access_token")
	}

	parts := strings.Split(token, ".")
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims struct {
		Ent map[string]string `json:"ent"`
	}
	if err := json.Unmarshal(pb, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if claims.Ent["tier"] != "gold" || claims.Ent["region"] != "eu" {
		t.Fatalf("ent claim = %v, want tier=gold region=eu", claims.Ent)
	}
}

func TestNoEntitlementsOmitsClaim(t *testing.T) {
	f := srvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{
		Workspace: "team", Subject: "plain", Kind: "agent",
		AllowedScopes: []string{"mesh:send"},
	})
	_, body := srvPostToken(t, f, url.Values{
		"grant_type": {"client_credentials"}, "client_id": {id},
		"client_secret": {secret}, "resource": {"https://mesh.example/mcp"},
	}, nil)
	token, _ := body["access_token"].(string)
	parts := strings.Split(token, ".")
	pb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if strings.Contains(string(pb), `"ent"`) {
		t.Fatalf("token without entitlements should omit the ent claim: %s", pb)
	}
}
