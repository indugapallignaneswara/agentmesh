package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// testBudgetDefaultsZero: budgets default to 0/0 (unlimited) on explicit
// create AND on lazy EnsureWorkspace — existing deployments are unaffected.
func testBudgetDefaultsZero(t *testing.T, s store.Store) {
	ctx := context.Background()
	created, err := s.CreateWorkspace(ctx, model.Workspace{
		Name: "team", Status: model.WorkspaceOpen, CreatedBy: "alice",
		CreatedAt: base, UpdatedAt: base,
	})
	mustNoErr(t, err)
	if created.BudgetDailyBytes != 0 || created.BudgetMemberDailyBytes != 0 {
		t.Fatalf("created budgets = %d/%d, want 0/0", created.BudgetDailyBytes, created.BudgetMemberDailyBytes)
	}

	ensured, err := s.EnsureWorkspace(ctx, "auto", base)
	mustNoErr(t, err)
	if ensured.BudgetDailyBytes != 0 || ensured.BudgetMemberDailyBytes != 0 {
		t.Fatalf("ensured budgets = %d/%d, want 0/0", ensured.BudgetDailyBytes, ensured.BudgetMemberDailyBytes)
	}
}

// testBudgetSetRoundTrip: SetWorkspaceBudget round-trips through both read
// paths (GetWorkspace and ListWorkspaces) and bumps updated_at.
func testBudgetSetRoundTrip(t *testing.T, s store.Store) {
	ctx := context.Background()
	mustCreateRoom(t, s, "team", "alice")

	later := base.Add(time.Hour)
	w, err := s.SetWorkspaceBudget(ctx, "team", 1_000_000, 250_000, later)
	mustNoErr(t, err)
	if w.BudgetDailyBytes != 1_000_000 || w.BudgetMemberDailyBytes != 250_000 {
		t.Fatalf("set returned budgets = %d/%d, want 1000000/250000", w.BudgetDailyBytes, w.BudgetMemberDailyBytes)
	}
	if !w.UpdatedAt.Equal(later) {
		t.Fatalf("updated_at = %v, want %v", w.UpdatedAt, later)
	}

	got, err := s.GetWorkspace(ctx, "team")
	mustNoErr(t, err)
	if got.BudgetDailyBytes != 1_000_000 || got.BudgetMemberDailyBytes != 250_000 {
		t.Fatalf("get budgets = %d/%d, want 1000000/250000", got.BudgetDailyBytes, got.BudgetMemberDailyBytes)
	}

	list, err := s.ListWorkspaces(ctx, nil)
	mustNoErr(t, err)
	if len(list) != 1 || list[0].BudgetDailyBytes != 1_000_000 || list[0].BudgetMemberDailyBytes != 250_000 {
		t.Fatalf("list = %+v, want one room with budgets 1000000/250000", list)
	}

	// Setting back to 0/0 (unlimited) round-trips too.
	w, err = s.SetWorkspaceBudget(ctx, "team", 0, 0, later.Add(time.Hour))
	mustNoErr(t, err)
	if w.BudgetDailyBytes != 0 || w.BudgetMemberDailyBytes != 0 {
		t.Fatalf("cleared budgets = %d/%d, want 0/0", w.BudgetDailyBytes, w.BudgetMemberDailyBytes)
	}

	// Unknown room is ErrNotFound.
	if _, err := s.SetWorkspaceBudget(ctx, "ghost", 1, 1, later); !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("set budget on missing room err = %v, want ErrNotFound", err)
	}
}

// testBudgetSurvivesStatusChange: close/reopen must not clobber budgets —
// this is the assertion that catches a broken UPDATE (e.g. a status UPDATE
// that rewrites the whole row from a struct missing the budget fields).
func testBudgetSurvivesStatusChange(t *testing.T, s store.Store) {
	ctx := context.Background()
	mustCreateRoom(t, s, "team", "alice")
	_, err := s.SetWorkspaceBudget(ctx, "team", 5_000_000, 1_000_000, base.Add(time.Minute))
	mustNoErr(t, err)

	closed, err := s.SetWorkspaceStatus(ctx, "team", model.WorkspaceClosed, "alice", base.Add(time.Hour))
	mustNoErr(t, err)
	if closed.BudgetDailyBytes != 5_000_000 || closed.BudgetMemberDailyBytes != 1_000_000 {
		t.Fatalf("close clobbered budgets: %d/%d, want 5000000/1000000", closed.BudgetDailyBytes, closed.BudgetMemberDailyBytes)
	}

	reopened, err := s.SetWorkspaceStatus(ctx, "team", model.WorkspaceOpen, "alice", base.Add(2*time.Hour))
	mustNoErr(t, err)
	if reopened.BudgetDailyBytes != 5_000_000 || reopened.BudgetMemberDailyBytes != 1_000_000 {
		t.Fatalf("reopen clobbered budgets: %d/%d, want 5000000/1000000", reopened.BudgetDailyBytes, reopened.BudgetMemberDailyBytes)
	}

	// And the persisted row agrees.
	got, err := s.GetWorkspace(ctx, "team")
	mustNoErr(t, err)
	if got.BudgetDailyBytes != 5_000_000 || got.BudgetMemberDailyBytes != 1_000_000 {
		t.Fatalf("persisted budgets after close/reopen = %d/%d, want 5000000/1000000", got.BudgetDailyBytes, got.BudgetMemberDailyBytes)
	}
}
