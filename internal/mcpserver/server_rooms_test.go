package mcpserver_test

import (
	"context"
	"testing"

	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectExplicit wires a client to a server whose service has implicit-room
// creation OFF, so room lifecycle is exercised over MCP.
func connectExplicit(t *testing.T) (*mcp.ClientSession, context.Context) {
	t.Helper()
	svc := workspace.New(store.NewMemory(), bus.NewNoop(), workspace.WithImplicitRooms(false))
	return connectSvc(t, svc)
}

// TestMCPRoomLifecycle drives room_create -> join -> close (writes rejected,
// reads OK) -> reopen over the MCP transport.
func TestMCPRoomLifecycle(t *testing.T) {
	cs, ctx := connectExplicit(t)

	// Without the room, join fails.
	callExpectError(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "alice", "kind": "human"})

	// Create it, then join + seed.
	var room struct{ Name, Status string }
	callJSON(t, cs, ctx, "room_create", map[string]any{"name": "team", "creator": "alice"}, &room)
	if room.Status != "open" {
		t.Fatalf("created room = %+v", room)
	}
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "alice", "kind": "human"}, nil)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "bot", "kind": "agent"}, nil)
	callJSON(t, cs, ctx, "send_message",
		map[string]any{"workspace": "team", "from": "alice", "to": "bot", "body": "hi"}, nil)

	// Close it — writes now rejected, reads still work.
	callJSON(t, cs, ctx, "room_close", map[string]any{"name": "team", "actor": "alice"}, nil)
	callExpectError(t, cs, ctx, "send_message",
		map[string]any{"workspace": "team", "from": "alice", "to": "bot", "body": "again"})

	var inbox struct {
		Count int `json:"count"`
	}
	callJSON(t, cs, ctx, "read_inbox", map[string]any{"workspace": "team", "member": "bot"}, &inbox)
	if inbox.Count != 1 {
		t.Fatalf("closed-room inbox = %d, want 1 (reads still work)", inbox.Count)
	}

	// room_list shows it closed.
	var list struct {
		Count int                             `json:"count"`
		Rooms []struct{ Name, Status string } `json:"rooms"`
	}
	callJSON(t, cs, ctx, "room_list", map[string]any{}, &list)
	if list.Count != 1 || list.Rooms[0].Status != "closed" {
		t.Fatalf("room_list = %+v", list)
	}

	// Reopen -> writes flow again.
	callJSON(t, cs, ctx, "room_reopen", map[string]any{"name": "team", "actor": "alice"}, nil)
	callJSON(t, cs, ctx, "send_message",
		map[string]any{"workspace": "team", "from": "alice", "to": "bot", "body": "back"}, nil)
}
