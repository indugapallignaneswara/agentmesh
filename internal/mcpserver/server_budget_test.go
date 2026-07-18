package mcpserver_test

// M8 «Budget» exit-criteria tests: a flooding agent is stopped by its budget
// while the human moderator keeps acting; the budget survives a restart
// (seeded from the usage ledger); concurrent spends cannot overshoot beyond
// the documented in-flight bound.

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

// connectBudget wires a metered server and returns the service so tests can
// set budgets directly (the tool-level gating is covered in the rooms tests).
func connectBudget(t *testing.T, st *store.Memory) (*mcp.ClientSession, context.Context, *workspace.Service) {
	t.Helper()
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
	return cs, ctx, svc
}

// call invokes a tool and returns (isError, text).
func call(t *testing.T, cs *mcp.ClientSession, ctx context.Context, name string, args map[string]any) (bool, string) {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s transport error: %v", name, err)
	}
	return res.IsError, textOf(res)
}

// TestFloodingAgentBlockedWhileHumanActs is THE M8 exit criterion: the agent
// hits its budget mid-flood and gets a retryable refusal, while the human's
// exemption lets them broadcast and kick — the moderator is never silenced.
func TestFloodingAgentBlockedWhileHumanActs(t *testing.T) {
	st := store.NewMemory()
	cs, ctx, svc := connectBudget(t, st)
	const ws = "floodroom"

	callJSON(t, cs, ctx, "workspace_join", map[string]any{"workspace": ws, "name": "lead", "kind": "human"}, nil)
	callJSON(t, cs, ctx, "workspace_join", map[string]any{"workspace": ws, "name": "flood", "kind": "agent"}, nil)
	if _, err := svc.RoomSetBudget(ctx, ws, "lead", 0, 20_000); err != nil {
		t.Fatalf("RoomSetBudget: %v", err)
	}

	// Flood until blocked. Each send is ~1.3KB of ingress+egress, so a 20KB
	// member cap must trip within a few dozen sends.
	body := strings.Repeat("f", 1024)
	blocked := false
	var successes int
	for i := 0; i < 100; i++ {
		isErr, text := call(t, cs, ctx, "send_message", map[string]any{
			"workspace": ws, "from": "flood", "to": "lead", "body": body,
		})
		if isErr {
			if !strings.Contains(text, "budget exceeded") {
				t.Fatalf("send %d failed with %q, want a budget refusal", i, text)
			}
			blocked = true
			break
		}
		successes++
	}
	if !blocked {
		t.Fatal("flooding agent was never blocked by its 20KB member budget")
	}
	if successes == 0 {
		t.Fatal("agent was blocked before spending anything — budget must not pre-block")
	}

	// The human is exempt: broadcast and kick still work mid-flood.
	if isErr, text := call(t, cs, ctx, "broadcast", map[string]any{
		"workspace": ws, "from": "lead", "body": "flood detected — removing the agent",
	}); isErr {
		t.Fatalf("human broadcast blocked during flood: %s", text)
	}
	if isErr, text := call(t, cs, ctx, "room_kick", map[string]any{
		"workspace": ws, "actor": "lead", "target": "flood",
	}); isErr {
		t.Fatalf("human kick blocked during flood: %s", text)
	}
}

// TestBudgetSurvivesRestart: the tracker is only a cache — a fresh process
// (new Service, same store) must seed today's spend from the usage ledger and
// keep an exhausted agent blocked. This is the kill -9 exit criterion.
func TestBudgetSurvivesRestart(t *testing.T) {
	st := store.NewMemory()

	// "Previous process": room, members, budget, and a ledger showing the
	// agent already spent past its cap today.
	{
		_, ctx, svc := connectBudget(t, st)
		_ = ctx
		if _, err := svc.RoomCreate(context.Background(), "persist", "lead"); err != nil {
			t.Fatalf("RoomCreate: %v", err)
		}
		if _, err := svc.Join(context.Background(), "persist", "lead", model.KindHuman, nil); err != nil {
			t.Fatalf("join lead: %v", err)
		}
		if _, err := svc.Join(context.Background(), "persist", "burner", model.KindAgent, nil); err != nil {
			t.Fatalf("join burner: %v", err)
		}
		if _, err := svc.RoomSetBudget(context.Background(), "persist", "lead", 0, 10_000); err != nil {
			t.Fatalf("RoomSetBudget: %v", err)
		}
		if err := st.AppendUsage(context.Background(), []model.UsageEvent{{
			TS: time.Now().UTC(), Workspace: "persist", Member: "burner",
			Kind: model.KindAgent, Tool: "send_message",
			Direction: model.UsageIngress, Bytes: 12_000, Authenticated: false,
		}}); err != nil {
			t.Fatalf("AppendUsage: %v", err)
		}
	}

	// "Restarted process": fresh service and tracker over the same store.
	cs, ctx, _ := connectBudget(t, st)
	isErr, text := call(t, cs, ctx, "send_message", map[string]any{
		"workspace": "persist", "from": "burner", "to": "lead", "body": "still here?",
	})
	if !isErr || !strings.Contains(text, "budget exceeded") {
		t.Fatalf("after restart the exhausted agent sent successfully (isErr=%v %q) — seed from the ledger failed", isErr, text)
	}
}

