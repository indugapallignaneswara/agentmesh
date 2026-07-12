package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// CreateTask adds a task to the shared board. The creator must be a member; the
// title is required; dependencies (if any) must reference existing tasks in the
// same workspace (the store rejects dangling ids). Returns the stored task.
func (s *Service) CreateTask(ctx context.Context, workspace, creator, title, details string, dependsOn []string) (model.Task, error) {
	if err := validName("workspace", workspace); err != nil {
		return model.Task{}, err
	}
	if err := validName("creator", creator); err != nil {
		return model.Task{}, err
	}
	if title == "" || len(title) > maxTaskTitle {
		return model.Task{}, fmt.Errorf("%w: title must be 1-%d characters", ErrInvalidInput, maxTaskTitle)
	}
	if len(details) > maxTaskDetails {
		return model.Task{}, fmt.Errorf("%w: details must be at most %d bytes", ErrInvalidInput, maxTaskDetails)
	}
	if len(dependsOn) > maxDependsOn {
		return model.Task{}, fmt.Errorf("%w: at most %d dependencies", ErrInvalidInput, maxDependsOn)
	}
	if err := auth.CheckActor(ctx, workspace, creator); err != nil {
		return model.Task{}, err
	}
	if err := s.requireOpenRoom(ctx, workspace); err != nil {
		return model.Task{}, err
	}
	if err := s.requireMember(ctx, workspace, creator); err != nil {
		return model.Task{}, err
	}

	now := s.now()
	t := model.Task{
		ID:        s.newID(),
		Workspace: workspace,
		Title:     title,
		Details:   details,
		Status:    model.TaskPending,
		CreatedBy: creator,
		CreatedAt: now,
		UpdatedAt: now,
	}
	created, err := s.store.CreateTask(ctx, t, dependsOn)
	if err != nil {
		return model.Task{}, mapTaskErr(err)
	}
	s.touch(ctx, workspace, creator)
	s.appendEvent(ctx, workspace, creator, EventTaskCreated, map[string]any{
		"task_id": created.ID, "title": title, "depends_on": dependsOn,
	})
	return created, nil
}

// ClaimTask claims the next eligible task for agent (oldest-first; dependencies
// completed). Returns store.ErrNoClaimableTask wrapped as ErrInvalidInput-free
// sentinel when nothing is available, so callers can distinguish "empty" from
// real failures.
func (s *Service) ClaimTask(ctx context.Context, workspace, agent string) (model.Task, error) {
	if err := validName("workspace", workspace); err != nil {
		return model.Task{}, err
	}
	if err := validName("agent", agent); err != nil {
		return model.Task{}, err
	}
	if err := auth.CheckActor(ctx, workspace, agent); err != nil {
		return model.Task{}, err
	}
	if err := s.requireMember(ctx, workspace, agent); err != nil {
		return model.Task{}, err
	}
	t, err := s.store.ClaimNextTask(ctx, workspace, agent, s.now(), s.taskLease)
	if err != nil {
		return model.Task{}, err // ErrNoClaimableTask passes through untouched
	}
	s.touch(ctx, workspace, agent)
	s.appendEvent(ctx, workspace, agent, EventTaskClaimed, map[string]any{
		"task_id": t.ID, "lease_expires_at": t.LeaseExpiresAt,
	})
	return t, nil
}

// CompleteTask marks a claimed task completed or failed. The caller must be the
// current assignee and the task still claimed (a lapsed lease that another
// agent stole yields ErrTaskConflict). done=false records a failure.
func (s *Service) CompleteTask(ctx context.Context, workspace, id, agent, result string, done bool) (model.Task, error) {
	if err := validName("workspace", workspace); err != nil {
		return model.Task{}, err
	}
	if err := validName("agent", agent); err != nil {
		return model.Task{}, err
	}
	if id == "" {
		return model.Task{}, fmt.Errorf("%w: task id is required", ErrInvalidInput)
	}
	if len(result) > maxTaskResult {
		return model.Task{}, fmt.Errorf("%w: result must be at most %d bytes", ErrInvalidInput, maxTaskResult)
	}
	if err := auth.CheckActor(ctx, workspace, agent); err != nil {
		return model.Task{}, err
	}
	status := model.TaskCompleted
	if !done {
		status = model.TaskFailed
	}
	t, err := s.store.CompleteTask(ctx, workspace, id, agent, status, result, s.now())
	if err != nil {
		return model.Task{}, err // ErrNotFound / ErrTaskConflict pass through
	}
	s.touch(ctx, workspace, agent)
	s.appendEvent(ctx, workspace, agent, EventTaskCompleted, map[string]any{
		"task_id": id, "status": status,
	})
	return t, nil
}

