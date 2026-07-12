package workspace_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// newLimitedService builds a service with a tight, deterministic budget:
// broadcast burst 1 at 1 per 10s, send burst 2 at 1/s.
func newLimitedService(t *testing.T) (*workspace.Service, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	svc := workspace.New(store.NewMemory(), bus.NewNoop(),
		workspace.WithClock(c.now),
		workspace.WithIDGen(seqIDs()),
		workspace.WithRateLimits(workspace.RateLimits{
			Send: rate.Limit(1), SendBurst: 2,
			Broadcast: rate.Limit(0.1), BroadcastBurst: 1,
		}),
	)
	return svc, c
}

// TestRateLimitThrottlesFlood is the anti-flood guarantee: a looping agent
// exhausts its budget and is rejected with a retryable error, so the room
// stays usable long enough for a human to kick it (M1 moderation).
func TestRateLimitThrottlesFlood(t *testing.T) {
	svc, c := newLimitedService(t)
	ctx := context.Background()
	for _, m := range []struct {
		n string
		k model.Kind
	}{{"alice", model.KindHuman}, {"bot", model.KindAgent}} {
		if _, err := svc.Join(ctx, "ws", m.n, m.k, nil); err != nil {
			t.Fatal(err)
		}
	}

	// Burst of 2 sends is allowed...
	for i := 0; i < 2; i++ {
		if _, err := svc.SendMessage(ctx, "ws", "bot", "alice", "spam"); err != nil {
			t.Fatalf("send %d within burst: %v", i, err)
		}
	}
	// ...the third is throttled.
	_, err := svc.SendMessage(ctx, "ws", "bot", "alice", "spam")
	if !errors.Is(err, workspace.ErrRateLimited) {
		t.Fatalf("send past burst err = %v, want ErrRateLimited", err)
	}
	// After a second of quiet, one token refills.
	c.advance(time.Second)
	if _, err := svc.SendMessage(ctx, "ws", "bot", "alice", "ok now"); err != nil {
		t.Fatalf("send after refill: %v", err)
	}
}

func TestRateLimitBroadcastTighterAndPerPrincipal(t *testing.T) {
	svc, c := newLimitedService(t)
	ctx := context.Background()
	for _, m := range []struct {
		n string
		k model.Kind
	}{{"alice", model.KindHuman}, {"bot", model.KindAgent}, {"bot2", model.KindAgent}} {
		if _, err := svc.Join(ctx, "ws", m.n, m.k, nil); err != nil {
			t.Fatal(err)
		}
	}

	// bot gets its single broadcast token...
	if _, _, err := svc.Broadcast(ctx, "ws", "bot", "hello"); err != nil {
		t.Fatal(err)
	}
	// ...and is then throttled.
	if _, _, err := svc.Broadcast(ctx, "ws", "bot", "flood"); !errors.Is(err, workspace.ErrRateLimited) {
		t.Fatalf("broadcast past burst err = %v, want ErrRateLimited", err)
	}
	// Budgets are PER PRINCIPAL: bot2 is unaffected by bot's flood.
	if _, _, err := svc.Broadcast(ctx, "ws", "bot2", "unrelated"); err != nil {
		t.Fatalf("other principal throttled by bot's budget: %v", err)
	}
	// And sends still work — throttling is per operation, not global.
	if _, err := svc.SendMessage(ctx, "ws", "bot", "alice", "still fine"); err != nil {
		t.Fatalf("send throttled by broadcast budget: %v", err)
	}
	// Broadcast refills only after the (much longer) window.
	c.advance(10 * time.Second)
	if _, _, err := svc.Broadcast(ctx, "ws", "bot", "later"); err != nil {
		t.Fatalf("broadcast after refill window: %v", err)
	}
}

// TestRateLimitOffByDefault pins backward compatibility: with no limits
// configured, nothing is throttled.
func TestRateLimitOffByDefault(t *testing.T) {
	svc, _ := newService(t) // no WithRateLimits
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")
	mustJoin(t, svc, "ws", "bot")
	for i := 0; i < 50; i++ {
		if _, err := svc.SendMessage(ctx, "ws", "bot", "alice", "burst"); err != nil {
			t.Fatalf("send %d throttled with limiting off: %v", i, err)
		}
	}
}
