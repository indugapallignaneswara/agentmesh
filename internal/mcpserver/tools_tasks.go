package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// --- task tool arguments & results ---

type createTaskArgs struct {
	Workspace string   `json:"workspace" jsonschema:"the workspace identifier"`
	Creator   string   `json:"creator" jsonschema:"the member creating the task"`
	Title     string   `json:"title" jsonschema:"a short task title"`
	Details   string   `json:"details,omitempty" jsonschema:"optional longer description"`
	DependsOn []string `json:"depends_on,omitempty" jsonschema:"optional ids of tasks that must complete first"`
}

type claimTaskArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
	Agent     string `json:"agent" jsonschema:"the agent claiming a task"`
}

// claimTaskResult distinguishes "claimed a task" from "nothing available"
// without making the latter an error the agent must catch.
type claimTaskResult struct {
	Claimable bool        `json:"claimable"`
	Task      *model.Task `json:"task,omitempty"`
}

type completeTaskArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
	ID        string `json:"id" jsonschema:"the task id"`
	Agent     string `json:"agent" jsonschema:"the assignee completing the task"`
	Result    string `json:"result,omitempty" jsonschema:"optional result/output text"`
	Done      *bool  `json:"done,omitempty" jsonschema:"true (default) marks completed; false marks failed"`
}

type getTaskArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
	ID        string `json:"id" jsonschema:"the task id"`
}

type listTasksArgs struct {
	Workspace string   `json:"workspace" jsonschema:"the workspace identifier"`
	Statuses  []string `json:"statuses,omitempty" jsonschema:"optional status filter: pending, claimed, completed, failed"`
}

type listTasksResult struct {
	Tasks []model.Task `json:"tasks"`
	Count int          `json:"count"`
}

// --- handlers ---

func createTaskHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, createTaskArgs) (*mcp.CallToolResult, model.Task, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a createTaskArgs) (*mcp.CallToolResult, model.Task, error) {
		t, err := svc.CreateTask(ctx, a.Workspace, a.Creator, a.Title, a.Details, a.DependsOn)
		if err != nil {
			return failTask[model.Task](err)
		}
		return ok(t)
	}
}

func claimTaskHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, claimTaskArgs) (*mcp.CallToolResult, claimTaskResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a claimTaskArgs) (*mcp.CallToolResult, claimTaskResult, error) {
		t, err := svc.ClaimTask(ctx, a.Workspace, a.Agent)
		if errors.Is(err, store.ErrNoClaimableTask) {
			return ok(claimTaskResult{Claimable: false})
		}
		if err != nil {
			return failTask[claimTaskResult](err)
		}
		return ok(claimTaskResult{Claimable: true, Task: &t})
	}
}

func completeTaskHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, completeTaskArgs) (*mcp.CallToolResult, model.Task, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a completeTaskArgs) (*mcp.CallToolResult, model.Task, error) {
		done := true
		if a.Done != nil {
			done = *a.Done
		}
		t, err := svc.CompleteTask(ctx, a.Workspace, a.ID, a.Agent, a.Result, done)
		if err != nil {
			return failTask[model.Task](err)
		}
		return ok(t)
	}
}

func getTaskHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, getTaskArgs) (*mcp.CallToolResult, model.Task, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a getTaskArgs) (*mcp.CallToolResult, model.Task, error) {
		t, err := svc.GetTask(ctx, a.Workspace, a.ID)
		if err != nil {
			return failTask[model.Task](err)
		}
		return ok(t)
	}
}

func listTasksHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, listTasksArgs) (*mcp.CallToolResult, listTasksResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a listTasksArgs) (*mcp.CallToolResult, listTasksResult, error) {
		statuses := make([]model.TaskStatus, len(a.Statuses))
		for i, s := range a.Statuses {
			statuses[i] = model.TaskStatus(s)
		}
		tasks, err := svc.ListTasks(ctx, a.Workspace, statuses)
		if err != nil {
			return failTask[listTasksResult](err)
		}
		return ok(listTasksResult{Tasks: tasks, Count: len(tasks)})
	}
}

// failTask maps task service errors to tool-level errors the agent can read:
// bad input, missing task/member, and claim conflicts are all recoverable
// client errors; anything else is a protocol error.
func failTask[T any](err error) (*mcp.CallToolResult, T, error) {
	var zero T
	if errors.Is(err, workspace.ErrInvalidInput) ||
		errors.Is(err, store.ErrNotFound) ||
		errors.Is(err, store.ErrInvalidDependency) ||
		errors.Is(err, store.ErrTaskConflict) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		}, zero, nil
	}
	return nil, zero, err
}
