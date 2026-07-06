package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

func (s *Postgres) CreateTask(ctx context.Context, t model.Task, dependsOn []string) (model.Task, error) {
	// Dedupe up front so the returned task matches the Memory store (which
	// dedupes) and the edge inserts are unique.
	deps := dedupeStrings(dependsOn)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Task{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after a successful commit

	// Validate every dependency BEFORE inserting the task. This rejects a
	// dangling/cross-workspace id — and, matching the Memory store, a
	// self-dependency (t.ID does not exist yet, so it fails the check) — rather
	// than silently accepting an edge to the task's own not-yet-committed row.
	for _, dep := range deps {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1 AND workspace = $2)`,
			dep, t.Workspace,
		).Scan(&exists); err != nil {
			return model.Task{}, err
		}
		if !exists {
			return model.Task{}, fmt.Errorf("%w: %q", ErrInvalidDependency, dep)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks (id, workspace, title, details, status, created_by,
		                   assigned_agent, result, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, '', '', $7, $8)`,
		t.ID, t.Workspace, t.Title, t.Details, string(t.Status), t.CreatedBy,
		t.CreatedAt, t.UpdatedAt,
	); err != nil {
		return model.Task{}, err
	}

	for _, dep := range deps {
		if _, err := tx.Exec(ctx,
			`INSERT INTO task_deps (task_id, depends_on_id) VALUES ($1, $2)
			 ON CONFLICT DO NOTHING`, t.ID, dep,
		); err != nil {
			return model.Task{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return model.Task{}, err
	}
	t.DependsOn = deps
	return t, nil
}

func (s *Postgres) GetTask(ctx context.Context, workspace, id string) (model.Task, error) {
	t, err := scanTask(s.pool.QueryRow(ctx, taskSelect+` WHERE id = $1 AND workspace = $2`, id, workspace))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Task{}, ErrNotFound
	}
	if err != nil {
		return model.Task{}, err
	}
	deps, err := s.taskDeps(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	t.DependsOn = deps
	return t, nil
}

func (s *Postgres) ListTasks(ctx context.Context, workspace string, statuses []model.TaskStatus, now time.Time) ([]model.Task, error) {
	// effectiveStatus downgrades a claimed-but-lease-expired task to pending in
	// the projection, so reads agree with claim eligibility. Status filtering is
	// applied to this effective status in Go (after the query) to keep the SQL
	// simple and the semantics in one place.
	rows, err := s.pool.Query(ctx, taskSelect+` WHERE workspace = $1 ORDER BY created_at, id`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []model.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		t.Status = effectiveStatus(t, now)
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	tasks = filterByStatus(tasks, statuses)

	// Populate dependencies for the returned set.
	for i := range tasks {
		deps, err := s.taskDeps(ctx, tasks[i].ID)
		if err != nil {
			return nil, err
		}
		tasks[i].DependsOn = deps
	}
	return tasks, nil
}

func (s *Postgres) ClaimNextTask(ctx context.Context, workspace, agent string, now time.Time, lease time.Duration) (model.Task, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Task{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after a successful commit

	// Select one eligible task and lock its row, skipping rows already locked by
	// a concurrent claimer. Eligibility:
	//   - pending, OR claimed with a lease expired at/before now (work-stealing)
	//   - every dependency completed
	// FOR UPDATE SKIP LOCKED is the no-double-claim guarantee: two concurrent
	// transactions can never select and lock the same row.
	const sel = `
		SELECT id FROM tasks t
		WHERE t.workspace = $1
		  AND (t.status = 'pending'
		       OR (t.status = 'claimed' AND t.lease_expires_at IS NOT NULL AND t.lease_expires_at <= $2))
		  AND NOT EXISTS (
		      SELECT 1 FROM task_deps d
		      JOIN tasks dep ON dep.id = d.depends_on_id
		      WHERE d.task_id = t.id AND dep.status <> 'completed'
		  )
		ORDER BY t.created_at, t.id
		FOR UPDATE SKIP LOCKED
		LIMIT 1`
	var id string
	if err := tx.QueryRow(ctx, sel, workspace, now).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.Task{}, ErrNoClaimableTask
		}
		return model.Task{}, err
	}

	expires := now.Add(lease)
	if _, err := tx.Exec(ctx, `
		UPDATE tasks
		SET status = 'claimed', assigned_agent = $2, claimed_at = $3,
		    lease_expires_at = $4, updated_at = $3
		WHERE id = $1`,
		id, agent, now, expires,
	); err != nil {
		return model.Task{}, err
	}

	t, err := scanTask(tx.QueryRow(ctx, taskSelect+` WHERE id = $1`, id))
	if err != nil {
		return model.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Task{}, err
	}
	deps, err := s.taskDeps(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	t.DependsOn = deps
	return t, nil
}

func (s *Postgres) CompleteTask(ctx context.Context, workspace, id, agent string, status model.TaskStatus, result string, now time.Time) (model.Task, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Task{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after a successful commit

	// Lock the row, then enforce the ownership + state guard in Go so we can
	// return precise errors (not-found vs conflict).
	var curStatus, curAgent string
	err = tx.QueryRow(ctx,
		`SELECT status, assigned_agent FROM tasks WHERE id = $1 AND workspace = $2 FOR UPDATE`,
		id, workspace,
	).Scan(&curStatus, &curAgent)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Task{}, ErrNotFound
	}
	if err != nil {
		return model.Task{}, err
	}
	if curStatus != string(model.TaskClaimed) || curAgent != agent {
		return model.Task{}, fmt.Errorf("%w: task %q is %s (assignee %q), caller %q",
			ErrTaskConflict, id, curStatus, curAgent, agent)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE tasks
		SET status = $2, result = $3, updated_at = $4, lease_expires_at = NULL
		WHERE id = $1`,
		id, string(status), result, now,
	); err != nil {
		return model.Task{}, err
	}

	t, err := scanTask(tx.QueryRow(ctx, taskSelect+` WHERE id = $1`, id))
	if err != nil {
		return model.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Task{}, err
	}
	deps, err := s.taskDeps(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	t.DependsOn = deps
	return t, nil
}

func (s *Postgres) RetryTask(ctx context.Context, workspace, id string, now time.Time) (model.Task, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Task{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after a successful commit

	var curStatus string
	err = tx.QueryRow(ctx,
		`SELECT status FROM tasks WHERE id = $1 AND workspace = $2 FOR UPDATE`,
		id, workspace,
	).Scan(&curStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Task{}, ErrNotFound
	}
	if err != nil {
		return model.Task{}, err
	}
	if curStatus != string(model.TaskFailed) {
		return model.Task{}, fmt.Errorf("%w: task %q is %s, only failed tasks can be retried",
			ErrTaskConflict, id, curStatus)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE tasks
		SET status = 'pending', assigned_agent = '', result = '',
		    claimed_at = NULL, lease_expires_at = NULL, updated_at = $2
		WHERE id = $1`,
		id, now,
	); err != nil {
		return model.Task{}, err
	}

	t, err := scanTask(tx.QueryRow(ctx, taskSelect+` WHERE id = $1`, id))
	if err != nil {
		return model.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Task{}, err
	}
	deps, err := s.taskDeps(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	t.DependsOn = deps
	return t, nil
}

// taskSelect lists task columns in a fixed order for scanTask.
const taskSelect = `
	SELECT id, workspace, title, details, status, created_by, assigned_agent,
	       result, created_at, updated_at, claimed_at, lease_expires_at
	FROM tasks`

// scanTask scans a task row (columns per taskSelect) from any pgx row source.
func scanTask(row pgx.Row) (model.Task, error) {
	var t model.Task
	var status string
	if err := row.Scan(
		&t.ID, &t.Workspace, &t.Title, &t.Details, &status, &t.CreatedBy,
		&t.AssignedAgent, &t.Result, &t.CreatedAt, &t.UpdatedAt,
		&t.ClaimedAt, &t.LeaseExpiresAt,
	); err != nil {
		return model.Task{}, err
	}
	t.Status = model.TaskStatus(status)
	return t, nil
}

// taskDeps returns the dependency ids for a task, ordered for determinism.
func (s *Postgres) taskDeps(ctx context.Context, taskID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT depends_on_id FROM task_deps WHERE task_id = $1 ORDER BY depends_on_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deps []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, rows.Err()
}
