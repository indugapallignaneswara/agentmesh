package workspace_test

import (
	"context"
	"errors"
	"testing"

	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

func TestArtifactCoEditFlow(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "agent-a")
	joinHuman(t, svc, "ws", "alice")

	// Agent creates the doc; human edits it; agent's stale write is rejected;
	// agent merges and succeeds — human+agent co-editing without lost updates.
	created, err := svc.ArtifactPut(ctx, "ws", "agent-a", "design-notes", "## Plan\n- step 1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 {
		t.Fatalf("created = %+v", created)
	}

	human, err := svc.ArtifactPut(ctx, "ws", "alice", "design-notes", "## Plan\n- step 1\n- human note", 1)
	if err != nil {
		t.Fatal(err)
	}
	if human.Version != 2 || human.UpdatedBy != "alice" {
		t.Fatalf("human edit = %+v", human)
	}

	if _, err := svc.ArtifactPut(ctx, "ws", "agent-a", "design-notes", "stale overwrite", 1); !errors.Is(err, store.ErrArtifactConflict) {
		t.Fatalf("stale write err = %v, want ErrArtifactConflict", err)
	}

	cur, err := svc.ArtifactGet(ctx, "ws", "design-notes")
	if err != nil {
		t.Fatal(err)
	}
	merged, err := svc.ArtifactPut(ctx, "ws", "agent-a", "design-notes", cur.Content+"\n- agent addendum", cur.Version)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Version != 3 || merged.CreatedBy != "agent-a" || merged.UpdatedBy != "agent-a" {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestArtifactValidation(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	mustJoin(t, svc, "ws", "agent-a")

	if _, err := svc.ArtifactPut(ctx, "ws", "ghost", "doc", "x", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("non-member err = %v, want ErrNotFound", err)
	}
	if _, err := svc.ArtifactPut(ctx, "ws", "agent-a", "bad name", "x", 0); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("bad name err = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.ArtifactPut(ctx, "ws", "agent-a", "doc", "x", -1); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("negative base err = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.ArtifactGet(ctx, "ws", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get missing err = %v, want ErrNotFound", err)
	}
}
