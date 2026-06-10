package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

const tokenSelect = `
	SELECT id, token_hash, workspace, member, kind, created_at, expires_at, revoked_at
	FROM auth_tokens`

func (s *Postgres) CreateAuthToken(ctx context.Context, t model.AuthToken) (model.AuthToken, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth_tokens (id, token_hash, workspace, member, kind, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		t.ID, t.TokenHash, t.Workspace, t.Member, string(t.Kind), t.CreatedAt, t.ExpiresAt)
	return t, err
}

func (s *Postgres) GetAuthTokenByHash(ctx context.Context, hash string, now time.Time) (model.AuthToken, error) {
	t, err := scanToken(s.pool.QueryRow(ctx, tokenSelect+`
		WHERE token_hash = $1
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > $2)`, hash, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.AuthToken{}, ErrNotFound
	}
	return t, err
}

func (s *Postgres) RevokeAuthToken(ctx context.Context, id string, now time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE auth_tokens SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, id, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Postgres) ListAuthTokens(ctx context.Context, workspace string) ([]model.AuthToken, error) {
	rows, err := s.pool.Query(ctx, tokenSelect+`
		WHERE workspace = $1 ORDER BY created_at DESC, id`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AuthToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanToken(row pgx.Row) (model.AuthToken, error) {
	var t model.AuthToken
	var kind string
	if err := row.Scan(&t.ID, &t.TokenHash, &t.Workspace, &t.Member, &kind,
		&t.CreatedAt, &t.ExpiresAt, &t.RevokedAt); err != nil {
		return model.AuthToken{}, err
	}
	t.Kind = model.Kind(kind)
	return t, nil
}
