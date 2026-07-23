package iam

import (
	"context"
	"sync"
)

// MemAuditStore is the in-memory AuditStore (demo/tests; resets on restart).
// Events are held in append order and walked backwards on Query, so newest
// first falls out naturally.
type MemAuditStore struct {
	mu     sync.RWMutex
	events []AuditEvent
}

// NewMemAuditStore returns an empty in-memory audit store.
func NewMemAuditStore() *MemAuditStore { return &MemAuditStore{} }

func (s *MemAuditStore) Append(_ context.Context, e AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

func (s *MemAuditStore) Query(_ context.Context, f AuditFilter) ([]AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := clampAuditLimit(f.Limit)
	out := []AuditEvent{}
	for i := len(s.events) - 1; i >= 0 && len(out) < limit; i-- {
		if e := s.events[i]; auditMatches(e, f) {
			out = append(out, e)
		}
	}
	return out, nil
}

// auditMatches reports whether an event passes the filter's non-zero fields.
// The time window is inclusive-exclusive: ts >= From, ts < To.
func auditMatches(e AuditEvent, f AuditFilter) bool {
	if f.ClientID != "" && e.ClientID != f.ClientID {
		return false
	}
	if f.Subject != "" && e.Subject != f.Subject {
		return false
	}
	if f.Workspace != "" && e.Workspace != f.Workspace {
		return false
	}
	if f.Type != "" && e.Type != f.Type {
		return false
	}
	if !f.From.IsZero() && e.TS.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && !e.TS.Before(f.To) {
		return false
	}
	return true
}

// clampAuditLimit maps a requested Limit into [1, MaxAuditLimit]: zero or
// negative means DefaultAuditLimit; anything above the cap is clamped to it.
func clampAuditLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultAuditLimit
	case limit > MaxAuditLimit:
		return MaxAuditLimit
	default:
		return limit
	}
}
