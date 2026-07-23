package iam

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGAuditStore is the Postgres implementation of AuditStore. Like the other PG
// stores it is self-contained: it owns one table (iam_audit) and creates it on
// first use, so the audit trail lives next to the client registry without
// depending on AgentMesh's schema. Rows are append-only; the bigserial id
// breaks ties between events sharing a timestamp so ordering is total.
type PGAuditStore struct {
	pool *pgxpool.Pool
}

// NewPGAuditStore connects to Postgres and ensures the audit table exists.
func NewPGAuditStore(ctx context.Context, dsn string) (*PGAuditStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	s := &PGAuditStore{pool: pool}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS iam_audit (
			id           bigserial   PRIMARY KEY,
			ts           timestamptz NOT NULL,
			type         text        NOT NULL,
			client_id    text,
			subject      text,
			workspace    text,
			kind         text,
			jti          text,
			scope        text,
			audience     text,
			actor        text,
			actor_issuer text,
			dpop_bound   boolean     NOT NULL DEFAULT false,
			result       text        NOT NULL,
			reason       text,
			remote_ip    text
		)`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate iam_audit: %w", err)
	}
	// Indexes for the query patterns the /audit endpoint serves: the bare
	// newest-first page, and the per-client / per-workspace drill-downs.
	for _, ddl := range []string{
		`CREATE INDEX IF NOT EXISTS iam_audit_ts_idx ON iam_audit (ts DESC)`,
		`CREATE INDEX IF NOT EXISTS iam_audit_client_ts_idx ON iam_audit (client_id, ts DESC)`,
		`CREATE INDEX IF NOT EXISTS iam_audit_workspace_ts_idx ON iam_audit (workspace, ts DESC)`,
	} {
		if _, err := pool.Exec(ctx, ddl); err != nil {
			pool.Close()
			return nil, fmt.Errorf("migrate iam_audit indexes: %w", err)
		}
	}
	return s, nil
}

// Close releases the connection pool.
func (s *PGAuditStore) Close() { s.pool.Close() }

func (s *PGAuditStore) Append(ctx context.Context, e AuditEvent) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO iam_audit (
			ts, type, client_id, subject, workspace, kind, jti, scope,
			audience, actor, actor_issuer, dpop_bound, result, reason, remote_ip
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		e.TS, e.Type, e.ClientID, e.Subject, e.Workspace, e.Kind, e.JTI, e.Scope,
		e.Audience, e.Actor, e.ActorIssuer, e.DPoPBound, e.Result, e.Reason, e.RemoteIP,
	); err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}

func (s *PGAuditStore) Query(ctx context.Context, f AuditFilter) ([]AuditEvent, error) {
	// Build the WHERE clause from the non-zero filter fields. Values travel as
	// parameters only — never interpolated into the SQL text.
	var (
		conds []string
		args  []any
	)
	add := func(expr string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(expr, len(args)))
	}
	if f.ClientID != "" {
		add("client_id = $%d", f.ClientID)
	}
	if f.Subject != "" {
		add("subject = $%d", f.Subject)
	}
	if f.Workspace != "" {
		add("workspace = $%d", f.Workspace)
	}
	if f.Type != "" {
		add("type = $%d", f.Type)
	}
	if !f.From.IsZero() {
		add("ts >= $%d", f.From)
	}
	if !f.To.IsZero() {
		add("ts < $%d", f.To)
	}
	q := `
		SELECT ts, type, client_id, subject, workspace, kind, jti, scope,
		       audience, actor, actor_issuer, dpop_bound, result, reason, remote_ip
		FROM iam_audit`
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, clampAuditLimit(f.Limit))
	q += fmt.Sprintf(" ORDER BY ts DESC, id DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query audit: %w", err)
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.TS, &e.Type, &e.ClientID, &e.Subject, &e.Workspace,
			&e.Kind, &e.JTI, &e.Scope, &e.Audience, &e.Actor, &e.ActorIssuer,
			&e.DPoPBound, &e.Result, &e.Reason, &e.RemoteIP); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		e.TS = e.TS.UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query audit: %w", err)
	}
	return out, nil
}
