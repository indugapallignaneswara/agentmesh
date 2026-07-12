// Package storetest provides a single behavioural test suite that every
// store.Store implementation must pass. Running the in-memory and Postgres
// stores through the same suite prevents them from drifting.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// errorsIs is errors.Is, named locally for brevity in assertions. The store
// wraps sentinels with %w, so equality checks must unwrap.
func errorsIs(err, target error) bool { return errors.Is(err, target) }

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

	t.Run("TaskCreateAndGet", func(t *testing.T) { testTaskCreateGet(t, newStore(t)) })
	t.Run("TaskDanglingDependencyRejected", func(t *testing.T) { testTaskDanglingDep(t, newStore(t)) })
	t.Run("TaskClaimOldestFirst", func(t *testing.T) { testTaskClaimOrder(t, newStore(t)) })
	t.Run("TaskClaimRespectsDependencies", func(t *testing.T) { testTaskClaimDeps(t, newStore(t)) })
	t.Run("TaskCompleteByAssignee", func(t *testing.T) { testTaskComplete(t, newStore(t)) })
	t.Run("TaskCompleteWrongAgentRejected", func(t *testing.T) { testTaskCompleteWrongAgent(t, newStore(t)) })
	t.Run("TaskLeaseExpiryWorkStealing", func(t *testing.T) { testTaskLeaseExpiry(t, newStore(t)) })
	t.Run("TaskNoClaimableWhenEmpty", func(t *testing.T) { testTaskNoClaimable(t, newStore(t)) })
	t.Run("TaskListByStatus", func(t *testing.T) { testTaskListByStatus(t, newStore(t)) })
	t.Run("TaskCreateDedupesDeps", func(t *testing.T) { testTaskCreateDedupesDeps(t, newStore(t)) })
	t.Run("TaskRetryUnblocksDependents", func(t *testing.T) { testTaskRetryUnblocksDependents(t, newStore(t)) })
	t.Run("TaskRetryOnlyFailed", func(t *testing.T) { testTaskRetryOnlyFailed(t, newStore(t)) })

	t.Run("MemoryCreateGet", func(t *testing.T) { testMemoryCreateGet(t, newStore(t)) })
	t.Run("MemorySearchVisibility", func(t *testing.T) { testMemorySearchVisibility(t, newStore(t)) })
	t.Run("MemorySearchRankAndLimit", func(t *testing.T) { testMemorySearchRankAndLimit(t, newStore(t)) })
	t.Run("MemoryReviewApprove", func(t *testing.T) { testMemoryReviewApprove(t, newStore(t)) })
	t.Run("MemoryReviewRejectAndConflicts", func(t *testing.T) { testMemoryReviewRejectAndConflicts(t, newStore(t)) })

	t.Run("ArtifactCreateGetList", func(t *testing.T) { testArtifactCreateGetList(t, newStore(t)) })
	t.Run("ArtifactOptimisticConcurrency", func(t *testing.T) { testArtifactOptimisticConcurrency(t, newStore(t)) })

	t.Run("RoomCreateGet", func(t *testing.T) { testRoomCreateGet(t, newStore(t)) })
	t.Run("RoomEnsureIdempotent", func(t *testing.T) { testRoomEnsureIdempotent(t, newStore(t)) })
	t.Run("RoomCloseReopenList", func(t *testing.T) { testRoomCloseReopenList(t, newStore(t)) })

	t.Run("MemberRoleSetAndSurvivesRejoin", func(t *testing.T) { testMemberRoleSetAndSurvivesRejoin(t, newStore(t)) })
	t.Run("RemoveMemberPurgesUndelivered", func(t *testing.T) { testRemoveMemberPurgesUndelivered(t, newStore(t)) })
	t.Run("BanLifecycle", func(t *testing.T) { testBanLifecycle(t, newStore(t)) })
	t.Run("ListMessagesPaging", func(t *testing.T) { testListMessagesPaging(t, newStore(t)) })

	t.Run("InviteCreateGetByHash", func(t *testing.T) { testInviteCreateGetByHash(t, newStore(t)) })
	t.Run("InviteRedeemAtomic", func(t *testing.T) { testInviteRedeemAtomic(t, newStore(t)) })
	t.Run("InviteListOrder", func(t *testing.T) { testInviteListOrder(t, newStore(t)) })
	t.Run("InviteRevoke", func(t *testing.T) { testInviteRevoke(t, newStore(t)) })
	t.Run("WorkspacePolicy", func(t *testing.T) { testWorkspacePolicy(t, newStore(t)) })

	t.Run("TokenCreateGetByHash", func(t *testing.T) { testTokenCreateGetByHash(t, newStore(t)) })
	t.Run("TokenRevocation", func(t *testing.T) { testTokenRevocation(t, newStore(t)) })
	t.Run("TokenExpiry", func(t *testing.T) { testTokenExpiry(t, newStore(t)) })
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

