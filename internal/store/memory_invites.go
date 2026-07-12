package store

import (
	"context"
	"sort"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

func (s *Memory) CreateInvite(_ context.Context, inv model.Invite) (model.Invite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invites = append(s.invites, inv)
	return inv, nil
}

func (s *Memory) GetInviteByHash(_ context.Context, hash string) (model.Invite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, inv := range s.invites {
		if inv.CodeHash == hash {
			return inv, nil
		}
	}
	return model.Invite{}, ErrNotFound
}

func (s *Memory) RedeemInvite(_ context.Context, hash string, now time.Time) (model.Invite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.invites {
		if s.invites[i].CodeHash != hash {
			continue
		}
		if !inviteRedeemable(s.invites[i], now) {
			return model.Invite{}, ErrInviteSpent
		}
		s.invites[i].Uses++
		return s.invites[i], nil
	}
	return model.Invite{}, ErrNotFound
}

func (s *Memory) ListInvites(_ context.Context, workspace string) ([]model.Invite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.Invite
	for _, inv := range s.invites {
		if inv.Workspace == workspace {
			out = append(out, inv)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Memory) RevokeInvite(_ context.Context, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.invites {
		if s.invites[i].ID == id && s.invites[i].RevokedAt == nil {
			t := now
			s.invites[i].RevokedAt = &t
			return nil
		}
	}
	return ErrNotFound
}

func (s *Memory) SetWorkspacePolicy(_ context.Context, name string, jp model.JoinPolicy, bp model.BroadcastPolicy, now time.Time) (model.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.rooms[name]
	if !ok {
		return model.Workspace{}, ErrNotFound
	}
	w.JoinPolicy = jp
	w.WhoMayBroadcast = bp
	w.UpdatedAt = now
	s.rooms[name] = w
	return w, nil
}

// inviteRedeemable mirrors the Postgres WHERE clause: not revoked, not
// expired, uses still below max_uses.
func inviteRedeemable(inv model.Invite, now time.Time) bool {
	if inv.RevokedAt != nil {
		return false
	}
	if inv.ExpiresAt != nil && !inv.ExpiresAt.After(now) {
		return false
	}
	return inv.Uses < inv.MaxUses
}
