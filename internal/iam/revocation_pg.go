package iam

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRevocationStore is the Postgres implementation of RevocationStore. Like
// PGStore it is self-contained: it owns one table (iam_revocations) and
// creates it on first use, so the denylist lives next to the client registry
// without depending on AgentMesh's schema.
type PGRevocationStore struct {
	pool *pgxpool.Pool
}

// NewPGRevocationStore connects to Postgres and ensures the revocation table
// exists.
func NewPGRevocationStore(ctx context.Context, dsn string) (*PGRevocationStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	s := &PGRevocationStore{pool: pool}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS iam_revocations (
			jti text        PRIMARY KEY,
			exp timestamptz NOT NULL
		)`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate iam_revocations: %w", err)
	}
	return s, nil
}

// Close releases the connection pool.
func (s *PGRevocationStore) Close() { s.pool.Close() }

func (s *PGRevocationStore) Revoke(ctx context.Context, jti string, exp time.Time) error {
	// Opportunistic prune: expired entries protect nothing (the token is
	// invalid on its own) and revokes are rare, so the sweep is cheap here.
	if _, err := s.pool.Exec(ctx, `DELETE FROM iam_revocations WHERE exp <= now()`); err != nil {
		return fmt.Errorf("prune revocations: %w", err)
	}
	// ON CONFLICT DO NOTHING makes a double revoke of the same jti idempotent
	// (the expiry is a property of the token, so it cannot legitimately change).
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO iam_revocations (jti, exp) VALUES ($1, $2)
		ON CONFLICT (jti) DO NOTHING`, jti, exp); err != nil {
		return fmt.Errorf("revoke: %w", err)
	}
	return nil
}

func (s *PGRevocationStore) IsRevoked(ctx context.Context, jti string, now time.Time) (bool, error) {
	var revoked bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM iam_revocations WHERE jti = $1 AND exp > $2)`,
		jti, now).Scan(&revoked); err != nil {
		return false, fmt.Errorf("is revoked: %w", err)
	}
	return revoked, nil
}

func (s *PGRevocationStore) ListActive(ctx context.Context, now time.Time) ([]Revocation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT jti, exp FROM iam_revocations WHERE exp > $1 ORDER BY exp`, now)
	if err != nil {
		return nil, fmt.Errorf("list revocations: %w", err)
	}
	defer rows.Close()
	var out []Revocation
	for rows.Next() {
		var r Revocation
		if err := rows.Scan(&r.JTI, &r.Expiry); err != nil {
			return nil, fmt.Errorf("scan revocation: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
