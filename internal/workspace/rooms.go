package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// RoomCreate creates a room owned by a human creator. This is a bootstrap
// action — the creator need not be a member yet — so it is gated by kind
// (humans control rooms) and, when auth is on, by the token's identity.
func (s *Service) RoomCreate(ctx context.Context, name, creator string) (model.Workspace, error) {
	if err := validName("name", name); err != nil {
		return model.Workspace{}, err
	}
	if err := validName("creator", creator); err != nil {
		return model.Workspace{}, err
	}
	// The creator must be (or claim to be) a human, and — when auth is on —
	// must match the presented token.
	if err := auth.CheckActor(ctx, name, creator); err != nil {
		return model.Workspace{}, err
	}
	if err := auth.CheckKind(ctx, model.KindHuman); err != nil {
		return model.Workspace{}, err
	}
	now := s.now()
	w, err := s.store.CreateWorkspace(ctx, model.Workspace{
		Name: name, Status: model.WorkspaceOpen, CreatedBy: creator,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return model.Workspace{}, err // ErrRoomExists passes through
	}
	s.appendEvent(ctx, name, creator, EventRoomCreated, map[string]any{"created_by": creator})
	return w, nil
}

// RoomClose closes a room: new writes are rejected while reads stay open. The
// actor must be a human member of the room.
func (s *Service) RoomClose(ctx context.Context, name, actor string) (model.Workspace, error) {
	return s.setRoomStatus(ctx, name, actor, model.WorkspaceClosed, EventRoomClosed)
}

// RoomReopen reopens a closed room. The actor must be a human member.
func (s *Service) RoomReopen(ctx context.Context, name, actor string) (model.Workspace, error) {
	return s.setRoomStatus(ctx, name, actor, model.WorkspaceOpen, EventRoomReopened)
}

func (s *Service) setRoomStatus(ctx context.Context, name, actor string, status model.WorkspaceStatus, event string) (model.Workspace, error) {
	if err := validName("name", name); err != nil {
		return model.Workspace{}, err
	}
	if err := validName("actor", actor); err != nil {
		return model.Workspace{}, err
	}
	if err := auth.CheckActor(ctx, name, actor); err != nil {
		return model.Workspace{}, err
	}
	// Human authority (moderator roles arrive in M1.2). requireHuman also
	// confirms the actor is a member of the room.
	if err := s.requireHuman(ctx, name, actor); err != nil {
		return model.Workspace{}, err
	}
	w, err := s.store.SetWorkspaceStatus(ctx, name, status, actor, s.now())
	if err != nil {
		return model.Workspace{}, err
	}
	s.appendEvent(ctx, name, actor, event, map[string]any{"status": status})
	return w, nil
}

// EventRoomBudgetSet is appended when a room's byte budgets change. It lives
// here rather than in service.go's event block because the budget track owns
// only this file (M8; docs/token-metering.md §7).
const EventRoomBudgetSet = "room_budget_set"

// RoomSetBudget sets the room's daily coordination-byte budgets for agent
// traffic (moderator only, mirroring RoomSetPolicy). Zero means unlimited.
// Budgets bound AGENT bytes (ingress+egress); humans are exempt by design —
// a runaway agent must never silence the humans who would stop it.
func (s *Service) RoomSetBudget(ctx context.Context, name, actor string, dailyBytes, memberDailyBytes int64) (model.Workspace, error) {
	if err := validName("workspace", name); err != nil {
		return model.Workspace{}, err
	}
	if _, err := s.requireModerator(ctx, name, actor); err != nil {
		return model.Workspace{}, err
	}
	if dailyBytes < 0 {
		return model.Workspace{}, fmt.Errorf("%w: daily_bytes must be >= 0 (0 = unlimited)", ErrInvalidInput)
	}
	if memberDailyBytes < 0 {
		return model.Workspace{}, fmt.Errorf("%w: member_daily_bytes must be >= 0 (0 = unlimited)", ErrInvalidInput)
	}
	w, err := s.store.SetWorkspaceBudget(ctx, name, dailyBytes, memberDailyBytes, s.now())
	if err != nil {
		return model.Workspace{}, err
	}
	// Drop the enforcement tracker's cached policy so the new budget takes
	// effect on the very next call, not after the policy TTL.
	s.BudgetInvalidate(name)
	s.appendEvent(ctx, name, actor, EventRoomBudgetSet, map[string]any{
		"daily_bytes": dailyBytes, "member_daily_bytes": memberDailyBytes, "by": actor,
	})
	return w, nil
}

// RoomList returns rooms, optionally filtered by status. Listing is a
// read/discovery action available to any authenticated caller.
func (s *Service) RoomList(ctx context.Context, statuses []model.WorkspaceStatus) ([]model.Workspace, error) {
	for _, st := range statuses {
		if st != model.WorkspaceOpen && st != model.WorkspaceClosed {
			return nil, fmt.Errorf("%w: unknown status %q", ErrInvalidInput, st)
		}
	}
	return s.store.ListWorkspaces(ctx, statuses)
}

// requireOpenRoom is the write-path gate. It ensures the room exists and is
// open before a message/task/memory/artifact write proceeds. In implicit-room
// mode a missing room is lazily created (open) so pre-v0.2 behaviour and the
// zero-setup demo keep working; otherwise a missing room is ErrNotFound.
func (s *Service) requireOpenRoom(ctx context.Context, name string) error {
	_, err := s.openRoom(ctx, name)
	return err
}

// openRoom is requireOpenRoom returning the room itself, for callers that
// also need its policy fields (join/broadcast gating) without a second fetch.
func (s *Service) openRoom(ctx context.Context, name string) (model.Workspace, error) {
	w, err := s.store.GetWorkspace(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		if s.implicitRoom {
			return s.store.EnsureWorkspace(ctx, name, s.now()) // freshly created rooms are open
		}
		return model.Workspace{}, fmt.Errorf("room %q: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return model.Workspace{}, err
	}
	if w.Status != model.WorkspaceOpen {
		return model.Workspace{}, fmt.Errorf("%w: %q", ErrRoomClosed, name)
	}
	return w, nil
}
