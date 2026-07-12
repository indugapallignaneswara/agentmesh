package mcpserver_test

import (
	"testing"
)

// TestMCPAckFlow drives the at-least-once flow over MCP: ack-mode read leases,
// a second read inside the window sees nothing, ack finalises.
func TestMCPAckFlow(t *testing.T) {
	cs, ctx := connect(t)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "alice", "kind": "human"}, nil)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "bot", "kind": "agent"}, nil)
	callJSON(t, cs, ctx, "send_message",
		map[string]any{"workspace": "team", "from": "alice", "to": "bot", "body": "do the thing"}, nil)

	// Ack-mode read: leased, not consumed.
	var leased struct {
		Count    int `json:"count"`
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	callJSON(t, cs, ctx, "read_inbox",
		map[string]any{"workspace": "team", "member": "bot", "ack_mode": true}, &leased)
	if leased.Count != 1 {
		t.Fatalf("leased = %+v, want 1", leased)
	}

	// Second ack-mode read inside the window: in flight, nothing returned.
	var second struct {
		Count int `json:"count"`
	}
	callJSON(t, cs, ctx, "read_inbox",
		map[string]any{"workspace": "team", "member": "bot", "ack_mode": true}, &second)
	if second.Count != 0 {
		t.Fatalf("in-flight re-leased: %+v", second)
	}

	// Ack finalises.
	var acked struct {
		Acked int `json:"acked"`
	}
	callJSON(t, cs, ctx, "ack_messages", map[string]any{
		"workspace": "team", "member": "bot", "ids": []string{leased.Messages[0].ID},
	}, &acked)
	if acked.Acked != 1 {
		t.Fatalf("acked = %+v, want 1", acked)
	}

	// Plain read confirms nothing remains.
	var plain struct {
		Count int `json:"count"`
	}
	callJSON(t, cs, ctx, "read_inbox",
		map[string]any{"workspace": "team", "member": "bot"}, &plain)
	if plain.Count != 0 {
		t.Fatalf("after ack = %+v, want empty", plain)
	}
}
