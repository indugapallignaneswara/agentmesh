// Package model holds the core domain types shared across the coordination
// workspace: members (presence), messages (inbox), and events (episodic log).
package model

import (
	"encoding/json"
	"time"
)

// WorkspaceStatus is the lifecycle state of a room. Open rooms accept joins
// and new content; closed rooms reject new content (messages, tasks, memory,
// artifact writes) while remaining readable so humans can review what
// happened. Reopening is allowed — closing is a moderation action, not a
// deletion.
type WorkspaceStatus string

const (
	WorkspaceOpen   WorkspaceStatus = "open"
	WorkspaceClosed WorkspaceStatus = "closed"
)

// JoinPolicy controls who may join a room. Open rooms accept any join;
// invite-only rooms require a valid invite code minted by a moderator.
type JoinPolicy string

const (
	JoinOpen   JoinPolicy = "open"
	JoinInvite JoinPolicy = "invite"
)

// Valid reports whether p is a recognised join policy.
func (p JoinPolicy) Valid() bool { return p == JoinOpen || p == JoinInvite }

// BroadcastPolicy controls who may broadcast in a room. "anyone" is the
// default; "moderators" restricts fan-out to the (human) owner/moderators.
type BroadcastPolicy string

const (
	BroadcastAnyone     BroadcastPolicy = "anyone"
	BroadcastModerators BroadcastPolicy = "moderators"
)

// Valid reports whether p is a recognised broadcast policy.
func (p BroadcastPolicy) Valid() bool {
	return p == BroadcastAnyone || p == BroadcastModerators
}

