// Package client is a thin Go wrapper over the AgentMesh MCP server's
// Streamable-HTTP endpoint. It powers the `coord` CLI (and, through it, session
// hooks) so non-MCP-native agents — and shell scripts — can reach the shared
// workspace. Each method opens a session, calls one tool, and returns the
// tool's structured JSON result.
package client

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Client connects to an AgentMesh MCP endpoint.
type Client struct {
	endpoint string
	impl     *mcp.Implementation
}

// New returns a Client targeting the given MCP endpoint URL (e.g.
// http://localhost:8080/mcp).
func New(endpoint string) *Client {
	return &Client{
		endpoint: endpoint,
		impl:     &mcp.Implementation{Name: "coord-cli", Version: "0.1.0"},
	}
}

// session dials the endpoint and completes the MCP handshake. The caller must
// Close the returned session. Standalone SSE is disabled: the CLI is strictly
// request/response and does not need server-initiated messages.
func (c *Client) session(ctx context.Context) (*mcp.ClientSession, error) {
	transport := &mcp.StreamableClientTransport{
		Endpoint:             c.endpoint,
		DisableStandaloneSSE: true,
	}
	cs, err := mcp.NewClient(c.impl, nil).Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", c.endpoint, err)
	}
	return cs, nil
}

// call invokes one tool and decodes its JSON text result into out (if non-nil).
// A tool-level error (IsError) is returned as a Go error carrying the server's
// message, so callers and the CLI surface the same text the agent would see.
func (c *Client) call(ctx context.Context, name string, args map[string]any, out any) error {
	cs, err := c.session(ctx)
	if err != nil {
		return err
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return fmt.Errorf("call %s: %w", name, err)
	}
	text := firstText(res)
	if res.IsError {
		if text == "" {
			text = "tool reported an error"
		}
		return fmt.Errorf("%s: %s", name, text)
	}
	if out != nil && text != "" {
		if err := json.Unmarshal([]byte(text), out); err != nil {
			return fmt.Errorf("decode %s result: %w", name, err)
		}
	}
	return nil
}

// firstText returns the first text content block of a tool result, or "".
func firstText(res *mcp.CallToolResult) string {
	for _, content := range res.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// Raw invokes a tool and returns its raw JSON text result, for callers (the CLI
// in JSON mode) that want to pass server output through verbatim.
func (c *Client) Raw(ctx context.Context, name string, args map[string]any) (string, error) {
	cs, err := c.session(ctx)
	if err != nil {
		return "", err
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("call %s: %w", name, err)
	}
	text := firstText(res)
	if res.IsError {
		if text == "" {
			text = "tool reported an error"
		}
		return "", fmt.Errorf("%s: %s", name, text)
	}
	return text, nil
}
