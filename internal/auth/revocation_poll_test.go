package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// feedServer serves a swappable /revocations body and counts hits.
type feedServer struct {
	body atomic.Value // string
	hits atomic.Int64
	srv  *httptest.Server
}

func newFeedServer(t *testing.T, initial string) *feedServer {
	t.Helper()
	fs := &feedServer{}
	fs.body.Store(initial)
	fs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.hits.Add(1)
		body := fs.body.Load().(string)
		if body == "__500__" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(fs.srv.Close)
	return fs
}

// feedBody builds a feed JSON body listing the given jtis, mirroring the
// locked iam.RevocationFeed wire shape.
func feedBody(jtis ...string) string {
	entries := ""
	for i, j := range jtis {
		if i > 0 {
			entries += ","
		}
		entries += fmt.Sprintf(`{"jti":%q,"exp":"2099-01-01T00:00:00Z"}`, j)
	}
	return fmt.Sprintf(`{"as_of":"2026-01-01T00:00:00Z","entries":[%s]}`, entries)
}

// waitFor polls cond until it is true or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

func TestPollingRefreshOnce(t *testing.T) {
	fs := newFeedServer(t, feedBody("a", "b"))
	c := NewPollingRevocationChecker(fs.srv.URL, time.Hour)

	if c.IsRevoked("a") {
		t.Fatal("revoked before any refresh")
	}
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !c.IsRevoked("a") || !c.IsRevoked("b") {
		t.Error("expected a and b revoked after refresh")
	}
	if c.IsRevoked("c") {
		t.Error("unknown jti c reported revoked")
	}
	if c.LastSync().IsZero() {
		t.Error("lastSync not recorded after successful refresh")
	}
}

func TestPollingStartPicksUpChangesAndReplaces(t *testing.T) {
	fs := newFeedServer(t, feedBody("a", "b"))
	c := NewPollingRevocationChecker(fs.srv.URL, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)
	defer c.Stop()

	waitFor(t, 2*time.Second, func() bool { return c.IsRevoked("a") && c.IsRevoked("b") }, "initial feed loaded")

	// A new revocation appears at the AS: visible within one interval.
	fs.body.Store(feedBody("a", "b", "c"))
	waitFor(t, 2*time.Second, func() bool { return c.IsRevoked("c") }, `"c" revoked after feed update`)

	// The AS drops all entries (tokens expired): replace semantics, not merge.
	fs.body.Store(feedBody())
	waitFor(t, 2*time.Second, func() bool { return !c.IsRevoked("a") }, `"a" un-revoked after feed emptied`)
	if c.IsRevoked("b") || c.IsRevoked("c") {
		t.Error("stale entries lingered after empty feed (merge instead of replace?)")
	}
}

func TestPollingFailSafeKeepsCacheOnError(t *testing.T) {
	fs := newFeedServer(t, feedBody("a"))
	c := NewPollingRevocationChecker(fs.srv.URL, time.Hour)

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !c.IsRevoked("a") {
		t.Fatal("expected a revoked")
	}

	// AS starts returning 500: refresh errors, cache retained.
	fs.body.Store("__500__")
	if err := c.Refresh(context.Background()); err == nil {
		t.Error("expected error from 500 response")
	}
	if !c.IsRevoked("a") {
		t.Error("cache lost after 500 response — must fail safe, not un-revoke")
	}

	// AS returns garbage: same story.
	fs.body.Store(`{"as_of": not json`)
	if err := c.Refresh(context.Background()); err == nil {
		t.Error("expected error from garbage body")
	}
	if !c.IsRevoked("a") {
		t.Error("cache lost after garbage body — must fail safe, not un-revoke")
	}
}

func TestPollingConcurrentReadsDuringRefresh(t *testing.T) {
	fs := newFeedServer(t, feedBody("a"))
	c := NewPollingRevocationChecker(fs.srv.URL, time.Hour)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = c.IsRevoked("a")
					_ = c.IsRevoked("z")
				}
			}
		}()
	}
	// Writer: swap the set repeatedly while readers hammer it.
	for i := 0; i < 50; i++ {
		if i%2 == 0 {
			fs.body.Store(feedBody("a", "b"))
		} else {
			fs.body.Store(feedBody("a"))
		}
		if err := c.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh %d: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
	if !c.IsRevoked("a") {
		t.Error("a should remain revoked throughout")
	}
}

func TestPollingStopIdempotentAndHaltsPolling(t *testing.T) {
	fs := newFeedServer(t, feedBody("a"))
	c := NewPollingRevocationChecker(fs.srv.URL, 10*time.Millisecond)

	c.Start(context.Background())
	waitFor(t, 2*time.Second, func() bool { return fs.hits.Load() >= 3 }, "a few polls to land")

	c.Stop()
	c.Stop() // idempotent: second call must not panic or block

	// No fetch after Stop: hit count stays flat across several intervals.
	after := fs.hits.Load()
	time.Sleep(100 * time.Millisecond)
	if got := fs.hits.Load(); got != after {
		t.Errorf("feed fetched after Stop: %d -> %d", after, got)
	}

	// Stop before Start on a fresh checker is a no-op, not a panic.
	fresh := NewPollingRevocationChecker(fs.srv.URL, time.Hour)
	fresh.Stop()
}
