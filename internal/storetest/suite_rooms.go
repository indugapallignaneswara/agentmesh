package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

func testRoomCreateGet(t *testing.T, s store.Store) {
	ctx := context.Background()
	created, err := s.CreateWorkspace(ctx, model.Workspace{
		Name: "team", Status: model.WorkspaceOpen, CreatedBy: "alice",
		CreatedAt: base, UpdatedAt: base,
	})
	mustNoErr(t, err)
	if created.Status != model.WorkspaceOpen || created.CreatedBy != "alice" {
		t.Fatalf("created = %+v", created)
	}
	got, err := s.GetWorkspace(ctx, "team")
	mustNoErr(t, err)
	if got.Name != "team" || got.Status != model.WorkspaceOpen {
		t.Fatalf("got = %+v", got)
	}
	// Duplicate create is a conflict.
	if _, err := s.CreateWorkspace(ctx, model.Workspace{
		Name: "team", Status: model.WorkspaceOpen, CreatedAt: base, UpdatedAt: base,
	}); err != store.ErrRoomExists {
		t.Fatalf("dup create err = %v, want ErrRoomExists", err)
	}
	if _, err := s.GetWorkspace(ctx, "missing"); err != store.ErrNotFound {
		t.Fatalf("get missing err = %v, want ErrNotFound", err)
	}
}

func testRoomEnsureIdempotent(t *testing.T, s store.Store) {
	ctx := context.Background()
	w1, err := s.EnsureWorkspace(ctx, "auto", base)
	mustNoErr(t, err)
	if w1.Status != model.WorkspaceOpen {
		t.Fatalf("ensured status = %s, want open", w1.Status)
	}
	// Second ensure returns the same room, does not reset it.
	w2, err := s.EnsureWorkspace(ctx, "auto", base.Add(time.Hour))
	mustNoErr(t, err)
	if !w2.CreatedAt.Equal(w1.CreatedAt) {
		t.Fatalf("ensure recreated room: created_at %v != %v", w2.CreatedAt, w1.CreatedAt)
	}
	// Ensure does not clobber an explicitly-created room's owner.
	_, err = s.CreateWorkspace(ctx, model.Workspace{
		Name: "owned", Status: model.WorkspaceOpen, CreatedBy: "alice",
		CreatedAt: base, UpdatedAt: base,
	})
	mustNoErr(t, err)
	w3, err := s.EnsureWorkspace(ctx, "owned", base.Add(time.Hour))
	mustNoErr(t, err)
	if w3.CreatedBy != "alice" {
		t.Fatalf("ensure clobbered owner: %+v", w3)
	}
}

func testRoomCloseReopenList(t *testing.T, s store.Store) {
	ctx := context.Background()
	mustCreateRoom(t, s, "a", "alice")
	mustCreateRoom(t, s, "b", "bob")

	// Close a -> records closer + time.
	closed, err := s.SetWorkspaceStatus(ctx, "a", model.WorkspaceClosed, "alice", base.Add(time.Hour))
	mustNoErr(t, err)
	if closed.Status != model.WorkspaceClosed || closed.ClosedBy != "alice" || closed.ClosedAt == nil {
		t.Fatalf("closed = %+v", closed)
	}

	// List all vs by status.
	all, err := s.ListWorkspaces(ctx, nil)
	mustNoErr(t, err)
	if len(all) != 2 {
		t.Fatalf("list all = %d, want 2", len(all))
	}
	open, err := s.ListWorkspaces(ctx, []model.WorkspaceStatus{model.WorkspaceOpen})
	mustNoErr(t, err)
	if len(open) != 1 || open[0].Name != "b" {
		t.Fatalf("open = %v, want [b]", roomNames(open))
	}

	// Reopen a -> clears close metadata.
	reopened, err := s.SetWorkspaceStatus(ctx, "a", model.WorkspaceOpen, "alice", base.Add(2*time.Hour))
	mustNoErr(t, err)
	if reopened.Status != model.WorkspaceOpen || reopened.ClosedBy != "" || reopened.ClosedAt != nil {
		t.Fatalf("reopened = %+v, want open with cleared close metadata", reopened)
	}

	if _, err := s.SetWorkspaceStatus(ctx, "ghost", model.WorkspaceClosed, "x", base); err != store.ErrNotFound {
		t.Fatalf("close missing err = %v, want ErrNotFound", err)
	}
}

func mustCreateRoom(t *testing.T, s store.Store, name, by string) {
	t.Helper()
	_, err := s.CreateWorkspace(context.Background(), model.Workspace{
		Name: name, Status: model.WorkspaceOpen, CreatedBy: by, CreatedAt: base, UpdatedAt: base,
	})
	mustNoErr(t, err)
}

func roomNames(ws []model.Workspace) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Name
	}
	return out
}
