// Package metrics exposes AgentMesh's operational counters in the Prometheus
// text exposition format. It is deliberately dependency-free: the whole point
// of AgentMesh is a single self-hosted binary, and a scrape endpoint does not
// justify pulling in a client library. The format is simple and stable —
// https://prometheus.io/docs/instrumenting/exposition_formats/
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// latency buckets in seconds (cumulative, Prometheus histogram semantics).
var buckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Registry collects metrics. The zero value is not usable; call New.
type Registry struct {
	mu sync.Mutex

	// tool call counters keyed by tool name, split by outcome.
	toolCalls  map[string]uint64
	toolErrors map[string]uint64
	// latency histograms keyed by tool name: bucket index -> count.
	toolHist map[string][]uint64
	toolSum  map[string]float64

	// http request counters keyed by "path status".
	httpReqs map[string]uint64

	// usage byte counters keyed by "workspace\x00tool\x00direction". Workspace
	// cardinality is capped (see usageWorkspaceCap): beyond the cap, new
	// workspaces aggregate under "_other" — same bounded-cardinality
	// discipline as normalisePath. Exact per-member accounting lives in the
	// usage ledger, not here.
	usageBytes   map[string]uint64
	usageWs      map[string]bool // workspaces currently tracked distinctly
	usageDropped uint64          // ledger events dropped (buffer overflow / flush failure)

	startedAt time.Time
	// gauges are sampled at scrape time by the collector func, if set.
	gauges func() map[string]float64
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{
		toolCalls:  make(map[string]uint64),
		toolErrors: make(map[string]uint64),
		toolHist:   make(map[string][]uint64),
		toolSum:    make(map[string]float64),
		httpReqs:   make(map[string]uint64),
		usageBytes: make(map[string]uint64),
		usageWs:    make(map[string]bool),
		startedAt:  time.Now(),
	}
}

// SetGauges registers a callback sampled on each scrape (e.g. queue depths).
// Keys become gauge metric names, prefixed agentmesh_.
func (r *Registry) SetGauges(f func() map[string]float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges = f
}

// ObserveTool records one tool call: its duration and whether it errored.
func (r *Registry) ObserveTool(tool string, d time.Duration, isErr bool) {
	secs := d.Seconds()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolCalls[tool]++
	if isErr {
		r.toolErrors[tool]++
	}
	h, ok := r.toolHist[tool]
	if !ok {
		h = make([]uint64, len(buckets))
		r.toolHist[tool] = h
	}
	for i, b := range buckets {
		if secs <= b {
			h[i]++
		}
	}
	r.toolSum[tool] += secs
}

// ObserveHTTP records one HTTP response.
func (r *Registry) ObserveHTTP(path string, status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.httpReqs[path+" "+strconv.Itoa(status)]++
}

// usageWorkspaceCap bounds the number of workspaces given distinct label
// values in usage counters; the rest fold into "_other".
const usageWorkspaceCap = 100

// ObserveUsage adds metered bytes for one tool call side. Synchronous and
// in-memory (a mutex around a map — nanoseconds), so aggregate counters stay
// accurate even when the async ledger drops rows.
func (r *Registry) ObserveUsage(workspace, tool, direction string, bytes int64) {
	if bytes < 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.usageWs[workspace] {
		if len(r.usageWs) >= usageWorkspaceCap {
			workspace = "_other"
		} else {
			r.usageWs[workspace] = true
		}
	}
	r.usageBytes[workspace+"\x00"+tool+"\x00"+direction] += uint64(bytes)
}

// AddUsageDropped counts usage-ledger events that were dropped (buffer
// overflow or flush failure) — the honesty counter for best-effort metering.
func (r *Registry) AddUsageDropped(n int) {
	if n <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usageDropped += uint64(n)
}

// Handler serves the metrics in Prometheus text format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(r.render()))
	})
}

