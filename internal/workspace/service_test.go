package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// clock is a controllable time source for deterministic tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newService builds a service over an in-memory store with a deterministic
// clock and sequential message IDs.
func newService(t *testing.T) (*workspace.Service, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	var n int
	var mu sync.Mutex
	id := func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return "msg-" + string(rune('a'+n-1))
	}
	svc := workspace.New(store.NewMemory(), bus.NewNoop(),
		workspace.WithClock(c.now),
		workspace.WithIDGen(id),
		workspace.WithPresenceTTL(60*time.Second),
	)
	return svc, c
}

func mustJoin(t *testing.T, svc *workspace.Service, ws, name string) {
	t.Helper()
	if _, err := svc.Join(context.Background(), ws, name, model.KindAgent, nil); err != nil {
		t.Fatalf("join %s: %v", name, err)
	}
}

func TestJoinAndPresence(t *testing.T) {
	svc, c := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")
	mustJoin(t, svc, "ws", "bob")

	present, err := svc.Presence(ctx, "ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(present) != 2 {
		t.Fatalf("present = %d, want 2", len(present))
	}

	// After the TTL elapses with no activity, nobody is "present"...
	c.advance(2 * time.Minute)
	present, err = svc.Presence(ctx, "ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(present) != 0 {
		t.Fatalf("present after TTL = %d, want 0", len(present))
	}
}

func TestJoinValidation(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	cases := []struct {
		name string
		ws   string
		mem  string
		kind model.Kind
	}{
		{"bad workspace", "has space", "alice", model.KindAgent},
		{"bad name", "ws", "has.dot", model.KindAgent},
		{"bad kind", "ws", "alice", model.Kind("robot")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Join(ctx, tc.ws, tc.mem, tc.kind, nil)
			if !errors.Is(err, workspace.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestSendMessageDelivery(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")
	mustJoin(t, svc, "ws", "bob")

	if _, err := svc.SendMessage(ctx, "ws", "alice", "bob", "hello bob"); err != nil {
		t.Fatal(err)
	}
	msgs, err := svc.ReadInbox(ctx, "ws", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Body != "hello bob" || msgs[0].Kind != model.MessageDirect {
		t.Fatalf("inbox = %+v", msgs)
	}
	// Consumed: a second read is empty.
	msgs, err = svc.ReadInbox(ctx, "ws", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("second read = %d, want 0", len(msgs))
	}
}

func TestSendMessageUnknownMember(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")

	if _, err := svc.SendMessage(ctx, "ws", "alice", "ghost", "hi"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown recipient err = %v, want ErrNotFound", err)
	}
	if _, err := svc.SendMessage(ctx, "ws", "ghost", "alice", "hi"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown sender err = %v, want ErrNotFound", err)
	}
}

func TestSendMessageEmptyBody(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")
	mustJoin(t, svc, "ws", "bob")
	if _, err := svc.SendMessage(ctx, "ws", "alice", "bob", ""); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("empty body err = %v, want ErrInvalidInput", err)
	}
}

func TestBroadcastFanOut(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")
	mustJoin(t, svc, "ws", "bob")
	mustJoin(t, svc, "ws", "carol")

	msg, recipients, err := svc.Broadcast(ctx, "ws", "alice", "all hands")
	if err != nil {
		t.Fatal(err)
	}
	if recipients != 2 || msg.Kind != model.MessageBroadcast {
		t.Fatalf("recipients = %d kind = %s, want 2 broadcast", recipients, msg.Kind)
	}
	for _, who := range []string{"bob", "carol"} {
		msgs, err := svc.ReadInbox(ctx, "ws", who)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 || msgs[0].Body != "all hands" {
			t.Fatalf("%s inbox = %+v", who, msgs)
		}
	}
	// Sender does not receive its own broadcast.
	msgs, err := svc.ReadInbox(ctx, "ws", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("sender inbox = %d, want 0", len(msgs))
	}
}

// TestBroadcastReachesInactiveMember is the critical correctness case: delivery
// is to durable members, not just those active within the presence TTL. A
// member mid-long-turn (no recent heartbeat) must still receive broadcasts.
func TestBroadcastReachesInactiveMember(t *testing.T) {
	svc, c := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")
	mustJoin(t, svc, "ws", "worker")

	// worker goes quiet well past the presence TTL.
	c.advance(10 * time.Minute)

	present, err := svc.Presence(ctx, "ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(present) != 0 {
		t.Fatalf("expected nobody present, got %d", len(present))
	}

	// alice rejoins (becomes active) and broadcasts.
	mustJoin(t, svc, "ws", "alice")
	_, recipients, err := svc.Broadcast(ctx, "ws", "alice", "ping inactive")
	if err != nil {
		t.Fatal(err)
	}
	if recipients != 1 {
		t.Fatalf("recipients = %d, want 1 (the inactive worker)", recipients)
	}
	msgs, err := svc.ReadInbox(ctx, "ws", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Body != "ping inactive" {
		t.Fatalf("inactive worker inbox = %+v, want the broadcast", msgs)
	}
}

func TestPublishAndSubscribe(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")

	// Joining already produced a member_joined system event; capture the cursor.
	events, cursor, err := svc.Subscribe(ctx, "ws", "alice", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].Type != workspace.EventMemberJoined {
		t.Fatalf("first event = %+v, want member_joined", events)
	}

	if _, err := svc.PublishEvent(ctx, "ws", "alice", "deploy", json.RawMessage(`{"env":"prod"}`)); err != nil {
		t.Fatal(err)
	}
	events, cursor, err = svc.Subscribe(ctx, "ws", "alice", cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "deploy" {
		t.Fatalf("after cursor = %+v, want one deploy", events)
	}
	if string(events[0].Payload) != `{"env":"prod"}` {
		t.Fatalf("payload = %s", events[0].Payload)
	}
	_ = cursor
}

func TestSubscribeHeartbeat(t *testing.T) {
	svc, c := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "poller")

	// Advance almost to the TTL, then poll (which should refresh presence).
	c.advance(50 * time.Second)
	if _, _, err := svc.Subscribe(ctx, "ws", "poller", 0, 100); err != nil {
		t.Fatal(err)
	}
	// Advance again by under the TTL from the poll; poller should still be present.
	c.advance(50 * time.Second)
	present, err := svc.Presence(ctx, "ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(present) != 1 {
		t.Fatalf("present = %d, want 1 (heartbeat kept poller alive)", len(present))
	}
}

func TestPublishEventUnknownSource(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.PublishEvent(ctx, "ws", "ghost", "x", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
