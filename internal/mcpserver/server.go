// Package mcpserver exposes the coordination workspace as a Model Context
// Protocol server over the Streamable-HTTP transport, so any MCP-capable agent
// (Claude Code, Codex, Cursor, …) can register one endpoint and reach the
// shared workspace.
package mcpserver

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// NewServer builds an MCP server with all coordination tools registered.
func NewServer(svc *workspace.Service, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "agentmesh",
		Version: version,
	}, nil)
	registerTools(s, svc)
	return s
}

// Handler returns an http.Handler serving the MCP server over Streamable HTTP.
// Tools are stateless, so a single server instance is shared across sessions.
func Handler(svc *workspace.Service, version string) http.Handler {
	srv := NewServer(svc, version)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)
}
