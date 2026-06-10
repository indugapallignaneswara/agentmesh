package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

// memorySelect lists memory columns in a fixed order for scanMemory.
const memorySelect = `
	SELECT id, workspace, scope, owner, status, content, source, created_by,
	       reviewed_by, review_note, created_at, updated_at, reviewed_at
	FROM memories`

func (s *Postgres) CreateMemory(ctx context.Context, m model.Memory) (model.Memory, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO memories (id, workspace, scope, owner, status, content, source,
		                      created_by, reviewed_by, review_note, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, '', '', $9, $10)`,
		m.ID, m.Workspace, string(m.Scope), m.Owner, string(m.Status),
		m.Content, m.Source, m.CreatedBy, m.CreatedAt, m.UpdatedAt)
	return m, err
}

func (s *Postgres) GetMemory(ctx context.Context, workspace, id string) (model.Memory, error) {
	m, err := scanMemory(s.pool.QueryRow(ctx,
		memorySelect+` WHERE workspace = $1 AND id = $2`, workspace, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Memory{}, ErrNotFound
	}
	return m, err
}

func (s *Postgres) SearchMemories(ctx context.Context, workspace, requester, query string, limit int) ([]model.Memory, error) {
	// Recall-oriented OR semantics: websearch_to_tsquery defaults to AND-ing
	// terms, which would hide an item matching only some of the query. Joining
	// the terms with OR keeps every partial match while ts_rank still puts
	// fuller matches first. websearch_to_tsquery accepts free-form text safely
	// (no tsquery syntax errors from user input). The WHERE clause is the
	// canonical visibility predicate: own private items, or approved shared.
	orQuery := strings.Join(strings.Fields(query), " OR ")
	if orQuery == "" {
		return nil, nil
	}
	const q = memorySelect + `
		WHERE workspace = $1
		  AND ((scope = 'private' AND owner = $2)
		       OR (scope = 'shared' AND status = 'approved'))
		  AND tsv @@ websearch_to_tsquery('english', $3)
		ORDER BY ts_rank(tsv, websearch_to_tsquery('english', $3)) DESC,
		         created_at DESC, id
		LIMIT $4`
	rows, err := s.pool.Query(ctx, q, workspace, requester, orQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectMemories(rows)
}

func (s *Postgres) ListPendingShared(ctx context.Context, workspace string) ([]model.Memory, error) {
	rows, err := s.pool.Query(ctx, memorySelect+`
		WHERE workspace = $1 AND scope = 'shared' AND status = 'pending'
		ORDER BY created_at, id`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectMemories(rows)
}

func (s *Postgres) ReviewMemory(ctx context.Context, workspace, id, reviewer string, approve bool, note string, now time.Time) (model.Memory, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Memory{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after a successful commit

	// Lock the row, then enforce the state guard in Go for precise errors.
	var scope, status string
	err = tx.QueryRow(ctx,
		`SELECT scope, status FROM memories WHERE workspace = $1 AND id = $2 FOR UPDATE`,
		workspace, id).Scan(&scope, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Memory{}, ErrNotFound
	}
	if err != nil {
		return model.Memory{}, err
	}
	if scope != string(model.MemoryShared) || status != string(model.MemoryPending) {
		return model.Memory{}, ErrMemoryConflict
	}

	newStatus := model.MemoryRejected
	if approve {
		newStatus = model.MemoryApproved
	}
	if _, err := tx.Exec(ctx, `
		UPDATE memories
		SET status = $3, reviewed_by = $4, review_note = $5, reviewed_at = $6, updated_at = $6
		WHERE workspace = $1 AND id = $2`,
		workspace, id, string(newStatus), reviewer, note, now); err != nil {
		return model.Memory{}, err
	}
	m, err := scanMemory(tx.QueryRow(ctx, memorySelect+` WHERE workspace = $1 AND id = $2`, workspace, id))
	if err != nil {
		return model.Memory{}, err
	}
	return m, tx.Commit(ctx)
}

func collectMemories(rows pgx.Rows) ([]model.Memory, error) {
	var out []model.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// scanMemory scans a memory row (columns per memorySelect) from any pgx row.
func scanMemory(row pgx.Row) (model.Memory, error) {
	var m model.Memory
	var scope, status string
	if err := row.Scan(
		&m.ID, &m.Workspace, &scope, &m.Owner, &status, &m.Content, &m.Source,
		&m.CreatedBy, &m.ReviewedBy, &m.ReviewNote, &m.CreatedAt, &m.UpdatedAt,
		&m.ReviewedAt,
	); err != nil {
		return model.Memory{}, err
	}
	m.Scope = model.MemoryScope(scope)
	m.Status = model.MemoryStatus(status)
	return m, nil
}
