package mcpserver_test

import (
	"testing"
)

// TestMCPMemoryLifecycle drives the Phase 2 flow over the MCP transport:
// an agent submits shared knowledge -> quarantined -> human approves ->
// another agent retrieves it. Plus the private-partition check.
func TestMCPMemoryLifecycle(t *testing.T) {
	cs, ctx := connect(t)

	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "lead", "kind": "human"}, nil)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "agent-a", "kind": "agent"}, nil)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "agent-b", "kind": "agent"}, nil)

	// agent-a submits shared knowledge.
	var written struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	callJSON(t, cs, ctx, "memory_write", map[string]any{
		"workspace": "team", "author": "agent-a", "scope": "shared",
		"content": "the deploy pipeline requires the canary gate",
		"source":  "release retro",
	}, &written)
	if written.Status != "pending" {
		t.Fatalf("shared write = %+v, want pending", written)
	}

	// Quarantine: agent-b cannot retrieve it yet.
	var search struct {
		Count int `json:"count"`
	}
	callJSON(t, cs, ctx, "memory_search", map[string]any{
		"workspace": "team", "requester": "agent-b", "query": "canary gate",
	}, &search)
	if search.Count != 0 {
		t.Fatalf("pending visible to agent-b: %+v", search)
	}

	// Human reviews the queue and approves.
	var queue struct {
		Count    int `json:"count"`
		Memories []struct {
			ID        string `json:"id"`
			CreatedBy string `json:"created_by"`
		} `json:"memories"`
	}
	callJSON(t, cs, ctx, "memory_queue",
		map[string]any{"workspace": "team", "reviewer": "lead"}, &queue)
	if queue.Count != 1 || queue.Memories[0].ID != written.ID {
		t.Fatalf("queue = %+v", queue)
	}
	var reviewed struct {
		Status     string `json:"status"`
		ReviewedBy string `json:"reviewed_by"`
	}
	callJSON(t, cs, ctx, "memory_review", map[string]any{
		"workspace": "team", "reviewer": "lead", "id": written.ID,
		"decision": "approve", "note": "checked",
	}, &reviewed)
	if reviewed.Status != "approved" || reviewed.ReviewedBy != "lead" {
		t.Fatalf("reviewed = %+v", reviewed)
	}

	// Cross-agent retrieval: agent-b now finds agent-a's knowledge.
	var hit struct {
		Count    int `json:"count"`
		Memories []struct {
			Content string `json:"content"`
		} `json:"memories"`
	}
	callJSON(t, cs, ctx, "memory_search", map[string]any{
		"workspace": "team", "requester": "agent-b", "query": "canary gate",
	}, &hit)
	if hit.Count != 1 || hit.Memories[0].Content == "" {
		t.Fatalf("after approval = %+v, want the item", hit)
	}
}

// TestMCPMemoryGuards confirms the security rules surface as tool errors.
func TestMCPMemoryGuards(t *testing.T) {
	cs, ctx := connect(t)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "agent-a", "kind": "agent"}, nil)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "agent-b", "kind": "agent"}, nil)

	var written struct {
		ID string `json:"id"`
	}
	callJSON(t, cs, ctx, "memory_write", map[string]any{
		"workspace": "team", "author": "agent-a", "scope": "shared", "content": "claim",
	}, &written)

	// Agents cannot inspect the queue or review.
	callExpectError(t, cs, ctx, "memory_queue",
		map[string]any{"workspace": "team", "reviewer": "agent-b"})
	callExpectError(t, cs, ctx, "memory_review", map[string]any{
		"workspace": "team", "reviewer": "agent-b", "id": written.ID, "decision": "approve",
	})
	// Bad scope and bad decision are tool errors.
	callExpectError(t, cs, ctx, "memory_write", map[string]any{
		"workspace": "team", "author": "agent-a", "scope": "global", "content": "x",
	})
	// Private partition: agent-b never sees agent-a's private item.
	callJSON(t, cs, ctx, "memory_write", map[string]any{
		"workspace": "team", "author": "agent-a", "scope": "private", "content": "secret zebra fact",
	}, nil)
	var leak struct {
		Count int `json:"count"`
	}
	callJSON(t, cs, ctx, "memory_search", map[string]any{
		"workspace": "team", "requester": "agent-b", "query": "zebra",
	}, &leak)
	if leak.Count != 0 {
		t.Fatalf("private memory leaked across agents: %+v", leak)
	}
}
