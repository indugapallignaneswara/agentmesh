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
// a single recipient; a broadcast fans out to every other member. Recipient is
// populated only when a message is read from a specific member's inbox.
type Message struct {
	ID        string      `json:"id"`
	Workspace string      `json:"workspace"`
	Sender    string      `json:"sender"`
	Recipient string      `json:"recipient,omitempty"`
	Kind      MessageKind `json:"kind"`
	Body      string      `json:"body"`
	CreatedAt time.Time   `json:"created_at"`
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
