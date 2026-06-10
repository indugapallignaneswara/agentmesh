package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

// Memory is an in-memory Store implementation used for fast unit tests. It is
// safe for concurrent use. Event Seq is assigned from a single monotonic
// counter, mirroring the global bigserial used by the Postgres store.
type Memory struct {
	mu       sync.Mutex
	members  map[string]model.Member // key: workspace + "\x00" + name
	messages []memMessage
	deliv    []memDelivery
	events   []model.Event
	seq      int64
	tasks    []memTask // insertion order preserved for oldest-first claiming
	mems     []model.Memory
	arts     map[string]model.Artifact // key: workspace + "\x00" + name
	tokens   []model.AuthToken
}

// memTask holds a task plus its dependency ids. The single Memory.mu serialises
// all task operations, which gives the correct no-double-claim behaviour for a
// single process. The cross-process guarantee is the Postgres store's job
// (FOR UPDATE SKIP LOCKED) and is validated there under real concurrency.
type memTask struct {
	task model.Task
	deps []string
}

type memMessage struct {
	msg model.Message
}

type memDelivery struct {
	workspace   string
	messageID   string
	recipient   string
	deliveredAt *time.Time
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		members: make(map[string]model.Member),
		arts:    make(map[string]model.Artifact),
	}
}

func memKey(workspace, name string) string { return workspace + "\x00" + name }

func (s *Memory) UpsertMember(_ context.Context, m model.Member) (model.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := memKey(m.Workspace, m.Name)
	if existing, ok := s.members[k]; ok {
		// Preserve the original JoinedAt; refresh mutable fields.
		existing.Kind = m.Kind
		existing.AgentCard = m.AgentCard
		existing.LastSeen = m.LastSeen
		s.members[k] = existing
		return existing, nil
	}
	s.members[k] = m
	return m, nil
}

func (s *Memory) GetMember(_ context.Context, workspace, name string) (model.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.members[memKey(workspace, name)]
	if !ok {
		return model.Member{}, ErrNotFound
	}
	return m, nil
}

func (s *Memory) TouchMember(_ context.Context, workspace, name string, ts time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := memKey(workspace, name)
	m, ok := s.members[k]
	if !ok {
		return ErrNotFound
	}
	m.LastSeen = ts
	s.members[k] = m
	return nil
}

func (s *Memory) ListMembers(_ context.Context, workspace string) ([]model.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.collectMembers(workspace, nil), nil
}

func (s *Memory) ListActiveMembers(_ context.Context, workspace string, notBefore time.Time) ([]model.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.collectMembers(workspace, &notBefore), nil
}

// collectMembers returns a sorted copy of a workspace's members, optionally
// filtered to those last seen at or after notBefore. Caller holds s.mu.
func (s *Memory) collectMembers(workspace string, notBefore *time.Time) []model.Member {
	var out []model.Member
	for _, m := range s.members {
		if m.Workspace != workspace {
			continue
		}
		if notBefore != nil && m.LastSeen.Before(*notBefore) {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Memory) CreateMessage(_ context.Context, msg model.Message, recipients []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, memMessage{msg: msg})
	for _, r := range recipients {
		s.deliv = append(s.deliv, memDelivery{
			workspace: msg.Workspace,
			messageID: msg.ID,
			recipient: r,
		})
	}
	return nil
}

func (s *Memory) ReadInbox(_ context.Context, workspace, member string, now time.Time) ([]model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	byID := make(map[string]model.Message, len(s.messages))
	for _, mm := range s.messages {
		byID[mm.msg.ID] = mm.msg
	}

	var out []model.Message
	for i := range s.deliv {
		d := &s.deliv[i]
		if d.workspace != workspace || d.recipient != member || d.deliveredAt != nil {
			continue
		}
		msg, ok := byID[d.messageID]
		if !ok {
			continue
		}
		msg.Recipient = member
		out = append(out, msg)
		t := now
		d.deliveredAt = &t
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Memory) AppendEvent(_ context.Context, e model.Event) (model.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	e.Seq = s.seq
	s.events = append(s.events, e)
	return e, nil
}

func (s *Memory) EventsSince(_ context.Context, workspace string, sinceSeq int64, limit int) ([]model.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.Event
	for _, e := range s.events {
		if e.Workspace == workspace && e.Seq > sinceSeq {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Memory) Close() error { return nil }