// TestBudgetOvershootBounded: concurrent senders may each pass the pre-call
// check before any spend lands, so the worst-case overshoot is bounded by the
// calls in flight — never unbounded. With N concurrent callers the ledger
// total must stay under cap + N*callSize; afterwards every call is refused.
func TestBudgetOvershootBounded(t *testing.T) {
	st := store.NewMemory()
	cs, ctx, svc := connectBudget(t, st)
	const ws = "raceroom"

	callJSON(t, cs, ctx, "workspace_join", map[string]any{"workspace": ws, "name": "lead", "kind": "human"}, nil)
	callJSON(t, cs, ctx, "workspace_join", map[string]any{"workspace": ws, "name": "racer", "kind": "agent"}, nil)
	if _, err := svc.RoomSetBudget(ctx, ws, "lead", 0, 8_000); err != nil {
		t.Fatalf("RoomSetBudget: %v", err)
	}

	const n = 16
	const bodyLen = 512
	body := strings.Repeat("r", bodyLen)
	results := make(chan bool, n)
	for i := 0; i < n; i++ {
		go func() {
			res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{
				"workspace": ws, "from": "racer", "to": "lead", "body": body,
			}})
			results <- err == nil && !res.IsError
		}()
	}
	var okCount int
	for i := 0; i < n; i++ {
		if <-results {
			okCount++
		}
	}
	// Each successful call is ~bodyLen ingress + ~2.2x bodyLen egress; bound
	// the byte overshoot by in-flight count, generously.
	const perCallMax = 4 * bodyLen
	if int64(okCount*perCallMax) > 8_000+int64(n*perCallMax) {
		t.Fatalf("%d concurrent sends succeeded — overshoot beyond the in-flight bound", okCount)
	}
	// Once everything has landed, the very next call must be refused.
	isErr, text := call(t, cs, ctx, "send_message", map[string]any{
		"workspace": ws, "from": "racer", "to": "lead", "body": body,
	})
	if !isErr || !strings.Contains(text, "budget exceeded") {
		t.Fatalf("post-race send not refused (isErr=%v %q)", isErr, text)
	}
}

// TestBudgetWarningEventOnce: crossing 80% of the room budget appends exactly
// one budget_warning event to the room's log.
func TestBudgetWarningEventOnce(t *testing.T) {
	st := store.NewMemory()
	cs, ctx, svc := connectBudget(t, st)
	const ws = "warnroom"

	callJSON(t, cs, ctx, "workspace_join", map[string]any{"workspace": ws, "name": "lead", "kind": "human"}, nil)
	callJSON(t, cs, ctx, "workspace_join", map[string]any{"workspace": ws, "name": "worker", "kind": "agent"}, nil)
	if _, err := svc.RoomSetBudget(ctx, ws, "lead", 30_000, 0); err != nil {
		t.Fatalf("RoomSetBudget: %v", err)
	}

	body := strings.Repeat("w", 1024)
	for i := 0; i < 30; i++ {
		if isErr, _ := call(t, cs, ctx, "send_message", map[string]any{
			"workspace": ws, "from": "worker", "to": "lead", "body": body,
		}); isErr {
			break // hit the hard cap — fine, the warning must have fired first
		}
	}

	events, _, err := svc.Subscribe(ctx, ws, "", 0, 500)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	var warnings int
	for _, e := range events {
		if e.Type == "budget_warning" {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("budget_warning events = %d, want exactly 1 (warn once per room per day)", warnings)
	}
}
