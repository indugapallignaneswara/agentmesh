// Package store defines the persistence contract for the coordination
// workspace and provides interchangeable implementations (in-memory for tests,
// Postgres for production). Both implementations are validated against the same
// suite in internal/storetest so they cannot drift.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

// ErrNotFound is returned when a requested member does not exist. Callers use
// it to translate a missing principal into a clean error rather than a panic or
// a constraint violation.
var ErrNotFound = errors.New("not found")

// Store is the authoritative system of record for the workspace. All timestamps
// and message IDs are supplied by the caller (the service layer) so behaviour is
// deterministic and testable; the store assigns only the monotonic event Seq.
type Store interface {
	// UpsertMember inserts a member or, on conflict with an existing
	// (workspace, name), refreshes its kind, agent card and LastSeen while
	// preserving the original JoinedAt. It returns the stored member.
	UpsertMember(ctx context.Context, m model.Member) (model.Member, error)

	// GetMember returns a single member or ErrNotFound.
	GetMember(ctx context.Context, workspace, name string) (model.Member, error)

	// TouchMember updates a member's LastSeen heartbeat. It returns ErrNotFound
	// if the member does not exist.
	TouchMember(ctx context.Context, workspace, name string, ts time.Time) error

	// ListMembers returns every durable member of a workspace, ordered by name.
	ListMembers(ctx context.Context, workspace string) ([]model.Member, error)

	// ListActiveMembers returns members whose LastSeen is at or after notBefore.
	// It backs the presence display and never affects delivery.
	ListActiveMembers(ctx context.Context, workspace string, notBefore time.Time) ([]model.Member, error)

	// CreateMessage atomically persists a message and one delivery row per
	// recipient. The caller computes recipients (the single target for a direct
	// message, or all members except the sender for a broadcast). msg.ID and
	// msg.CreatedAt must be set by the caller.
	CreateMessage(ctx context.Context, msg model.Message, recipients []string) error

	// ReadInbox returns a member's undelivered messages oldest-first and marks
	// them delivered with timestamp now, in a single transaction. Each returned
	// message has Recipient set to member.
	ReadInbox(ctx context.Context, workspace, member string, now time.Time) ([]model.Message, error)

	// AppendEvent appends an event and returns it with the assigned Seq.
	AppendEvent(ctx context.Context, e model.Event) (model.Event, error)

	// EventsSince returns events for a workspace with Seq strictly greater than
	// sinceSeq, ordered by Seq ascending, capped at limit.
	EventsSince(ctx context.Context, workspace string, sinceSeq int64, limit int) ([]model.Event, error)

	// Close releases any resources held by the store.
	Close() error
}
