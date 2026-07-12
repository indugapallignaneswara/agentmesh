package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

func (s *Postgres) SetMemberRole(ctx context.Context, workspace, name string, role model.Role) (model.Member, error) {
	const q = `
		UPDATE members SET role = $3
		WHERE workspace = $1 AND name = $2
		RETURNING workspace, name, kind, role, agent_card, joined_at, last_seen`
	m, err := scanMember(s.pool.QueryRow(ctx, q, workspace, name, string(role)))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Member{}, ErrNotFound
	}
	return m, err
}

func (s *Postgres) RemoveMember(ctx context.Context, workspace, name string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after a successful commit

	// Purge the departed member's undelivered deliveries so it stops accruing
	// inbox rows; delivered rows are history and stay.
	if _, err := tx.Exec(ctx,
		`DELETE FROM deliveries WHERE workspace = $1 AND recipient = $2 AND delivered_at IS NULL`,
		workspace, name); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx,
		`DELETE FROM members WHERE workspace = $1 AND name = $2`, workspace, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound // rollback via the deferred Rollback
	}
	return tx.Commit(ctx)
}

func (s *Postgres) CreateBan(ctx context.Context, b model.Ban) (model.Ban, error) {
	const q = `
		INSERT INTO bans (workspace, name, banned_by, reason, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (workspace, name) DO UPDATE
		SET banned_by = EXCLUDED.banned_by,
		    reason = EXCLUDED.reason,
		    created_at = EXCLUDED.created_at`
	if _, err := s.pool.Exec(ctx, q, b.Workspace, b.Name, b.BannedBy, b.Reason, b.CreatedAt); err != nil {
		return model.Ban{}, err
	}
	return b, nil
}

func (s *Postgres) GetBan(ctx context.Context, workspace, name string) (model.Ban, error) {
	const q = `
		SELECT workspace, name, banned_by, reason, created_at
		FROM bans WHERE workspace = $1 AND name = $2`
	b, err := scanBan(s.pool.QueryRow(ctx, q, workspace, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Ban{}, ErrNotFound
	}
	return b, err
}

func (s *Postgres) RemoveBan(ctx context.Context, workspace, name string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM bans WHERE workspace = $1 AND name = $2`, workspace, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Postgres) ListBans(ctx context.Context, workspace string) ([]model.Ban, error) {
	const q = `
		SELECT workspace, name, banned_by, reason, created_at
		FROM bans WHERE workspace = $1 ORDER BY name`
	rows, err := s.pool.Query(ctx, q, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Ban
	for rows.Next() {
		b, err := scanBan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Postgres) ListMessages(ctx context.Context, workspace, afterID string, limit int) ([]model.Message, error) {
	limit = clampMessageLimit(limit)

	const sel = `
		SELECT id, workspace, sender, kind, body, created_at
		FROM messages WHERE workspace = $1`

	// Resolve the afterID cursor first; an unknown id pages from the start.
	if afterID != "" {
		var cursorAt time.Time
		var cursorID string
		err := s.pool.QueryRow(ctx,
			`SELECT created_at, id FROM messages WHERE id = $1 AND workspace = $2`,
			afterID, workspace).Scan(&cursorAt, &cursorID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Fall through to the uncursored query.
		case err != nil:
			return nil, err
		default:
			return s.queryMessages(ctx,
				sel+` AND (created_at, id) > ($2, $3) ORDER BY created_at, id LIMIT $4`,
				workspace, cursorAt, cursorID, limit)
		}
	}
	return s.queryMessages(ctx, sel+` ORDER BY created_at, id LIMIT $2`, workspace, limit)
}

func (s *Postgres) queryMessages(ctx context.Context, q string, args ...any) ([]model.Message, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Message
	for rows.Next() {
		var msg model.Message
		var kind string
		if err := rows.Scan(&msg.ID, &msg.Workspace, &msg.Sender, &kind, &msg.Body, &msg.CreatedAt); err != nil {
			return nil, err
		}
		msg.Kind = model.MessageKind(kind)
		out = append(out, msg)
	}
	return out, rows.Err()
}

// scanBan scans a ban row from any pgx row source.
func scanBan(row pgx.Row) (model.Ban, error) {
	var b model.Ban
	if err := row.Scan(&b.Workspace, &b.Name, &b.BannedBy, &b.Reason, &b.CreatedAt); err != nil {
		return model.Ban{}, err
	}
	return b, nil
}
