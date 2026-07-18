// Package usage implements the M6 token-metering write path
// (docs/token-metering.md §4): a non-blocking, batching recorder that moves
// UsageEvents from the tool-call hot path to a Sink (the store) on a single
// background flusher goroutine.
//
// Posture, per the design doc: metering degrades, coordination never does.
// Record never blocks and never adds a store round-trip; overflow and flush
// failures drop events and count them, they never stall a tool call.
package usage

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

// Sink receives flushed batches of usage events. *store.Memory and
// *store.Postgres satisfy it structurally; this package deliberately does not
// import internal/store. Implementations must not retain the events slice
// after AppendUsage returns — the recorder reuses the backing array — and
// should honor ctx cancellation.
type Sink interface {
	AppendUsage(ctx context.Context, events []model.UsageEvent) error
}

// Defaults applied by NewRecorder when the corresponding Options field is
// zero or negative.
const (
	DefaultBufferSize    = 8192
	DefaultFlushInterval = 2 * time.Second
	DefaultFlushBatch    = 500

	// flushTimeout bounds each AppendUsage call so a stuck sink cannot wedge
	// the flusher forever.
	flushTimeout = 5 * time.Second
)

// Options configures a Recorder. The zero value is usable: every field has a
// default or is optional.
type Options struct {
	BufferSize    int           // channel capacity; default 8192
	FlushInterval time.Duration // default 2s
	FlushBatch    int           // flush when this many buffered; default 500
	// OnDrop is called (possibly batched: n >= 1) whenever events are
	// dropped — buffer overflow, flush failure, or Record after Close. It may
	// be invoked concurrently from multiple goroutines and must be
	// goroutine-safe and fast. Optional.
	OnDrop func(n int)
	Logger *slog.Logger // optional; flush failures are logged here
}

// Recorder buffers usage events and flushes them to its Sink in batches from
// one background goroutine. The zero value is inert but safe: Record drops
// (and counts) every event, Close and Dropped are no-ops. Use NewRecorder for
// a working instance.
type Recorder struct {
	sink   Sink
	ch     chan model.UsageEvent
	opts   Options
	logger *slog.Logger

	dropped   atomic.Int64
	closed    atomic.Bool
	closeOnce sync.Once
	done      chan struct{} // closed by Close to stop the flusher
	finished  chan struct{} // closed by the flusher when it exits
}

// NewRecorder starts a recorder flushing to sink. sink must be non-nil;
// NewRecorder panics otherwise (fail fast at wiring time rather than silently
// discarding metering data — pass a no-op Sink if metering is off). Zero or
// negative Options fields take the package defaults. Call Close to stop the
// flusher and flush the remainder.
func NewRecorder(sink Sink, opts Options) *Recorder {
	if sink == nil {
		panic("usage.NewRecorder: nil Sink")
	}
	if opts.BufferSize <= 0 {
		opts.BufferSize = DefaultBufferSize
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = DefaultFlushInterval
	}
	if opts.FlushBatch <= 0 {
		opts.FlushBatch = DefaultFlushBatch
	}
	r := &Recorder{
		sink:     sink,
		ch:       make(chan model.UsageEvent, opts.BufferSize),
		opts:     opts,
		logger:   opts.Logger,
		done:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	go r.run()
	return r
}

// Record enqueues one usage event. It never blocks: if the buffer is full or
// the recorder is closed (or is the zero value), the event is dropped, the
// drop counter is incremented, and OnDrop(1) is called if set. Safe for
// concurrent use.
func (r *Recorder) Record(ev model.UsageEvent) {
	if r.ch == nil || r.closed.Load() {
		r.drop(1)
		return
	}
	select {
	case r.ch <- ev:
	default:
		r.drop(1)
	}
}

// Dropped reports the total number of events dropped so far: buffer
// overflows, failed-flush batches, and Records after Close.
func (r *Recorder) Dropped() int64 {
	return r.dropped.Load()
}

// Close stops the flusher and flushes whatever is buffered (each flush bounded
// by a short timeout context). Idempotent; blocks until the final flush
// completes. Record after Close silently drops (counted, no panic).
func (r *Recorder) Close() {
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		if r.done == nil { // zero-value Recorder: nothing running
			return
		}
		close(r.done)
		<-r.finished
	})
}

func (r *Recorder) drop(n int) {
	r.dropped.Add(int64(n))
	if r.opts.OnDrop != nil {
		r.opts.OnDrop(n)
	}
}

// run is the single flusher goroutine: it drains the channel into a batch and
// flushes when the batch reaches FlushBatch or on each FlushInterval tick with
// a non-empty batch. On done it drains what remains and performs a final
// flush.
func (r *Recorder) run() {
	defer close(r.finished)
	ticker := time.NewTicker(r.opts.FlushInterval)
	defer ticker.Stop()

	batch := make([]model.UsageEvent, 0, r.opts.FlushBatch)
	for {
		select {
		case ev := <-r.ch:
			batch = append(batch, ev)
			if len(batch) >= r.opts.FlushBatch {
				r.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				r.flush(batch)
				batch = batch[:0]
			}
		case <-r.done:
			for {
				select {
				case ev := <-r.ch:
					batch = append(batch, ev)
					if len(batch) >= r.opts.FlushBatch {
						r.flush(batch)
						batch = batch[:0]
					}
				default:
					if len(batch) > 0 {
						r.flush(batch)
					}
					return
				}
			}
		}
	}
}

// flush writes one batch to the sink under a timeout. A flush error is logged
// (if a Logger was provided) and the whole batch is counted as dropped —
// metering is best-effort, the ledger tolerates gaps.
func (r *Recorder) flush(batch []model.UsageEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	if err := r.sink.AppendUsage(ctx, batch); err != nil {
		if r.logger != nil {
			r.logger.Error("usage: flush failed, dropping batch",
				"events", len(batch), "error", err)
		}
		r.drop(len(batch))
	}
}
