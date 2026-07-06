package mcpserver_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/mcpserver"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// connect wires an MCP client to the coordination server over the SDK's
// in-memory transport, exercising the real tool-dispatch path end to end.
func connect(t *testing.T) (*mcp.ClientSession, context.Context) {
	t.Helper()
	return connectSvc(t, workspace.New(store.NewMemory(), bus.NewNoop()))
}

// connectSvc wires an MCP client to a server over the given service, so tests
// can vary service options (e.g. implicit-room mode).
func connectSvc(t *testing.T, svc *workspace.Service) (*mcp.ClientSession, context.Context) {
	t.Helper()
	ctx := context.Background()
	srv := mcpserver.NewServer(svc, "test")

	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, ctx
}

// callJSON invokes a tool and decodes its JSON text content into out. It fails
// the test if the tool reported an error.
func callJSON(t *testing.T, cs *mcp.ClientSession, ctx context.Context, name string, args map[string]any, out any) {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("call %s returned tool error: %s", name, textOf(res))
	}
	if out != nil {
		if err := json.Unmarshal([]byte(textOf(res)), out); err != nil {
			t.Fatalf("decode %s result %q: %v", name, textOf(res), err)
		}
	}
}

// callExpectError invokes a tool expecting a tool-level error result.
func callExpectError(t *testing.T, cs *mcp.ClientSession, ctx context.Context, name string, args map[string]any) {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s transport error: %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("call %s: expected tool error, got %s", name, textOf(res))
	}
}

func textOf(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func TestMCPListTools(t *testing.T) {
	cs, ctx := connect(t)
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{
		"workspace_join", "workspace_presence", "send_message",
		"read_inbox", "broadcast", "publish_event", "subscribe",
	} {
		if !got[want] {
			t.Errorf("missing tool %q (have %v)", want, got)
		}
	}
}

// TestMCPCoreLoop exercises the Phase 0 success metric over MCP: two members
// join, one addresses the other by name, and a broadcast reaches all others.
func TestMCPCoreLoop(t *testing.T) {
	cs, ctx := connect(t)

	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "alice", "kind": "human"}, nil)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "backend", "kind": "agent"}, nil)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "frontend", "kind": "agent"}, nil)

	// Presence lists all three.
	var presence struct {
		Count int `json:"count"`
	}
	callJSON(t, cs, ctx, "workspace_presence", map[string]any{"workspace": "team"}, &presence)
	if presence.Count != 3 {
		t.Fatalf("presence count = %d, want 3", presence.Count)
	}

	// Direct message alice -> backend.
	callJSON(t, cs, ctx, "send_message", map[string]any{
		"workspace": "team", "from": "alice", "to": "backend", "body": "ship it",
	}, nil)

	var inbox struct {
		Count    int `json:"count"`
		Messages []struct {
			Body string `json:"body"`
			Kind string `json:"kind"`
		} `json:"messages"`
	}
	callJSON(t, cs, ctx, "read_inbox", map[string]any{"workspace": "team", "member": "backend"}, &inbox)
	if inbox.Count != 1 || inbox.Messages[0].Body != "ship it" || inbox.Messages[0].Kind != "direct" {
		t.Fatalf("backend inbox = %+v", inbox)
	}

	// Broadcast from alice reaches backend + frontend (2 recipients).
	var bc struct {
		Recipients int `json:"recipients"`
	}
	callJSON(t, cs, ctx, "broadcast", map[string]any{
		"workspace": "team", "from": "alice", "body": "standup in 5",
	}, &bc)
	if bc.Recipients != 2 {
		t.Fatalf("broadcast recipients = %d, want 2", bc.Recipients)
	}
	for _, who := range []string{"backend", "frontend"} {
		var ib struct {
			Count int `json:"count"`
		}
		callJSON(t, cs, ctx, "read_inbox", map[string]any{"workspace": "team", "member": who}, &ib)
		if ib.Count != 1 {
			t.Fatalf("%s inbox after broadcast = %d, want 1", who, ib.Count)
		}
	}
}

func TestMCPEventLog(t *testing.T) {
	cs, ctx := connect(t)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "alice", "kind": "agent"}, nil)

	callJSON(t, cs, ctx, "publish_event", map[string]any{
		"workspace": "team", "source": "alice", "type": "deploy",
		"payload": json.RawMessage(`{"env":"prod"}`),
	}, nil)

	var sub struct {
		Count  int   `json:"count"`
		Cursor int64 `json:"cursor"`
		Events []struct {
			Type string `json:"type"`
		} `json:"events"`
	}
	callJSON(t, cs, ctx, "subscribe", map[string]any{"workspace": "team", "since": 0}, &sub)
	// member_joined + deploy.
	if sub.Count < 2 {
		t.Fatalf("event count = %d, want >=2", sub.Count)
	}
	last := sub.Events[len(sub.Events)-1]
	if last.Type != "deploy" {
		t.Fatalf("last event type = %q, want deploy", last.Type)
	}
}

// TestMCPErrorsAreToolErrors verifies that invalid input and unknown members
// surface as readable tool errors (IsError), not transport failures.
func TestMCPErrorsAreToolErrors(t *testing.T) {
	cs, ctx := connect(t)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "alice", "kind": "agent"}, nil)

	// Unknown recipient.
	callExpectError(t, cs, ctx, "send_message", map[string]any{
		"workspace": "team", "from": "alice", "to": "ghost", "body": "hi",
	})
	// Invalid workspace name.
	callExpectError(t, cs, ctx, "workspace_join", map[string]any{
		"workspace": "bad name", "name": "x", "kind": "agent",
	})
	// Invalid kind.
	callExpectError(t, cs, ctx, "workspace_join", map[string]any{
		"workspace": "team", "name": "x", "kind": "robot",
	})
}
