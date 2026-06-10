package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

const artifactSelect = `
	SELECT workspace, name, content, version, created_by, updated_by, created_at, updated_at
	FROM artifacts`

func (s *Postgres) PutArtifact(ctx context.Context, a model.Artifact, baseVersion int64) (model.Artifact, error) {
	if baseVersion == 0 {
		// Create: the conditional insert is the uniqueness guard.
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO artifacts (workspace, name, content, version, created_by, updated_by, created_at, updated_at)
			VALUES ($1, $2, $3, 1, $4, $4, $5, $5)
			ON CONFLICT (workspace, name) DO NOTHING`,
			a.Workspace, a.Name, a.Content, a.UpdatedBy, a.UpdatedAt)
		if err != nil {
			return model.Artifact{}, err
		}
		if tag.RowsAffected() == 0 {
			return model.Artifact{}, ErrArtifactConflict
		}
		return s.GetArtifact(ctx, a.Workspace, a.Name)
	}

	// Update: conditioned on the base version — the lost-update guard.
	tag, err := s.pool.Exec(ctx, `
		UPDATE artifacts
		SET content = $3, version = version + 1, updated_by = $4, updated_at = $5
		WHERE workspace = $1 AND name = $2 AND version = $6`,
		a.Workspace, a.Name, a.Content, a.UpdatedBy, a.UpdatedAt, baseVersion)
	if err != nil {
		return model.Artifact{}, err
	}
	if tag.RowsAffected() == 0 {
		// Distinguish "missing" from "stale".
		if _, err := s.GetArtifact(ctx, a.Workspace, a.Name); errors.Is(err, ErrNotFound) {
			return model.Artifact{}, ErrNotFound
		}
		return model.Artifact{}, ErrArtifactConflict
	}
	return s.GetArtifact(ctx, a.Workspace, a.Name)
}

func (s *Postgres) GetArtifact(ctx context.Context, workspace, name string) (model.Artifact, error) {
	a, err := scanArtifact(s.pool.QueryRow(ctx,
		artifactSelect+` WHERE workspace = $1 AND name = $2`, workspace, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Artifact{}, ErrNotFound
	}
	return a, err
}

func (s *Postgres) ListArtifacts(ctx context.Context, workspace string) ([]model.Artifact, error) {
	rows, err := s.pool.Query(ctx, artifactSelect+` WHERE workspace = $1 ORDER BY name`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanArtifact(row pgx.Row) (model.Artifact, error) {
	var a model.Artifact
	if err := row.Scan(&a.Workspace, &a.Name, &a.Content, &a.Version,
		&a.CreatedBy, &a.UpdatedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return model.Artifact{}, err
	}
	return a, nil
}
