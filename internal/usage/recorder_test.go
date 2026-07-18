package usage_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/usage"
)

// fakeSink captures flushed batches. If block is non-nil, AppendUsage waits
// for it to be closed (or ctx to expire) before proceeding. err, when set,
// is returned instead of capturing.
type fakeSink struct {
	mu      sync.Mutex
	batches [][]model.UsageEvent
	err     error
	block   chan struct{}
}

func (s *fakeSink) AppendUsage(ctx context.Context, events []model.UsageEvent) error {
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	cp := make([]model.UsageEvent, len(events))
	copy(cp, events)
	s.batches = append(s.batches, cp)
	return nil
}

func (s *fakeSink) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// delivered returns all captured events flattened, in arrival order.
func (s *fakeSink) delivered() []model.UsageEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.UsageEvent
	for _, b := range s.batches {
		out = append(out, b...)
	}
	return out
}

func (s *fakeSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.batches {
		n += len(b)
	}
	return n
}

// ev builds a distinguishable event: Bytes carries a sequence number.
func ev(seq int) model.UsageEvent {
	return model.UsageEvent{
		TS:        time.Now(),
		Workspace: "ws",
		Member:    "alice",
		Tool:      "send_message",
		Direction: model.UsageIngress,
		Bytes:     int64(seq),
	}
}

// waitFor polls cond until it returns true or the deadline passes.
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

func TestHappyPathOrderedNoDrops(t *testing.T) {
	sink := &fakeSink{}
	r := usage.NewRecorder(sink, usage.Options{FlushBatch: 7, FlushInterval: 20 * time.Millisecond})
	const n = 100
	for i := 0; i < n; i++ {
		r.Record(ev(i))
	}
	r.Close()

	got := sink.delivered()
	if len(got) != n {
		t.Fatalf("delivered %d events, want %d", len(got), n)
	}
	for i, e := range got {
		if e.Bytes != int64(i) {
			t.Fatalf("event %d out of order: got seq %d", i, e.Bytes)
		}
	}
	if d := r.Dropped(); d != 0 {
		t.Fatalf("Dropped() = %d, want 0", d)
	}
}

func TestBatchTriggerFlushesBeforeInterval(t *testing.T) {
	sink := &fakeSink{}
	r := usage.NewRecorder(sink, usage.Options{FlushBatch: 10, FlushInterval: 10 * time.Second})
	defer r.Close()

	start := time.Now()
	for i := 0; i < 25; i++ {
		r.Record(ev(i))
	}
	if !waitFor(t, 3*time.Second, func() bool { return sink.count() >= 10 }) {
		t.Fatalf("first batch never arrived; delivered %d", sink.count())
	}
	if elapsed := time.Since(start); elapsed >= 10*time.Second {
		t.Fatalf("batch flush took %v, should be well before the 10s interval", elapsed)
	}

	r.Close()
	if got := sink.count(); got != 25 {
		t.Fatalf("after Close delivered %d, want 25", got)
	}
	if d := r.Dropped(); d != 0 {
		t.Fatalf("Dropped() = %d, want 0", d)
	}
}

func TestIntervalTriggerFlushesSmallBatch(t *testing.T) {
	sink := &fakeSink{}
	r := usage.NewRecorder(sink, usage.Options{FlushBatch: 1_000_000, FlushInterval: 50 * time.Millisecond})
	defer r.Close()

	for i := 0; i < 3; i++ {
		r.Record(ev(i))
	}
	if !waitFor(t, time.Second, func() bool { return sink.count() == 3 }) {
		t.Fatalf("interval flush never delivered 3 events; got %d", sink.count())
	}
}

