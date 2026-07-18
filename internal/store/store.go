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

	// ErrArtifactConflict is returned by PutArtifact when the caller's base
	// version is stale (someone else wrote first) or when creating an artifact
	// that already exists. The caller should re-read, merge, and retry.
	ErrArtifactConflict = errors.New("artifact version conflict")

	// ErrRoomExists is returned by CreateWorkspace when the room already exists.
	ErrRoomExists = errors.New("room already exists")

	// ErrBanned is returned by GetBan (via the service) when a name is banned.
	ErrBanned = errors.New("member is banned")

	// ErrInviteSpent is returned by RedeemInvite when the invite exists but is
	// not redeemable: revoked, expired, or all uses consumed.
	ErrInviteSpent = errors.New("invite not redeemable")
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

	// SetMemberRole updates a member's role. Returns ErrNotFound if the member
	// does not exist.
	SetMemberRole(ctx context.Context, workspace, name string, role model.Role) (model.Member, error)

	// RemoveMember deletes a member and all of its undelivered deliveries (so a
	// kicked/departed member stops accruing inbox rows). Returns ErrNotFound if
	// the member does not exist.
	RemoveMember(ctx context.Context, workspace, name string) error

	// CreateBan records a ban for (workspace, name); upserts if one exists.
	CreateBan(ctx context.Context, b model.Ban) (model.Ban, error)

	// GetBan returns the ban for (workspace, name) or ErrNotFound.
	GetBan(ctx context.Context, workspace, name string) (model.Ban, error)

	// RemoveBan lifts a ban. Returns ErrNotFound if none exists.
	RemoveBan(ctx context.Context, workspace, name string) error

	// ListBans returns a workspace's bans ordered by name.
	ListBans(ctx context.Context, workspace string) ([]model.Ban, error)

	// ListMessages returns a room's messages oldest-first for human review
	// (non-consuming, unlike ReadInbox). afterID pages from the last-seen id
	// (empty for the start); limit caps the result.
	ListMessages(ctx context.Context, workspace, afterID string, limit int) ([]model.Message, error)

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

	// ReadInboxLeased returns the member's messages that are undelivered AND
	// not currently in flight (in_flight_until is NULL or <= now),
	// oldest-first, marking each in flight until now+visibility. Messages are
	// NOT marked delivered: they reappear after the visibility deadline unless
	// acked — at-least-once delivery. Recipient is set on returned messages.
	ReadInboxLeased(ctx context.Context, workspace, member string, now time.Time, visibility time.Duration) ([]model.Message, error)

	// AckInbox marks the given message ids delivered for member (finalising an
	// at-least-once read). Unknown/foreign ids are ignored; returns the number
	// of deliveries acked.
	AckInbox(ctx context.Context, workspace, member string, ids []string, now time.Time) (int, error)

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

	// RetryTask requeues a failed task: it transitions failed -> pending and
	// clears the assignee, result and lease so the task (and anything that
	// depends on it) becomes claimable again. Returns ErrNotFound if the task
	// is missing, or ErrTaskConflict if it is not in the failed state.
	RetryTask(ctx context.Context, workspace, id string, now time.Time) (model.Task, error)

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

	// CreateWorkspace inserts a new room. Returns ErrRoomExists if a room with
	// that name already exists.
	CreateWorkspace(ctx context.Context, w model.Workspace) (model.Workspace, error)

	// GetWorkspace returns a room or ErrNotFound.
	GetWorkspace(ctx context.Context, name string) (model.Workspace, error)

	// EnsureWorkspace lazily creates a room with default (open) status if it
	// does not exist, and returns the current row. It is idempotent and backs
	// implicit-workspace mode (auto-create on first join).
	EnsureWorkspace(ctx context.Context, name string, now time.Time) (model.Workspace, error)

	// ListWorkspaces returns all rooms ordered by name. If statuses is
	// non-empty, only rooms in those statuses are returned.
	ListWorkspaces(ctx context.Context, statuses []model.WorkspaceStatus) ([]model.Workspace, error)

	// SetWorkspaceStatus transitions a room's status (e.g. open->closed).
	// actor and now are recorded on a close. Returns ErrNotFound if absent.
	SetWorkspaceStatus(ctx context.Context, name string, status model.WorkspaceStatus, actor string, now time.Time) (model.Workspace, error)

	// SetWorkspacePolicy updates a room's join and broadcast policies, bumping
	// UpdatedAt. Returns ErrNotFound if the room does not exist.
	SetWorkspacePolicy(ctx context.Context, name string, jp model.JoinPolicy, bp model.BroadcastPolicy, now time.Time) (model.Workspace, error)

	// SetWorkspaceBudget updates a room's daily byte budgets for agent
	// traffic (0 = unlimited), bumping updated_at. Returns ErrNotFound for
	// an unknown room. Budgets bound AGENT coordination bytes
	// (ingress+egress); humans are exempt by design.
	SetWorkspaceBudget(ctx context.Context, name string, dailyBytes, memberDailyBytes int64, now time.Time) (model.Workspace, error)

	// CreateInvite persists an invite record (hash only; the caller keeps the
	// plaintext code). ID and CodeHash must be unique.
	CreateInvite(ctx context.Context, inv model.Invite) (model.Invite, error)

	// GetInviteByHash returns the invite with this code hash in ANY state
	// (spent/revoked/expired included, so callers can validate before
	// redeeming), or ErrNotFound if absent.
	GetInviteByHash(ctx context.Context, hash string) (model.Invite, error)

	// RedeemInvite atomically consumes one use of the invite with this code
	// hash: only if it is not revoked, not expired as of now, and uses is
	// still below max_uses does uses increment. It returns the updated invite.
	// An existing but non-redeemable invite returns ErrInviteSpent; an absent
	// hash returns ErrNotFound.
	RedeemInvite(ctx context.Context, hash string, now time.Time) (model.Invite, error)

	// ListInvites returns a workspace's invites (all states, for audit),
	// newest first.
	ListInvites(ctx context.Context, workspace string) ([]model.Invite, error)

	// RevokeInvite soft-revokes an invite by ID. ErrNotFound if absent or
	// already revoked.
	RevokeInvite(ctx context.Context, id string, now time.Time) error

	// CreateAuthToken persists a token record (hash only; the caller keeps the
	// secret). ID and TokenHash must be unique.
	CreateAuthToken(ctx context.Context, t model.AuthToken) (model.AuthToken, error)

	// GetAuthTokenByHash returns the ACTIVE token with this hash: not revoked
	// and not expired as of now. Missing, revoked or expired all return
	// ErrNotFound so callers cannot distinguish (no oracle for attackers).
	GetAuthTokenByHash(ctx context.Context, hash string, now time.Time) (model.AuthToken, error)

	// RevokeAuthToken soft-revokes a token by ID. ErrNotFound if absent or
	// already revoked.
	RevokeAuthToken(ctx context.Context, id string, now time.Time) error

	// ListAuthTokens returns a workspace's tokens (including revoked/expired,
	// for audit), newest first.
	ListAuthTokens(ctx context.Context, workspace string) ([]model.AuthToken, error)

	// PutArtifact writes an artifact with optimistic concurrency. baseVersion 0
	// creates the artifact (ErrArtifactConflict if it already exists); a
	// non-zero baseVersion updates it only if the stored version still equals
	// baseVersion (ErrArtifactConflict if stale, ErrNotFound if absent). On
	// success the stored Version is baseVersion+1. a.UpdatedBy/UpdatedAt are
	// the writer and write time; on create they also seed CreatedBy/CreatedAt.
	PutArtifact(ctx context.Context, a model.Artifact, baseVersion int64) (model.Artifact, error)

	// GetArtifact returns one artifact or ErrNotFound.
	GetArtifact(ctx context.Context, workspace, name string) (model.Artifact, error)

	// ListArtifacts returns a workspace's artifacts ordered by name.
	ListArtifacts(ctx context.Context, workspace string) ([]model.Artifact, error)

	// Ping reports whether the store is reachable. It backs the readiness
	// probe: a server whose store is down is live but not ready.
	Ping(ctx context.Context) error

	// Close releases any resources held by the store.
	Close() error

	// AppendUsage appends a batch of usage events to the ledger and folds them
	// into the daily rollup. Best-effort by contract: callers batch and may drop.
	AppendUsage(ctx context.Context, events []model.UsageEvent) error

	// UsageSummary aggregates per-member usage for a workspace in [from, to).
	UsageSummary(ctx context.Context, workspace string, from, to time.Time) ([]model.UsageSummary, error)

	// UsageDaily returns the last `days` UTC day-buckets for a workspace, newest
	// day first, all members.
	UsageDaily(ctx context.Context, workspace string, days int) ([]model.UsageDay, error)
}
