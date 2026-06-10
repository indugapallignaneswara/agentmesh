package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

// Postgres is the production Store backed by a pgx connection pool.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres connects to the given DSN, verifies connectivity, and applies
// pending migrations. The returned store owns the pool and closes it on Close.
func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

// jsonbArg converts a RawMessage to an argument that stores NULL when empty.
func jsonbArg(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

func (s *Postgres) UpsertMember(ctx context.Context, m model.Member) (model.Member, error) {
	const q = `
		INSERT INTO members (workspace, name, kind, agent_card, joined_at, last_seen)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (workspace, name) DO UPDATE
		SET kind = EXCLUDED.kind,
		    agent_card = EXCLUDED.agent_card,
		    last_seen = EXCLUDED.last_seen
		RETURNING workspace, name, kind, agent_card, joined_at, last_seen`
	row := s.pool.QueryRow(ctx, q,
		m.Workspace, m.Name, string(m.Kind), jsonbArg(m.AgentCard), m.JoinedAt, m.LastSeen)
	return scanMember(row)
}

func (s *Postgres) GetMember(ctx context.Context, workspace, name string) (model.Member, error) {
	const q = `
		SELECT workspace, name, kind, agent_card, joined_at, last_seen
		FROM members WHERE workspace = $1 AND name = $2`
	m, err := scanMember(s.pool.QueryRow(ctx, q, workspace, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Member{}, ErrNotFound
	}
	return m, err
}

func (s *Postgres) TouchMember(ctx context.Context, workspace, name string, ts time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE members SET last_seen = $3 WHERE workspace = $1 AND name = $2`,
		workspace, name, ts)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Postgres) ListMembers(ctx context.Context, workspace string) ([]model.Member, error) {
	const q = `
		SELECT workspace, name, kind, agent_card, joined_at, last_seen
		FROM members WHERE workspace = $1 ORDER BY name`
	return s.queryMembers(ctx, q, workspace)
}

func (s *Postgres) ListActiveMembers(ctx context.Context, workspace string, notBefore time.Time) ([]model.Member, error) {
	const q = `
		SELECT workspace, name, kind, agent_card, joined_at, last_seen
		FROM members WHERE workspace = $1 AND last_seen >= $2 ORDER BY name`
	return s.queryMembers(ctx, q, workspace, notBefore)
}

func (s *Postgres) queryMembers(ctx context.Context, q string, args ...any) ([]model.Member, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Postgres) CreateMessage(ctx context.Context, msg model.Message, recipients []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after a successful commit

	if _, err := tx.Exec(ctx,
		`INSERT INTO messages (id, workspace, sender, kind, body, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		msg.ID, msg.Workspace, msg.Sender, string(msg.Kind), msg.Body, msg.CreatedAt,
	); err != nil {
		return err
	}

	batch := &pgx.Batch{}
	for _, r := range recipients {
		batch.Queue(
			`INSERT INTO deliveries (message_id, workspace, recipient) VALUES ($1, $2, $3)`,
			msg.ID, msg.Workspace, r)
	}
	if batch.Len() > 0 {
		br := tx.SendBatch(ctx, batch)
		for range recipients {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return err
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Postgres) ReadInbox(ctx context.Context, workspace, member string, now time.Time) ([]model.Message, error) {
	const q = `
		WITH claimed AS (
			UPDATE deliveries
			SET delivered_at = $3
			WHERE workspace = $1 AND recipient = $2 AND delivered_at IS NULL
			RETURNING message_id
		)
		SELECT m.id, m.workspace, m.sender, m.kind, m.body, m.created_at
		FROM messages m
		JOIN claimed c ON c.message_id = m.id
		ORDER BY m.created_at, m.id`
	rows, err := s.pool.Query(ctx, q, workspace, member, now)
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
		msg.Recipient = member
		out = append(out, msg)
	}
	return out, rows.Err()
}

func (s *Postgres) AppendEvent(ctx context.Context, e model.Event) (model.Event, error) {
	const q = `
		INSERT INTO events (workspace, source, type, payload, created_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING seq`
	err := s.pool.QueryRow(ctx, q,
		e.Workspace, e.Source, e.Type, jsonbArg(e.Payload), e.CreatedAt).Scan(&e.Seq)
	return e, err
}

func (s *Postgres) EventsSince(ctx context.Context, workspace string, sinceSeq int64, limit int) ([]model.Event, error) {
	const q = `
		SELECT seq, workspace, source, type, payload, created_at
		FROM events WHERE workspace = $1 AND seq > $2 ORDER BY seq ASC LIMIT $3`
	rows, err := s.pool.Query(ctx, q, workspace, sinceSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		var payload []byte
		if err := rows.Scan(&e.Seq, &e.Workspace, &e.Source, &e.Type, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Payload = payload
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Postgres) Close() error {
	s.pool.Close()
	return nil
}

// TruncateAll removes all coordination data while leaving the schema and
// migration history intact. It exists for integration tests that require an
// empty store between cases.
func (s *Postgres) TruncateAll(ctx context.Context) error {
	_, err := s.pool.Exec(ctx,
		`TRUNCATE artifacts, memories, task_deps, tasks, deliveries, messages, members, events RESTART IDENTITY`)
	return err
}

// scanMember scans a member row from any pgx row source.
func scanMember(row pgx.Row) (model.Member, error) {
	var m model.Member
	var kind string
	var card []byte
	if err := row.Scan(&m.Workspace, &m.Name, &kind, &card, &m.JoinedAt, &m.LastSeen); err != nil {
		return model.Member{}, err
	}
	m.Kind = model.Kind(kind)
	m.AgentCard = card
	return m, nil
}
