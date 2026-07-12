package workspace_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// TestAckModeCrashRecovery simulates the M2 exit criterion at the service
// layer: a reader that leases messages and dies without acking loses nothing —
// the messages redeliver after the visibility window; once acked they are
// final.
func TestAckModeCrashRecovery(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	svc := workspace.New(store.NewMemory(), bus.NewNoop(),
		workspace.WithClock(c.now),
		workspace.WithIDGen(seqIDs()),
		workspace.WithAckVisibility(10*time.Second),
	)
	ctx := context.Background()
	for _, m := range []struct {
		name string
		kind model.Kind
	}{{"alice", model.KindHuman}, {"bot", model.KindAgent}} {
		if _, err := svc.Join(ctx, "ws", m.name, m.kind, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.SendMessage(ctx, "ws", "alice", "bot", "critical instruction"); err != nil {
		t.Fatal(err)
	}

	// Lease the message ("the reader receives it...").
	msgs, err := svc.ReadInboxAck(ctx, "ws", "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("leased = %d, want 1", len(msgs))
	}
	id := msgs[0].ID

	// "...but crashes before acting." Within the window nothing re-leases.
	c.advance(5 * time.Second)
	again, err := svc.ReadInboxAck(ctx, "ws", "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("in-flight re-leased early: %d", len(again))
	}

	// Past the window: REDELIVERED — no message lost.
	c.advance(6 * time.Second)
	redelivered, err := svc.ReadInboxAck(ctx, "ws", "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(redelivered) != 1 || redelivered[0].ID != id {
		t.Fatalf("redelivery = %+v, want the same message", redelivered)
	}
	if redelivered[0].SenderKind != model.KindHuman {
		t.Fatalf("sender kind = %v, want human (annotation preserved)", redelivered[0].SenderKind)
	}

	// Ack -> gone forever.
	n, err := svc.AckMessages(ctx, "ws", "bot", []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("acked = %d, want 1", n)
	}
	c.advance(time.Minute)
	final, err := svc.ReadInboxAck(ctx, "ws", "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(final) != 0 {
		t.Fatalf("acked message redelivered: %+v", final)
	}
}

func TestAckValidation(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "bot")
	if _, err := svc.AckMessages(ctx, "ws", "bot", nil); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("empty ids err = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.AckMessages(ctx, "ws", "bot", []string{""}); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("blank id err = %v, want ErrInvalidInput", err)
	}
	// Foreign/bogus ids ack 0 silently.
	n, err := svc.AckMessages(ctx, "ws", "bot", []string{"nope"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("bogus ack = %d, want 0", n)
	}
}
