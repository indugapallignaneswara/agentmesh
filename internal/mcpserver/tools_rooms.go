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

// registerRoomTools wires the M1 room-lifecycle tools.
func registerRoomTools(s *mcp.Server, svc *workspace.Service) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "room_create",
		Description: "Create a room (workspace) owned by a human. Rooms are human-controlled containers agents join to coordinate. Fails if the room already exists.",
	}, roomCreateHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "room_close",
		Description: "Close a room: new content (messages, tasks, memory, artifact writes, joins) is rejected while the room stays readable for review. Requires a human member.",
	}, roomCloseHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "room_reopen",
		Description: "Reopen a closed room so writes flow again. Requires a human member.",
	}, roomReopenHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "room_list",
		Description: "List rooms on this server, optionally filtered by status (open, closed).",
	}, roomListHandler(svc))
}

type roomCreateArgs struct {
	Name    string `json:"name" jsonschema:"the room (workspace) name to create"`
	Creator string `json:"creator" jsonschema:"the human creating the room"`
}

type roomModArgs struct {
	Name  string `json:"name" jsonschema:"the room name"`
	Actor string `json:"actor" jsonschema:"the human member performing the action"`
}

type roomListArgs struct {
	Statuses []string `json:"statuses,omitempty" jsonschema:"optional status filter: open, closed"`
}

type roomListResult struct {
	Rooms []model.Workspace `json:"rooms"`
	Count int               `json:"count"`
}

func roomCreateHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, roomCreateArgs) (*mcp.CallToolResult, model.Workspace, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a roomCreateArgs) (*mcp.CallToolResult, model.Workspace, error) {
		w, err := svc.RoomCreate(ctx, a.Name, a.Creator)
		if err != nil {
			return failRoom[model.Workspace](err)
		}
		return ok(w)
	}
}

func roomCloseHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, roomModArgs) (*mcp.CallToolResult, model.Workspace, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a roomModArgs) (*mcp.CallToolResult, model.Workspace, error) {
		w, err := svc.RoomClose(ctx, a.Name, a.Actor)
		if err != nil {
			return failRoom[model.Workspace](err)
		}
		return ok(w)
	}
}

func roomReopenHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, roomModArgs) (*mcp.CallToolResult, model.Workspace, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a roomModArgs) (*mcp.CallToolResult, model.Workspace, error) {
		w, err := svc.RoomReopen(ctx, a.Name, a.Actor)
		if err != nil {
			return failRoom[model.Workspace](err)
		}
		return ok(w)
	}
}

func roomListHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, roomListArgs) (*mcp.CallToolResult, roomListResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a roomListArgs) (*mcp.CallToolResult, roomListResult, error) {
		statuses := make([]model.WorkspaceStatus, len(a.Statuses))
		for i, st := range a.Statuses {
			statuses[i] = model.WorkspaceStatus(st)
		}
		rooms, err := svc.RoomList(ctx, statuses)
		if err != nil {
			return failRoom[roomListResult](err)
		}
		return ok(roomListResult{Rooms: rooms, Count: len(rooms)})
	}
}

func failRoom[T any](err error) (*mcp.CallToolResult, T, error) {
	var zero T
	if errors.Is(err, workspace.ErrInvalidInput) ||
		errors.Is(err, workspace.ErrRoomClosed) ||
		errors.Is(err, store.ErrNotFound) ||
		errors.Is(err, store.ErrRoomExists) ||
		errors.Is(err, auth.ErrForbidden) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		}, zero, nil
	}
	return nil, zero, err
}
