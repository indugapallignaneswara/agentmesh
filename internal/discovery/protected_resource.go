package discovery

import (
	"encoding/json"
	"net/http"
)

// ProtectedResourcePath is where an OAuth 2.1 resource server publishes its
// metadata (RFC 9728). The MCP authorization spec makes this MUST-level: a
// protected MCP server publishes it, and clients use it to discover which
// authorization server to get a token from. The 401 challenge points here.
const ProtectedResourcePath = "/.well-known/oauth-protected-resource"

// ProtectedResource is the RFC 9728 metadata document.
type ProtectedResource struct {
	// Resource is this server's canonical URI — the value clients must request
	// tokens for (RFC 8707 resource indicator) and that we validate as `aud`.
	Resource string `json:"resource"`
	// AuthorizationServers lists the issuers whose tokens we accept.
	AuthorizationServers []string `json:"authorization_servers"`
	// BearerMethodsSupported: we only accept the Authorization header.
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceName           string   `json:"resource_name,omitempty"`
	ResourceDocumentation  string   `json:"resource_documentation,omitempty"`
}

// ProtectedResourceHandler serves the RFC 9728 document. It is unauthenticated
// by definition: it is how a client learns where to authenticate.
func ProtectedResourceHandler(resource, issuer string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		doc := ProtectedResource{
			Resource:               resource,
			AuthorizationServers:   []string{issuer},
			BearerMethodsSupported: []string{"header"},
			ResourceName:           "AgentMesh",
			ResourceDocumentation:  "https://github.com/indugapallignaneswara/agentmesh",
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_ = json.NewEncoder(w).Encode(doc)
	})
}
