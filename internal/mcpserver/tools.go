package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// registerTools wires the coordination service to the seven Phase 0 MCP tools.
func registerTools(s *mcp.Server, svc *workspace.Service) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "workspace_join",
		Description: "Join a shared workspace as a named human or agent member, or refresh an existing membership. Must be called before sending or reading messages.",
	}, joinHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "workspace_presence",
		Description: "List the members currently active in a workspace (seen within the presence window).",
	}, presenceHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "send_message",
		Description: "Send a direct, point-to-point message to one named member of the workspace (any-to-any addressing).",
	}, sendMessageHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "read_inbox",
		Description: "Read and consume undelivered messages addressed to a member. Delivery is at-most-once: each message is returned exactly once and marked delivered, so subsequent calls return only newer messages.",
	}, readInboxHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "broadcast",
		Description: "Send a message to every other member of the workspace (many-to-many fan-out).",
	}, broadcastHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "publish_event",
		Description: "Append a typed event to the workspace's append-only observation log.",
	}, publishEventHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "subscribe",
		Description: "Read events from the observation log after a cursor. Returns the events and the next cursor to poll with. Pull-based: call repeatedly to follow the log.",
	}, subscribeHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_task",
		Description: "Add a task to the shared task board. Optionally depends_on other task ids (which must exist in this workspace); a task is only claimable once all its dependencies are completed.",
	}, createTaskHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "claim_task",
		Description: "Atomically claim the next eligible task (oldest first, dependencies completed) for an agent. No two agents ever claim the same task. The claim holds a lease; if the agent dies without completing, the task returns to the pool. Returns claimable=false when nothing is available.",
	}, claimTaskHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "complete_task",
		Description: "Mark a claimed task completed (or failed, with done=false). Only the current assignee may complete it; if the lease lapsed and another agent took over, this is rejected.",
	}, completeTaskHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_task",
		Description: "Fetch a single task by id, including its status, assignee and dependencies.",
	}, getTaskHandler(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_tasks",
		Description: "List the workspace's tasks, optionally filtered by status (pending, claimed, completed, failed). A claimed task whose lease has expired is reported as pending.",
	}, listTasksHandler(svc))
}

// --- tool argument and result types ---
//
// Blob fields (agent_card, payload) are typed `any` so the generated input
// schema accepts any JSON value; result types embed the DTOs from dto.go so the
// generated output schema is likewise permissive. See dto.go for why.

type joinArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier (letters, digits, '-' and '_')"`
	Name      string `json:"name" jsonschema:"the member's unique name within the workspace"`
	Kind      string `json:"kind" jsonschema:"the member kind: 'human' or 'agent'"`
	AgentCard any    `json:"agent_card,omitempty" jsonschema:"optional JSON capability/identity card for the member"`
}

type presenceArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
}

type presenceResult struct {
	Members []memberDTO `json:"members"`
	Count   int         `json:"count"`
}

type sendMessageArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
	From      string `json:"from" jsonschema:"the sending member's name"`
	To        string `json:"to" jsonschema:"the recipient member's name"`
	Body      string `json:"body" jsonschema:"the message body"`
}

type readInboxArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
	Member    string `json:"member" jsonschema:"the member whose inbox to read"`
}

type readInboxResult struct {
	Messages []model.Message `json:"messages"`
	Count    int             `json:"count"`
}

type broadcastArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
	From      string `json:"from" jsonschema:"the sending member's name"`
	Body      string `json:"body" jsonschema:"the message body"`
}

type broadcastResult struct {
	Message    model.Message `json:"message"`
	Recipients int           `json:"recipients"`
}

type publishEventArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
	Source    string `json:"source" jsonschema:"the member publishing the event"`
	Type      string `json:"type" jsonschema:"a short event type name"`
	Payload   any    `json:"payload,omitempty" jsonschema:"optional JSON event payload"`
}

type subscribeArgs struct {
	Workspace string `json:"workspace" jsonschema:"the workspace identifier"`
	Member    string `json:"member,omitempty" jsonschema:"optional: the polling member, whose presence is refreshed"`
	Since     int64  `json:"since,omitempty" jsonschema:"return events with sequence greater than this cursor (0 for the beginning)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum events to return (default 100, max 1000)"`
}

type subscribeResult struct {
	Events []eventDTO `json:"events"`
	Cursor int64      `json:"cursor"`
	Count  int        `json:"count"`
}

