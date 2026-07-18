package mcpserver_test

// M7 «Attribute» tests: the usage_report tool records client-claimed vendor
// token usage as direction=reported ledger rows, summed per member by
// usage_stats — always labelled, never merged into measured byte series.

import (
	"strings"
	"testing"
)

func TestUsageReportToolRecordsReportedTokens(t *testing.T) {
	cs, ctx := connect(t)
	const ws = "reportroom"

	callJSON(t, cs, ctx, "workspace_join", map[string]any{
		"workspace": ws, "name": "hookbot", "kind": "agent",
	}, nil)
	callJSON(t, cs, ctx, "workspace_join", map[string]any{
		"workspace": ws, "name": "alice", "kind": "human",
	}, nil)

	// Two reports from the same member must sum in the window.
	var rep struct {
		Recorded  bool   `json:"recorded"`
		Workspace string `json:"workspace"`
		Member    string `json:"member"`
	}
	callJSON(t, cs, ctx, "usage_report", map[string]any{
		"workspace": ws, "member": "hookbot",
		"prompt_tokens": 18234, "completion_tokens": 412,
		"vendor": "anthropic", "model": "claude-sonnet-4-5",
	}, &rep)
	if !rep.Recorded || rep.Workspace != ws || rep.Member != "hookbot" {
		t.Fatalf("usage_report result = %+v", rep)
	}
	callJSON(t, cs, ctx, "usage_report", map[string]any{
		"workspace": ws, "member": "hookbot",
		"prompt_tokens": 1766, "completion_tokens": 88,
	}, nil)
	// Completion-only report is valid (at least one count > 0).
	callJSON(t, cs, ctx, "usage_report", map[string]any{
		"workspace": ws, "member": "alice", "completion_tokens": 5,
	}, nil)

	// usage_report appends synchronously, so usage_stats sees it immediately
	// (no recorder flush involved).
	var stats struct {
		Members []struct {
			Member                   string `json:"member"`
			Kind                     string `json:"kind"`
			ReportedPromptTokens     int64  `json:"reported_prompt_tokens"`
			ReportedCompletionTokens int64  `json:"reported_completion_tokens"`
		} `json:"members"`
	}
	callJSON(t, cs, ctx, "usage_stats", map[string]any{"workspace": ws}, &stats)

	byMember := map[string]struct {
		kind             string
		prompt, complete int64
	}{}
	for _, m := range stats.Members {
		byMember[m.Member] = struct {
			kind             string
			prompt, complete int64
		}{m.Kind, m.ReportedPromptTokens, m.ReportedCompletionTokens}
	}
	hb := byMember["hookbot"]
	if hb.prompt != 18234+1766 || hb.complete != 412+88 {
		t.Fatalf("hookbot reported = %d/%d, want %d/%d", hb.prompt, hb.complete, 18234+1766, 412+88)
	}
	if hb.kind != "agent" {
		t.Fatalf("hookbot kind = %q, want agent (member's kind on the reported row)", hb.kind)
	}
	al := byMember["alice"]
	if al.prompt != 0 || al.complete != 5 {
		t.Fatalf("alice reported = %d/%d, want 0/5", al.prompt, al.complete)
	}
}

func TestUsageReportToolRejections(t *testing.T) {
	cs, ctx := connect(t)
	const ws = "rejectroom"
	callJSON(t, cs, ctx, "workspace_join", map[string]any{
		"workspace": ws, "name": "bot", "kind": "agent",
	}, nil)

	cases := []struct {
		name string
		args map[string]any
	}{
		{"both counts zero", map[string]any{
			"workspace": ws, "member": "bot",
		}},
		{"unknown member", map[string]any{
			"workspace": ws, "member": "ghost", "prompt_tokens": 10,
		}},
		{"negative prompt", map[string]any{
			"workspace": ws, "member": "bot", "prompt_tokens": -1, "completion_tokens": 5,
		}},
		{"negative completion", map[string]any{
			"workspace": ws, "member": "bot", "prompt_tokens": 5, "completion_tokens": -1,
		}},
		{"oversize vendor", map[string]any{
			"workspace": ws, "member": "bot", "prompt_tokens": 10,
			"vendor": strings.Repeat("v", 65),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			callExpectError(t, cs, ctx, "usage_report", tc.args)
		})
	}

	// Rejected reports must leave no reported tokens behind.
	var stats struct {
		Members []struct {
			Member                   string `json:"member"`
			ReportedPromptTokens     int64  `json:"reported_prompt_tokens"`
			ReportedCompletionTokens int64  `json:"reported_completion_tokens"`
		} `json:"members"`
	}
	callJSON(t, cs, ctx, "usage_stats", map[string]any{"workspace": ws}, &stats)
	for _, m := range stats.Members {
		if m.ReportedPromptTokens != 0 || m.ReportedCompletionTokens != 0 {
			t.Fatalf("member %q has reported tokens %d/%d after rejected reports",
				m.Member, m.ReportedPromptTokens, m.ReportedCompletionTokens)
		}
	}
}
