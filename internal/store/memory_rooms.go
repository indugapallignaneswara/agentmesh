package store

import (
	"context"
	"sort"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

func (s *Memory) CreateWorkspace(_ context.Context, w model.Workspace) (model.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rooms[w.Name]; ok {
		return model.Workspace{}, ErrRoomExists
	}
	w = defaultPolicies(w)
	s.rooms[w.Name] = w
	return w, nil
}

// defaultPolicies fills the M1.4 policy fields when unset, mirroring the
// Postgres column defaults so both stores round-trip identically.
func defaultPolicies(w model.Workspace) model.Workspace {
	if w.JoinPolicy == "" {
		w.JoinPolicy = model.JoinOpen
	}
	if w.WhoMayBroadcast == "" {
		w.WhoMayBroadcast = model.BroadcastAnyone
	}
	return w
}

func (s *Memory) GetWorkspace(_ context.Context, name string) (model.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.rooms[name]
	if !ok {
		return model.Workspace{}, ErrNotFound
	}
	return w, nil
}

func (s *Memory) EnsureWorkspace(_ context.Context, name string, now time.Time) (model.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.rooms[name]; ok {
		return w, nil
	}
	w := defaultPolicies(model.Workspace{Name: name, Status: model.WorkspaceOpen, CreatedAt: now, UpdatedAt: now})
	s.rooms[name] = w
	return w, nil
}

func (s *Memory) ListWorkspaces(_ context.Context, statuses []model.WorkspaceStatus) ([]model.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[model.WorkspaceStatus]bool, len(statuses))
	for _, st := range statuses {
		want[st] = true
	}
	var out []model.Workspace
	for _, w := range s.rooms {
		if len(want) == 0 || want[w.Status] {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Memory) SetWorkspaceStatus(_ context.Context, name string, status model.WorkspaceStatus, actor string, now time.Time) (model.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.rooms[name]
	if !ok {
		return model.Workspace{}, ErrNotFound
	}
	w.Status = status
	w.UpdatedAt = now
	if status == model.WorkspaceClosed {
		w.ClosedBy = actor
		t := now
		w.ClosedAt = &t
	} else {
		w.ClosedBy = ""
		w.ClosedAt = nil
	}
	s.rooms[name] = w
	return w, nil
}