// TestAdversarialDropAccounting is the roadmap exit criterion: with a tiny
// buffer and a blocked sink, Record must never block, must drop, and every
// one of the 10_000 events must be accounted for as delivered or dropped.
func TestAdversarialDropAccounting(t *testing.T) {
	release := make(chan struct{})
	sink := &fakeSink{block: release}
	var onDrop atomic.Int64
	r := usage.NewRecorder(sink, usage.Options{
		BufferSize:    8,
		FlushBatch:    16,
		FlushInterval: 10 * time.Millisecond,
		OnDrop:        func(n int) { onDrop.Add(int64(n)) },
	})

	const total = 10_000
	start := time.Now()
	for i := 0; i < total; i++ {
		r.Record(ev(i))
	}
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("Record blocked: 10k non-blocking sends took %v", elapsed)
	}
	if r.Dropped() == 0 {
		t.Fatal("expected drops with BufferSize=8 and a blocked sink, got none")
	}

	close(release)
	r.Close()

	delivered := int64(sink.count())
	dropped := r.Dropped()
	if delivered+dropped != total {
		t.Fatalf("lost accounting: delivered %d + dropped %d = %d, want %d",
			delivered, dropped, delivered+dropped, total)
	}
	if onDrop.Load() != dropped {
		t.Fatalf("OnDrop counted %d, Dropped() = %d; must agree", onDrop.Load(), dropped)
	}
}

func TestFlushErrorDropsBatchAndRecovers(t *testing.T) {
	sink := &fakeSink{err: errors.New("db down")}
	var onDrop atomic.Int64
	r := usage.NewRecorder(sink, usage.Options{
		FlushBatch:    5,
		FlushInterval: 20 * time.Millisecond,
		OnDrop:        func(n int) { onDrop.Add(int64(n)) },
	})
	defer r.Close()

	for i := 0; i < 5; i++ {
		r.Record(ev(i))
	}
	if !waitFor(t, 2*time.Second, func() bool { return r.Dropped() >= 5 }) {
		t.Fatalf("failed flush not counted as drops; Dropped() = %d", r.Dropped())
	}
	if got := sink.count(); got != 0 {
		t.Fatalf("sink captured %d events despite erroring, want 0", got)
	}
	if onDrop.Load() != r.Dropped() {
		t.Fatalf("OnDrop counted %d, Dropped() = %d", onDrop.Load(), r.Dropped())
	}

	// Recorder keeps working once the sink recovers.
	sink.setErr(nil)
	for i := 100; i < 105; i++ {
		r.Record(ev(i))
	}
	r.Close()
	if got := sink.count(); got != 5 {
		t.Fatalf("after recovery delivered %d, want 5", got)
	}
}

func TestCloseIdempotentAndRecordAfterClose(t *testing.T) {
	sink := &fakeSink{}
	r := usage.NewRecorder(sink, usage.Options{FlushInterval: 20 * time.Millisecond})
	r.Record(ev(1))
	r.Close()
	r.Close() // must not panic or deadlock

	before := r.Dropped()
	r.Record(ev(2)) // must not panic; silently drops
	if d := r.Dropped(); d != before+1 {
		t.Fatalf("Record after Close: Dropped() = %d, want %d", d, before+1)
	}
	if got := sink.count(); got != 1 {
		t.Fatalf("delivered %d, want 1 (post-Close event must not be delivered)", got)
	}
}

func TestNewRecorderNilSinkPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewRecorder(nil, ...) did not panic")
		}
	}()
	usage.NewRecorder(nil, usage.Options{})
}

func TestZeroValueRecorderSafe(t *testing.T) {
	var r usage.Recorder
	r.Record(ev(1)) // drops, no panic
	if d := r.Dropped(); d != 1 {
		t.Fatalf("zero-value Dropped() = %d, want 1", d)
	}
	r.Close()
	r.Close()
}

func TestEstTokens(t *testing.T) {
	cases := []struct {
		name  string
		bytes int64
		ratio float64
		want  int64
	}{
		{"default ratio", 4000, 0, 1000},
		{"explicit ratio", 4000, 2.0, 2000},
		{"zero bytes", 0, 0, 0},
		{"negative ratio falls back to default", 4000, -1, 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := usage.EstTokens(tc.bytes, tc.ratio); got != tc.want {
				t.Fatalf("EstTokens(%d, %v) = %d, want %d", tc.bytes, tc.ratio, got, tc.want)
			}
		})
	}
}
