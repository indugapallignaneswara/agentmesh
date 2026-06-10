package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/client"
	"github.com/indugapallignaneswara/agentmesh/internal/mcpserver"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// newGatedServer stands up the REAL stack in token mode: MCP handler wrapped in
// the auth middleware, exactly as cmd/agentmesh wires it. It returns the
// endpoint plus seeded credentials for an agent and a human.
func newGatedServer(t *testing.T) (endpoint, agentTok, humanTok string) {
	t.Helper()
	st := store.NewMemory()
	svc := workspace.New(st, bus.NewNoop())
	authn := &auth.TokenAuthenticator{Store: st}

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.Handler(svc, "test"))
	srv := httptest.NewServer(auth.Middleware(authn, "/healthz")(mux))
	t.Cleanup(srv.Close)

	mk := func(member string, kind model.Kind) string {
		secret, id, hash, err := auth.GenerateSecret()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateAuthToken(context.Background(), model.AuthToken{
			ID: id, TokenHash: hash, Workspace: "team", Member: member, Kind: kind,
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		return secret
	}
	return srv.URL + "/mcp", mk("backend", model.KindAgent), mk("alice", model.KindHuman)
}

func TestAuthRejectsMissingAndBogusTokens(t *testing.T) {
	ctx := context.Background()
	endpoint, _, _ := newGatedServer(t)

	// No token: the MCP handshake itself must fail with 401.
	if _, err := client.New(endpoint).Raw(ctx, "workspace_presence",
		map[string]any{"workspace": "team"}); err == nil {
		t.Fatal("tokenless call succeeded against a gated server")
	}
	// Bogus token: same.
	if _, err := client.New(endpoint, client.WithToken("amt_bogus_bogus_bogus_bogus")).
		Raw(ctx, "workspace_presence", map[string]any{"workspace": "team"}); err == nil {
		t.Fatal("bogus token accepted")
	}
}

func TestAuthEnforcesIdentity(t *testing.T) {
	ctx := context.Background()
	endpoint, agentTok, humanTok := newGatedServer(t)
	agent := client.New(endpoint, client.WithToken(agentTok))
	human := client.New(endpoint, client.WithToken(humanTok))

	// Each principal joins as itself (and only as its own kind).
	if _, err := agent.Raw(ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "backend", "kind": "agent"}); err != nil {
		t.Fatalf("agent join: %v", err)
	}
	if _, err := human.Raw(ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "alice", "kind": "human"}); err != nil {
		t.Fatalf("human join: %v", err)
	}

	// KIND SPOOF: the agent token cannot join as a human (would gain review
	// authority over shared memory).
	if _, err := agent.Raw(ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "backend", "kind": "human"}); err == nil ||
		!strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("kind spoof err = %v, want forbidden", err)
	}

	// ACTOR SPOOF: the agent cannot send a message claiming to be alice.
	if _, err := agent.Raw(ctx, "send_message", map[string]any{
		"workspace": "team", "from": "alice", "to": "backend", "body": "spoofed",
	}); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("actor spoof err = %v, want forbidden", err)
	}

	// INBOX THEFT: the agent cannot drain alice's inbox.
	if _, err := agent.Raw(ctx, "read_inbox",
		map[string]any{"workspace": "team", "member": "alice"}); err == nil ||
		!strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("inbox theft err = %v, want forbidden", err)
	}

	// WORKSPACE ESCAPE: the token is bound to "team".
	if _, err := agent.Raw(ctx, "workspace_presence",
		map[string]any{"workspace": "other"}); err == nil ||
		!strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("workspace escape err = %v, want forbidden", err)
	}

	// And the legitimate flow still works end to end.
	if _, err := human.Raw(ctx, "send_message", map[string]any{
		"workspace": "team", "from": "alice", "to": "backend", "body": "real message",
	}); err != nil {
		t.Fatalf("legit send: %v", err)
	}
	raw, err := agent.Raw(ctx, "read_inbox",
		map[string]any{"workspace": "team", "member": "backend"})
	if err != nil {
		t.Fatalf("legit inbox: %v", err)
	}
	if !strings.Contains(raw, "real message") {
		t.Fatalf("inbox = %s", raw)
	}
}
