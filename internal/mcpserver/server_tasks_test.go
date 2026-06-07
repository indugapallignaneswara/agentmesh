package mcpserver_test

import (
	"testing"
)

// TestMCPTaskToolsRegistered confirms the Phase 1 task tools appear over MCP
// (this also exercises schema generation for model.Task, which has pointer
// time.Time fields — a regression here would fail tool registration).
func TestMCPTaskToolsRegistered(t *testing.T) {
	cs, ctx := connect(t)
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{"create_task", "claim_task", "complete_task", "get_task", "list_tasks"} {
		if !got[want] {
			t.Errorf("missing task tool %q", want)
		}
	}
}

// TestMCPTaskLifecycle drives the full create -> claim -> complete loop over the
// MCP transport, including the "nothing to claim" result shape.
func TestMCPTaskLifecycle(t *testing.T) {
	cs, ctx := connect(t)

	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "alice", "kind": "human"}, nil)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "worker", "kind": "agent"}, nil)

	// Create a task.
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	callJSON(t, cs, ctx, "create_task", map[string]any{
		"workspace": "team", "creator": "alice", "title": "ship the release",
	}, &created)
	if created.ID == "" || created.Status != "pending" {
		t.Fatalf("created = %+v", created)
	}

	// Claim it.
	var claim struct {
		Claimable bool `json:"claimable"`
		Task      struct {
			ID            string `json:"id"`
			AssignedAgent string `json:"assigned_agent"`
			Status        string `json:"status"`
		} `json:"task"`
	}
	callJSON(t, cs, ctx, "claim_task",
		map[string]any{"workspace": "team", "agent": "worker"}, &claim)
	if !claim.Claimable || claim.Task.ID != created.ID || claim.Task.AssignedAgent != "worker" {
		t.Fatalf("claim = %+v", claim)
	}

	// Nothing left to claim.
	var empty struct {
		Claimable bool `json:"claimable"`
	}
	callJSON(t, cs, ctx, "claim_task",
		map[string]any{"workspace": "team", "agent": "worker"}, &empty)
	if empty.Claimable {
		t.Fatalf("expected claimable=false when pool empty, got %+v", empty)
	}

	// Complete it.
	var done struct {
		Status string `json:"status"`
		Result string `json:"result"`
	}
	callJSON(t, cs, ctx, "complete_task", map[string]any{
		"workspace": "team", "id": created.ID, "agent": "worker", "result": "done",
	}, &done)
	if done.Status != "completed" || done.Result != "done" {
		t.Fatalf("done = %+v", done)
	}

	// list_tasks shows it completed.
	var list struct {
		Count int `json:"count"`
		Tasks []struct {
			Status string `json:"status"`
		} `json:"tasks"`
	}
	callJSON(t, cs, ctx, "list_tasks",
		map[string]any{"workspace": "team", "statuses": []string{"completed"}}, &list)
	if list.Count != 1 || list.Tasks[0].Status != "completed" {
		t.Fatalf("list = %+v", list)
	}
}

// TestMCPTaskErrors confirms client errors surface as tool errors.
func TestMCPTaskErrors(t *testing.T) {
	cs, ctx := connect(t)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "alice", "kind": "human"}, nil)

	// Dangling dependency.
	callExpectError(t, cs, ctx, "create_task", map[string]any{
		"workspace": "team", "creator": "alice", "title": "x", "depends_on": []string{"ghost"},
	})
	// Empty title.
	callExpectError(t, cs, ctx, "create_task", map[string]any{
		"workspace": "team", "creator": "alice", "title": "",
	})
	// Completing a non-existent task.
	callExpectError(t, cs, ctx, "complete_task", map[string]any{
		"workspace": "team", "id": "nope", "agent": "alice",
	})
}
