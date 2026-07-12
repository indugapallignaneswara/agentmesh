package store

import (
	"context"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

func (s *Postgres) ReadInboxLeased(ctx context.Context, workspace, member string, now time.Time, visibility time.Duration) ([]model.Message, error) {
	// Mirrors ReadInbox's CTE, but sets a visibility deadline instead of
	// delivered_at: unless acked, the rows become eligible again after the
	// deadline (at-least-once). The deadline is computed in Go.
	deadline := now.Add(visibility)
	const q = `
		WITH leased AS (
			UPDATE deliveries
			SET in_flight_until = $4
			WHERE workspace = $1 AND recipient = $2 AND delivered_at IS NULL
			  AND (in_flight_until IS NULL OR in_flight_until <= $3)
			RETURNING message_id
		)
		SELECT m.id, m.workspace, m.sender, m.kind, m.body, m.created_at
		FROM messages m
		JOIN leased l ON l.message_id = m.id
		ORDER BY m.created_at, m.id`
	rows, err := s.pool.Query(ctx, q, workspace, member, now, deadline)
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

func (s *Postgres) AckInbox(ctx context.Context, workspace, member string, ids []string, now time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE deliveries
		SET delivered_at = $3
		WHERE workspace = $1 AND recipient = $2 AND delivered_at IS NULL
		  AND message_id = ANY($4)`,
		workspace, member, now, ids)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
