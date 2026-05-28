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
