package workspace_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// seqIDs returns a deterministic sequential id generator for tests.
func seqIDs() func() string {
	var mu sync.Mutex
	var n int
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return "id-" + string(rune('a'+n-1))
	}
}

func TestCreateTaskRequiresMember(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.CreateTask(ctx, "ws", "ghost", "do it", "", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (non-member creator)", err)
	}
}

func TestCreateTaskValidation(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")
	if _, err := svc.CreateTask(ctx, "ws", "alice", "", "", nil); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("empty title err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateTaskDanglingDepIsInvalidInput(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")
	// A dangling dependency should surface as ErrInvalidInput (client error),
	// not a raw store error.
	if _, err := svc.CreateTask(ctx, "ws", "alice", "task", "", []string{"nope"}); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestClaimCompleteFlow(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")
	mustJoin(t, svc, "ws", "worker")

	created, err := svc.CreateTask(ctx, "ws", "alice", "ship it", "details", nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != model.TaskPending {
		t.Fatalf("created status = %s, want pending", created.Status)
	}

	claimed, err := svc.ClaimTask(ctx, "ws", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != created.ID || claimed.AssignedAgent != "worker" || claimed.Status != model.TaskClaimed {
		t.Fatalf("claimed = %+v", claimed)
	}

	done, err := svc.CompleteTask(ctx, "ws", created.ID, "worker", "deployed", true)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != model.TaskCompleted || done.Result != "deployed" {
		t.Fatalf("done = %+v", done)
	}
}

func TestClaimNoTasksAvailable(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "worker")
	if _, err := svc.ClaimTask(ctx, "ws", "worker"); !errors.Is(err, store.ErrNoClaimableTask) {
		t.Fatalf("err = %v, want ErrNoClaimableTask", err)
	}
}

func TestCompleteByNonAssigneeRejected(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")
	mustJoin(t, svc, "ws", "w1")
	mustJoin(t, svc, "ws", "w2")
	created, err := svc.CreateTask(ctx, "ws", "alice", "task", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimTask(ctx, "ws", "w1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteTask(ctx, "ws", created.ID, "w2", "", true); !errors.Is(err, store.ErrTaskConflict) {
		t.Fatalf("err = %v, want ErrTaskConflict", err)
	}
}

func TestLeaseExpiryAllowsReclaim(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	svc := workspace.New(store.NewMemory(), bus.NewNoop(),
		workspace.WithClock(c.now),
		workspace.WithIDGen(seqIDs()),
		workspace.WithTaskLease(time.Minute),
	)
	ctx := context.Background()
	if _, err := svc.Join(ctx, "ws", "alice", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "ws", "w1", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "ws", "w2", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreateTask(ctx, "ws", "alice", "task", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimTask(ctx, "ws", "w1"); err != nil {
		t.Fatal(err)
	}
	// Before lease expiry, w2 gets nothing.
	c.advance(30 * time.Second)
	if _, err := svc.ClaimTask(ctx, "ws", "w2"); !errors.Is(err, store.ErrNoClaimableTask) {
		t.Fatalf("err = %v, want ErrNoClaimableTask before expiry", err)
	}
	// After expiry, w2 steals it.
	c.advance(2 * time.Minute)
	stolen, err := svc.ClaimTask(ctx, "ws", "w2")
	if err != nil {
		t.Fatal(err)
	}
	if stolen.ID != created.ID || stolen.AssignedAgent != "w2" {
		t.Fatalf("stolen = %+v, want reclaimed by w2", stolen)
	}
}

func TestListTasksFilter(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")
	mustJoin(t, svc, "ws", "worker")
	if _, err := svc.CreateTask(ctx, "ws", "alice", "a", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTask(ctx, "ws", "alice", "b", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimTask(ctx, "ws", "worker"); err != nil {
		t.Fatal(err)
	}
	pending, err := svc.ListTasks(ctx, "ws", []model.TaskStatus{model.TaskPending})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	// Invalid status is rejected.
	if _, err := svc.ListTasks(ctx, "ws", []model.TaskStatus{"bogus"}); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput for bad status", err)
	}
}
