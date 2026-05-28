package bus

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// streamName is the JetStream stream that durably retains all workspace
// notifications so late-joining consumers can replay history.
const streamName = "AGENTMESH"

// NATS is a Bus backed by NATS JetStream. A single stream captures the entire
// workspace.> subject space.
type NATS struct {
	conn *nats.Conn
	js   jetstream.JetStream
}

// NewNATS connects to a NATS server and ensures the AGENTMESH stream exists.
func NewNATS(ctx context.Context, url string) (*NATS, error) {
	conn, err := nats.Connect(url,
		nats.Name("agentmesh"),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        streamName,
		Subjects:    []string{"workspace.>"},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		Description: "AgentMesh coordination notifications",
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ensure stream: %w", err)
	}
	return &NATS{conn: conn, js: js}, nil
}

// Publish sends data to subject and waits for the JetStream acknowledgement.
func (n *NATS) Publish(ctx context.Context, subject string, data []byte) error {
	if _, err := n.js.Publish(ctx, subject, data); err != nil {
		return fmt.Errorf("publish %s: %w", subject, err)
	}
	return nil
}

// Close drains and closes the underlying connection.
func (n *NATS) Close() error {
	return n.conn.Drain()
}
