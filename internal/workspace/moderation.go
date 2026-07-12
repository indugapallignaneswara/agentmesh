package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

const (
	maxBanReason      = 512
	maxHistoryLimit   = 200
	defaultHistoryLim = 50
)

// requireModerator verifies actor is a human member of the room with authority
// to moderate. Authority = role owner/moderator. For rooms with no recorded
// owner (implicit/demo rooms created by a bare join), any human member is
// treated as a moderator so the zero-setup demo stays usable; once a room has
// an owner, only owner/moderator roles qualify.
func (s *Service) requireModerator(ctx context.Context, workspace, actor string) (model.Member, error) {
	if err := validName("actor", actor); err != nil {
		return model.Member{}, err
	}
	if err := auth.CheckActor(ctx, workspace, actor); err != nil {
		return model.Member{}, err
	}
	m, err := s.store.GetMember(ctx, workspace, actor)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return model.Member{}, fmt.Errorf("member %q: %w", actor, store.ErrNotFound)
		}
		return model.Member{}, err
	}
	if m.Kind != model.KindHuman {
		return model.Member{}, fmt.Errorf("%w: %q is not a human member (moderation requires a human)", ErrInvalidInput, actor)
	}
	if m.Role == model.RoleOwner || m.Role == model.RoleModerator {
		return m, nil
	}
	// Implicit/demo room with no owner: any human moderates.
	if room, rerr := s.store.GetWorkspace(ctx, workspace); rerr == nil && room.CreatedBy == "" {
		return m, nil
	}
	return model.Member{}, fmt.Errorf("%w: %q is not a moderator", ErrInvalidInput, actor)
}

// RoomKick removes a member from the room and purges its undelivered messages.
// The actor must be a moderator; owners cannot be kicked, and you cannot kick
// yourself (use leave).
func (s *Service) RoomKick(ctx context.Context, workspace, actor, target string) error {
	if err := validName("target", target); err != nil {
		return err
	}
	mod, err := s.requireModerator(ctx, workspace, actor)
	if err != nil {
		return err
	}
	if target == actor {
		return fmt.Errorf("%w: cannot kick yourself (use leave)", ErrInvalidInput)
	}
	if err := s.guardTargetRemovable(ctx, workspace, target, mod); err != nil {
		return err
	}
	if err := s.store.RemoveMember(ctx, workspace, target); err != nil {
		return err
	}
	s.appendEvent(ctx, workspace, actor, EventMemberKicked, map[string]any{"target": target})
	return nil
}

// RoomBan removes a member (if present) and blocks the name from rejoining.
func (s *Service) RoomBan(ctx context.Context, workspace, actor, target, reason string) (model.Ban, error) {
	if err := validName("target", target); err != nil {
		return model.Ban{}, err
	}
	if len(reason) > maxBanReason {
		return model.Ban{}, fmt.Errorf("%w: reason must be at most %d bytes", ErrInvalidInput, maxBanReason)
	}
	mod, err := s.requireModerator(ctx, workspace, actor)
	if err != nil {
		return model.Ban{}, err
	}
	if target == actor {
		return model.Ban{}, fmt.Errorf("%w: cannot ban yourself", ErrInvalidInput)
	}
	// If the target is a current member, they must be removable (not an owner).
	if _, gerr := s.store.GetMember(ctx, workspace, target); gerr == nil {
		if err := s.guardTargetRemovable(ctx, workspace, target, mod); err != nil {
			return model.Ban{}, err
		}
		if err := s.store.RemoveMember(ctx, workspace, target); err != nil {
			return model.Ban{}, err
		}
	} else if !errors.Is(gerr, store.ErrNotFound) {
		return model.Ban{}, gerr
	}
	ban, err := s.store.CreateBan(ctx, model.Ban{
		Workspace: workspace, Name: target, BannedBy: actor, Reason: reason, CreatedAt: s.now(),
	})
	if err != nil {
		return model.Ban{}, err
	}
	s.appendEvent(ctx, workspace, actor, EventMemberBanned, map[string]any{"target": target})
	return ban, nil
}

// RoomUnban lifts a ban so the name may rejoin.
func (s *Service) RoomUnban(ctx context.Context, workspace, actor, target string) error {
	if err := validName("target", target); err != nil {
		return err
	}
	if _, err := s.requireModerator(ctx, workspace, actor); err != nil {
		return err
	}
	if err := s.store.RemoveBan(ctx, workspace, target); err != nil {
		return err // ErrNotFound if no such ban
	}
	s.appendEvent(ctx, workspace, actor, EventMemberUnbanned, map[string]any{"target": target})
	return nil
}

// RoomListBans returns the room's active bans (moderator only).
func (s *Service) RoomListBans(ctx context.Context, workspace, actor string) ([]model.Ban, error) {
	if err := validName("workspace", workspace); err != nil {
		return nil, err
	}
	if _, err := s.requireModerator(ctx, workspace, actor); err != nil {
		return nil, err
	}
	return s.store.ListBans(ctx, workspace)
}

