package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

const workspaceSelect = `
	SELECT name, status, created_by, created_at, updated_at, closed_by, closed_at, join_policy, who_may_broadcast, budget_daily_bytes, budget_member_daily_bytes
	FROM workspaces`

func (s *Postgres) CreateWorkspace(ctx context.Context, w model.Workspace) (model.Workspace, error) {
	// The policy columns take their DB defaults (open/anyone); the returned
	// struct is normalised the same way so both stores round-trip identically.
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO workspaces (name, status, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (name) DO NOTHING`,
		w.Name, string(w.Status), w.CreatedBy, w.CreatedAt, w.UpdatedAt)
	if err != nil {
		return model.Workspace{}, err
	}
	if tag.RowsAffected() == 0 {
		return model.Workspace{}, ErrRoomExists
	}
	return defaultPolicies(w), nil
}

func (s *Postgres) GetWorkspace(ctx context.Context, name string) (model.Workspace, error) {
	w, err := scanWorkspace(s.pool.QueryRow(ctx, workspaceSelect+` WHERE name = $1`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Workspace{}, ErrNotFound
	}
	return w, err
}

func (s *Postgres) EnsureWorkspace(ctx context.Context, name string, now time.Time) (model.Workspace, error) {
	// Insert-if-absent, then read the authoritative row (whether we or a
	// concurrent caller created it).
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO workspaces (name, status, created_at, updated_at)
		VALUES ($1, 'open', $2, $2)
		ON CONFLICT (name) DO NOTHING`, name, now); err != nil {
		return model.Workspace{}, err
	}
	return s.GetWorkspace(ctx, name)
}

func (s *Postgres) ListWorkspaces(ctx context.Context, statuses []model.WorkspaceStatus) ([]model.Workspace, error) {
	q := workspaceSelect + ` ORDER BY name`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	want := make(map[model.WorkspaceStatus]bool, len(statuses))
	for _, st := range statuses {
		want[st] = true
	}
	var out []model.Workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		if len(want) == 0 || want[w.Status] {
			out = append(out, w)
		}
	}
	return out, rows.Err()
}

func (s *Postgres) SetWorkspaceStatus(ctx context.Context, name string, status model.WorkspaceStatus, actor string, now time.Time) (model.Workspace, error) {
	var closedBy string
	var closedAt *time.Time
	if status == model.WorkspaceClosed {
		closedBy = actor
		closedAt = &now
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE workspaces
		SET status = $2, updated_at = $3, closed_by = $4, closed_at = $5
		WHERE name = $1`,
		name, string(status), now, closedBy, closedAt)
	if err != nil {
		return model.Workspace{}, err
	}
	if tag.RowsAffected() == 0 {
		return model.Workspace{}, ErrNotFound
	}
	return s.GetWorkspace(ctx, name)
}

// SetWorkspaceBudget updates a room's daily agent-byte budgets (0 = unlimited),
// bumping updated_at. Mirrors SetWorkspacePolicy: update-then-reread so the
// returned row is authoritative.
func (s *Postgres) SetWorkspaceBudget(ctx context.Context, name string, dailyBytes, memberDailyBytes int64, now time.Time) (model.Workspace, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE workspaces
		SET budget_daily_bytes = $2, budget_member_daily_bytes = $3, updated_at = $4
		WHERE name = $1`,
		name, dailyBytes, memberDailyBytes, now)
	if err != nil {
		return model.Workspace{}, err
	}
	if tag.RowsAffected() == 0 {
		return model.Workspace{}, ErrNotFound
	}
	return s.GetWorkspace(ctx, name)
}

func scanWorkspace(row pgx.Row) (model.Workspace, error) {
	var w model.Workspace
	var status, joinPolicy, broadcast string
	if err := row.Scan(&w.Name, &status, &w.CreatedBy, &w.CreatedAt, &w.UpdatedAt,
		&w.ClosedBy, &w.ClosedAt, &joinPolicy, &broadcast,
		&w.BudgetDailyBytes, &w.BudgetMemberDailyBytes); err != nil {
		return model.Workspace{}, err
	}
	w.Status = model.WorkspaceStatus(status)
	w.JoinPolicy = model.JoinPolicy(joinPolicy)
	w.WhoMayBroadcast = model.BroadcastPolicy(broadcast)
	return w, nil
}
