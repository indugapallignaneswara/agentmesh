package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// registerArtifactTools wires the Phase 3 co-edited artifact tools.
func registerArtifactTools(s *mcp.Server, svc *workspace.Service) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_artifact",
		Description: "Read a co-edited workspace artifact (design notes, plans, runbooks) including its current version. Use the version as base_version when updating.",
	}, getArtifactHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_artifact",
		Description: "Write a workspace artifact with optimistic concurrency. base_version=0 creates a new artifact; otherwise pass the version you read. " +
			"If someone else wrote first you get a version-conflict error: call get_artifact again, merge your changes into the latest content, and retry with the new version. Never overwrite blindly.",
	}, updateArtifactHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_artifacts",
		Description: "List the workspace's co-edited artifacts with their versions and last editors.",
	}, listArtifactsHandler(svc))
}

type getArtifactArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
	Name      string `json:"name" jsonschema:"the artifact name"`
}

type updateArtifactArgs struct {
	Workspace   string `json:"workspace" jsonschema:"the workspace identifier"`
	Author      string `json:"author" jsonschema:"the member writing"`
	Name        string `json:"name" jsonschema:"the artifact name"`
	Content     string `json:"content" jsonschema:"the full new content of the artifact"`
	BaseVersion int64  `json:"base_version" jsonschema:"the version this edit is based on (0 to create)"`
}

type listArtifactsArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
}

type listArtifactsResult struct {
	Artifacts []model.Artifact `json:"artifacts"`
	Count     int              `json:"count"`
}

func getArtifactHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, getArtifactArgs) (*mcp.CallToolResult, model.Artifact, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a getArtifactArgs) (*mcp.CallToolResult, model.Artifact, error) {
		art, err := svc.ArtifactGet(ctx, a.Workspace, a.Name)
		if err != nil {
			return failArtifact[model.Artifact](err)
		}
		return ok(art)
	}
}

func updateArtifactHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, updateArtifactArgs) (*mcp.CallToolResult, model.Artifact, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a updateArtifactArgs) (*mcp.CallToolResult, model.Artifact, error) {
		art, err := svc.ArtifactPut(ctx, a.Workspace, a.Author, a.Name, a.Content, a.BaseVersion)
		if errors.Is(err, store.ErrArtifactConflict) {
			// Make the conflict actionable: tell the agent the current version
			// so its next get->merge->retry loop is precise.
			msg := err.Error()
			if cur, gerr := svc.ArtifactGet(ctx, a.Workspace, a.Name); gerr == nil {
				msg = fmt.Sprintf(
					"version conflict: %q is now at version %d (you wrote from base_version %d). Call get_artifact, merge your changes into the latest content, then retry with base_version=%d.",
					a.Name, cur.Version, a.BaseVersion, cur.Version)
			}
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: msg}},
			}, model.Artifact{}, nil
		}
		if err != nil {
			return failArtifact[model.Artifact](err)
		}
		return ok(art)
	}
}

func listArtifactsHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, listArtifactsArgs) (*mcp.CallToolResult, listArtifactsResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a listArtifactsArgs) (*mcp.CallToolResult, listArtifactsResult, error) {
		arts, err := svc.ArtifactList(ctx, a.Workspace)
		if err != nil {
			return failArtifact[listArtifactsResult](err)
		}
		return ok(listArtifactsResult{Artifacts: arts, Count: len(arts)})
	}
}

func failArtifact[T any](err error) (*mcp.CallToolResult, T, error) {
	var zero T
	if errors.Is(err, workspace.ErrInvalidInput) ||
		errors.Is(err, store.ErrNotFound) ||
		errors.Is(err, store.ErrArtifactConflict) ||
		errors.Is(err, auth.ErrForbidden) ||
		errors.Is(err, workspace.ErrRoomClosed) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		}, zero, nil
	}
	return nil, zero, err
}
