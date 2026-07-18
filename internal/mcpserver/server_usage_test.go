package mcpserver_test

// M6 exit-criteria tests (ROADMAP «Meter»): broadcast fan-out cost appears as
// N × reader egress AT READ TIME, not send time — the write-once/read-N
// asymmetry that is the whole point of coordination-layer metering.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/mcpserver"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/usage"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// connectMetered wires a client to a server with the metering middleware
// installed, returning the backing store so tests can assert on the ledger.
func connectMetered(t *testing.T) (*mcp.ClientSession, context.Context, *store.Memory, *usage.Recorder) {
	t.Helper()
	st := store.NewMemory()
	svc := workspace.New(st, bus.NewNoop())
	rec := usage.NewRecorder(st, usage.Options{FlushBatch: 1, FlushInterval: 10 * time.Millisecond})
	t.Cleanup(rec.Close)

	srv := mcpserver.NewServerWithObservability(svc, "test", nil, rec)
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, ctx, st, rec
}

// summaries polls the ledger until the member's row satisfies pred (the
// recorder flushes asynchronously) or the deadline passes.
func waitUsage(t *testing.T, st *store.Memory, ws string, pred func(map[string]model.UsageSummary) bool) map[string]model.UsageSummary {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows, err := st.UsageSummary(context.Background(), ws, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		if err != nil {
			t.Fatalf("UsageSummary: %v", err)
		}
		byMember := make(map[string]model.UsageSummary, len(rows))
		for _, r := range rows {
			byMember[r.Member] = r
		}
		if pred(byMember) {
			return byMember
		}
		if time.Now().After(deadline) {
			t.Fatalf("ledger never satisfied predicate; have %+v", byMember)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestBroadcastFanOutMeteredAtReadTime is the M6 exit criterion: one 4 KB
// broadcast into a room with three readers costs ~nothing in egress at send
// time, then materializes as >= 3x payload of reader egress once each reader
// drains its inbox. Attribution: the writer pays ingress once; each reader
// pays egress for its own copy.
func TestBroadcastFanOutMeteredAtReadTime(t *testing.T) {
	cs, ctx, st, _ := connectMetered(t)
	const ws = "meterroom"
	payload := strings.Repeat("x", 4096)

	for _, m := range []string{"lead", "a1", "a2", "a3"} {
		kind := "agent"
		if m == "lead" {
			kind = "human"
		}
		callJSON(t, cs, ctx, "workspace_join", map[string]any{
			"workspace": ws, "name": m, "kind": kind,
		}, nil)
	}
	callJSON(t, cs, ctx, "broadcast", map[string]any{
		"workspace": ws, "from": "lead", "body": payload,
	}, nil)

	// After the broadcast lands in the ledger: the WRITER paid ingress >= the
	// payload; no READER has yet paid egress anywhere near the payload —
	// unread messages cost nobody anything.
	pre := waitUsage(t, st, ws, func(m map[string]model.UsageSummary) bool {
		return m["lead"].IngressBytes >= int64(len(payload))
	})
	for _, r := range []string{"a1", "a2", "a3"} {
		if got := pre[r].EgressBytes; got >= int64(len(payload)) {
			t.Fatalf("reader %s shows %d egress bytes BEFORE reading — fan-out cost must land at read time", r, got)
		}
	}

	// Each reader drains its inbox; each read returns the 4 KB body, so each
	// reader's egress must now include it.
	for _, r := range []string{"a1", "a2", "a3"} {
		callJSON(t, cs, ctx, "read_inbox", map[string]any{
			"workspace": ws, "member": r,
		}, nil)
	}
	post := waitUsage(t, st, ws, func(m map[string]model.UsageSummary) bool {
		return m["a1"].EgressBytes >= int64(len(payload)) &&
			m["a2"].EgressBytes >= int64(len(payload)) &&
			m["a3"].EgressBytes >= int64(len(payload))
	})

	var readers int64
	for _, r := range []string{"a1", "a2", "a3"} {
		readers += post[r].EgressBytes
	}
	if want := int64(3 * len(payload)); readers < want {
		t.Fatalf("total reader egress %d < 3x payload %d — fan-out must multiply by reader count", readers, want)
	}
	// The writer's egress stayed small: a broadcast ack, not the payload back.
	if lead := post["lead"].EgressBytes; lead >= int64(len(payload)) {
		t.Fatalf("writer egress %d unexpectedly includes the payload — broadcast should return an ack, not the body", lead)
	}
}

// TestUsageStatsToolReportsLedger exercises the usage_stats tool end to end:
// the numbers a room member sees must come from the same ledger, with a
// display-time token estimate attached.
func TestUsageStatsToolReportsLedger(t *testing.T) {
	cs, ctx, st, _ := connectMetered(t)
	const ws = "statsroom"
	payload := strings.Repeat("y", 2000)

	callJSON(t, cs, ctx, "workspace_join", map[string]any{"workspace": ws, "name": "alice", "kind": "agent"}, nil)
	callJSON(t, cs, ctx, "workspace_join", map[string]any{"workspace": ws, "name": "bob", "kind": "agent"}, nil)
	callJSON(t, cs, ctx, "send_message", map[string]any{"workspace": ws, "from": "alice", "to": "bob", "body": payload}, nil)
	callJSON(t, cs, ctx, "read_inbox", map[string]any{"workspace": ws, "member": "bob"}, nil)

	waitUsage(t, st, ws, func(m map[string]model.UsageSummary) bool {
		return m["alice"].IngressBytes >= int64(len(payload)) && m["bob"].EgressBytes >= int64(len(payload))
	})

	var stats struct {
		Workspace    string `json:"workspace"`
		IngressBytes int64  `json:"ingress_bytes"`
		EgressBytes  int64  `json:"egress_bytes"`
		EstTokens    int64  `json:"est_tokens"`
		Members      []struct {
			Member    string `json:"member"`
			EstTokens int64  `json:"est_tokens"`
		} `json:"members"`
	}
	callJSON(t, cs, ctx, "usage_stats", map[string]any{"workspace": ws}, &stats)

	if stats.Workspace != ws || len(stats.Members) < 2 {
		t.Fatalf("usage_stats = %+v, want both members of %q", stats, ws)
	}
	if stats.IngressBytes < int64(len(payload)) || stats.EgressBytes < int64(len(payload)) {
		t.Fatalf("usage_stats totals in=%d out=%d, want both >= %d", stats.IngressBytes, stats.EgressBytes, len(payload))
	}
	if stats.EstTokens <= 0 {
		t.Fatalf("est_tokens = %d, want > 0 (display-time estimate must be rendered)", stats.EstTokens)
	}
}
