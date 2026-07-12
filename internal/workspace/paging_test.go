package workspace_test

import (
	"context"
	"testing"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

// TestListTasksPagedCapsAndSignals pins that list results are bounded and that
// truncation is never silent — a caller always learns it needs to narrow or page.
func TestListTasksPagedCapsAndSignals(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "alice")
	for i := 0; i < 12; i++ {
		if _, err := svc.CreateTask(ctx, "ws", "alice", "task", "", nil); err != nil {
			t.Fatal(err)
		}
	}

	// An explicit small limit truncates and says so.
	tasks, truncated, err := svc.ListTasksPaged(ctx, "ws", nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 5 || !truncated {
		t.Fatalf("limit 5: got %d tasks truncated=%v, want 5/true", len(tasks), truncated)
	}

	// A limit above the result size returns everything and reports no truncation.
	tasks, truncated, err = svc.ListTasksPaged(ctx, "ws", nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 12 || truncated {
		t.Fatalf("limit 100: got %d tasks truncated=%v, want 12/false", len(tasks), truncated)
	}

	// An absurd limit is capped to the ceiling, not honoured verbatim.
	tasks, _, err = svc.ListTasksPaged(ctx, "ws", []model.TaskStatus{model.TaskPending}, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 12 {
		t.Fatalf("oversized limit: got %d, want all 12 (capped ceiling still >= 12)", len(tasks))
	}
}
