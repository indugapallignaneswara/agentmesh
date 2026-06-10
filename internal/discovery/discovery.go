// Package discovery serves AgentMesh's A2A Agent Card — the discovery
// document other agents and registries use to learn what this server is, where
// to reach it, and how to authenticate. Shape verified against the normative
// A2A v1.0 protobuf (specification/a2a.proto): required AgentCard fields are
// name, description, supportedInterfaces, version, capabilities,
// defaultInputModes, defaultOutputModes and skills; JSON uses camelCase; the
// card is served at /.well-known/agent-card.json (RFC 8615).
//
// AgentMesh's interface speaks MCP, not the A2A message bindings. The proto
// defines protocolBinding as "an open form string, to be easily extended for
// other protocol bindings", so the card advertises the MCP endpoint with
// protocolBinding "MCP" — honest discovery without claiming A2A transport.
package discovery

import (
	"encoding/json"
	"net/http"
)

// Card mirrors the A2A AgentCard JSON shape (camelCase per the proto JSON
// mapping). Only the fields AgentMesh uses are modelled.
type Card struct {
	Name                string                    `json:"name"`
	Description         string                    `json:"description"`
	SupportedInterfaces []Interface               `json:"supportedInterfaces"`
	Version             string                    `json:"version"`
	Capabilities        Capabilities              `json:"capabilities"`
	SecuritySchemes     map[string]SecurityScheme `json:"securitySchemes,omitempty"`
	DefaultInputModes   []string                  `json:"defaultInputModes"`
	DefaultOutputModes  []string                  `json:"defaultOutputModes"`
	Skills              []Skill                   `json:"skills"`
	DocumentationURL    string                    `json:"documentationUrl,omitempty"`
}

type Interface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	ProtocolVersion string `json:"protocolVersion"`
}

type Capabilities struct {
	Streaming bool `json:"streaming"`
}

// SecurityScheme models the http_auth_security_scheme variant.
type SecurityScheme struct {
	HTTPAuthSecurityScheme *HTTPAuth `json:"httpAuthSecurityScheme,omitempty"`
}

type HTTPAuth struct {
	Description string `json:"description,omitempty"`
	Scheme      string `json:"scheme"`
}

type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// WellKnownPath is where the card is served, per RFC 8615 and the A2A docs.
const WellKnownPath = "/.well-known/agent-card.json"

// Handler serves the agent card. The card is discovery metadata and is served
// without authentication — it is how clients learn the security scheme in the
// first place. The interface URL is derived from the request Host so the card
// is correct behind tunnels and reverse proxies.
func Handler(version, authMode string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		card := Card{
			Name:        "AgentMesh",
			Description: "Self-hosted coordination workspace for AI coding agents: presence, any-to-any messaging, broadcast, an observation log, a dependency-aware task board, review-gated shared memory, and co-edited artifacts — exposed as an MCP server.",
			SupportedInterfaces: []Interface{{
				URL:             scheme + "://" + r.Host + "/mcp",
				ProtocolBinding: "MCP", // open-form binding per the A2A proto; this is an MCP Streamable-HTTP endpoint
				ProtocolVersion: "1.0",
			}},
			Version:            version,
			Capabilities:       Capabilities{Streaming: true},
			DefaultInputModes:  []string{"application/json"},
			DefaultOutputModes: []string{"application/json"},
			Skills: []Skill{
				{ID: "coordination", Name: "Workspace coordination", Description: "Join workspaces, see presence, send direct messages, broadcast, and follow the append-only event log.", Tags: []string{"messaging", "presence", "events"}},
				{ID: "task-board", Name: "Shared task board", Description: "Create dependency-gated tasks and claim them atomically (no double-claim) with leases and work-stealing.", Tags: []string{"tasks", "coordination"}},
				{ID: "shared-memory", Name: "Reviewed shared memory", Description: "Write private or shared knowledge with provenance; shared submissions require human approval before becoming retrievable.", Tags: []string{"memory", "knowledge", "review"}},
				{ID: "artifacts", Name: "Co-edited artifacts", Description: "Read and update shared documents with optimistic concurrency — stale writes are rejected with merge guidance.", Tags: []string{"documents", "collaboration"}},
			},
			DocumentationURL: "https://github.com/indugapallignaneswara/agentmesh",
		}
		if authMode == "token" {
			card.SecuritySchemes = map[string]SecurityScheme{
				"bearer": {HTTPAuthSecurityScheme: &HTTPAuth{
					Scheme:      "Bearer",
					Description: "Opaque bearer token issued by `agentmesh token create`, presented as `Authorization: Bearer <token>`.",
				}},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_ = json.NewEncoder(w).Encode(card)
	})
}
