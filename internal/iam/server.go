package iam

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Config configures the authorization server.
type Config struct {
	// Issuer is this server's public URL; it becomes the token `iss` and the
	// resource server must be configured with the same value.
	Issuer string
	// DefaultTTL is the access-token lifetime when a client sets none.
	DefaultTTL time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time
	// Logger is optional.
	Logger *slog.Logger
}

// Server is the OAuth 2.1 authorization server for agents. It authenticates
// registered clients and issues short-lived RS256 access tokens that the
// AgentMesh resource server validates unchanged.
type Server struct {
	cfg   Config
	keys  *KeySet
	store Store
}

// NewServer builds an authorization server. Issuer and a key set are required.
func NewServer(cfg Config, keys *KeySet, store Store) (*Server, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("iam: issuer is required")
	}
	if keys == nil || keys.Active() == nil {
		return nil, errors.New("iam: a signing key is required")
	}
	if store == nil {
		return nil, errors.New("iam: a client store is required")
	}
	if cfg.DefaultTTL <= 0 {
		cfg.DefaultTTL = 15 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Server{cfg: cfg, keys: keys, store: store}, nil
}

// Handler returns the HTTP handler exposing /token, the JWKS, discovery
// metadata, and liveness.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", s.handleToken)
	mux.HandleFunc("GET /.well-known/jwks.json", s.keys.JWKSHandler())
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleMetadata)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// tokenResponse is the RFC 6749 §5.1 success body.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
}

// handleToken implements the token endpoint. Only grant_type=client_credentials
// is supported today; token-exchange (delegation) is the planned second grant.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}
	grant := r.PostForm.Get("grant_type")
	if grant != "client_credentials" {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"only client_credentials is supported")
		return
	}
	s.clientCredentials(w, r)
}

func (s *Server) clientCredentials(w http.ResponseWriter, r *http.Request) {
	clientID, secret, ok := clientAuth(r)
	if !ok || clientID == "" || secret == "" {
		// RFC 6749 §5.2: when the client attempted Basic auth, answer 401 with a
		// challenge; otherwise 400. Either way, invalid_client.
		writeInvalidClient(w, r)
		return
	}

	c, err := s.store.GetClient(r.Context(), clientID)
	if err != nil {
		if errors.Is(err, ErrClientNotFound) {
			// Burn the same hash-compare work as the found path so an unknown
			// client_id is not distinguishable from a wrong secret by timing.
			verifySecret(secret, unknownClientHash)
			writeInvalidClient(w, r)
			return
		}
		s.cfg.Logger.Error("iam: client lookup failed", "err", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "client lookup failed")
		return
	}
	// Constant-time secret check regardless of disabled state, so a disabled
	// client with a wrong secret is indistinguishable from a wrong secret.
	secretOK := verifySecret(secret, c.SecretHash)
	if !secretOK || c.Disabled {
		writeInvalidClient(w, r)
		return
	}

	resource := r.PostForm.Get("resource")
	if resource == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target",
			"the 'resource' parameter (target audience) is required")
		return
	}

	scopes, err := c.grantScopes(r.PostForm.Get("scope"))
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}

	ttl := c.TokenTTL
	if ttl <= 0 {
		ttl = s.cfg.DefaultTTL
	}
	now := s.cfg.Now()
	jti, err := newJTI()
	if err != nil {
		s.cfg.Logger.Error("iam: jti generation failed", "err", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "token id generation failed")
		return
	}
	kind := c.Kind
	if kind == "" {
		kind = "agent"
	}
	claims := Claims{
		Issuer:           s.cfg.Issuer,
		Subject:          c.Subject,
		ClientID:         c.ClientID,
		Audience:         resource,
		Workspace:        c.Workspace,
		Kind:             kind,
		Scope:            strings.Join(scopes, " "),
		BudgetDailyBytes: c.BudgetDailyBytes,
		IssuedAt:         now.Unix(),
		NotBefore:        now.Unix(),
		Expiry:           now.Add(ttl).Unix(),
		JTI:              jti,
	}
	token, err := s.keys.Sign(claims)
	if err != nil {
		s.cfg.Logger.Error("iam: token signing failed", "err", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "token signing failed")
		return
	}

	s.cfg.Logger.Info("iam: issued token",
		"client", clientID, "sub", c.Subject, "workspace", c.Workspace,
		"aud", resource, "scope", claims.Scope, "ttl", ttl.String())

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int64(ttl.Seconds()),
		Scope:       claims.Scope,
	})
}

// unknownClientHash is a fixed dummy hash compared against when the client id
// is unknown, equalising the timing of the two rejection paths.
var unknownClientHash = HashSecret("agentiam-unknown-client-timing-pad")

// clientAuth extracts client credentials from either HTTP Basic auth (preferred,
// RFC 6749 §2.3.1) or the request body.
func clientAuth(r *http.Request) (id, secret string, ok bool) {
	if u, p, has := r.BasicAuth(); has {
		return u, p, true
	}
	id = r.PostForm.Get("client_id")
	secret = r.PostForm.Get("client_secret")
	return id, secret, id != "" || secret != ""
}

// authServerMetadata is the RFC 8414 discovery document (the subset that
// matters for a client-credentials-only server).
type authServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	JWKSURI                           string   `json:"jwks_uri"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
}

func (s *Server) handleMetadata(w http.ResponseWriter, _ *http.Request) {
	base := strings.TrimRight(s.cfg.Issuer, "/")
	md := authServerMetadata{
		Issuer:                            s.cfg.Issuer,
		TokenEndpoint:                     base + "/token",
		JWKSURI:                           base + "/.well-known/jwks.json",
		GrantTypesSupported:               []string{"client_credentials"},
		TokenEndpointAuthMethodsSupported: []string{"client_secret_basic", "client_secret_post"},
		// OAuth 2.1 removes the implicit grant; this server issues tokens only
		// via the token endpoint, so no authorization-endpoint response types.
		ResponseTypesSupported: []string{},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(md)
}

// --- RFC 6749 §5.2 error rendering ---

type oauthError struct {
	Err  string `json:"error"`
	Desc string `json:"error_description,omitempty"`
}

func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(oauthError{Err: code, Desc: desc})
}

// writeInvalidClient answers a failed client authentication. Per RFC 6749 §5.2,
// if the client used the Authorization header we return 401 with a challenge;
// otherwise 400.
func writeInvalidClient(w http.ResponseWriter, r *http.Request) {
	if _, _, has := r.BasicAuth(); has {
		w.Header().Set("WWW-Authenticate", `Basic realm="agent-iam"`)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}
	writeOAuthError(w, http.StatusBadRequest, "invalid_client", "client authentication failed")
}

// RegisterClient is a convenience used by the admin CLI and tests: it mints
// credentials, stores the client, and returns the client id and the one-time
// secret.
func RegisterClient(ctx context.Context, store Store, c Client) (clientID, secret string, err error) {
	clientID, secret, hash, err := GenerateClientCredentials()
	if err != nil {
		return "", "", err
	}
	c.ClientID = clientID
	c.SecretHash = hash
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if c.Kind == "" {
		c.Kind = "agent"
	}
	if err := store.CreateClient(ctx, c); err != nil {
		return "", "", err
	}
	return clientID, secret, nil
}
