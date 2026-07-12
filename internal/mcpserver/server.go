// Package mcpserver exposes the coordination workspace as a Model Context
// Protocol server over the Streamable-HTTP transport, so any MCP-capable agent
// (Claude Code, Codex, Cursor, …) can register one endpoint and reach the
// shared workspace.
package mcpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/metrics"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// NewServer builds an MCP server with all coordination tools registered.
func NewServer(svc *workspace.Service, version string) *mcp.Server {
	return NewServerWithMetrics(svc, version, nil)
}

// NewServerWithMetrics builds the MCP server and, when reg is non-nil, records
// per-tool call counts, error counts and latency through the SDK's receiving
// middleware — one hook that covers every registered tool.
func NewServerWithMetrics(svc *workspace.Service, version string, reg *metrics.Registry) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "agentmesh",
		Version: version,
	}, nil)
	registerTools(s, svc)
	if reg != nil {
		s.AddReceivingMiddleware(toolMetrics(reg))
	}
	return s
}

// toolMetrics times tools/call invocations. A tool-level error result (IsError)
// counts as an error, as does a protocol error — both are failures from the
// caller's point of view.
func toolMetrics(reg *metrics.Registry) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			call, ok := req.(*mcp.CallToolRequest)
			if !ok || method != "tools/call" {
				return next(ctx, method, req)
			}
			start := time.Now()
			res, err := next(ctx, method, req)
			isErr := err != nil
			if ctr, ok := res.(*mcp.CallToolResult); ok && ctr != nil && ctr.IsError {
				isErr = true
			}
			name := "unknown"
			if call.Params != nil {
				name = call.Params.Name
			}
			reg.ObserveTool(name, time.Since(start), isErr)
			return res, err
		}
	}
}

// Handler returns an http.Handler serving the MCP server over Streamable HTTP.
// Tools are stateless, so a single server instance is shared across sessions.
func Handler(svc *workspace.Service, version string) http.Handler {
	return HandlerWithMetrics(svc, version, nil)
}

// HandlerWithMetrics is Handler with tool metrics recorded into reg.
func HandlerWithMetrics(svc *workspace.Service, version string, reg *metrics.Registry) http.Handler {
	srv := NewServerWithMetrics(svc, version, reg)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)
}
