// Package storetest provides a single behavioural test suite that every
// store.Store implementation must pass. Running the in-memory and Postgres
// stores through the same suite prevents them from drifting.
package storetest

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// Factory returns a fresh, empty store for a single subtest.
type Factory func(t *testing.T) store.Store

var base = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// RunSuite executes the full contract against stores produced by newStore.
func RunSuite(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("UpsertMemberPreservesJoinedAt", func(t *testing.T) { testUpsert(t, newStore(t)) })
	t.Run("GetMemberNotFound", func(t *testing.T) { testGetNotFound(t, newStore(t)) })
	t.Run("TouchMember", func(t *testing.T) { testTouch(t, newStore(t)) })
	t.Run("ListMembersOrdered", func(t *testing.T) { testListMembers(t, newStore(t)) })
	t.Run("ListActiveMembersByTTL", func(t *testing.T) { testActiveMembers(t, newStore(t)) })
	t.Run("DirectMessageReadOnce", func(t *testing.T) { testDirectInbox(t, newStore(t)) })
	t.Run("BroadcastPerRecipient", func(t *testing.T) { testBroadcastInbox(t, newStore(t)) })
	t.Run("InboxOrderedByCreatedAt", func(t *testing.T) { testInboxOrder(t, newStore(t)) })
	t.Run("EventsSeqAndCursor", func(t *testing.T) { testEvents(t, newStore(t)) })
	t.Run("EventsWorkspaceIsolation", func(t *testing.T) { testEventIsolation(t, newStore(t)) })
}

func testUpsert(t *testing.T, s store.Store) {
	ctx := context.Background()
	m := model.Member{
		Workspace: "ws1", Name: "alice", Kind: model.KindAgent,
		AgentCard: json.RawMessage(`{"role":"backend"}`),
		JoinedAt:  base, LastSeen: base,
	}
	got, err := s.UpsertMember(ctx, m)
	mustNoErr(t, err)
	if got.JoinedAt.Equal(base) == false {
		t.Fatalf("JoinedAt = %v, want %v", got.JoinedAt, base)
	}
	if !jsonEqual(t, got.AgentCard, `{"role":"backend"}`) {
		t.Fatalf("AgentCard = %s", got.AgentCard)
	}

	// Re-upsert later with a new card: JoinedAt must be preserved, others refreshed.
	later := base.Add(time.Hour)
	got2, err := s.UpsertMember(ctx, model.Member{
		Workspace: "ws1", Name: "alice", Kind: model.KindHuman,
		AgentCard: json.RawMessage(`{"role":"lead"}`),
		JoinedAt:  later, LastSeen: later,
	})
	mustNoErr(t, err)
	if !got2.JoinedAt.Equal(base) {
		t.Fatalf("re-upsert JoinedAt = %v, want preserved %v", got2.JoinedAt, base)
	}
	if !got2.LastSeen.Equal(later) {
		t.Fatalf("re-upsert LastSeen = %v, want %v", got2.LastSeen, later)
	}
	if got2.Kind != model.KindHuman {
		t.Fatalf("re-upsert Kind = %v, want human", got2.Kind)
	}
	if !jsonEqual(t, got2.AgentCard, `{"role":"lead"}`) {
		t.Fatalf("re-upsert AgentCard = %s", got2.AgentCard)
	}
}

