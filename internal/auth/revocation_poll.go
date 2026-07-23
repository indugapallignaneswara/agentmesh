package auth

// PollingRevocationChecker: the resource-server half of token revocation (P3).
// The authorization server owns the jti denylist and publishes the currently
// active entries at GET /revocations; this checker polls that feed on an
// interval and caches the set locally, so IsRevoked is a map lookup on the
// request hot path — no per-request network call.
//
// Freshness/availability tradeoff (the standard CRL-style one): a revocation
// becomes visible here within one interval of a successful poll. On a fetch or
// decode error the previous cache is KEPT and the error logged — fail-safe,
// not fail-closed. Failing closed on staleness would take the whole mesh down
// whenever the authorization server blips, which is a worse posture for
// short-lived JIT tokens; during an AS outage the last-known denylist persists
// and tokens remain bounded by their own exp (checked separately by the
// verifier).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// feedJTI and feed mirror iam.Revocation and iam.RevocationFeed by wire
// contract (the LOCKED JSON body of GET /revocations). They are redeclared
// here, with the same json tags, so that internal/auth does not grow an import
// edge on internal/iam — the two sides of P3 are separate processes coupled
// only by the feed's JSON shape.
type feedJTI struct {
	JTI string    `json:"jti"`
	Exp time.Time `json:"exp"`
}

type feed struct {
	AsOf    time.Time `json:"as_of"`
	Entries []feedJTI `json:"entries"`
}

// PollingRevocationChecker satisfies RevocationChecker by polling an
// authorization server's /revocations feed and caching the revoked-jti set.
//
// Concurrency: the cached set lives in an atomic.Value holding an immutable
// map[string]struct{}; readers load it without locking and each refresh swaps
// in a freshly built map. IsRevoked is therefore cheap enough to sit on the
// per-request path. The mutex only serialises the writer side (refresh,
// lastSync, start/stop state).
type PollingRevocationChecker struct {
	feedURL  string
	interval time.Duration
	client   *http.Client
	logger   *slog.Logger

	revoked atomic.Value // map[string]struct{}, replaced wholesale on refresh

	mu       sync.Mutex
	lastSync time.Time
	stop     chan struct{} // non-nil once Start has run
	stopped  bool
	done     chan struct{} // closed when the polling goroutine exits
}

// PollOption customises a PollingRevocationChecker.
type PollOption func(*PollingRevocationChecker)

// WithPollHTTPClient overrides the HTTP client used to fetch the feed
// (defaults to a 10s-timeout client, matching the JWKS fetcher).
func WithPollHTTPClient(c *http.Client) PollOption {
	return func(p *PollingRevocationChecker) {
		if c != nil {
			p.client = c
		}
	}
}

// WithPollLogger sets the logger for fetch failures (default: discard).
func WithPollLogger(l *slog.Logger) PollOption {
	return func(p *PollingRevocationChecker) {
		if l != nil {
			p.logger = l
		}
	}
}

// NewPollingRevocationChecker builds a checker for the given feed URL. An
// interval <= 0 defaults to 30s. The checker is usable immediately (IsRevoked
// reports false for everything) but stays empty until Start or Refresh runs.
func NewPollingRevocationChecker(feedURL string, interval time.Duration, opts ...PollOption) *PollingRevocationChecker {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	p := &PollingRevocationChecker{
		feedURL:  feedURL,
		interval: interval,
		client:   &http.Client{Timeout: 10 * time.Second},
		logger:   slog.New(slog.DiscardHandler),
	}
	for _, opt := range opts {
		opt(p)
	}
	p.revoked.Store(map[string]struct{}{})
	return p
}

// IsRevoked reports whether jti appears in the last successfully fetched
// denylist. Unknown jtis are not revoked. Safe under concurrent calls with a
// running refresh: the read is a lock-free atomic load of an immutable map.
func (p *PollingRevocationChecker) IsRevoked(jti string) bool {
	set := p.revoked.Load().(map[string]struct{})
	_, ok := set[jti]
	return ok
}

// LastSync returns the time of the last successful feed fetch (zero if none).
func (p *PollingRevocationChecker) LastSync() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastSync
}

// Refresh fetches the feed once, synchronously, and atomically replaces the
// cached set on success. On error the previous cache is retained (fail-safe:
// a transient AS outage must not silently un-revoke tokens).
func (p *PollingRevocationChecker) Refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.feedURL, nil)
	if err != nil {
		return err
	}
	res, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch revocations: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch revocations: status %d", res.StatusCode)
	}
	var f feed
	if err := json.NewDecoder(res.Body).Decode(&f); err != nil {
		return fmt.Errorf("decode revocations: %w", err)
	}

	// The feed is the FULL currently-active set, so replace rather than merge:
	// merging would keep entries the AS has dropped after their token expired.
	// Only membership matters here — each entry's exp can be ignored because
	// the feed already excludes expired entries, and a revoked token past its
	// own exp is rejected by the verifier's exp check anyway.
	set := make(map[string]struct{}, len(f.Entries))
	for _, e := range f.Entries {
		set[e.JTI] = struct{}{}
	}
	p.revoked.Store(set)

	p.mu.Lock()
	p.lastSync = time.Now()
	p.mu.Unlock()
	return nil
}

// Start launches the background poller: one immediate fetch, then one every
// interval, until ctx is cancelled or Stop is called. Start is not designed to
// be called more than once.
func (p *PollingRevocationChecker) Start(ctx context.Context) {
	p.mu.Lock()
	if p.stop != nil {
		p.mu.Unlock()
		return // already started
	}
	p.stop = make(chan struct{})
	p.done = make(chan struct{})
	stop, done := p.stop, p.done
	p.mu.Unlock()

	go func() {
		defer close(done)
		if err := p.Refresh(ctx); err != nil {
			p.logger.Warn("revocation feed refresh failed; keeping previous denylist", "url", p.feedURL, "error", err)
		}
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				if err := p.Refresh(ctx); err != nil {
					p.logger.Warn("revocation feed refresh failed; keeping previous denylist", "url", p.feedURL, "error", err)
				}
			}
		}
	}()
}

// Stop halts the background poller and waits for it to exit. Idempotent, and
// a no-op if Start was never called.
func (p *PollingRevocationChecker) Stop() {
	p.mu.Lock()
	if p.stop == nil || p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	close(p.stop)
	done := p.done
	p.mu.Unlock()
	<-done
}

// Compile-time proof that the poller satisfies the locked interface.
var _ RevocationChecker = (*PollingRevocationChecker)(nil)