// --- task tests ---

const oneMin = time.Minute

// mkTask creates a pending task and fails the test on error.
func mkTask(t *testing.T, s store.Store, ws, id, title string, deps ...string) model.Task {
	t.Helper()
	got, err := s.CreateTask(context.Background(), model.Task{
		ID: id, Workspace: ws, Title: title, Status: model.TaskPending,
		CreatedBy: "creator", CreatedAt: base, UpdatedAt: base,
	}, deps)
	mustNoErr(t, err)
	return got
}

func testTaskCreateGet(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkTask(t, s, "ws1", "t1", "build")
	created := mkTask(t, s, "ws1", "t2", "deploy", "t1")
	if len(created.DependsOn) != 1 || created.DependsOn[0] != "t1" {
		t.Fatalf("created.DependsOn = %v, want [t1]", created.DependsOn)
	}
	got, err := s.GetTask(ctx, "ws1", "t2")
	mustNoErr(t, err)
	if got.Title != "deploy" || got.Status != model.TaskPending || got.CreatedBy != "creator" {
		t.Fatalf("got = %+v", got)
	}
	if len(got.DependsOn) != 1 || got.DependsOn[0] != "t1" {
		t.Fatalf("got.DependsOn = %v, want [t1]", got.DependsOn)
	}
	if _, err := s.GetTask(ctx, "ws1", "missing"); err != store.ErrNotFound {
		t.Fatalf("GetTask(missing) err = %v, want ErrNotFound", err)
	}
}

func testTaskDanglingDep(t *testing.T, s store.Store) {
	_, err := s.CreateTask(context.Background(), model.Task{
		ID: "t1", Workspace: "ws1", Title: "x", Status: model.TaskPending,
		CreatedBy: "c", CreatedAt: base, UpdatedAt: base,
	}, []string{"ghost"})
	if !errorsIs(err, store.ErrInvalidDependency) {
		t.Fatalf("err = %v, want ErrInvalidDependency", err)
	}
	// The failed create must not have persisted the task.
	if _, err := s.GetTask(context.Background(), "ws1", "t1"); err != store.ErrNotFound {
		t.Fatalf("dangling-dep task leaked: GetTask err = %v, want ErrNotFound", err)
	}
}

func testTaskClaimOrder(t *testing.T, s store.Store) {
	ctx := context.Background()
	// Insert with increasing CreatedAt by using distinct ids; both pending.
	_, err := s.CreateTask(ctx, model.Task{ID: "a", Workspace: "ws1", Title: "first", Status: model.TaskPending, CreatedBy: "c", CreatedAt: base, UpdatedAt: base}, nil)
	mustNoErr(t, err)
	_, err = s.CreateTask(ctx, model.Task{ID: "b", Workspace: "ws1", Title: "second", Status: model.TaskPending, CreatedBy: "c", CreatedAt: base.Add(time.Second), UpdatedAt: base.Add(time.Second)}, nil)
	mustNoErr(t, err)

	first, err := s.ClaimNextTask(ctx, "ws1", "agent1", base.Add(time.Minute), oneMin)
	mustNoErr(t, err)
	if first.ID != "a" {
		t.Fatalf("claimed %q first, want a (oldest)", first.ID)
	}
	if first.Status != model.TaskClaimed || first.AssignedAgent != "agent1" {
		t.Fatalf("claimed task = %+v, want claimed by agent1", first)
	}
	second, err := s.ClaimNextTask(ctx, "ws1", "agent2", base.Add(time.Minute), oneMin)
	mustNoErr(t, err)
	if second.ID != "b" {
		t.Fatalf("claimed %q second, want b", second.ID)
	}
}

