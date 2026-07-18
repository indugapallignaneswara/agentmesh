package storetest

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// usageWSCounter disambiguates workspaces created in the same nanosecond.
var usageWSCounter atomic.Int64

// usageWS returns a workspace name unique across test runs. The usage ledger
// is append-only and (unlike the coordination tables) not covered by the
// Postgres factory's TruncateAll, so isolation comes from never reusing a
// workspace name against a persistent database.
func usageWS(prefix string) string {
	return fmt.Sprintf("usage-%s-%d-%d", prefix, time.Now().UnixNano(), usageWSCounter.Add(1))
}

// ue builds a platform-measured (ingress/egress) usage event.
func ue(ws, member string, kind model.Kind, tool string, dir model.UsageDirection, bytes int64, ts time.Time) model.UsageEvent {
	return model.UsageEvent{
		TS: ts, Workspace: ws, Member: member, Kind: kind,
		Tool: tool, Direction: dir, Bytes: bytes, Authenticated: true,
	}
}

// ur builds a client-reported vendor-usage event.
func ur(ws, member string, kind model.Kind, prompt, completion int64, ts time.Time) model.UsageEvent {
	return model.UsageEvent{
		TS: ts, Workspace: ws, Member: member, Kind: kind,
		Tool: "usage_report", Direction: model.UsageReported,
		ReportedPromptTokens: prompt, ReportedCompletionTokens: completion,
		Vendor: "anthropic", Model: "claude-test",
	}
}

// testUsageSummary appends one mixed batch (two members; ingress, egress and
// reported rows; out-of-window rows) and asserts exact per-member sums with
// [from, to) window filtering.
func testUsageSummary(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := usageWS("summary")
	from, to := base, base.Add(time.Hour)

	batch := []model.UsageEvent{
		ue(ws, "alice", model.KindAgent, "send_message", model.UsageIngress, 100, base),
		ue(ws, "alice", model.KindAgent, "read_inbox", model.UsageEgress, 400, base.Add(time.Minute)),
		ur(ws, "alice", model.KindHuman, 50, 25, base.Add(2*time.Minute)), // latest ts: kind human wins
		ue(ws, "bob", model.KindAgent, "broadcast", model.UsageIngress, 7, base.Add(time.Minute)),
		ue(ws, "bob", model.KindAgent, "get_artifact", model.UsageEgress, 11, base.Add(2*time.Minute)),
		// Outside the window: before from, and exactly at to (half-open).
		ue(ws, "alice", model.KindAgent, "read_inbox", model.UsageEgress, 9999, from.Add(-time.Second)),
		ue(ws, "alice", model.KindAgent, "read_inbox", model.UsageEgress, 8888, to),
	}
	mustNoErr(t, s.AppendUsage(ctx, batch))

	got, err := s.UsageSummary(ctx, ws, from, to)
	mustNoErr(t, err)
	if len(got) != 2 {
		t.Fatalf("summary rows = %d (%+v), want 2", len(got), got)
	}
	alice, bob := got[0], got[1]
	if alice.Member != "alice" || bob.Member != "bob" {
		t.Fatalf("member order = [%s %s], want [alice bob]", alice.Member, bob.Member)
	}
	if alice.IngressBytes != 100 || alice.EgressBytes != 400 || alice.Events != 3 {
		t.Fatalf("alice = %+v, want ingress 100, egress 400, events 3", alice)
	}
	if alice.ReportedPromptTokens != 50 || alice.ReportedCompletionTokens != 25 {
		t.Fatalf("alice reported = %d/%d, want 50/25", alice.ReportedPromptTokens, alice.ReportedCompletionTokens)
	}
	if alice.Kind != model.KindHuman {
		t.Fatalf("alice kind = %q, want human (last-seen wins)", alice.Kind)
	}
	if bob.IngressBytes != 7 || bob.EgressBytes != 11 || bob.Events != 2 {
		t.Fatalf("bob = %+v, want ingress 7, egress 11, events 2", bob)
	}
	if bob.ReportedPromptTokens != 0 || bob.ReportedCompletionTokens != 0 {
		t.Fatalf("bob reported = %+v, want zero", bob)
	}
	if bob.Kind != model.KindAgent {
		t.Fatalf("bob kind = %q, want agent", bob.Kind)
	}
}

