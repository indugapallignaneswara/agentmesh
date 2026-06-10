package discovery_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/discovery"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

func fetchCard(t *testing.T, url string) map[string]any {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var card map[string]any
	if err := json.NewDecoder(res.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	return card
}

func TestAgentCardShape(t *testing.T) {
	srv := httptest.NewServer(discovery.Handler("1.2.3", "off"))
	t.Cleanup(srv.Close)
	card := fetchCard(t, srv.URL+discovery.WellKnownPath)

	// All A2A-required fields present (per the normative a2a.proto).
	for _, f := range []string{"name", "description", "supportedInterfaces", "version",
		"capabilities", "defaultInputModes", "defaultOutputModes", "skills"} {
		if _, ok := card[f]; !ok {
			t.Errorf("required field %q missing", f)
		}
	}
	if card["version"] != "1.2.3" {
		t.Fatalf("version = %v", card["version"])
	}
	// The interface URL derives from the request host and points at /mcp.
	ifaces := card["supportedInterfaces"].([]any)
	first := ifaces[0].(map[string]any)
	if first["url"] != srv.URL+"/mcp" || first["protocolBinding"] != "MCP" || first["protocolVersion"] == "" {
		t.Fatalf("interface = %+v", first)
	}
	// Skills carry their required fields.
	skills := card["skills"].([]any)
	if len(skills) == 0 {
		t.Fatal("no skills")
	}
	s0 := skills[0].(map[string]any)
	for _, f := range []string{"id", "name", "description", "tags"} {
		if _, ok := s0[f]; !ok {
			t.Errorf("skill missing %q", f)
		}
	}
	// Auth off: no security schemes advertised.
	if _, ok := card["securitySchemes"]; ok {
		t.Fatal("securitySchemes present in off mode")
	}
}

// TestAgentCardServedUnauthenticated proves the card stays reachable when the
// server is token-gated (discovery must work pre-auth) and advertises the
// bearer scheme.
func TestAgentCardServedUnauthenticated(t *testing.T) {
	st := store.NewMemory()
	authn := &auth.TokenAuthenticator{Store: st}
	mux := http.NewServeMux()
	mux.Handle(discovery.WellKnownPath, discovery.Handler("1.0.0", "token"))
	srv := httptest.NewServer(auth.Middleware(authn, "/healthz", "/ui", discovery.WellKnownPath)(mux))
	t.Cleanup(srv.Close)

	card := fetchCard(t, srv.URL+discovery.WellKnownPath) // no token presented
	schemes, ok := card["securitySchemes"].(map[string]any)
	if !ok {
		t.Fatal("securitySchemes missing in token mode")
	}
	bearer := schemes["bearer"].(map[string]any)["httpAuthSecurityScheme"].(map[string]any)
	if bearer["scheme"] != "Bearer" {
		t.Fatalf("scheme = %v", bearer["scheme"])
	}
}
