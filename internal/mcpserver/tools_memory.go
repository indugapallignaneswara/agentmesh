package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// registerMemoryTools wires the Phase 2 shared-memory tools.
func registerMemoryTools(s *mcp.Server, svc *workspace.Service) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "memory_write",
		Description: "Store a memory item with provenance. scope=private is visible only to you, immediately. " +
			"scope=shared goes to a human review queue and becomes visible to the workspace only after approval — agents cannot publish shared memory directly.",
	}, memoryWriteHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name: "memory_search",
		Description: "Full-text search over the memories visible to you: your own private items plus approved shared items. " +
			"Best match first. Pending/rejected shared items and other members' private items are never returned.",
	}, memorySearchHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "memory_queue",
		Description: "List shared memory submissions awaiting review (oldest first). Restricted to human members.",
	}, memoryQueueHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "memory_review",
		Description: "Approve or reject a pending shared memory submission. Restricted to human members; you cannot review your own submission.",
	}, memoryReviewHandler(svc))
}

// --- arguments & results ---

type memoryWriteArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
	Author    string `json:"author" jsonschema:"the member writing the memory"`
	Scope     string `json:"scope" jsonschema:"'private' (own notes, immediate) or 'shared' (workspace knowledge, review-gated)"`
	Content   string `json:"content" jsonschema:"the memory content"`
	Source    string `json:"source,omitempty" jsonschema:"optional provenance: where this fact came from"`
}

type memorySearchArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
	Requester string `json:"requester" jsonschema:"the member searching"`
	Query     string `json:"query" jsonschema:"free-text search query"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum results (default 10, max 100)"`
}

type memoryListResult struct {
	Memories []model.Memory `json:"memories"`
	Count    int            `json:"count"`
}

type memoryQueueArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
	Reviewer  string `json:"reviewer" jsonschema:"the human member inspecting the queue"`
}

type memoryReviewArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
	Reviewer  string `json:"reviewer" jsonschema:"the human member reviewing"`
	ID        string `json:"id" jsonschema:"the memory id to review"`
	Decision  string `json:"decision" jsonschema:"'approve' or 'reject'"`
	Note      string `json:"note,omitempty" jsonschema:"optional review note"`
}

// --- handlers ---

func memoryWriteHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, memoryWriteArgs) (*mcp.CallToolResult, model.Memory, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a memoryWriteArgs) (*mcp.CallToolResult, model.Memory, error) {
		m, err := svc.MemoryWrite(ctx, a.Workspace, a.Author, model.MemoryScope(a.Scope), a.Content, a.Source)
		if err != nil {
			return failMemory[model.Memory](err)
		}
		return ok(m)
	}
}

func memorySearchHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, memorySearchArgs) (*mcp.CallToolResult, memoryListResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a memorySearchArgs) (*mcp.CallToolResult, memoryListResult, error) {
		ms, err := svc.MemorySearch(ctx, a.Workspace, a.Requester, a.Query, a.Limit)
		if err != nil {
			return failMemory[memoryListResult](err)
		}
		return ok(memoryListResult{Memories: ms, Count: len(ms)})
	}
}

func memoryQueueHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, memoryQueueArgs) (*mcp.CallToolResult, memoryListResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a memoryQueueArgs) (*mcp.CallToolResult, memoryListResult, error) {
		ms, err := svc.MemoryQueue(ctx, a.Workspace, a.Reviewer)
		if err != nil {
			return failMemory[memoryListResult](err)
		}
		return ok(memoryListResult{Memories: ms, Count: len(ms)})
	}
}

func memoryReviewHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, memoryReviewArgs) (*mcp.CallToolResult, model.Memory, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a memoryReviewArgs) (*mcp.CallToolResult, model.Memory, error) {
		var approve bool
		switch a.Decision {
		case "approve":
			approve = true
		case "reject":
			approve = false
		default:
			return failMemory[model.Memory](errInvalidDecision)
		}
		m, err := svc.MemoryReview(ctx, a.Workspace, a.Reviewer, a.ID, approve, a.Note)
		if err != nil {
			return failMemory[model.Memory](err)
		}
		return ok(m)
	}
}

// errInvalidDecision is an ErrInvalidInput-wrapped sentinel for the decision enum.
var errInvalidDecision = errors.Join(workspace.ErrInvalidInput, errors.New("decision must be 'approve' or 'reject'"))

// failMemory maps memory service errors to tool-level errors the agent can
// read; anything unexpected is a protocol error.
func failMemory[T any](err error) (*mcp.CallToolResult, T, error) {
	var zero T
	if errors.Is(err, workspace.ErrInvalidInput) ||
		errors.Is(err, store.ErrNotFound) ||
		errors.Is(err, store.ErrMemoryConflict) ||
		errors.Is(err, auth.ErrForbidden) ||
		errors.Is(err, workspace.ErrRoomClosed) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		}, zero, nil
	}
	return nil, zero, err
}
