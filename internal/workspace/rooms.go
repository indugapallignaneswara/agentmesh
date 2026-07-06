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
	w, err := s.store.GetWorkspace(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		if s.implicitRoom {
			if _, err := s.store.EnsureWorkspace(ctx, name, s.now()); err != nil {
				return err
			}
			return nil // freshly created rooms are open
		}
		return fmt.Errorf("room %q: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return err
	}
	if w.Status != model.WorkspaceOpen {
		return fmt.Errorf("%w: %q", ErrRoomClosed, name)
	}
	return nil
}
