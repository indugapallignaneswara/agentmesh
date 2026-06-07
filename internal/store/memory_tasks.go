package store

import (
	"context"
	"fmt"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

func (s *Memory) CreateTask(_ context.Context, t model.Task, dependsOn []string) (model.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, dep := range dependsOn {
		if s.findTaskLocked(t.Workspace, dep) == nil {
			return model.Task{}, fmt.Errorf("%w: %q", ErrInvalidDependency, dep)
		}
	}
	deps := dedupeStrings(dependsOn)
	t.DependsOn = deps
	s.tasks = append(s.tasks, memTask{task: t, deps: deps})
	return t, nil
}

func (s *Memory) GetTask(_ context.Context, workspace, id string) (model.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mt := s.findTaskLocked(workspace, id)
	if mt == nil {
		return model.Task{}, ErrNotFound
	}
	return s.materialise(*mt), nil
}

func (s *Memory) ListTasks(_ context.Context, workspace string, statuses []model.TaskStatus, now time.Time) ([]model.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.Task
	for _, mt := range s.tasks {
		if mt.task.Workspace != workspace {
			continue
		}
		t := s.materialise(mt)
		t.Status = effectiveStatus(t, now)
		out = append(out, t)
	}
	// s.tasks is already in insertion (created_at) order.
	return filterByStatus(out, statuses), nil
}

func (s *Memory) ClaimNextTask(_ context.Context, workspace, agent string, now time.Time, lease time.Duration) (model.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tasks {
		mt := &s.tasks[i]
		if mt.task.Workspace != workspace {
			continue
		}
		if !s.eligibleLocked(mt, now) {
			continue
		}
		expires := now.Add(lease)
		mt.task.Status = model.TaskClaimed
		mt.task.AssignedAgent = agent
		mt.task.ClaimedAt = &now
		mt.task.LeaseExpiresAt = &expires
		mt.task.UpdatedAt = now
		return s.materialise(*mt), nil
	}
	return model.Task{}, ErrNoClaimableTask
}

func (s *Memory) CompleteTask(_ context.Context, workspace, id, agent string, status model.TaskStatus, result string, now time.Time) (model.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mt := s.findTaskLocked(workspace, id)
	if mt == nil {
		return model.Task{}, ErrNotFound
	}
	if mt.task.Status != model.TaskClaimed || mt.task.AssignedAgent != agent {
		return model.Task{}, fmt.Errorf("%w: task %q is %s (assignee %q), caller %q",
			ErrTaskConflict, id, mt.task.Status, mt.task.AssignedAgent, agent)
	}
	mt.task.Status = status
	mt.task.Result = result
	mt.task.UpdatedAt = now
	mt.task.LeaseExpiresAt = nil
	return s.materialise(*mt), nil
}

// --- helpers (caller holds s.mu) ---

// findTaskLocked returns a pointer to the stored task, or nil.
func (s *Memory) findTaskLocked(workspace, id string) *memTask {
	for i := range s.tasks {
		if s.tasks[i].task.Workspace == workspace && s.tasks[i].task.ID == id {
			return &s.tasks[i]
		}
	}
	return nil
}

// eligibleLocked reports whether a task can be claimed now: pending (or claimed
// with an expired lease) and every dependency completed.
func (s *Memory) eligibleLocked(mt *memTask, now time.Time) bool {
	switch mt.task.Status {
	case model.TaskPending:
	case model.TaskClaimed:
		if mt.task.LeaseExpiresAt == nil || mt.task.LeaseExpiresAt.After(now) {
			return false
		}
	default:
		return false
	}
	for _, dep := range mt.deps {
		d := s.findTaskLocked(mt.task.Workspace, dep)
		if d == nil || d.task.Status != model.TaskCompleted {
			return false
		}
	}
	return true
}

// materialise returns a copy of the task with DependsOn populated, so callers
// never alias the stored slice.
func (s *Memory) materialise(mt memTask) model.Task {
	t := mt.task
	t.DependsOn = append([]string(nil), mt.deps...)
	return t
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
