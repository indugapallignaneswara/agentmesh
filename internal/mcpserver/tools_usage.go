package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// registerUsageTools wires the usage-visibility surface (M6 stats, M7
// reported vendor usage). Metering is a
// coordination feature, not an admin secret: any member may read their room's
// burn — the same posture as presence.
func registerUsageTools(s *mcp.Server, svc *workspace.Service) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "usage_stats",
		Description: "Per-member coordination usage for a workspace over a trailing window " +
			"(default 24h): bytes each member wrote into the room (ingress) and bytes the mesh " +
			"returned to them (egress — what entered their context window), with display-time " +
			"estimated tokens. Bytes are measured exactly; token counts are estimates.",
	}, usageStatsHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name: "usage_report",
		Description: "Attach your vendor-reported token usage for the current turn. These numbers " +
			"are client claims — AgentMesh records them as 'reported', never verifies them, and " +
			"never bills on them alone.",
	}, usageReportHandler(svc))
}

type usageStatsArgs struct {
	Workspace  string `json:"workspace" jsonschema:"the workspace identifier"`
	SinceHours int    `json:"since_hours,omitempty" jsonschema:"trailing window in hours (default 24, max 720)"`
}

func usageStatsHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, usageStatsArgs) (*mcp.CallToolResult, workspace.UsageStats, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a usageStatsArgs) (*mcp.CallToolResult, workspace.UsageStats, error) {
		hours := a.SinceHours
		if hours <= 0 {
			hours = 24
		}
		if hours > 720 {
			hours = 720
		}
		stats, err := svc.UsageStatsWindow(ctx, a.Workspace, time.Duration(hours)*time.Hour)
		if err != nil {
			return fail[workspace.UsageStats](err)
		}
		return ok(stats)
	}
}

type usageReportArgs struct {
	Workspace        string `json:"workspace" jsonschema:"the workspace identifier"`
	Member           string `json:"member" jsonschema:"the reporting member; with auth on it must match the token"`
	PromptTokens     int64  `json:"prompt_tokens,omitempty" jsonschema:"prompt tokens consumed at the member's own LLM vendor since its previous report"`
	CompletionTokens int64  `json:"completion_tokens,omitempty" jsonschema:"completion tokens produced at the member's own LLM vendor since its previous report"`
	Vendor           string `json:"vendor,omitempty" jsonschema:"LLM vendor, e.g. anthropic"`
	Model            string `json:"model,omitempty" jsonschema:"model identifier, e.g. claude-sonnet-4-5"`
}

type usageReportResult struct {
	Recorded  bool   `json:"recorded"`
	Workspace string `json:"workspace"`
	Member    string `json:"member"`
}

func usageReportHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, usageReportArgs) (*mcp.CallToolResult, usageReportResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a usageReportArgs) (*mcp.CallToolResult, usageReportResult, error) {
		err := svc.ReportUsage(ctx, a.Workspace, a.Member, a.PromptTokens, a.CompletionTokens, a.Vendor, a.Model)
		if err != nil {
			return fail[usageReportResult](err)
		}
		return ok(usageReportResult{Recorded: true, Workspace: a.Workspace, Member: a.Member})
	}
}