func testTaskClaimDeps(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkTask(t, s, "ws1", "dep", "must finish first")
	mkTask(t, s, "ws1", "main", "blocked", "dep")

	// "main" is blocked by an incomplete dependency, so the only claimable task
	// is "dep".
	got, err := s.ClaimNextTask(ctx, "ws1", "a1", base.Add(time.Minute), oneMin)
	mustNoErr(t, err)
	if got.ID != "dep" {
		t.Fatalf("claimed %q, want dep (main is blocked)", got.ID)
	}
	// Nothing else is claimable until dep completes.
	if _, err := s.ClaimNextTask(ctx, "ws1", "a2", base.Add(time.Minute), oneMin); err != store.ErrNoClaimableTask {
		t.Fatalf("err = %v, want ErrNoClaimableTask while dep incomplete", err)
	}
	// Complete dep -> main becomes claimable.
	_, err = s.CompleteTask(ctx, "ws1", "dep", "a1", model.TaskCompleted, "done", base.Add(2*time.Minute))
	mustNoErr(t, err)
	got, err = s.ClaimNextTask(ctx, "ws1", "a2", base.Add(3*time.Minute), oneMin)
	mustNoErr(t, err)
	if got.ID != "main" {
		t.Fatalf("claimed %q, want main after dep completed", got.ID)
	}
}

func testTaskComplete(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkTask(t, s, "ws1", "t1", "work")
	claimed, err := s.ClaimNextTask(ctx, "ws1", "a1", base.Add(time.Minute), oneMin)
	mustNoErr(t, err)
	done, err := s.CompleteTask(ctx, "ws1", claimed.ID, "a1", model.TaskCompleted, "shipped", base.Add(2*time.Minute))
	mustNoErr(t, err)
	if done.Status != model.TaskCompleted || done.Result != "shipped" {
		t.Fatalf("done = %+v, want completed/shipped", done)
	}
	if done.LeaseExpiresAt != nil {
		t.Fatalf("completed task should have no lease, got %v", done.LeaseExpiresAt)
	}
}

