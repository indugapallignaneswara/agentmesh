package store

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

func (s *Memory) CreateMemory(_ context.Context, m model.Memory) (model.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mems = append(s.mems, m)
	return m, nil
}

func (s *Memory) GetMemory(_ context.Context, workspace, id string) (model.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.findMemoryLocked(workspace, id); m != nil {
		return *m, nil
	}
	return model.Memory{}, ErrNotFound
}

// SearchMemories scores by the number of distinct query tokens present in the
// content (a deliberately simple stand-in for Postgres ts_rank; the shared
// contract suite only pins behaviours both engines agree on: visibility,
// more-matching-terms-ranks-higher, and limit).
func (s *Memory) SearchMemories(_ context.Context, workspace, requester, query string, limit int) ([]model.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	qTokens := tokenize(query)
	if len(qTokens) == 0 {
		return nil, nil
	}

	type scored struct {
		m     model.Memory
		score int
	}
	var hits []scored
	for _, m := range s.mems {
		if m.Workspace != workspace || !memoryVisible(m, requester) {
			continue
		}
		content := tokenSet(m.Content)
		score := 0
		for tok := range qTokens {
			if content[tok] {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, scored{m: m, score: score})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if !hits[i].m.CreatedAt.Equal(hits[j].m.CreatedAt) {
			return hits[i].m.CreatedAt.After(hits[j].m.CreatedAt)
		}
		return hits[i].m.ID < hits[j].m.ID
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]model.Memory, len(hits))
	for i, h := range hits {
		out[i] = h.m
	}
	return out, nil
}

func (s *Memory) ListPendingShared(_ context.Context, workspace string) ([]model.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.Memory
	for _, m := range s.mems {
		if m.Workspace == workspace && m.Scope == model.MemoryShared && m.Status == model.MemoryPending {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Memory) ReviewMemory(_ context.Context, workspace, id, reviewer string, approve bool, note string, now time.Time) (model.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.findMemoryLocked(workspace, id)
	if m == nil {
		return model.Memory{}, ErrNotFound
	}
	if m.Scope != model.MemoryShared || m.Status != model.MemoryPending {
		return model.Memory{}, ErrMemoryConflict
	}
	if approve {
		m.Status = model.MemoryApproved
	} else {
		m.Status = model.MemoryRejected
	}
	m.ReviewedBy = reviewer
	m.ReviewNote = note
	t := now
	m.ReviewedAt = &t
	m.UpdatedAt = now
	return *m, nil
}

// findMemoryLocked returns a pointer into s.mems, or nil. Caller holds s.mu.
func (s *Memory) findMemoryLocked(workspace, id string) *model.Memory {
	for i := range s.mems {
		if s.mems[i].Workspace == workspace && s.mems[i].ID == id {
			return &s.mems[i]
		}
	}
	return nil
}

// memoryVisible is the canonical visibility predicate, mirrored by the
// Postgres WHERE clause: own private items, or approved shared items.
func memoryVisible(m model.Memory, requester string) bool {
	switch m.Scope {
	case model.MemoryPrivate:
		return m.Owner == requester
	case model.MemoryShared:
		return m.Status == model.MemoryApproved
	default:
		return false
	}
}

// tokenize lowercases and splits on non-alphanumerics, returning the set of
// distinct tokens.
func tokenize(s string) map[string]bool {
	return tokenSet(s)
}

func tokenSet(s string) map[string]bool {
	out := make(map[string]bool)
	for _, tok := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		out[tok] = true
	}
	return out
}
