package bus_test

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/indugapallignaneswara/agentmesh/internal/bus"
)

// startEmbeddedNATS runs a real NATS server with JetStream inside the test
// process — no external daemon or container needed, so this runs in CI as-is.
func startEmbeddedNATS(t *testing.T) *natsserver.Server {
	t.Helper()
	ns, err := natsserver.NewServer(&natsserver.Options{
		Port:      -1, // random free port
		JetStream: true,
		StoreDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go ns.Start()
	t.Cleanup(ns.Shutdown)
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS not ready")
	}
	return ns
}

// TestNATSBusPublishAndReplay proves the production bus path end to end:
// NewNATS provisions the AGENTMESH stream, Publish lands messages durably on
// workspace.> subjects, and a late-joining consumer can replay history (the
// blueprint's late-joiner property).
func TestNATSBusPublishAndReplay(t *testing.T) {
	ns := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	b, err := bus.NewNATS(ctx, ns.ClientURL())
	if err != nil {
		t.Fatalf("NewNATS: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	// Publish BEFORE any consumer exists — replay must still deliver these.
	if err := b.Publish(ctx, "workspace.team.events", []byte(`{"seq":1}`)); err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	if err := b.Publish(ctx, "workspace.team.agent.backend.inbox", []byte(`{"msg":"hi"}`)); err != nil {
		t.Fatalf("publish 2: %v", err)
	}

	// A separate, late-joining consumer connection.
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatalf("consumer connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}

	// The bus must have provisioned the durable stream over workspace.>.
	stream, err := js.Stream(ctx, "AGENTMESH")
	if err != nil {
		t.Fatalf("AGENTMESH stream missing: %v", err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 2 {
		t.Fatalf("stream holds %d msgs, want 2", info.State.Msgs)
	}

	// Replay everything from the beginning and verify subjects + payloads.
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := cons.Fetch(2, jetstream.FetchMaxWait(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var got []jetstream.Msg
	for m := range batch.Messages() {
		got = append(got, m)
		_ = m.Ack()
	}
	if err := batch.Error(); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("replayed %d msgs, want 2", len(got))
	}
	if got[0].Subject() != "workspace.team.events" || string(got[0].Data()) != `{"seq":1}` {
		t.Fatalf("msg1 = %s %s", got[0].Subject(), got[0].Data())
	}
	if got[1].Subject() != "workspace.team.agent.backend.inbox" {
		t.Fatalf("msg2 subject = %s", got[1].Subject())
	}
}
