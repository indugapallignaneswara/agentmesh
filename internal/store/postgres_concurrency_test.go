package store_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// TestPostgresConcurrentClaimNoDuplicates is the acceptance test for the Phase 1
// guarantee: under genuinely concurrent claims, no two agents ever claim the
// same task. This can only be validated against real Postgres — the in-memory
// store serialises all claims under one mutex, so it would pass regardless of
// whether the SQL used FOR UPDATE SKIP LOCKED. Here, N goroutines on separate
// pooled connections race to claim M tasks; we assert exactly M claims with M
// distinct task IDs and no task assigned to two agents.
//
// (To confirm this test has teeth: drop "FOR UPDATE SKIP LOCKED" from the claim
// query in postgres_tasks.go and it fails with duplicate claims.)
func TestPostgresConcurrentClaimNoDuplicates(t *testing.T) {
	dsn := os.Getenv("AGENTMESH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set AGENTMESH_TEST_DATABASE_URL to run Postgres concurrency test")
	}
	ctx := context.Background()
	pg, err := store.NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	if err := pg.TruncateAll(ctx); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	const (
		ws       = "race"
		numTasks = 50
		numAgent = 16
	)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < numTasks; i++ {
		id := "t" + itoa(i)
		if _, err := pg.CreateTask(ctx, model.Task{
			ID: id, Workspace: ws, Title: id, Status: model.TaskPending,
			CreatedBy: "c", CreatedAt: now.Add(time.Duration(i) * time.Millisecond),
			UpdatedAt: now,
		}, nil); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	// Each agent loops claiming until the pool is drained, recording what it got.
	type claim struct {
		agent  string
		taskID string
	}
	var (
		mu     sync.Mutex
		claims []claim
		wg     sync.WaitGroup
	)
	wg.Add(numAgent)
	for a := 0; a < numAgent; a++ {
		go func(agent string) {
			defer wg.Done()
			for {
				tk, err := pg.ClaimNextTask(ctx, ws, agent, now, time.Hour)
				if err == store.ErrNoClaimableTask {
					return
				}
				if err != nil {
					t.Errorf("claim by %s: %v", agent, err)
					return
				}
				mu.Lock()
				claims = append(claims, claim{agent: agent, taskID: tk.ID})
				mu.Unlock()
			}
		}("agent" + itoa(a))
	}
	wg.Wait()

	// Every task claimed exactly once, by exactly one agent.
	if len(claims) != numTasks {
		t.Fatalf("total claims = %d, want %d", len(claims), numTasks)
	}
	owner := make(map[string]string, numTasks)
	for _, c := range claims {
		if prev, dup := owner[c.taskID]; dup {
			t.Fatalf("task %s claimed twice: by %s and %s", c.taskID, prev, c.agent)
		}
		owner[c.taskID] = c.agent
	}
	if len(owner) != numTasks {
		t.Fatalf("distinct claimed tasks = %d, want %d", len(owner), numTasks)
	}
}

// itoa is a tiny strconv.Itoa to avoid an import just for test ids.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
