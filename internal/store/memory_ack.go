package store

import (
	"context"
	"sort"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

func (s *Memory) ReadInboxLeased(_ context.Context, workspace, member string, now time.Time, visibility time.Duration) ([]model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	byID := make(map[string]model.Message, len(s.messages))
	for _, mm := range s.messages {
		byID[mm.msg.ID] = mm.msg
	}

	deadline := now.Add(visibility)
	var out []model.Message
	for i := range s.deliv {
		d := &s.deliv[i]
		if d.workspace != workspace || d.recipient != member || d.deliveredAt != nil {
			continue
		}
		// Skip messages still in flight; expired leases become eligible again.
		if d.inFlightUntil != nil && d.inFlightUntil.After(now) {
			continue
		}
		msg, ok := byID[d.messageID]
		if !ok {
			continue
		}
		msg.Recipient = member
		out = append(out, msg)
		t := deadline
		d.inFlightUntil = &t
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Memory) AckInbox(_ context.Context, workspace, member string, ids []string, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	acked := 0
	for i := range s.deliv {
		d := &s.deliv[i]
		if d.workspace != workspace || d.recipient != member || d.deliveredAt != nil {
			continue
		}
		if !want[d.messageID] {
			continue
		}
		t := now
		d.deliveredAt = &t
		acked++
	}
	return acked, nil
}
