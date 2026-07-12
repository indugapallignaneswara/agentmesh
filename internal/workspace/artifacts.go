package workspace

import (
	"context"
	"fmt"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

// EventArtifactUpdated is recorded for every successful artifact write
// (version 1 marks the creation).
const EventArtifactUpdated = "artifact_updated"

const maxArtifactContent = 256 * 1024

// ArtifactPut writes a co-edited artifact with optimistic concurrency.
// baseVersion 0 creates; otherwise it must match the stored version or the
// write is rejected with store.ErrArtifactConflict, in which case the caller
// re-reads, merges, and retries — the no-lost-updates protocol.
func (s *Service) ArtifactPut(ctx context.Context, workspace, author, name, content string, baseVersion int64) (model.Artifact, error) {
	if err := validName("workspace", workspace); err != nil {
		return model.Artifact{}, err
	}
	if err := validName("author", author); err != nil {
		return model.Artifact{}, err
	}
	if err := validName("name", name); err != nil {
		return model.Artifact{}, err
	}
	if len(content) > maxArtifactContent {
		return model.Artifact{}, fmt.Errorf("%w: content must be at most %d bytes", ErrInvalidInput, maxArtifactContent)
	}
	if baseVersion < 0 {
		return model.Artifact{}, fmt.Errorf("%w: base_version must be >= 0", ErrInvalidInput)
	}
	if err := auth.CheckActor(ctx, workspace, author); err != nil {
		return model.Artifact{}, err
	}
	if err := s.requireOpenRoom(ctx, workspace); err != nil {
		return model.Artifact{}, err
	}
	if err := s.requireMember(ctx, workspace, author); err != nil {
		return model.Artifact{}, err
	}

	now := s.now()
	stored, err := s.store.PutArtifact(ctx, model.Artifact{
		Workspace: workspace,
		Name:      name,
		Content:   content,
		UpdatedBy: author,
		UpdatedAt: now,
	}, baseVersion)
	if err != nil {
		return model.Artifact{}, err
	}
	s.touch(ctx, workspace, author)
	s.appendEvent(ctx, workspace, author, EventArtifactUpdated, map[string]any{
		"artifact": name, "version": stored.Version,
	})
	return stored, nil
}

// ArtifactGet returns one artifact or store.ErrNotFound.
func (s *Service) ArtifactGet(ctx context.Context, workspace, name string) (model.Artifact, error) {
	if err := validName("workspace", workspace); err != nil {
		return model.Artifact{}, err
	}
	if err := validName("name", name); err != nil {
		return model.Artifact{}, err
	}
	if err := auth.CheckWorkspace(ctx, workspace); err != nil {
		return model.Artifact{}, err
	}
	return s.store.GetArtifact(ctx, workspace, name)
}

// ArtifactList returns the workspace's artifacts ordered by name.
func (s *Service) ArtifactList(ctx context.Context, workspace string) ([]model.Artifact, error) {
	if err := validName("workspace", workspace); err != nil {
		return nil, err
	}
	if err := auth.CheckWorkspace(ctx, workspace); err != nil {
		return nil, err
	}
	arts, err := s.store.ListArtifacts(ctx, workspace)
	if err != nil {
		return nil, err
	}
	arts, _ = capList(arts, 0)
	return arts, nil
}
