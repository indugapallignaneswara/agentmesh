package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

const vis = 30 * time.Second

func sendTo(t *testing.T, s store.Store, ws, id, to string, at time.Time) {
	t.Helper()
	mustNoErr(t, s.CreateMessage(context.Background(), model.Message{
		ID: id, Workspace: ws, Sender: "sender", Kind: model.MessageDirect,
		Body: "body-" + id, CreatedAt: at,
	}, []string{to}))
}

// testLeasedReadRedelivers is the at-least-once core: a leased message is
// invisible during its visibility window and redelivered after it — a crashed
// reader loses nothing.
func testLeasedReadRedelivers(t *testing.T, s store.Store) {
	ctx := context.Background()
	join(t, s, "ws1", "bob", base)
	sendTo(t, s, "ws1", "m1", "bob", base)

	got, err := s.ReadInboxLeased(ctx, "ws1", "bob", base, vis)
	mustNoErr(t, err)
	if len(got) != 1 || got[0].ID != "m1" || got[0].Recipient != "bob" {
		t.Fatalf("leased read = %+v, want [m1]", got)
	}
	// Within the window: nothing (in flight).
	got, err = s.ReadInboxLeased(ctx, "ws1", "bob", base.Add(10*time.Second), vis)
	mustNoErr(t, err)
	if len(got) != 0 {
		t.Fatalf("in-flight message re-leased early: %+v", got)
	}
	// Past the deadline: the SAME message redelivers (reader crashed, no ack).
	got, err = s.ReadInboxLeased(ctx, "ws1", "bob", base.Add(vis+time.Second), vis)
	mustNoErr(t, err)
	if len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("redelivery after visibility = %+v, want [m1]", got)
	}
}

func testAckFinalises(t *testing.T, s store.Store) {
	ctx := context.Background()
	join(t, s, "ws1", "bob", base)
	sendTo(t, s, "ws1", "m1", "bob", base)

	got, err := s.ReadInboxLeased(ctx, "ws1", "bob", base, vis)
	mustNoErr(t, err)
	if len(got) != 1 {
		t.Fatalf("leased = %d, want 1", len(got))
	}
	n, err := s.AckInbox(ctx, "ws1", "bob", []string{"m1"}, base.Add(time.Second))
	mustNoErr(t, err)
	if n != 1 {
		t.Fatalf("acked = %d, want 1", n)
	}
	// Acked message never redelivers, even past the deadline.
	got, err = s.ReadInboxLeased(ctx, "ws1", "bob", base.Add(vis+time.Minute), vis)
	mustNoErr(t, err)
	if len(got) != 0 {
		t.Fatalf("acked message redelivered: %+v", got)
	}
	// Double-ack counts 0.
	n, err = s.AckInbox(ctx, "ws1", "bob", []string{"m1"}, base.Add(2*time.Second))
	mustNoErr(t, err)
	if n != 0 {
		t.Fatalf("double ack = %d, want 0", n)
	}
}

func testPartialAck(t *testing.T, s store.Store) {
	ctx := context.Background()
	join(t, s, "ws1", "bob", base)
	sendTo(t, s, "ws1", "m1", "bob", base)
	sendTo(t, s, "ws1", "m2", "bob", base.Add(time.Second))
	sendTo(t, s, "ws1", "m3", "bob", base.Add(2*time.Second))

	got, err := s.ReadInboxLeased(ctx, "ws1", "bob", base.Add(3*time.Second), vis)
	mustNoErr(t, err)
	if len(got) != 3 {
		t.Fatalf("leased = %d, want 3", len(got))
	}
	n, err := s.AckInbox(ctx, "ws1", "bob", []string{"m2"}, base.Add(4*time.Second))
	mustNoErr(t, err)
	if n != 1 {
		t.Fatalf("acked = %d, want 1", n)
	}
	// After expiry only the unacked two redeliver, oldest-first.
	got, err = s.ReadInboxLeased(ctx, "ws1", "bob", base.Add(3*time.Second+vis+time.Second), vis)
	mustNoErr(t, err)
	if len(got) != 2 || got[0].ID != "m1" || got[1].ID != "m3" {
		t.Fatalf("redelivered = %v, want [m1 m3]", messageIDs(got))
	}
}

func testAckIgnoresForeignIds(t *testing.T, s store.Store) {
	ctx := context.Background()
	join(t, s, "ws1", "bob", base)
	join(t, s, "ws1", "eve", base)
	sendTo(t, s, "ws1", "m1", "bob", base)

	// eve acking bob's message id: counts 0, does not consume it.
	n, err := s.AckInbox(ctx, "ws1", "eve", []string{"m1", "bogus"}, base)
	mustNoErr(t, err)
	if n != 0 {
		t.Fatalf("foreign ack = %d, want 0", n)
	}
	got, err := s.ReadInboxLeased(ctx, "ws1", "bob", base, vis)
	mustNoErr(t, err)
	if len(got) != 1 {
		t.Fatalf("bob's message consumed by foreign ack: %d msgs", len(got))
	}
}

// testLeasedAndPlainInterop pins mixed-mode behavior: plain ReadInbox
// eligibility is delivered_at only, so it consumes even in-flight messages —
// a deliberate "plain read finalises" rule.
func testLeasedAndPlainInterop(t *testing.T, s store.Store) {
	ctx := context.Background()
	join(t, s, "ws1", "bob", base)
	sendTo(t, s, "ws1", "m1", "bob", base)

	if _, err := s.ReadInboxLeased(ctx, "ws1", "bob", base, vis); err != nil {
		t.Fatal(err)
	}
	// Plain read while in flight: consumes it.
	got, err := s.ReadInbox(ctx, "ws1", "bob", base.Add(time.Second))
	mustNoErr(t, err)
	if len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("plain read during flight = %v, want [m1]", messageIDs(got))
	}
	// Nothing redelivers after that, even past the deadline.
	got, err = s.ReadInboxLeased(ctx, "ws1", "bob", base.Add(vis+time.Minute), vis)
	mustNoErr(t, err)
	if len(got) != 0 {
		t.Fatalf("consumed message redelivered: %+v", got)
	}
}
