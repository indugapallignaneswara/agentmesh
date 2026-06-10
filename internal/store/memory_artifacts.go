package store

import (
	"context"
	"sort"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

func (s *Memory) PutArtifact(_ context.Context, a model.Artifact, baseVersion int64) (model.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := memKey(a.Workspace, a.Name)
	existing, exists := s.arts[k]

	if baseVersion == 0 {
		if exists {
			return model.Artifact{}, ErrArtifactConflict
		}
		a.Version = 1
		a.CreatedBy = a.UpdatedBy
		a.CreatedAt = a.UpdatedAt
		s.arts[k] = a
		return a, nil
	}
	if !exists {
		return model.Artifact{}, ErrNotFound
	}
	if existing.Version != baseVersion {
		return model.Artifact{}, ErrArtifactConflict
	}
	existing.Content = a.Content
	existing.Version = baseVersion + 1
	existing.UpdatedBy = a.UpdatedBy
	existing.UpdatedAt = a.UpdatedAt
	s.arts[k] = existing
	return existing, nil
}

func (s *Memory) GetArtifact(_ context.Context, workspace, name string) (model.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.arts[memKey(workspace, name)]
	if !ok {
		return model.Artifact{}, ErrNotFound
	}
	return a, nil
}

func (s *Memory) ListArtifacts(_ context.Context, workspace string) ([]model.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.Artifact
	for _, a := range s.arts {
		if a.Workspace == workspace {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