// --- handlers ---
//
// Each handler returns a concrete structured output type. The MCP output schema
// is generated from that same type, so the value we return always satisfies it.
// The human-readable JSON text is attached as content for clients that show it.

func joinHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, joinArgs) (*mcp.CallToolResult, memberDTO, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a joinArgs) (*mcp.CallToolResult, memberDTO, error) {
		card, err := toRawJSON(a.AgentCard)
		if err != nil {
			return fail[memberDTO](fmt.Errorf("%w: agent_card: %v", workspace.ErrInvalidInput, err))
		}
		m, err := svc.Join(ctx, a.Workspace, a.Name, model.Kind(a.Kind), card)
		if err != nil {
			return fail[memberDTO](err)
		}
		return ok(toMemberDTO(m))
	}
}

func presenceHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, presenceArgs) (*mcp.CallToolResult, presenceResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a presenceArgs) (*mcp.CallToolResult, presenceResult, error) {
		members, err := svc.Presence(ctx, a.Workspace)
		if err != nil {
			return fail[presenceResult](err)
		}
		dtos := toMemberDTOs(members)
		return ok(presenceResult{Members: dtos, Count: len(dtos)})
	}
}

func sendMessageHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, sendMessageArgs) (*mcp.CallToolResult, model.Message, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a sendMessageArgs) (*mcp.CallToolResult, model.Message, error) {
		msg, err := svc.SendMessage(ctx, a.Workspace, a.From, a.To, a.Body)
		if err != nil {
			return fail[model.Message](err)
		}
		return ok(msg)
	}
}

func readInboxHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, readInboxArgs) (*mcp.CallToolResult, readInboxResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a readInboxArgs) (*mcp.CallToolResult, readInboxResult, error) {
		msgs, err := svc.ReadInbox(ctx, a.Workspace, a.Member)
		if err != nil {
			return fail[readInboxResult](err)
		}
		return ok(readInboxResult{Messages: msgs, Count: len(msgs)})
	}
}

func broadcastHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, broadcastArgs) (*mcp.CallToolResult, broadcastResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a broadcastArgs) (*mcp.CallToolResult, broadcastResult, error) {
		msg, recipients, err := svc.Broadcast(ctx, a.Workspace, a.From, a.Body)
		if err != nil {
			return fail[broadcastResult](err)
		}
		return ok(broadcastResult{Message: msg, Recipients: recipients})
	}
}

func publishEventHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, publishEventArgs) (*mcp.CallToolResult, eventDTO, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a publishEventArgs) (*mcp.CallToolResult, eventDTO, error) {
		payload, err := toRawJSON(a.Payload)
		if err != nil {
			return fail[eventDTO](fmt.Errorf("%w: payload: %v", workspace.ErrInvalidInput, err))
		}
		e, err := svc.PublishEvent(ctx, a.Workspace, a.Source, a.Type, payload)
		if err != nil {
			return fail[eventDTO](err)
		}
		return ok(toEventDTO(e))
	}
}

func subscribeHandler(svc *workspace.Service) func(context.Context, *mcp.CallToolRequest, subscribeArgs) (*mcp.CallToolResult, subscribeResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, a subscribeArgs) (*mcp.CallToolResult, subscribeResult, error) {
		events, cursor, err := svc.Subscribe(ctx, a.Workspace, a.Member, a.Since, a.Limit)
		if err != nil {
			return fail[subscribeResult](err)
		}
		dtos := toEventDTOs(events)
		return ok(subscribeResult{Events: dtos, Cursor: cursor, Count: len(dtos)})
	}
}

// --- helpers ---

// ok wraps a successful result: structured output plus pretty-printed JSON text.
func ok[T any](v T) (*mcp.CallToolResult, T, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		var zero T
		return nil, zero, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}, v, nil
}

// fail converts service errors into the right kind of MCP response: expected
// client errors (bad input, missing member) become tool errors the agent can
// read and recover from; unexpected errors become protocol errors.
func fail[T any](err error) (*mcp.CallToolResult, T, error) {
	var zero T
	if errors.Is(err, workspace.ErrInvalidInput) || errors.Is(err, store.ErrNotFound) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		}, zero, nil
	}
	return nil, zero, err
}

// toRawJSON canonicalises a decoded JSON value (from a permissive `any` tool
// argument) back into json.RawMessage for the service layer. A nil value (the
// field was omitted) yields a nil RawMessage rather than the literal "null".
func toRawJSON(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}
