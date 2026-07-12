package store

import (
	"context"
	"sort"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

func (s *Memory) SetMemberRole(_ context.Context, workspace, name string, role model.Role) (model.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := memKey(workspace, name)
	m, ok := s.members[k]
	if !ok {
		return model.Member{}, ErrNotFound
	}
	m.Role = role
	s.members[k] = m
	return m, nil
}

func (s *Memory) RemoveMember(_ context.Context, workspace, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := memKey(workspace, name)
	if _, ok := s.members[k]; !ok {
		return ErrNotFound
	}
	delete(s.members, k)
	// Purge the departed member's undelivered deliveries so it stops accruing
	// inbox rows; delivered rows are history and stay.
	kept := s.deliv[:0]
	for _, d := range s.deliv {
		if d.workspace == workspace && d.recipient == name && d.deliveredAt == nil {
			continue
		}
		kept = append(kept, d)
	}
	s.deliv = kept
	return nil
}

func (s *Memory) CreateBan(_ context.Context, b model.Ban) (model.Ban, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bans[memKey(b.Workspace, b.Name)] = b
	return b, nil
}

func (s *Memory) GetBan(_ context.Context, workspace, name string) (model.Ban, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bans[memKey(workspace, name)]
	if !ok {
		return model.Ban{}, ErrNotFound
	}
	return b, nil
}

func (s *Memory) RemoveBan(_ context.Context, workspace, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := memKey(workspace, name)
	if _, ok := s.bans[k]; !ok {
		return ErrNotFound
	}
	delete(s.bans, k)
	return nil
}

func (s *Memory) ListBans(_ context.Context, workspace string) ([]model.Ban, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.Ban
	for _, b := range s.bans {
		if b.Workspace == workspace {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Memory) ListMessages(_ context.Context, workspace, afterID string, limit int) ([]model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit = clampMessageLimit(limit)

	var all []model.Message
	for _, mm := range s.messages {
		if mm.msg.Workspace == workspace {
			all = append(all, mm.msg)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID < all[j].ID
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})

	// afterID pages from the last-seen id; an unknown id pages from the start.
	start := 0
	if afterID != "" {
		for i, m := range all {
			if m.ID == afterID {
				start = i + 1
				break
			}
		}
	}
	out := all[start:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// clampMessageLimit applies the ListMessages paging bounds: a non-positive
// limit falls back to 50 and anything above 500 is capped at 500.
func clampMessageLimit(limit int) int {
	switch {
	case limit <= 0:
		return 50
	case limit > 500:
		return 500
	default:
		return limit
	}
}