// RetryTask requeues a failed task (failed -> pending), which also unblocks any
// task that depended on it. The caller must be a member of the workspace; the
// task must currently be failed. This is the escape hatch for the otherwise
// permanent dead-end of a task that depends on a failed one.
func (s *Service) RetryTask(ctx context.Context, workspace, actor, id string) (model.Task, error) {
	if err := validName("workspace", workspace); err != nil {
		return model.Task{}, err
	}
	if err := validName("actor", actor); err != nil {
		return model.Task{}, err
	}
	if id == "" {
		return model.Task{}, fmt.Errorf("%w: task id is required", ErrInvalidInput)
	}
	if err := auth.CheckActor(ctx, workspace, actor); err != nil {
		return model.Task{}, err
	}
	if err := s.requireMember(ctx, workspace, actor); err != nil {
		return model.Task{}, err
	}
	t, err := s.store.RetryTask(ctx, workspace, id, s.now())
	if err != nil {
		return model.Task{}, err // ErrNotFound / ErrTaskConflict pass through
	}
	s.touch(ctx, workspace, actor)
	s.appendEvent(ctx, workspace, actor, EventTaskRetried, map[string]any{"task_id": id})
	return t, nil
}

// GetTask returns a single task or store.ErrNotFound.
func (s *Service) GetTask(ctx context.Context, workspace, id string) (model.Task, error) {
	if err := validName("workspace", workspace); err != nil {
		return model.Task{}, err
	}
	if id == "" {
		return model.Task{}, fmt.Errorf("%w: task id is required", ErrInvalidInput)
	}
	if err := auth.CheckWorkspace(ctx, workspace); err != nil {
		return model.Task{}, err
	}
	return s.store.GetTask(ctx, workspace, id)
}

// ListTasks returns a workspace's tasks (capped at the default list limit),
// optionally filtered to the given statuses. Reported status is effective (a
// claimed task past its lease shows as pending).
func (s *Service) ListTasks(ctx context.Context, workspace string, statuses []model.TaskStatus) ([]model.Task, error) {
	tasks, _, err := s.ListTasksPaged(ctx, workspace, statuses, 0)
	return tasks, err
}

// ListTasksPaged is ListTasks with an explicit limit, also reporting whether
// the result was truncated — so a caller knows to narrow its filter rather
// than silently miss tasks.
func (s *Service) ListTasksPaged(ctx context.Context, workspace string, statuses []model.TaskStatus, limit int) ([]model.Task, bool, error) {
	if err := validName("workspace", workspace); err != nil {
		return nil, false, err
	}
	for _, st := range statuses {
		switch st {
		case model.TaskPending, model.TaskClaimed, model.TaskCompleted, model.TaskFailed:
		default:
			return nil, false, fmt.Errorf("%w: unknown status %q", ErrInvalidInput, st)
		}
	}
	if err := auth.CheckWorkspace(ctx, workspace); err != nil {
		return nil, false, err
	}
	tasks, err := s.store.ListTasks(ctx, workspace, statuses, s.now())
	if err != nil {
		return nil, false, err
	}
	tasks, truncated := capList(tasks, limit)
	return tasks, truncated, nil
}

// mapTaskErr converts a store dependency error into an ErrInvalidInput so the
// transport layer reports it as a client error the agent can fix.
func mapTaskErr(err error) error {
	if errors.Is(err, store.ErrInvalidDependency) {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return err
}
