// Package bus is the real-time fan-out layer. Postgres remains the
// authoritative system of record; the bus carries post-commit notifications to
// live consumers (session hooks, the future web UI, other coordination nodes).
// Publishes are therefore best-effort: a failure is reported to the caller for
// logging but never rolls back the persisted state.
package bus

import "context"

// Bus publishes coordination notifications to a subject hierarchy such as
// workspace.{id}.events or workspace.{id}.agent.{name}.inbox.
type Bus interface {
	Publish(ctx context.Context, subject string, data []byte) error
	Close() error
}

// Noop is a Bus that discards everything. It is used in tests and when no
// message bus is configured.
type Noop struct{}

// NewNoop returns a no-op bus.
func NewNoop() Noop { return Noop{} }

func (Noop) Publish(context.Context, string, []byte) error { return nil }
func (Noop) Close() error                                  { return nil }
