package mcpserver_test

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPArtifactConflictGuidance drives create -> concurrent edit -> stale
// write over MCP and checks the conflict error coaches the agent through the
// get -> merge -> retry protocol with the exact current version.
func TestMCPArtifactConflictGuidance(t *testing.T) {
	cs, ctx := connect(t)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "agent-a", "kind": "agent"}, nil)
	callJSON(t, cs, ctx, "workspace_join",
		map[string]any{"workspace": "team", "name": "agent-b", "kind": "agent"}, nil)

	callJSON(t, cs, ctx, "update_artifact", map[string]any{
		"workspace": "team", "author": "agent-a", "name": "plan",
		"content": "step 1", "base_version": 0,
	}, nil)
	callJSON(t, cs, ctx, "update_artifact", map[string]any{
		"workspace": "team", "author": "agent-b", "name": "plan",
		"content": "step 1\nstep 2 (b)", "base_version": 1,
	}, nil)

	// agent-a writes from the stale base 1 -> actionable tool error.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "update_artifact", Arguments: map[string]any{
		"workspace": "team", "author": "agent-a", "name": "plan",
		"content": "stale", "base_version": 1,
	}})
	if err != nil {
		t.Fatalf("transport err: %v", err)
	}
	if !res.IsError {
		t.Fatal("stale write succeeded; lost update!")
	}
	msg := textOf(res)
	if !strings.Contains(msg, "version 2") || !strings.Contains(msg, "base_version=2") {
		t.Fatalf("conflict message not actionable: %q", msg)
	}

	// Follow the guidance: get, merge, retry.
	var cur struct {
		Content string `json:"content"`
		Version int64  `json:"version"`
	}
	callJSON(t, cs, ctx, "get_artifact", map[string]any{"workspace": "team", "name": "plan"}, &cur)
	var merged struct {
		Version int64 `json:"version"`
	}
	callJSON(t, cs, ctx, "update_artifact", map[string]any{
		"workspace": "team", "author": "agent-a", "name": "plan",
		"content": cur.Content + "\nstep 3 (a)", "base_version": cur.Version,
	}, &merged)
	if merged.Version != 3 {
		t.Fatalf("merged version = %d, want 3", merged.Version)
	}

	var list struct {
		Count int `json:"count"`
	}
	callJSON(t, cs, ctx, "list_artifacts", map[string]any{"workspace": "team"}, &list)
	if list.Count != 1 {
		t.Fatalf("list = %+v", list)
	}
}
