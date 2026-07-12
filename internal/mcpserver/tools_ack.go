package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// registerAckTools wires the M2.1 at-least-once acknowledgement tool.
func registerAckTools(s *mcp.Server, svc *workspace.Service) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "ack_messages",
		Description: "Acknowledge messages received via read_inbox with ack_mode=true, finalising their delivery. " +
			"Unacknowledged leased messages are redelivered after the visibility window (at-least-once). Unknown ids are ignored.",
	}, ackMessagesHandler(svc))
}

type ackMessagesArgs struct {
	Workspace string   `json:"workspace" jsonschema:"the workspace identifier"`
	Member    string   `json:"member" jsonschema:"the member acknowledging its messages"`
	IDs       []string `json:"ids" jsonschema:"message ids from a previous ack-mode read"`
}

type ackMessagesResult struct {
	Acked int `json:"acked"`
}

func ackMessagesHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, ackMessagesArgs) (*mcp.CallToolResult, ackMessagesResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a ackMessagesArgs) (*mcp.CallToolResult, ackMessagesResult, error) {
		n, err := svc.AckMessages(ctx, a.Workspace, a.Member, a.IDs)
		if err != nil {
			return fail[ackMessagesResult](err)
		}
		return ok(ackMessagesResult{Acked: n})
	}
}
