package workspace

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ErrRateLimited is returned when a principal exceeds its budget for an
// operation. It is retryable: the caller should back off and try again.
var ErrRateLimited = errors.New("rate limited")

// RateLimits configures per-principal token buckets. A zero rate disables
// limiting for that operation (the default, so existing deployments and the
// demo are unaffected until limits are turned on).
type RateLimits struct {
	// Send bounds send_message calls.
	Send      rate.Limit
	SendBurst int
	// Broadcast bounds broadcast calls — deliberately much tighter, since one
	// call fans out to every member.
	Broadcast      rate.Limit
	BroadcastBurst int
	// Event bounds publish_event calls.
	Event      rate.Limit
	EventBurst int
}

// DefaultRateLimits are sane production budgets: agents are loops, so a single
// misbehaving one must not be able to drown a room before a human can kick it
// (the M1 moderation tools are the remedy; this buys the human time).
func DefaultRateLimits() RateLimits {
	return RateLimits{
		Send: rate.Limit(1), SendBurst: 10, // ~1/s, burst 10
		Broadcast: rate.Limit(0.1), BroadcastBurst: 2, // ~1 per 10s, burst 2
		Event: rate.Limit(5), EventBurst: 20, // ~5/s, burst 20
	}
}

// op identifies a rate-limited operation.
type op string

const (
	opSend      op = "send_message"
	opBroadcast op = "broadcast"
	opEvent     op = "publish_event"
)

// limiter holds per-(workspace, member, op) token buckets. Buckets are created
// lazily and swept periodically so a churning membership cannot leak memory.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	limits  RateLimits
	now     func() time.Time
}

type bucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

func newLimiter(limits RateLimits, now func() time.Time) *limiter {
	return &limiter{buckets: make(map[string]*bucket), limits: limits, now: now}
}

// rateFor returns the configured limit/burst for an operation.
func (l *limiter) rateFor(o op) (rate.Limit, int) {
	switch o {
	case opSend:
		return l.limits.Send, l.limits.SendBurst
	case opBroadcast:
		return l.limits.Broadcast, l.limits.BroadcastBurst
	case opEvent:
		return l.limits.Event, l.limits.EventBurst
	}
	return 0, 0
}

// allow reports whether the principal may perform the operation now. A zero
// configured rate means "unlimited" and always allows.
func (l *limiter) allow(workspace, member string, o op) error {
	r, burst := l.rateFor(o)
	if r <= 0 || burst <= 0 {
		return nil // limiting disabled for this op
	}
	key := string(o) + "\x00" + workspace + "\x00" + member

	l.mu.Lock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(r, burst)}
		l.buckets[key] = b
		l.sweepLocked()
	}
	now := l.now()
	b.lastSeen = now
	l.mu.Unlock()

	if !b.lim.AllowN(now, 1) {
		return fmt.Errorf("%w: %s budget exhausted for %q; retry shortly", ErrRateLimited, o, member)
	}
	return nil
}

// sweepLocked drops buckets unused for an hour. Called on bucket creation, so
// the cost is amortised and no background goroutine is needed. Caller holds mu.
func (l *limiter) sweepLocked() {
	if len(l.buckets) < 1024 {
		return
	}
	cutoff := l.now().Add(-time.Hour)
	for k, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}
