package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// registerModerationTools wires the M1 moderation and history tools.
func registerModerationTools(s *mcp.Server, svc *workspace.Service) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "room_kick",
		Description: "Remove a member from the room and purge their undelivered messages. Requires a human moderator; owners cannot be kicked.",
	}, roomKickHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "room_ban",
		Description: "Remove a member (if present) and block the name from rejoining, with an optional reason. Requires a human moderator; owners cannot be banned.",
	}, roomBanHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "room_unban",
		Description: "Lift a ban so the name may rejoin the room. Requires a human moderator.",
	}, roomUnbanHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "room_bans",
		Description: "List the room's active bans. Requires a human moderator.",
	}, roomBansHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "room_set_role",
		Description: "Change a member's role to 'moderator' or 'member'. Only the room owner may change roles; the owner role itself cannot be assigned or removed.",
	}, roomSetRoleHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "workspace_leave",
		Description: "Leave the room (self-service departure). Removes the caller's membership; use room_kick to remove someone else.",
	}, workspaceLeaveHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "message_history",
		Description: "Read a room's message log oldest-first for review. Human-only and non-consuming: it never marks messages delivered and is not available to agents, who keep their consume-once inbox.",
	}, messageHistoryHandler(svc))
}

type modTargetArgs struct {
	Workspace string `json:"workspace" jsonschema:"the room (workspace) name"`
	Actor     string `json:"actor" jsonschema:"the human moderator performing the action"`
	Target    string `json:"target" jsonschema:"the member name the action applies to"`
}

type roomBanArgs struct {
	Workspace string `json:"workspace" jsonschema:"the room (workspace) name"`
	Actor     string `json:"actor" jsonschema:"the human moderator performing the action"`
	Target    string `json:"target" jsonschema:"the member name to ban"`
	Reason    string `json:"reason,omitempty" jsonschema:"optional reason recorded with the ban"`
}

type roomBansArgs struct {
	Workspace string `json:"workspace" jsonschema:"the room (workspace) name"`
	Actor     string `json:"actor" jsonschema:"the human moderator requesting the list"`
}

type roomSetRoleArgs struct {
	Workspace string `json:"workspace" jsonschema:"the room (workspace) name"`
	Actor     string `json:"actor" jsonschema:"the room owner performing the change"`
	Target    string `json:"target" jsonschema:"the member whose role to change"`
	Role      string `json:"role" jsonschema:"the new role: 'moderator' or 'member'"`
}

type workspaceLeaveArgs struct {
	Workspace string `json:"workspace" jsonschema:"the room (workspace) name"`
	Actor     string `json:"actor" jsonschema:"the member leaving the room"`
}

type messageHistoryArgs struct {
	Workspace string `json:"workspace" jsonschema:"the room (workspace) name"`
	Viewer    string `json:"viewer" jsonschema:"the human member reviewing the history"`
	AfterID   string `json:"after_id,omitempty" jsonschema:"return messages after this message id (empty for the beginning)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum messages to return (default 50, max 200)"`
}

type okResult struct {
	OK bool `json:"ok"`
}

type roomBansResult struct {
	Bans  []model.Ban `json:"bans"`
	Count int         `json:"count"`
}

type messageHistoryResult struct {
	Messages []model.Message `json:"messages"`
	Count    int             `json:"count"`
}

func roomKickHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, modTargetArgs) (*mcp.CallToolResult, okResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a modTargetArgs) (*mcp.CallToolResult, okResult, error) {
		if err := svc.RoomKick(ctx, a.Workspace, a.Actor, a.Target); err != nil {
			return failMod[okResult](err)
		}
		return ok(okResult{OK: true})
	}
}

func roomBanHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, roomBanArgs) (*mcp.CallToolResult, model.Ban, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a roomBanArgs) (*mcp.CallToolResult, model.Ban, error) {
		ban, err := svc.RoomBan(ctx, a.Workspace, a.Actor, a.Target, a.Reason)
		if err != nil {
			return failMod[model.Ban](err)
		}
		return ok(ban)
	}
}

func roomUnbanHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, modTargetArgs) (*mcp.CallToolResult, okResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a modTargetArgs) (*mcp.CallToolResult, okResult, error) {
		if err := svc.RoomUnban(ctx, a.Workspace, a.Actor, a.Target); err != nil {
			return failMod[okResult](err)
		}
		return ok(okResult{OK: true})
	}
}

func roomBansHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, roomBansArgs) (*mcp.CallToolResult, roomBansResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a roomBansArgs) (*mcp.CallToolResult, roomBansResult, error) {
		bans, err := svc.RoomListBans(ctx, a.Workspace, a.Actor)
		if err != nil {
			return failMod[roomBansResult](err)
		}
		return ok(roomBansResult{Bans: bans, Count: len(bans)})
	}
}

func roomSetRoleHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, roomSetRoleArgs) (*mcp.CallToolResult, model.Member, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a roomSetRoleArgs) (*mcp.CallToolResult, model.Member, error) {
		m, err := svc.RoomSetRole(ctx, a.Workspace, a.Actor, a.Target, model.Role(a.Role))
		if err != nil {
			return failMod[model.Member](err)
		}
		return ok(m)
	}
}

func workspaceLeaveHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, workspaceLeaveArgs) (*mcp.CallToolResult, okResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a workspaceLeaveArgs) (*mcp.CallToolResult, okResult, error) {
		if err := svc.Leave(ctx, a.Workspace, a.Actor); err != nil {
			return failMod[okResult](err)
		}
		return ok(okResult{OK: true})
	}
}

func messageHistoryHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, messageHistoryArgs) (*mcp.CallToolResult, messageHistoryResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a messageHistoryArgs) (*mcp.CallToolResult, messageHistoryResult, error) {
		msgs, err := svc.MessageHistory(ctx, a.Workspace, a.Viewer, a.AfterID, a.Limit)
		if err != nil {
			return failMod[messageHistoryResult](err)
		}
		return ok(messageHistoryResult{Messages: msgs, Count: len(msgs)})
	}
}

// failMod converts expected moderation service errors into tool errors the
// caller can read and recover from; unexpected errors become protocol errors.
func failMod[T any](err error) (*mcp.CallToolResult, T, error) {
	var zero T
	if errors.Is(err, workspace.ErrInvalidInput) ||
		errors.Is(err, workspace.ErrRoomClosed) ||
		errors.Is(err, store.ErrNotFound) ||
		errors.Is(err, store.ErrBanned) ||
		errors.Is(err, auth.ErrForbidden) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		}, zero, nil
	}
	return nil, zero, err
}