// Workspace is a room: the first-class, human-owned container that members
// join and coordinate in. Before v0.2 workspaces existed only implicitly as a
// side effect of joining; now a room can be explicitly created, listed and
// closed. CreatedBy is the human owner; ClosedAt/ClosedBy record the last
// close (nil while open). JoinPolicy and WhoMayBroadcast are the M1.4 room
// policies (defaults: open / anyone).
type Workspace struct {
	Name            string          `json:"name"`
	Status          WorkspaceStatus `json:"status"`
	CreatedBy       string          `json:"created_by"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	ClosedBy        string          `json:"closed_by,omitempty"`
	ClosedAt        *time.Time      `json:"closed_at,omitempty"`
	JoinPolicy      JoinPolicy      `json:"join_policy,omitempty"`
	WhoMayBroadcast BroadcastPolicy `json:"who_may_broadcast,omitempty"`

	// BudgetDailyBytes is the room-wide DAILY byte budget for agent traffic
	// (0 = unlimited). Budgets bound AGENT coordination bytes (ingress+egress);
	// humans are exempt by design — a runaway agent must never silence the
	// humans who would stop it.
	BudgetDailyBytes int64 `json:"budget_daily_bytes"`
	// BudgetMemberDailyBytes is the per-agent daily byte cap (0 = unlimited).
	// Budgets bound AGENT coordination bytes (ingress+egress); humans are
	// exempt by design — a runaway agent must never silence the humans who
	// would stop it.
	BudgetMemberDailyBytes int64 `json:"budget_member_daily_bytes"`
}

// Kind distinguishes the two principal types that participate in a workspace.
type Kind string

const (
	KindHuman Kind = "human"
	KindAgent Kind = "agent"
)

// Valid reports whether k is a recognised member kind.
func (k Kind) Valid() bool {
	return k == KindHuman || k == KindAgent
}

// Role is a member's authority within a room. Owners and moderators (who must
// also be human) can close/reopen rooms, kick/ban members, view message
// history and change roles (owner only). The room creator becomes owner when
// they join.
type Role string

const (
	RoleOwner     Role = "owner"
	RoleModerator Role = "moderator"
	RoleMember    Role = "member"
)

// Valid reports whether r is a recognised role.
func (r Role) Valid() bool {
	return r == RoleOwner || r == RoleModerator || r == RoleMember
}

// Member is a participant in a workspace. Membership is durable: once a member
// has joined it exists until explicitly removed (leave/kick). LastSeen drives
// the *presence* display (who is active now) but never affects message
// delivery, which is addressed to durable members regardless of activity.
type Member struct {
	Workspace string          `json:"workspace"`
	Name      string          `json:"name"`
	Kind      Kind            `json:"kind"`
	Role      Role            `json:"role,omitempty"`
	AgentCard json.RawMessage `json:"agent_card,omitempty"`
	JoinedAt  time.Time       `json:"joined_at"`
	LastSeen  time.Time       `json:"last_seen"`
}

// Ban blocks a name from rejoining a room until lifted.
type Ban struct {
	Workspace string    `json:"workspace"`
	Name      string    `json:"name"`
	BannedBy  string    `json:"banned_by"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Invite is a joinable credential for a room: a code (stored only as its
// SHA-256 hash) that admits up to MaxUses principals of the given Kind,
// optionally granting the moderator role on join. ExpiresAt bounds its
// lifetime; RevokedAt is a soft kill-switch so issuance stays auditable.
type Invite struct {
	ID        string     `json:"id"`
	Workspace string     `json:"workspace"`
	Kind      Kind       `json:"kind"`
	Role      Role       `json:"role"`
	MaxUses   int        `json:"max_uses"`
	Uses      int        `json:"uses"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	CodeHash  string     `json:"-"`
}

// MessageKind separates point-to-point messages from fan-out broadcasts.
type MessageKind string

const (
	MessageDirect    MessageKind = "direct"
	MessageBroadcast MessageKind = "broadcast"
)

// Message is a unit delivered through the workspace inbox. A direct message has
// a single recipient; a broadcast fans out to every other member. Recipient and
// SenderKind are populated only when a message is read from a specific member's
// inbox: SenderKind is the sender's kind (human/agent) at read time, the trust
// signal receivers use to weigh the body — message bodies are always untrusted
// data, never instructions, and agent-originated content doubly so.
type Message struct {
	ID         string      `json:"id"`
	Workspace  string      `json:"workspace"`
	Sender     string      `json:"sender"`
	SenderKind Kind        `json:"sender_kind,omitempty"`
	Recipient  string      `json:"recipient,omitempty"`
	Kind       MessageKind `json:"kind"`
	Body       string      `json:"body"`
	CreatedAt  time.Time   `json:"created_at"`
}

// TaskStatus is the lifecycle state of a shared task.
//
//	pending   -> claimed             (an agent claims an eligible task)
//	claimed   -> completed | failed  (the assignee finishes it)
//	claimed   -> pending             (lease expired; reclaimed by another agent)
type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskClaimed   TaskStatus = "claimed"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
)

// Terminal reports whether the status is an end state.
func (s TaskStatus) Terminal() bool { return s == TaskCompleted || s == TaskFailed }

// Task is a unit of work on the shared task board. Tasks decouple "what needs
// doing" from "who does it": any agent may claim an eligible (pending, all
// dependencies completed) task, and SKIP LOCKED claiming guarantees no two
// agents claim the same one. A claim carries a lease (LeaseExpiresAt); if the
// assignee dies without completing, the lease lapses and the task becomes
// claimable again (work-stealing).
type Task struct {
	ID             string     `json:"id"`
	Workspace      string     `json:"workspace"`
	Title          string     `json:"title"`
	Details        string     `json:"details,omitempty"`
	Status         TaskStatus `json:"status"`
	CreatedBy      string     `json:"created_by"`
	AssignedAgent  string     `json:"assigned_agent,omitempty"`
	DependsOn      []string   `json:"depends_on,omitempty"`
	Result         string     `json:"result,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ClaimedAt      *time.Time `json:"claimed_at,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
}

// MemoryScope partitions the memory store. Private memories belong to one
// member and are visible only to it; shared memories are visible to the whole
// workspace — but only after passing the review queue.
type MemoryScope string

const (
	MemoryPrivate MemoryScope = "private"
	MemoryShared  MemoryScope = "shared"
)

// MemoryStatus is the review state of a memory item. Private writes are
// approved immediately; shared writes start pending and require an approval
// (by a human reviewer) before they become retrievable. This quarantine is the
// core defense against shared-memory poisoning: no agent can silently plant
// instructions that other agents will later retrieve.
type MemoryStatus string

const (
	MemoryApproved MemoryStatus = "approved"
	MemoryPending  MemoryStatus = "pending"
	MemoryRejected MemoryStatus = "rejected"
)

// Memory is one item of durable knowledge with full provenance. Owner is the
// owning member for private items and empty for shared ones. CreatedBy,
// Source, CreatedAt record where the fact came from; ReviewedBy/ReviewedAt/
// ReviewNote record the review decision for shared items.
type Memory struct {
	ID         string       `json:"id"`
	Workspace  string       `json:"workspace"`
	Scope      MemoryScope  `json:"scope"`
	Owner      string       `json:"owner,omitempty"`
	Status     MemoryStatus `json:"status"`
	Content    string       `json:"content"`
	Source     string       `json:"source,omitempty"`
	CreatedBy  string       `json:"created_by"`
	ReviewedBy string       `json:"reviewed_by,omitempty"`
	ReviewNote string       `json:"review_note,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
	ReviewedAt *time.Time   `json:"reviewed_at,omitempty"`
}

// AuthToken is the stored record of a bearer credential bound to one principal
// (workspace + member + kind). Secret is never stored — only its SHA-256 hash.
// Revocation is soft (RevokedAt) so issuance history remains auditable.
type AuthToken struct {
	ID        string     `json:"id"`
	TokenHash string     `json:"-"`
	Workspace string     `json:"workspace"`
	Member    string     `json:"member"`
	Kind      Kind       `json:"kind"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// Artifact is a co-edited document (design notes, a plan, a runbook) shared by
// the whole workspace. Concurrency is optimistic: every write carries the
// version it was based on, the server increments Version on success, and a
// stale write is rejected with a conflict so the writer re-reads, merges, and
// retries — no lost updates. (A CRDT engine can replace this strategy later if
// offline/peer editing is ever needed; the read/modify/write contract stays.)
type Artifact struct {
	Workspace string    `json:"workspace"`
	Name      string    `json:"name"`
	Content   string    `json:"content"`
	Version   int64     `json:"version"`
	CreatedBy string    `json:"created_by"`
	UpdatedBy string    `json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Event is an entry in the append-only episodic log. Events are the observation
// path (read via subscribe with a monotonic cursor) and are intentionally
// independent of inbox delivery: an event may reference a message but the
// correctness of read_inbox never depends on an event existing.
type Event struct {
	Seq       int64           `json:"seq"`
	Workspace string          `json:"workspace"`
	Source    string          `json:"source"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// --- Usage metering (M6) ---

// UsageDirection labels which side of a tool call a usage event measures.
// Ingress bytes are what the caller sent INTO the mesh (their completion
// tokens at their vendor); egress bytes are what the mesh returned to the
// caller (future prompt tokens in the caller's context window). Reported rows
// carry vendor-exact counts a client volunteered (M7) and are never
// platform-verified.
type UsageDirection string

const (
	UsageIngress  UsageDirection = "ingress"
	UsageEgress   UsageDirection = "egress"
	UsageReported UsageDirection = "reported"
)

// UsageEvent is one metered observation of coordination traffic. Bytes are the
// immutable ground truth; token counts are derived at display time with a
// configurable ratio so history re-renders under recalibration (the design
// decision in docs/token-metering.md §3 — deliberately NOT stored per event).
// The ledger stores sizes, never payload content: a metering leak must not
// become a data leak.
type UsageEvent struct {
	TS        time.Time      `json:"ts"`
	Workspace string         `json:"workspace"`
	Member    string         `json:"member"`
	Kind      Kind           `json:"kind"`
	Tool      string         `json:"tool"`
	Direction UsageDirection `json:"direction"`
	Bytes     int64          `json:"bytes"`
	// Authenticated is true when the attribution came from a verified
	// Principal; false when it was sniffed from tool arguments in auth-off
	// mode (a claimed, not proven, identity).
	Authenticated bool `json:"authenticated"`
	// Reported vendor usage (direction == reported only; zero otherwise).
	ReportedPromptTokens     int64  `json:"reported_prompt_tokens,omitempty"`
	ReportedCompletionTokens int64  `json:"reported_completion_tokens,omitempty"`
	Vendor                   string `json:"vendor,omitempty"`
	Model                    string `json:"model,omitempty"`
}

// UsageSummary is per-member aggregated usage inside one workspace and window.
type UsageSummary struct {
	Workspace                string `json:"workspace"`
	Member                   string `json:"member"`
	Kind                     Kind   `json:"kind"`
	IngressBytes             int64  `json:"ingress_bytes"`
	EgressBytes              int64  `json:"egress_bytes"`
	Events                   int64  `json:"events"`
	ReportedPromptTokens     int64  `json:"reported_prompt_tokens"`
	ReportedCompletionTokens int64  `json:"reported_completion_tokens"`
}

// UsageDay is one day of a workspace's rolled-up usage (UTC day buckets).
type UsageDay struct {
	Workspace    string    `json:"workspace"`
	Member       string    `json:"member"`
	Day          time.Time `json:"day"` // midnight UTC
	IngressBytes int64     `json:"ingress_bytes"`
	EgressBytes  int64     `json:"egress_bytes"`
	Events       int64     `json:"events"`
}
