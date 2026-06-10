// Package model holds the core domain types shared across the coordination
// workspace: members (presence), messages (inbox), and events (episodic log).
package model

import (
	"encoding/json"
	"time"
)

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

// Member is a participant in a workspace. Membership is durable: once a member
// has joined it exists until explicitly removed. LastSeen drives the *presence*
// display (who is active now) but never affects message delivery, which is
// addressed to durable members regardless of activity.
type Member struct {
	Workspace string          `json:"workspace"`
	Name      string          `json:"name"`
	Kind      Kind            `json:"kind"`
	AgentCard json.RawMessage `json:"agent_card,omitempty"`
	JoinedAt  time.Time       `json:"joined_at"`
	LastSeen  time.Time       `json:"last_seen"`
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
