package iam

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the Postgres implementation of Store. It is self-contained: it owns
// one table (iam_clients) and creates it on first use, so Agent-IAM can run
// against its own database independent of AgentMesh's schema — it is a separate
// product that merely shares a repo today.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore connects to Postgres and ensures the client table exists.
func NewPGStore(ctx context.Context, dsn string) (*PGStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	s := &PGStore{pool: pool}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the connection pool.
func (s *PGStore) Close() { s.pool.Close() }

func (s *PGStore) migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS iam_clients (
			client_id      text        PRIMARY KEY,
			secret_hash    text        NOT NULL,
			workspace      text        NOT NULL,
			subject        text        NOT NULL,
			kind           text        NOT NULL DEFAULT 'agent',
			allowed_scopes text[]      NOT NULL DEFAULT '{}',
			token_ttl_secs bigint      NOT NULL DEFAULT 0,
			disabled       boolean     NOT NULL DEFAULT false,
			created_at     timestamptz NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("migrate iam_clients: %w", err)
	}
	return nil
}

func (s *PGStore) CreateClient(ctx context.Context, c Client) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO iam_clients
			(client_id, secret_hash, workspace, subject, kind, allowed_scopes, token_ttl_secs, disabled, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		c.ClientID, c.SecretHash, c.Workspace, c.Subject, c.Kind,
		c.AllowedScopes, int64(c.TokenTTL.Seconds()), c.Disabled, c.CreatedAt)
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	return nil
}

func (s *PGStore) GetClient(ctx context.Context, clientID string) (Client, error) {
	return scanClient(s.pool.QueryRow(ctx, clientSelect+` WHERE client_id = $1`, clientID))
}

func (s *PGStore) ListClients(ctx context.Context, workspace string) ([]Client, error) {
	sql := clientSelect
	var rows pgx.Rows
	var err error
	if workspace == "" {
		rows, err = s.pool.Query(ctx, sql+` ORDER BY created_at`)
	} else {
		rows, err = s.pool.Query(ctx, sql+` WHERE workspace = $1 ORDER BY created_at`, workspace)
	}
	if err != nil {
		return nil, fmt.Errorf("list clients: %w", err)
	}
	defer rows.Close()
	var out []Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PGStore) SetClientDisabled(ctx context.Context, clientID string, disabled bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE iam_clients SET disabled = $2 WHERE client_id = $1`, clientID, disabled)
	if err != nil {
		return fmt.Errorf("set client disabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrClientNotFound
	}
	return nil
}

const clientSelect = `
	SELECT client_id, secret_hash, workspace, subject, kind, allowed_scopes, token_ttl_secs, disabled, created_at
	FROM iam_clients`

// rowScanner is satisfied by both pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanClient(row rowScanner) (Client, error) {
	var c Client
	var ttlSecs int64
	if err := row.Scan(&c.ClientID, &c.SecretHash, &c.Workspace, &c.Subject,
		&c.Kind, &c.AllowedScopes, &ttlSecs, &c.Disabled, &c.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Client{}, ErrClientNotFound
		}
		return Client{}, err
	}
	c.TokenTTL = time.Duration(ttlSecs) * time.Second
	return c, nil
}
