package store_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// TestPostgresEventCursorNeverSkips is the regression guard for the event-log
// commit-order race: bigserial seq is assigned at INSERT but rows become
// visible at COMMIT, so without serialised appends a poller paging with
// `seq > cursor` can advance past a smaller seq that commits later and skip it
// forever. Here W writers append concurrently while a poller pages the whole
// time; every appended event must be observed exactly once. (Remove the
// advisory lock in AppendEvent and this test can lose events.)
func TestPostgresEventCursorNeverSkips(t *testing.T) {
	dsn := os.Getenv("AGENTMESH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set AGENTMESH_TEST_DATABASE_URL to run Postgres event-cursor test")
	}
	ctx := context.Background()
	pg, err := store.NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	if err := pg.TruncateAll(ctx); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	const (
		ws      = "evrace"
		writers = 8
		perW    = 50
	)
	total := writers * perW
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	wg.Add(writers)
	stopPoll := make(chan struct{})

	// Poller: pages continuously with a cursor while writes are in flight.
	seen := make(map[int64]bool)
	var pollErr error
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		var cursor int64
		for {
			evs, err := pg.EventsSince(ctx, ws, cursor, 25)
			if err != nil {
				pollErr = err
				return
			}
			for _, e := range evs {
				if seen[e.Seq] {
					pollErr = errDuplicate(e.Seq)
					return
				}
				seen[e.Seq] = true
				cursor = e.Seq
			}
			select {
			case <-stopPoll:
				// Drain once more after writers finish, then exit.
				for {
					evs, err := pg.EventsSince(ctx, ws, cursor, 100)
					if err != nil {
						pollErr = err
						return
					}
					if len(evs) == 0 {
						return
					}
					for _, e := range evs {
						seen[e.Seq] = true
						cursor = e.Seq
					}
				}
			default:
			}
		}
	}()

	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perW; i++ {
				if _, err := pg.AppendEvent(ctx, model.Event{
					Workspace: ws, Source: "s", Type: "tick", CreatedAt: now,
				}); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(stopPoll)
	<-pollDone

	if pollErr != nil {
		t.Fatalf("poller: %v", pollErr)
	}
	// The cursor-paging poller must have seen every event — a skip here means
	// the observation log lied to a subscriber.
	if len(seen) != total {
		t.Fatalf("poller observed %d events, want %d (events skipped by cursor)", len(seen), total)
	}
}

type errDuplicate int64

func (e errDuplicate) Error() string { return "duplicate seq observed" }
