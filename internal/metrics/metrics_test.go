package metrics_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/metrics"
)

func scrape(t *testing.T, r *metrics.Registry) string {
	t.Helper()
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("content-type = %q, want prometheus text", ct)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestToolMetricsExposition(t *testing.T) {
	r := metrics.New()
	r.ObserveTool("send_message", 12*time.Millisecond, false)
	r.ObserveTool("send_message", 30*time.Millisecond, false)
	r.ObserveTool("send_message", 5*time.Millisecond, true) // an error
	r.ObserveTool("broadcast", 2*time.Millisecond, false)

	body := scrape(t, r)
	for _, want := range []string{
		`agentmesh_tool_calls_total{tool="send_message"} 3`,
		`agentmesh_tool_errors_total{tool="send_message"} 1`,
		`agentmesh_tool_calls_total{tool="broadcast"} 1`,
		`agentmesh_tool_duration_seconds_count{tool="send_message"} 3`,
		`agentmesh_tool_duration_seconds_bucket{tool="send_message",le="+Inf"} 3`,
		"# TYPE agentmesh_tool_duration_seconds histogram",
		"agentmesh_uptime_seconds",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n---\n%s", want, body)
		}
	}
	// A 12ms observation must fall in the 25ms bucket but not the 5ms one.
	if !strings.Contains(body, `agentmesh_tool_duration_seconds_bucket{tool="send_message",le="0.025"} 2`) {
		t.Errorf("histogram bucketing wrong:\n%s", body)
	}
}

func TestHTTPMetricsMiddleware(t *testing.T) {
	r := metrics.New()
	h := r.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/mcp" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	for _, p := range []string{"/healthz", "/mcp", "/ui/api?workspace=x"} {
		res, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}

	body := scrape(t, r)
	for _, want := range []string{
		`agentmesh_http_requests_total{path="/healthz",status="200"} 1`,
		`agentmesh_http_requests_total{path="/mcp",status="401"} 1`,
		// Path cardinality is bounded: /ui/api collapses to /ui/*.
		`agentmesh_http_requests_total{path="/ui/*",status="200"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n---\n%s", want, body)
		}
	}
}

func TestGaugesSampledAtScrape(t *testing.T) {
	r := metrics.New()
	depth := 7.0
	r.SetGauges(func() map[string]float64 {
		return map[string]float64{"memory_review_queue_depth": depth}
	})
	if !strings.Contains(scrape(t, r), "agentmesh_memory_review_queue_depth 7") {
		t.Fatal("gauge not exposed")
	}
	depth = 3
	if !strings.Contains(scrape(t, r), "agentmesh_memory_review_queue_depth 3") {
		t.Fatal("gauge not re-sampled on scrape")
	}
}
