package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// registerUsageTools wires the usage-visibility surface (M6). Metering is a
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
