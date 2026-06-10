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

// Task-related sentinel errors.
var (
	// ErrInvalidDependency is returned by CreateTask when a dependency id does
	// not reference an existing task in the same workspace.
	ErrInvalidDependency = errors.New("invalid dependency")

	// ErrNoClaimableTask is returned by ClaimNextTask when no eligible task is
	// available to claim.
	ErrNoClaimableTask = errors.New("no claimable task")

	// ErrTaskConflict is returned by CompleteTask when the caller is not the
	// current assignee or the task is no longer in the claimed state.
	ErrTaskConflict = errors.New("task conflict")

	// ErrMemoryConflict is returned by ReviewMemory when the item is not a
	// shared, still-pending memory (already reviewed, or private).
	ErrMemoryConflict = errors.New("memory review conflict")
)

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

	// CreateTask persists a new task and its dependency edges atomically. The
	// caller sets ID, timestamps and Status (pending). Every id in dependsOn
	// must reference an existing task in the same workspace; otherwise the whole
	// operation fails with ErrInvalidDependency and nothing is written.
	CreateTask(ctx context.Context, t model.Task, dependsOn []string) (model.Task, error)

	// GetTask returns a single task (with DependsOn populated) or ErrNotFound.
	GetTask(ctx context.Context, workspace, id string) (model.Task, error)

	// ListTasks returns a workspace's tasks ordered by CreatedAt. If statuses is
	// non-empty, only tasks in those statuses are returned. The reported Status
	// is the *effective* status: a claimed task whose lease has expired (as of
	// now) is reported as pending, since it is once again claimable.
	ListTasks(ctx context.Context, workspace string, statuses []model.TaskStatus, now time.Time) ([]model.Task, error)

	// ClaimNextTask atomically claims one eligible task for agent and returns
	// it. A task is eligible when it is pending (or claimed with a lease expired
	// at or before now) and every dependency is completed. Eligible tasks are
	// considered oldest-first. Claiming sets status=claimed, assigned_agent,
	// claimed_at=now and lease_expires_at=now+lease. Concurrent callers never
	// receive the same task (SELECT ... FOR UPDATE SKIP LOCKED). Returns
	// ErrNoClaimableTask when nothing is eligible.
	ClaimNextTask(ctx context.Context, workspace, agent string, now time.Time, lease time.Duration) (model.Task, error)

	// CompleteTask transitions a claimed task to completed (or failed) with an
	// optional result, but only if agent is still the assignee and the task is
	// still in the claimed state. Returns ErrNotFound if the task is missing,
	// or ErrTaskConflict if the caller is not the current assignee or the task
	// is no longer claimed (e.g. its lease lapsed and another agent stole it).
	CompleteTask(ctx context.Context, workspace, id, agent string, status model.TaskStatus, result string, now time.Time) (model.Task, error)

	// CreateMemory persists a memory item. The caller (service layer) sets ID,
	// scope/owner/status and timestamps.
	CreateMemory(ctx context.Context, m model.Memory) (model.Memory, error)

	// GetMemory returns a single memory item or ErrNotFound. Access control is
	// the service layer's job; the store returns the raw record.
	GetMemory(ctx context.Context, workspace, id string) (model.Memory, error)

	// SearchMemories returns the memories visible to requester that match the
	// full-text query, best match first, capped at limit. Visibility is the
	// canonical predicate: (private AND owner = requester) OR (shared AND
	// status = approved). Pending and rejected shared items are never returned.
	SearchMemories(ctx context.Context, workspace, requester, query string, limit int) ([]model.Memory, error)

	// ListPendingShared returns the review queue: shared memories still
	// pending, oldest first.
	ListPendingShared(ctx context.Context, workspace string) ([]model.Memory, error)

	// ReviewMemory approves or rejects a pending shared memory, recording the
	// reviewer, note and time. Returns ErrNotFound if the item is missing, or
	// ErrMemoryConflict if it is not a shared, still-pending item (the service
	// layer enforces who may review).
	ReviewMemory(ctx context.Context, workspace, id, reviewer string, approve bool, note string, now time.Time) (model.Memory, error)

	// Close releases any resources held by the store.
	Close() error
}