// testUsageDailyRollup exercises the day-bucket rollup: two UTC days, newest
// first, and — the assertion that catches a broken ON CONFLICT — a second
// append into an existing (workspace, member, day) bucket must ACCUMULATE.
func testUsageDailyRollup(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := usageWS("daily")
	// UsageDaily's window is anchored on the wall clock, so use recent
	// timestamps: today and yesterday (UTC).
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)

	mustNoErr(t, s.AppendUsage(ctx, []model.UsageEvent{
		ue(ws, "alice", model.KindAgent, "send_message", model.UsageIngress, 10, yesterday.Add(time.Hour)),
		ue(ws, "alice", model.KindAgent, "read_inbox", model.UsageEgress, 20, today.Add(time.Hour)),
	}))

	got, err := s.UsageDaily(ctx, ws, 30)
	mustNoErr(t, err)
	if len(got) != 2 {
		t.Fatalf("daily buckets = %d (%+v), want 2", len(got), got)
	}
	if !got[0].Day.Equal(today) || !got[1].Day.Equal(yesterday) {
		t.Fatalf("days = [%v %v], want newest first [%v %v]", got[0].Day, got[1].Day, today, yesterday)
	}
	if got[0].EgressBytes != 20 || got[0].IngressBytes != 0 || got[0].Events != 1 {
		t.Fatalf("today bucket = %+v, want egress 20, 1 event", got[0])
	}
	if got[1].IngressBytes != 10 || got[1].EgressBytes != 0 || got[1].Events != 1 {
		t.Fatalf("yesterday bucket = %+v, want ingress 10, 1 event", got[1])
	}

	// Second append into today's existing bucket: sums must add, not replace.
	mustNoErr(t, s.AppendUsage(ctx, []model.UsageEvent{
		ue(ws, "alice", model.KindAgent, "send_message", model.UsageIngress, 5, today.Add(2*time.Hour)),
	}))
	got, err = s.UsageDaily(ctx, ws, 30)
	mustNoErr(t, err)
	if len(got) != 2 {
		t.Fatalf("buckets after second append = %d, want 2", len(got))
	}
	if got[0].IngressBytes != 5 || got[0].EgressBytes != 20 || got[0].Events != 2 {
		t.Fatalf("today bucket after upsert = %+v, want ingress 5, egress 20, events 2 (accumulated)", got[0])
	}

	// days=1 keeps only today's bucket (the window cutoff works).
	got, err = s.UsageDaily(ctx, ws, 1)
	mustNoErr(t, err)
	if len(got) != 1 || !got[0].Day.Equal(today) {
		t.Fatalf("UsageDaily(1) = %+v, want only today", got)
	}
}

// testUsageEmpty pins the zero cases: unknown workspace queries are empty and
// error-free, and a nil batch is a no-op.
func testUsageEmpty(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := usageWS("empty")

	mustNoErr(t, s.AppendUsage(ctx, nil))

	sums, err := s.UsageSummary(ctx, ws, base, base.Add(time.Hour))
	mustNoErr(t, err)
	if len(sums) != 0 {
		t.Fatalf("summary of empty workspace = %+v, want empty", sums)
	}
	days, err := s.UsageDaily(ctx, ws, 30)
	mustNoErr(t, err)
	if len(days) != 0 {
		t.Fatalf("daily of empty workspace = %+v, want empty", days)
	}
}

// testUsageIsolation verifies that one workspace's ledger never leaks into
// another's summary or rollup.
func testUsageIsolation(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws1, ws2 := usageWS("iso1"), usageWS("iso2")
	now := time.Now().UTC()

	mustNoErr(t, s.AppendUsage(ctx, []model.UsageEvent{
		ue(ws1, "alice", model.KindAgent, "send_message", model.UsageIngress, 100, now),
		ue(ws2, "mallory", model.KindAgent, "send_message", model.UsageIngress, 777, now),
	}))

	sums, err := s.UsageSummary(ctx, ws1, now.Add(-time.Hour), now.Add(time.Hour))
	mustNoErr(t, err)
	if len(sums) != 1 || sums[0].Member != "alice" || sums[0].IngressBytes != 100 {
		t.Fatalf("ws1 summary = %+v, want only alice/100", sums)
	}
	days, err := s.UsageDaily(ctx, ws1, 30)
	mustNoErr(t, err)
	if len(days) != 1 || days[0].Member != "alice" || days[0].Workspace != ws1 {
		t.Fatalf("ws1 daily = %+v, want only alice's bucket", days)
	}
}

// testUsageKindSticky verifies that an event with an empty kind (most tool
// calls carry no kind argument in auth-off mode) never blanks out a member's
// previously known kind — the summary keeps the last NON-EMPTY kind.
func testUsageKindSticky(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := usageWS("kind")
	now := time.Now().UTC()

	mustNoErr(t, s.AppendUsage(ctx, []model.UsageEvent{
		ue(ws, "a1", model.KindAgent, "workspace_join", model.UsageIngress, 50, now),
	}))
	// A later kind-less call (e.g. read_inbox sniffed without a kind arg).
	mustNoErr(t, s.AppendUsage(ctx, []model.UsageEvent{
		ue(ws, "a1", "", "read_inbox", model.UsageEgress, 4096, now.Add(time.Minute)),
	}))

	sums, err := s.UsageSummary(ctx, ws, now.Add(-time.Hour), now.Add(time.Hour))
	mustNoErr(t, err)
	if len(sums) != 1 {
		t.Fatalf("summary rows = %d, want 1", len(sums))
	}
	if sums[0].Kind != model.KindAgent {
		t.Fatalf("kind = %q after a kind-less later event, want %q kept", sums[0].Kind, model.KindAgent)
	}
	if sums[0].EgressBytes != 4096 {
		t.Fatalf("egress = %d, want 4096 (the kind-less event still counts)", sums[0].EgressBytes)
	}
}
