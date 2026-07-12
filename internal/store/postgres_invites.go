package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

const inviteSelect = `
	SELECT id, code_hash, workspace, kind, role, max_uses, uses, created_by, created_at, expires_at, revoked_at
	FROM invites`

func (s *Postgres) CreateInvite(ctx context.Context, inv model.Invite) (model.Invite, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO invites (id, code_hash, workspace, kind, role, max_uses, uses, created_by, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		inv.ID, inv.CodeHash, inv.Workspace, string(inv.Kind), string(inv.Role),
		inv.MaxUses, inv.Uses, inv.CreatedBy, inv.CreatedAt, inv.ExpiresAt)
	return inv, err
}

func (s *Postgres) GetInviteByHash(ctx context.Context, hash string) (model.Invite, error) {
	inv, err := scanInvite(s.pool.QueryRow(ctx, inviteSelect+` WHERE code_hash = $1`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Invite{}, ErrNotFound
	}
	return inv, err
}

func (s *Postgres) RedeemInvite(ctx context.Context, hash string, now time.Time) (model.Invite, error) {
	// Single atomic UPDATE: concurrent redeemers can never push uses past
	// max_uses because the guard and the increment share one statement.
	inv, err := scanInvite(s.pool.QueryRow(ctx, `
		UPDATE invites SET uses = uses + 1
		WHERE code_hash = $1
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > $2)
		  AND uses < max_uses
		RETURNING id, code_hash, workspace, kind, role, max_uses, uses, created_by, created_at, expires_at, revoked_at`,
		hash, now))
	if err == nil {
		return inv, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.Invite{}, err
	}
	// 0 rows: distinguish "absent" from "exists but not redeemable".
	if _, gerr := s.GetInviteByHash(ctx, hash); gerr == nil {
		return model.Invite{}, ErrInviteSpent
	} else if !errors.Is(gerr, ErrNotFound) {
		return model.Invite{}, gerr
	}
	return model.Invite{}, ErrNotFound
}

func (s *Postgres) ListInvites(ctx context.Context, workspace string) ([]model.Invite, error) {
	rows, err := s.pool.Query(ctx, inviteSelect+`
		WHERE workspace = $1 ORDER BY created_at DESC, id`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *Postgres) RevokeInvite(ctx context.Context, id string, now time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE invites SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, id, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Postgres) SetWorkspacePolicy(ctx context.Context, name string, jp model.JoinPolicy, bp model.BroadcastPolicy, now time.Time) (model.Workspace, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE workspaces
		SET join_policy = $2, who_may_broadcast = $3, updated_at = $4
		WHERE name = $1`,
		name, string(jp), string(bp), now)
	if err != nil {
		return model.Workspace{}, err
	}
	if tag.RowsAffected() == 0 {
		return model.Workspace{}, ErrNotFound
	}
	return s.GetWorkspace(ctx, name)
}

func scanInvite(row pgx.Row) (model.Invite, error) {
	var inv model.Invite
	var kind, role string
	if err := row.Scan(&inv.ID, &inv.CodeHash, &inv.Workspace, &kind, &role,
		&inv.MaxUses, &inv.Uses, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.RevokedAt); err != nil {
		return model.Invite{}, err
	}
	inv.Kind = model.Kind(kind)
	inv.Role = model.Role(role)
	return inv, nil
}
