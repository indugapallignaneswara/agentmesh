package mcpserver_test

import (
	"strings"
	"testing"
)

// TestMCPInviteFlow drives the M1.4 loop over MCP: make a room invite-only,
// mint an invite, watch a bare join bounce, then join with the code.
func TestMCPInviteFlow(t *testing.T) {
	cs, ctx := connectExplicit(t)

	callJSON(t, cs, ctx, "room_create", map[string]any{"name": "team", "creator": "alice"}, nil)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "alice", "kind": "human"}, nil)

	// Lock the room down.
	var room struct {
		JoinPolicy      string `json:"join_policy"`
		WhoMayBroadcast string `json:"who_may_broadcast"`
	}
	callJSON(t, cs, ctx, "room_set_policy", map[string]any{
		"workspace": "team", "actor": "alice",
		"join_policy": "invite", "who_may_broadcast": "anyone",
	}, &room)
	if room.JoinPolicy != "invite" || room.WhoMayBroadcast != "anyone" {
		t.Fatalf("room policy = %+v", room)
	}

	// A bare join is now rejected.
	callExpectError(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "bot", "kind": "agent"})

	// Mint an agent invite; the code comes back exactly once.
	var created struct {
		Code   string `json:"code"`
		Invite struct {
			ID      string `json:"id"`
			MaxUses int    `json:"max_uses"`
		} `json:"invite"`
	}
	callJSON(t, cs, ctx, "room_invite_create",
		map[string]any{"workspace": "team", "actor": "alice", "kind": "agent"}, &created)
	if !strings.HasPrefix(created.Code, "ami_") || created.Invite.MaxUses != 1 {
		t.Fatalf("created invite = %+v", created)
	}

	// Joining with the code succeeds.
	callJSON(t, cs, ctx, "workspace_join", map[string]any{
		"workspace": "team", "name": "bot", "kind": "agent", "invite_code": created.Code,
	}, nil)

	// Presence shows both members.
	var presence struct {
		Count int `json:"count"`
	}
	callJSON(t, cs, ctx, "workspace_presence", map[string]any{"workspace": "team"}, &presence)
	if presence.Count != 2 {
		t.Fatalf("presence = %d, want 2", presence.Count)
	}

	// The single-use code is spent; a reuse fails.
	callExpectError(t, cs, ctx, "workspace_join", map[string]any{
		"workspace": "team", "name": "bot2", "kind": "agent", "invite_code": created.Code,
	})

	// The moderator sees the burned use; revoking twice errors.
	var list struct {
		Count   int `json:"count"`
		Invites []struct {
			ID   string `json:"id"`
			Uses int    `json:"uses"`
		} `json:"invites"`
	}
	callJSON(t, cs, ctx, "room_invites", map[string]any{"workspace": "team", "actor": "alice"}, &list)
	if list.Count != 1 || list.Invites[0].Uses != 1 {
		t.Fatalf("room_invites = %+v", list)
	}
	callJSON(t, cs, ctx, "room_invite_revoke",
		map[string]any{"workspace": "team", "actor": "alice", "id": created.Invite.ID}, nil)
	callExpectError(t, cs, ctx, "room_invite_revoke",
		map[string]any{"workspace": "team", "actor": "alice", "id": created.Invite.ID})
}