func testGetNotFound(t *testing.T, s store.Store) {
	_, err := s.GetMember(context.Background(), "ws1", "ghost")
	if err != store.ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func testTouch(t *testing.T, s store.Store) {
	ctx := context.Background()
	if err := s.TouchMember(ctx, "ws1", "ghost", base); err != store.ErrNotFound {
		t.Fatalf("touch ghost err = %v, want ErrNotFound", err)
	}
	join(t, s, "ws1", "alice", base)
	later := base.Add(5 * time.Minute)
	mustNoErr(t, s.TouchMember(ctx, "ws1", "alice", later))
	got, err := s.GetMember(ctx, "ws1", "alice")
	mustNoErr(t, err)
	if !got.LastSeen.Equal(later) {
		t.Fatalf("LastSeen = %v, want %v", got.LastSeen, later)
	}
}

func testListMembers(t *testing.T, s store.Store) {
	join(t, s, "ws1", "charlie", base)
	join(t, s, "ws1", "alice", base)
	join(t, s, "ws1", "bob", base)
	join(t, s, "ws2", "zoe", base)
	got, err := s.ListMembers(context.Background(), "ws1")
	mustNoErr(t, err)
	names := memberNames(got)
	if !reflect.DeepEqual(names, []string{"alice", "bob", "charlie"}) {
		t.Fatalf("names = %v, want [alice bob charlie]", names)
	}
}

func testActiveMembers(t *testing.T, s store.Store) {
	join(t, s, "ws1", "fresh", base.Add(time.Hour))
	join(t, s, "ws1", "stale", base)
	cutoff := base.Add(30 * time.Minute)
	got, err := s.ListActiveMembers(context.Background(), "ws1", cutoff)
	mustNoErr(t, err)
	if names := memberNames(got); !reflect.DeepEqual(names, []string{"fresh"}) {
		t.Fatalf("active = %v, want [fresh]", names)
	}
}

func testDirectInbox(t *testing.T, s store.Store) {
	ctx := context.Background()
	join(t, s, "ws1", "alice", base)
	join(t, s, "ws1", "bob", base)
	msg := model.Message{
		ID: "m1", Workspace: "ws1", Sender: "alice",
		Kind: model.MessageDirect, Body: "hi bob", CreatedAt: base,
	}
	mustNoErr(t, s.CreateMessage(ctx, msg, []string{"bob"}))

	// Alice (not a recipient) sees nothing.
	got, err := s.ReadInbox(ctx, "ws1", "alice", base)
	mustNoErr(t, err)
	if len(got) != 0 {
		t.Fatalf("alice inbox = %d, want 0", len(got))
	}
	// Bob reads it once.
	got, err = s.ReadInbox(ctx, "ws1", "bob", base)
	mustNoErr(t, err)
	if len(got) != 1 || got[0].ID != "m1" || got[0].Recipient != "bob" || got[0].Body != "hi bob" {
		t.Fatalf("bob inbox = %+v, want one m1", got)
	}
	// A second read is empty (consumed).
	got, err = s.ReadInbox(ctx, "ws1", "bob", base)
	mustNoErr(t, err)
	if len(got) != 0 {
		t.Fatalf("bob second read = %d, want 0", len(got))
	}
}

func testBroadcastInbox(t *testing.T, s store.Store) {
	ctx := context.Background()
	join(t, s, "ws1", "alice", base)
	join(t, s, "ws1", "bob", base)
	join(t, s, "ws1", "carol", base)
	msg := model.Message{
		ID: "b1", Workspace: "ws1", Sender: "alice",
		Kind: model.MessageBroadcast, Body: "hello all", CreatedAt: base,
	}
	mustNoErr(t, s.CreateMessage(ctx, msg, []string{"bob", "carol"}))

	for _, who := range []string{"bob", "carol"} {
		got, err := s.ReadInbox(ctx, "ws1", who, base)
		mustNoErr(t, err)
		if len(got) != 1 || got[0].ID != "b1" || got[0].Recipient != who {
			t.Fatalf("%s inbox = %+v, want b1", who, got)
		}
	}
	// Sender is not a recipient.
	got, err := s.ReadInbox(ctx, "ws1", "alice", base)
	mustNoErr(t, err)
	if len(got) != 0 {
		t.Fatalf("alice inbox = %d, want 0", len(got))
	}
}

func testInboxOrder(t *testing.T, s store.Store) {
	ctx := context.Background()
	join(t, s, "ws1", "bob", base)
	// Insert out of chronological order; read must be oldest-first.
	mustNoErr(t, s.CreateMessage(ctx, model.Message{
		ID: "m2", Workspace: "ws1", Sender: "alice", Kind: model.MessageDirect,
		Body: "second", CreatedAt: base.Add(2 * time.Minute),
	}, []string{"bob"}))
	mustNoErr(t, s.CreateMessage(ctx, model.Message{
		ID: "m1", Workspace: "ws1", Sender: "alice", Kind: model.MessageDirect,
		Body: "first", CreatedAt: base.Add(1 * time.Minute),
	}, []string{"bob"}))
	got, err := s.ReadInbox(ctx, "ws1", "bob", base.Add(time.Hour))
	mustNoErr(t, err)
	if len(got) != 2 || got[0].ID != "m1" || got[1].ID != "m2" {
		t.Fatalf("order = %v, want [m1 m2]", messageIDs(got))
	}
}

func testEvents(t *testing.T, s store.Store) {
	ctx := context.Background()
	e1, err := s.AppendEvent(ctx, model.Event{
		Workspace: "ws1", Source: "alice", Type: "note",
		Payload: json.RawMessage(`{"n":1}`), CreatedAt: base,
	})
	mustNoErr(t, err)
	e2, err := s.AppendEvent(ctx, model.Event{
		Workspace: "ws1", Source: "alice", Type: "note",
		Payload: json.RawMessage(`{"n":2}`), CreatedAt: base,
	})
	mustNoErr(t, err)
	if e2.Seq <= e1.Seq {
		t.Fatalf("Seq not increasing: %d then %d", e1.Seq, e2.Seq)
	}

	// From the beginning.
	got, err := s.EventsSince(ctx, "ws1", 0, 100)
	mustNoErr(t, err)
	if len(got) != 2 || got[0].Seq != e1.Seq || got[1].Seq != e2.Seq {
		t.Fatalf("events = %+v, want [e1 e2]", got)
	}
	if !jsonEqual(t, got[0].Payload, `{"n":1}`) {
		t.Fatalf("payload = %s", got[0].Payload)
	}
	// After the first cursor.
	got, err = s.EventsSince(ctx, "ws1", e1.Seq, 100)
	mustNoErr(t, err)
	if len(got) != 1 || got[0].Seq != e2.Seq {
		t.Fatalf("after cursor = %+v, want [e2]", got)
	}
	// Limit is honoured.
	got, err = s.EventsSince(ctx, "ws1", 0, 1)
	mustNoErr(t, err)
	if len(got) != 1 || got[0].Seq != e1.Seq {
		t.Fatalf("limited = %+v, want [e1]", got)
	}
}

func testEventIsolation(t *testing.T, s store.Store) {
	ctx := context.Background()
	_, err := s.AppendEvent(ctx, model.Event{Workspace: "ws1", Source: "a", Type: "x", CreatedAt: base})
	mustNoErr(t, err)
	_, err = s.AppendEvent(ctx, model.Event{Workspace: "ws2", Source: "a", Type: "y", CreatedAt: base})
	mustNoErr(t, err)
	got, err := s.EventsSince(ctx, "ws1", 0, 100)
	mustNoErr(t, err)
	if len(got) != 1 || got[0].Workspace != "ws1" {
		t.Fatalf("ws1 events = %+v, want only ws1", got)
	}
}

// --- helpers ---

func join(t *testing.T, s store.Store, ws, name string, seen time.Time) {
	t.Helper()
	_, err := s.UpsertMember(context.Background(), model.Member{
		Workspace: ws, Name: name, Kind: model.KindAgent, JoinedAt: seen, LastSeen: seen,
	})
	mustNoErr(t, err)
}

func memberNames(ms []model.Member) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Name
	}
	return out
}

func messageIDs(ms []model.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// jsonEqual compares a stored RawMessage to an expected JSON string
// semantically, ignoring whitespace and key ordering differences introduced by
// jsonb round-trips.
func jsonEqual(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("got is not valid JSON (%q): %v", string(got), err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatalf("want is not valid JSON: %v", err)
	}
	return reflect.DeepEqual(a, b)
}
