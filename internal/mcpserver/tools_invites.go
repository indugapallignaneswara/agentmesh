package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// registerInviteTools wires the M1.4 invite and room-policy tools.
func registerInviteTools(s *mcp.Server, svc *workspace.Service) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "room_invite_create",
		Description: "Mint an invite code for the room (human moderator only). The code is returned ONCE and never stored — treat it as a secret and hand it to the invitee out of band. kind fixes the invitee kind; role 'moderator' grants moderation on join; max_uses caps redemptions (default 1); ttl_seconds bounds its lifetime (0 = no expiry).",
	}, roomInviteCreateHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "room_invite_revoke",
		Description: "Revoke one of the room's invites by id so it can no longer be redeemed. Requires a human moderator.",
	}, roomInviteRevokeHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "room_invites",
		Description: "List the room's invites newest-first, with usage and revocation state (codes are never shown). Requires a human moderator.",
	}, roomInvitesHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "room_set_policy",
		Description: "Set the room's policies (human moderator only). Both arguments are required and fully replace the current policies: join_policy 'open' or 'invite' (invite-only rooms require an invite code to join), who_may_broadcast 'anyone' or 'moderators' (restrict fan-out to the human owner/moderators).",
	}, roomSetPolicyHandler(svc))
}

type roomInviteCreateArgs struct {
	Workspace  string `json:"workspace" jsonschema:"the room (workspace) name"`
	Actor      string `json:"actor" jsonschema:"the human moderator minting the invite"`
	Kind       string `json:"kind" jsonschema:"the invitee kind the code admits: 'human' or 'agent'"`
	Role       string `json:"role,omitempty" jsonschema:"role granted on join: 'member' (default) or 'moderator'"`
	MaxUses    int    `json:"max_uses,omitempty" jsonschema:"how many joins the code admits (default 1, max 1000)"`
	TTLSeconds int    `json:"ttl_seconds,omitempty" jsonschema:"seconds until the code expires (0 or omitted = no expiry)"`
}

type roomInviteRevokeArgs struct {
	Workspace string `json:"workspace" jsonschema:"the room (workspace) name"`
	Actor     string `json:"actor" jsonschema:"the human moderator revoking the invite"`
	ID        string `json:"id" jsonschema:"the invite id to revoke (from room_invite_create or room_invites)"`
}

type roomInvitesArgs struct {
	Workspace string `json:"workspace" jsonschema:"the room (workspace) name"`
	Actor     string `json:"actor" jsonschema:"the human moderator requesting the list"`
}

type roomSetPolicyArgs struct {
	Workspace       string `json:"workspace" jsonschema:"the room (workspace) name"`
	Actor           string `json:"actor" jsonschema:"the human moderator changing the policy"`
	JoinPolicy      string `json:"join_policy" jsonschema:"who may join: 'open' or 'invite'"`
	WhoMayBroadcast string `json:"who_may_broadcast" jsonschema:"who may broadcast: 'anyone' or 'moderators'"`
}

type roomInviteCreateResult struct {
	Code   string       `json:"code"`
	Invite model.Invite `json:"invite"`
}

type roomInvitesResult struct {
	Invites []model.Invite `json:"invites"`
	Count   int            `json:"count"`
}

func roomInviteCreateHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, roomInviteCreateArgs) (*mcp.CallToolResult, roomInviteCreateResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a roomInviteCreateArgs) (*mcp.CallToolResult, roomInviteCreateResult, error) {
		code, inv, err := svc.RoomInviteCreate(ctx, a.Workspace, a.Actor,
			model.Kind(a.Kind), model.Role(a.Role), a.MaxUses, time.Duration(a.TTLSeconds)*time.Second)
		if err != nil {
			return failInv[roomInviteCreateResult](err)
		}
		return ok(roomInviteCreateResult{Code: code, Invite: inv})
	}
}

func roomInviteRevokeHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, roomInviteRevokeArgs) (*mcp.CallToolResult, okResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a roomInviteRevokeArgs) (*mcp.CallToolResult, okResult, error) {
		if err := svc.RoomInviteRevoke(ctx, a.Workspace, a.Actor, a.ID); err != nil {
			return failInv[okResult](err)
		}
		return ok(okResult{OK: true})
	}
}

func roomInvitesHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, roomInvitesArgs) (*mcp.CallToolResult, roomInvitesResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a roomInvitesArgs) (*mcp.CallToolResult, roomInvitesResult, error) {
		invs, err := svc.RoomInvites(ctx, a.Workspace, a.Actor)
		if err != nil {
			return failInv[roomInvitesResult](err)
		}
		return ok(roomInvitesResult{Invites: invs, Count: len(invs)})
	}
}

func roomSetPolicyHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, roomSetPolicyArgs) (*mcp.CallToolResult, model.Workspace, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a roomSetPolicyArgs) (*mcp.CallToolResult, model.Workspace, error) {
		w, err := svc.RoomSetPolicy(ctx, a.Workspace, a.Actor,
			model.JoinPolicy(a.JoinPolicy), model.BroadcastPolicy(a.WhoMayBroadcast))
		if err != nil {
			return failInv[model.Workspace](err)
		}
		return ok(w)
	}
}

// failInv converts expected invite/policy service errors into tool errors the
// caller can read and recover from; unexpected errors become protocol errors.
func failInv[T any](err error) (*mcp.CallToolResult, T, error) {
	var zero T
	if errors.Is(err, workspace.ErrInvalidInput) ||
		errors.Is(err, workspace.ErrRoomClosed) ||
		errors.Is(err, store.ErrNotFound) ||
		errors.Is(err, store.ErrBanned) ||
		errors.Is(err, store.ErrInviteSpent) ||
		errors.Is(err, store.ErrRoomExists) ||
		errors.Is(err, auth.ErrForbidden) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		}, zero, nil
	}
	return nil, zero, err
}
