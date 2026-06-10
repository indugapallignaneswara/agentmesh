package store

import (
	"context"
	"sort"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

func (s *Memory) CreateAuthToken(_ context.Context, t model.AuthToken) (model.AuthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = append(s.tokens, t)
	return t, nil
}

func (s *Memory) GetAuthTokenByHash(_ context.Context, hash string, now time.Time) (model.AuthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tokens {
		if t.TokenHash == hash && tokenActive(t, now) {
			return t, nil
		}
	}
	return model.AuthToken{}, ErrNotFound
}

func (s *Memory) RevokeAuthToken(_ context.Context, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tokens {
		if s.tokens[i].ID == id && s.tokens[i].RevokedAt == nil {
			t := now
			s.tokens[i].RevokedAt = &t
			return nil
		}
	}
	return ErrNotFound
}

func (s *Memory) ListAuthTokens(_ context.Context, workspace string) ([]model.AuthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.AuthToken
	for _, t := range s.tokens {
		if t.Workspace == workspace {
			out = append(out, t)
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

// tokenActive mirrors the Postgres WHERE clause: not revoked, not expired.
func tokenActive(t model.AuthToken, now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	if t.ExpiresAt != nil && !t.ExpiresAt.After(now) {
		return false
	}
	return true
}
