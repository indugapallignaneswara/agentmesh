package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

func putArtifact(t *testing.T, s store.Store, ws, name, content, by string, base int64, at time.Time) model.Artifact {
	t.Helper()
	got, err := s.PutArtifact(context.Background(), model.Artifact{
		Workspace: ws, Name: name, Content: content, UpdatedBy: by, UpdatedAt: at,
	}, base)
	mustNoErr(t, err)
	return got
}

func testArtifactCreateGetList(t *testing.T, s store.Store) {
	ctx := context.Background()
	created := putArtifact(t, s, "ws1", "design-notes", "v1 of the plan", "alice", 0, base)
	if created.Version != 1 || created.CreatedBy != "alice" || created.UpdatedBy != "alice" {
		t.Fatalf("created = %+v", created)
	}
	got, err := s.GetArtifact(ctx, "ws1", "design-notes")
	mustNoErr(t, err)
	if got.Content != "v1 of the plan" || got.Version != 1 {
		t.Fatalf("got = %+v", got)
	}
	if _, err := s.GetArtifact(ctx, "ws1", "missing"); err != store.ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	putArtifact(t, s, "ws1", "api-plan", "endpoints", "bob", 0, base)
	putArtifact(t, s, "ws2", "other", "x", "zoe", 0, base)
	list, err := s.ListArtifacts(ctx, "ws1")
	mustNoErr(t, err)
	if len(list) != 2 || list[0].Name != "api-plan" || list[1].Name != "design-notes" {
		t.Fatalf("list = %+v, want [api-plan design-notes]", list)
	}
}

func testArtifactOptimisticConcurrency(t *testing.T, s store.Store) {
	ctx := context.Background()
	putArtifact(t, s, "ws1", "doc", "draft", "alice", 0, base)

	// Creating again is a conflict.
	if _, err := s.PutArtifact(ctx, model.Artifact{
		Workspace: "ws1", Name: "doc", Content: "x", UpdatedBy: "bob", UpdatedAt: base,
	}, 0); !errorsIs(err, store.ErrArtifactConflict) {
		t.Fatalf("re-create err = %v, want ErrArtifactConflict", err)
	}

	// bob updates from base 1 -> version 2, provenance updated, creator kept.
	updated := putArtifact(t, s, "ws1", "doc", "draft + bob's edits", "bob", 1, base.Add(time.Minute))
	if updated.Version != 2 || updated.UpdatedBy != "bob" || updated.CreatedBy != "alice" {
		t.Fatalf("updated = %+v", updated)
	}

	// alice writes from the STALE base 1 -> conflict; content untouched.
	if _, err := s.PutArtifact(ctx, model.Artifact{
		Workspace: "ws1", Name: "doc", Content: "alice's lost-update attempt", UpdatedBy: "alice", UpdatedAt: base,
	}, 1); !errorsIs(err, store.ErrArtifactConflict) {
		t.Fatalf("stale write err = %v, want ErrArtifactConflict", err)
	}
	got, err := s.GetArtifact(ctx, "ws1", "doc")
	mustNoErr(t, err)
	if got.Content != "draft + bob's edits" || got.Version != 2 {
		t.Fatalf("after stale write = %+v, want bob's v2 intact", got)
	}

	// alice re-reads (v2), merges, retries -> v3. No lost update.
	merged := putArtifact(t, s, "ws1", "doc", "draft + bob's edits + alice's edits", "alice", 2, base.Add(2*time.Minute))
	if merged.Version != 3 {
		t.Fatalf("merged = %+v, want version 3", merged)
	}

	// Updating a missing artifact is ErrNotFound (not conflict).
	if _, err := s.PutArtifact(ctx, model.Artifact{
		Workspace: "ws1", Name: "ghost", Content: "x", UpdatedBy: "alice", UpdatedAt: base,
	}, 1); err != store.ErrNotFound {
		t.Fatalf("update missing err = %v, want ErrNotFound", err)
	}
}
