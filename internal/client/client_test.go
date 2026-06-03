package client_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/client"
	"github.com/indugapallignaneswara/agentmesh/internal/mcpserver"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// newTestServer starts the real MCP HTTP handler on a local httptest server and
// returns a client pointed at its /mcp endpoint. This exercises the full
// transport path (HTTP -> Streamable MCP -> service -> in-memory store).
func newTestServer(t *testing.T) *client.Client {
	t.Helper()
	svc := workspace.New(store.NewMemory(), bus.NewNoop())
	srv := httptest.NewServer(mcpserver.Handler(svc, "test"))
	t.Cleanup(srv.Close)
	return client.New(srv.URL + "/")
}

func TestClientCoreLoopOverHTTP(t *testing.T) {
	ctx := context.Background()
	cl := newTestServer(t)

	// Two members join.
	if _, err := cl.Raw(ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "alice", "kind": "human"}); err != nil {
		t.Fatalf("alice join: %v", err)
	}
	if _, err := cl.Raw(ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "backend", "kind": "agent"}); err != nil {
		t.Fatalf("backend join: %v", err)
	}

	// Direct message, then read it back.
	if _, err := cl.Raw(ctx, "send_message", map[string]any{
		"workspace": "team", "from": "alice", "to": "backend", "body": "ship it",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	raw, err := cl.Raw(ctx, "read_inbox",
		map[string]any{"workspace": "team", "member": "backend"})
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	var inbox struct {
		Count    int `json:"count"`
		Messages []struct {
			Body string `json:"body"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &inbox); err != nil {
		t.Fatalf("decode inbox: %v", err)
	}
	if inbox.Count != 1 || inbox.Messages[0].Body != "ship it" {
		t.Fatalf("inbox = %+v, want one 'ship it'", inbox)
	}
}

// TestClientToolErrorSurfaces confirms a tool-level error (unknown recipient)
// comes back as a Go error carrying the server's message, not a silent success.
func TestClientToolErrorSurfaces(t *testing.T) {
	ctx := context.Background()
	cl := newTestServer(t)
	if _, err := cl.Raw(ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "alice", "kind": "agent"}); err != nil {
		t.Fatalf("join: %v", err)
	}
	_, err := cl.Raw(ctx, "send_message", map[string]any{
		"workspace": "team", "from": "alice", "to": "ghost", "body": "x",
	})
	if err == nil {
		t.Fatal("expected error for unknown recipient, got nil")
	}
}