func (r *Registry) render() string {
	r.mu.Lock()
	gaugeFn := r.gauges
	// Snapshot under the lock, render outside it.
	calls := copyU64(r.toolCalls)
	errs := copyU64(r.toolErrors)
	hist := make(map[string][]uint64, len(r.toolHist))
	for k, v := range r.toolHist {
		hist[k] = append([]uint64(nil), v...)
	}
	sums := make(map[string]float64, len(r.toolSum))
	for k, v := range r.toolSum {
		sums[k] = v
	}
	reqs := copyU64(r.httpReqs)
	usage := copyU64(r.usageBytes)
	usageDropped := r.usageDropped
	uptime := time.Since(r.startedAt).Seconds()
	r.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "# HELP agentmesh_uptime_seconds Seconds since the server started.\n")
	fmt.Fprintf(&b, "# TYPE agentmesh_uptime_seconds gauge\nagentmesh_uptime_seconds %g\n", uptime)

	fmt.Fprintf(&b, "# HELP agentmesh_tool_calls_total MCP tool invocations.\n")
	fmt.Fprintf(&b, "# TYPE agentmesh_tool_calls_total counter\n")
	for _, k := range sortedKeys(calls) {
		fmt.Fprintf(&b, "agentmesh_tool_calls_total{tool=%q} %d\n", k, calls[k])
	}
	fmt.Fprintf(&b, "# HELP agentmesh_tool_errors_total MCP tool invocations that returned an error.\n")
	fmt.Fprintf(&b, "# TYPE agentmesh_tool_errors_total counter\n")
	for _, k := range sortedKeys(errs) {
		fmt.Fprintf(&b, "agentmesh_tool_errors_total{tool=%q} %d\n", k, errs[k])
	}

	fmt.Fprintf(&b, "# HELP agentmesh_tool_duration_seconds MCP tool latency.\n")
	fmt.Fprintf(&b, "# TYPE agentmesh_tool_duration_seconds histogram\n")
	for _, tool := range sortedKeysHist(hist) {
		h := hist[tool]
		for i, bound := range buckets {
			fmt.Fprintf(&b, "agentmesh_tool_duration_seconds_bucket{tool=%q,le=%q} %d\n",
				tool, strconv.FormatFloat(bound, 'g', -1, 64), h[i])
		}
		fmt.Fprintf(&b, "agentmesh_tool_duration_seconds_bucket{tool=%q,le=\"+Inf\"} %d\n", tool, calls[tool])
		fmt.Fprintf(&b, "agentmesh_tool_duration_seconds_sum{tool=%q} %g\n", tool, sums[tool])
		fmt.Fprintf(&b, "agentmesh_tool_duration_seconds_count{tool=%q} %d\n", tool, calls[tool])
	}

	fmt.Fprintf(&b, "# HELP agentmesh_http_requests_total HTTP responses by path and status.\n")
	fmt.Fprintf(&b, "# TYPE agentmesh_http_requests_total counter\n")
	for _, k := range sortedKeys(reqs) {
		path, status, _ := strings.Cut(k, " ")
		fmt.Fprintf(&b, "agentmesh_http_requests_total{path=%q,status=%q} %d\n", path, status, reqs[k])
	}

	fmt.Fprintf(&b, "# HELP agentmesh_usage_bytes_total Metered coordination bytes by workspace, tool and direction.\n")
	fmt.Fprintf(&b, "# TYPE agentmesh_usage_bytes_total counter\n")
	for _, k := range sortedKeys(usage) {
		parts := strings.SplitN(k, "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		fmt.Fprintf(&b, "agentmesh_usage_bytes_total{workspace=%q,tool=%q,direction=%q} %d\n",
			parts[0], parts[1], parts[2], usage[k])
	}
	fmt.Fprintf(&b, "# HELP agentmesh_usage_events_dropped_total Usage-ledger events dropped (buffer overflow or flush failure).\n")
	fmt.Fprintf(&b, "# TYPE agentmesh_usage_events_dropped_total counter\n")
	fmt.Fprintf(&b, "agentmesh_usage_events_dropped_total %d\n", usageDropped)

	if gaugeFn != nil {
		g := gaugeFn()
		for _, k := range sortedKeysF(g) {
			fmt.Fprintf(&b, "# TYPE agentmesh_%s gauge\nagentmesh_%s %g\n", k, k, g[k])
		}
	}
	return b.String()
}

func copyU64(m map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func sortedKeys(m map[string]uint64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysF(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysHist(m map[string][]uint64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// HTTPMiddleware records status codes per path.
func (r *Registry) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, req)
		r.ObserveHTTP(normalisePath(req.URL.Path), sw.status)
	})
}

// normalisePath keeps cardinality bounded: only known endpoints are labelled.
func normalisePath(p string) string {
	switch {
	case p == "/mcp", p == "/healthz", p == "/readyz", p == "/metrics", p == "/ui":
		return p
	case strings.HasPrefix(p, "/ui/"):
		return "/ui/*"
	case strings.HasPrefix(p, "/.well-known/"):
		return "/.well-known/*"
	default:
		return "other"
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

// Flush implements http.Flusher so SSE streaming (MCP) keeps working.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