// RoomSetRole changes a member's role. Only an owner may change roles, and the
// owner role cannot be assigned or removed via this call (ownership is fixed at
// creation in M1).
func (s *Service) RoomSetRole(ctx context.Context, workspace, actor, target string, role model.Role) (model.Member, error) {
	if err := validName("target", target); err != nil {
		return model.Member{}, err
	}
	if role != model.RoleModerator && role != model.RoleMember {
		return model.Member{}, fmt.Errorf("%w: role must be %q or %q", ErrInvalidInput, model.RoleModerator, model.RoleMember)
	}
	actorMember, err := s.requireModerator(ctx, workspace, actor)
	if err != nil {
		return model.Member{}, err
	}
	if actorMember.Role != model.RoleOwner {
		return model.Member{}, fmt.Errorf("%w: only the room owner may change roles", ErrInvalidInput)
	}
	tgt, err := s.store.GetMember(ctx, workspace, target)
	if err != nil {
		return model.Member{}, err
	}
	if tgt.Role == model.RoleOwner {
		return model.Member{}, fmt.Errorf("%w: cannot change the owner's role", ErrInvalidInput)
	}
	updated, err := s.store.SetMemberRole(ctx, workspace, target, role)
	if err != nil {
		return model.Member{}, err
	}
	s.appendEvent(ctx, workspace, actor, EventRoleChanged, map[string]any{"target": target, "role": role})
	return updated, nil
}

// Leave removes the caller from the room (self-service departure). Removing the
// owner is allowed but leaves the room ownerless — closing/reopening then falls
// back to any-human authority.
func (s *Service) Leave(ctx context.Context, workspace, actor string) error {
	if err := validName("workspace", workspace); err != nil {
		return err
	}
	if err := validName("actor", actor); err != nil {
		return err
	}
	if err := auth.CheckActor(ctx, workspace, actor); err != nil {
		return err
	}
	if err := s.store.RemoveMember(ctx, workspace, actor); err != nil {
		return err // ErrNotFound if not a member
	}
	s.appendEvent(ctx, workspace, actor, EventMemberLeft, map[string]any{})
	return nil
}

// MessageHistory returns a room's messages oldest-first for human review
// (non-consuming). Restricted to human members: agents keep their consume-once
// inbox and never get a bulk read of everyone's traffic.
func (s *Service) MessageHistory(ctx context.Context, workspace, viewer, afterID string, limit int) ([]model.Message, error) {
	if err := validName("workspace", workspace); err != nil {
		return nil, err
	}
	if err := auth.CheckActor(ctx, workspace, viewer); err != nil {
		return nil, err
	}
	if err := s.requireHuman(ctx, workspace, viewer); err != nil {
		return nil, err
	}
	switch {
	case limit <= 0:
		limit = defaultHistoryLim
	case limit > maxHistoryLimit:
		limit = maxHistoryLimit
	}
	msgs, err := s.store.ListMessages(ctx, workspace, afterID, limit)
	if err != nil {
		return nil, err
	}
	s.annotateSenderKinds(ctx, workspace, msgs)
	s.touch(ctx, workspace, viewer)
	return msgs, nil
}

// MessageHistoryPeek returns a room's recent messages for the web dashboard.
// Unlike MessageHistory it takes no viewer principal: the dashboard is an
// inherently human surface (like MemoryQueuePeek), gated with the rest of the
// UI when auth is enabled. It is non-consuming and read-only.
func (s *Service) MessageHistoryPeek(ctx context.Context, workspace string, limit int) ([]model.Message, error) {
	if err := validName("workspace", workspace); err != nil {
		return nil, err
	}
	switch {
	case limit <= 0:
		limit = defaultHistoryLim
	case limit > maxHistoryLimit:
		limit = maxHistoryLimit
	}
	msgs, err := s.store.ListMessages(ctx, workspace, "", limit)
	if err != nil {
		return nil, err
	}
	s.annotateSenderKinds(ctx, workspace, msgs)
	return msgs, nil
}

// guardTargetRemovable rejects removing an owner (and a moderator by a
// moderator — only an owner may remove another moderator).
func (s *Service) guardTargetRemovable(ctx context.Context, workspace, target string, actor model.Member) error {
	tgt, err := s.store.GetMember(ctx, workspace, target)
	if err != nil {
		return err // ErrNotFound if target isn't a member
	}
	if tgt.Role == model.RoleOwner {
		return fmt.Errorf("%w: cannot remove the room owner", ErrInvalidInput)
	}
	if tgt.Role == model.RoleModerator && actor.Role != model.RoleOwner {
		return fmt.Errorf("%w: only the owner may remove a moderator", ErrInvalidInput)
	}
	return nil
}