func testTaskCompleteWrongAgent(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkTask(t, s, "ws1", "t1", "work")
	_, err := s.ClaimNextTask(ctx, "ws1", "a1", base.Add(time.Minute), oneMin)
	mustNoErr(t, err)
	// A different agent cannot complete it.
	if _, err := s.CompleteTask(ctx, "ws1", "t1", "intruder", model.TaskCompleted, "", base.Add(2*time.Minute)); !errorsIs(err, store.ErrTaskConflict) {
		t.Fatalf("err = %v, want ErrTaskConflict", err)
	}
	// Completing a non-existent task is ErrNotFound.
	if _, err := s.CompleteTask(ctx, "ws1", "ghost", "a1", model.TaskCompleted, "", base); err != store.ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func testTaskLeaseExpiry(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkTask(t, s, "ws1", "t1", "work")
	// a1 claims with a 1-minute lease at T+1m.
	claimAt := base.Add(time.Minute)
	c1, err := s.ClaimNextTask(ctx, "ws1", "a1", claimAt, oneMin)
	mustNoErr(t, err)
	if c1.AssignedAgent != "a1" {
		t.Fatalf("first claim by %q, want a1", c1.AssignedAgent)
	}
	// Before expiry, no one else can claim.
	if _, err := s.ClaimNextTask(ctx, "ws1", "a2", claimAt.Add(30*time.Second), oneMin); err != store.ErrNoClaimableTask {
		t.Fatalf("err = %v, want ErrNoClaimableTask before lease expiry", err)
	}
	// After expiry, a2 steals it (work-stealing).
	steal := claimAt.Add(2 * time.Minute)
	c2, err := s.ClaimNextTask(ctx, "ws1", "a2", steal, oneMin)
	mustNoErr(t, err)
	if c2.AssignedAgent != "a2" {
		t.Fatalf("stolen claim by %q, want a2", c2.AssignedAgent)
	}
	// Now a1's completion must be rejected (it lost the task).
	if _, err := s.CompleteTask(ctx, "ws1", "t1", "a1", model.TaskCompleted, "", steal); !errorsIs(err, store.ErrTaskConflict) {
		t.Fatalf("stale owner completion err = %v, want ErrTaskConflict", err)
	}
}

func testTaskNoClaimable(t *testing.T, s store.Store) {
	if _, err := s.ClaimNextTask(context.Background(), "ws1", "a1", base, oneMin); err != store.ErrNoClaimableTask {
		t.Fatalf("err = %v, want ErrNoClaimableTask on empty workspace", err)
	}
}

func testTaskListByStatus(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkTask(t, s, "ws1", "t1", "one")
	mkTask(t, s, "ws1", "t2", "two")
	_, err := s.ClaimNextTask(ctx, "ws1", "a1", base.Add(time.Minute), oneMin)
	mustNoErr(t, err)

	now := base.Add(time.Minute)
	all, err := s.ListTasks(ctx, "ws1", nil, now)
	mustNoErr(t, err)
	if len(all) != 2 {
		t.Fatalf("ListTasks(all) = %d, want 2", len(all))
	}
	pending, err := s.ListTasks(ctx, "ws1", []model.TaskStatus{model.TaskPending}, now)
	mustNoErr(t, err)
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	claimed, err := s.ListTasks(ctx, "ws1", []model.TaskStatus{model.TaskClaimed}, now)
	mustNoErr(t, err)
	if len(claimed) != 1 {
		t.Fatalf("claimed = %d, want 1", len(claimed))
	}

	// After the lease expires, the claimed task reports as pending (effective
	// status), so a pending filter should now return both.
	later := now.Add(2 * time.Minute)
	pending2, err := s.ListTasks(ctx, "ws1", []model.TaskStatus{model.TaskPending}, later)
	mustNoErr(t, err)
	if len(pending2) != 2 {
		t.Fatalf("pending after lease expiry = %d, want 2", len(pending2))
	}
}

// testTaskCreateDedupesDeps guards the store-divergence fix: both stores must
// dedupe the returned DependsOn (previously Postgres returned duplicates).
func testTaskCreateDedupesDeps(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkTask(t, s, "ws1", "dep", "dependency")
	got, err := s.CreateTask(ctx, model.Task{
		ID: "main", Workspace: "ws1", Title: "m", Status: model.TaskPending,
		CreatedBy: "c", CreatedAt: base, UpdatedAt: base,
	}, []string{"dep", "dep", "dep"})
	mustNoErr(t, err)
	if len(got.DependsOn) != 1 || got.DependsOn[0] != "dep" {
		t.Fatalf("DependsOn = %v, want [dep] (deduped)", got.DependsOn)
	}
	reread, err := s.GetTask(ctx, "ws1", "main")
	mustNoErr(t, err)
	if len(reread.DependsOn) != 1 {
		t.Fatalf("reread DependsOn = %v, want single edge", reread.DependsOn)
	}
}

// testTaskRetryUnblocksDependents is the escape-hatch for the failed-dependency
// dead-end: retrying a failed dependency requeues it and unblocks its dependents.
func testTaskRetryUnblocksDependents(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkTask(t, s, "ws1", "dep", "must pass")
	mkTask(t, s, "ws1", "main", "blocked", "dep")

	// Claim and FAIL dep -> main is now permanently unclaimable.
	if _, err := s.ClaimNextTask(ctx, "ws1", "a1", base.Add(time.Minute), oneMin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteTask(ctx, "ws1", "dep", "a1", model.TaskFailed, "boom", base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextTask(ctx, "ws1", "a2", base.Add(3*time.Minute), oneMin); err != store.ErrNoClaimableTask {
		t.Fatalf("with dep failed, claim = %v, want ErrNoClaimableTask", err)
	}

	// Retry dep -> pending; now dep is claimable again.
	retried, err := s.RetryTask(ctx, "ws1", "dep", base.Add(4*time.Minute))
	mustNoErr(t, err)
	if retried.Status != model.TaskPending || retried.AssignedAgent != "" || retried.Result != "" {
		t.Fatalf("retried = %+v, want pending with cleared assignee/result", retried)
	}
	got, err := s.ClaimNextTask(ctx, "ws1", "a3", base.Add(5*time.Minute), oneMin)
	mustNoErr(t, err)
	if got.ID != "dep" {
		t.Fatalf("claimed %q after retry, want dep", got.ID)
	}
	// Complete dep -> main finally claimable.
	if _, err := s.CompleteTask(ctx, "ws1", "dep", "a3", model.TaskCompleted, "ok", base.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err = s.ClaimNextTask(ctx, "ws1", "a4", base.Add(7*time.Minute), oneMin)
	mustNoErr(t, err)
	if got.ID != "main" {
		t.Fatalf("claimed %q, want main after dep completed via retry", got.ID)
	}
}

func testTaskRetryOnlyFailed(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkTask(t, s, "ws1", "t1", "pending task")
	// Retrying a pending (not failed) task is a conflict.
	if _, err := s.RetryTask(ctx, "ws1", "t1", base); !errorsIs(err, store.ErrTaskConflict) {
		t.Fatalf("retry pending err = %v, want ErrTaskConflict", err)
	}
	// Retrying a missing task is not-found.
	if _, err := s.RetryTask(ctx, "ws1", "ghost", base); err != store.ErrNotFound {
		t.Fatalf("retry missing err = %v, want ErrNotFound", err)
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
