package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/indugapallignaneswara/agentmesh/internal/client"
)

// cmdUsage prints a room's coordination usage: who wrote how much into the
// room and how much the mesh returned into each member's context window.
// `coord usage report ...` posts client-reported vendor token usage instead.
func cmdUsage(ctx context.Context, cl *client.Client, out *output, args []string) error {
	if len(args) > 0 && args[0] == "report" {
		return cmdUsageReport(ctx, cl, out, args[1:])
	}
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace name")
	hours := fs.Int("hours", 24, "trailing window in hours")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ws == "" {
		return fmt.Errorf("--workspace (or AGENTMESH_WORKSPACE) is required")
	}

	raw, err := cl.Raw(ctx, "usage_stats", map[string]any{
		"workspace": *ws, "since_hours": *hours,
	})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Workspace    string  `json:"workspace"`
			IngressBytes int64   `json:"ingress_bytes"`
			EgressBytes  int64   `json:"egress_bytes"`
			EstTokens    int64   `json:"est_tokens"`
			BytesPerTok  float64 `json:"bytes_per_token"`
			Members      []struct {
				Member                   string `json:"member"`
				Kind                     string `json:"kind"`
				IngressBytes             int64  `json:"ingress_bytes"`
				EgressBytes              int64  `json:"egress_bytes"`
				Events                   int64  `json:"events"`
				EstTokens                int64  `json:"est_tokens"`
				ReportedPromptTokens     int64  `json:"reported_prompt_tokens"`
				ReportedCompletionTokens int64  `json:"reported_completion_tokens"`
			} `json:"members"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			fmt.Fprintf(w, "%s\n", b)
			return
		}
		fmt.Fprintf(w, "usage for %q (last %dh): in %s, out %s, ~%d est tokens (bytes/token=%.1f)\n",
			r.Workspace, *hours, fmtBytes(r.IngressBytes), fmtBytes(r.EgressBytes), r.EstTokens, r.BytesPerTok)
		if len(r.Members) == 0 {
			fmt.Fprintln(w, "  (no metered activity in window)")
			return
		}
		for _, m := range r.Members {
			name := m.Member
			if name == "" {
				// Calls with no attributable member arg (e.g. usage_stats
				// itself in auth-off mode) aggregate here.
				name = "(unattributed)"
			}
			// Reported vendor tokens are unverified client claims; shown only
			// when present, and always under the "reported" label.
			reported := ""
			if m.ReportedPromptTokens+m.ReportedCompletionTokens > 0 {
				reported = fmt.Sprintf(" reported: %d in / %d out",
					m.ReportedPromptTokens, m.ReportedCompletionTokens)
			}
			fmt.Fprintf(w, "  %-20s %-6s in %-10s out %-10s ~%d tokens (%d calls)%s\n",
				name, m.Kind, fmtBytes(m.IngressBytes), fmtBytes(m.EgressBytes), m.EstTokens, m.Events, reported)
		}
	})
	return nil
}

// cmdUsageReport posts client-reported vendor token usage for a member via
// the usage_report tool. The numbers are recorded as claims — labelled
// "reported", never verified, never a billing source alone.
func cmdUsageReport(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("usage report", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace name")
	member := stringFlag(fs, "member", "AGENTMESH_MEMBER", "", "reporting member")
	prompt := fs.Int64("prompt", 0, "prompt tokens consumed since the previous report")
	completion := fs.Int64("completion", 0, "completion tokens produced since the previous report")
	vendor := fs.String("vendor", "", "LLM vendor (e.g. anthropic)")
	mdl := fs.String("model", "", "model identifier (e.g. claude-sonnet-4-5)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ws == "" {
		return fmt.Errorf("--workspace (or AGENTMESH_WORKSPACE) is required")
	}
	if *member == "" {
		return fmt.Errorf("--member (or AGENTMESH_MEMBER) is required")
	}

	a := map[string]any{
		"workspace": *ws, "member": *member,
		"prompt_tokens": *prompt, "completion_tokens": *completion,
	}
	if *vendor != "" {
		a["vendor"] = *vendor
	}
	if *mdl != "" {
		a["model"] = *mdl
	}
	raw, err := cl.Raw(ctx, "usage_report", a)
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Recorded  bool   `json:"recorded"`
			Workspace string `json:"workspace"`
			Member    string `json:"member"`
		}
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "recorded reported usage for %s/%s: %d prompt / %d completion (client-claimed)\n",
			r.Workspace, r.Member, *prompt, *completion)
	})
	return nil
}

// fmtBytes renders a byte count human-readably (B/KB/MB).
func fmtBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
