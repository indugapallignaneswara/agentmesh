package iam

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/dpop"
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
	// SubjectIssuers are the external human IdPs trusted as subject_token
	// sources for the RFC 8693 token-exchange (delegation) grant. Empty means
	// delegation is disabled: every exchange is rejected.
	SubjectIssuers []TrustedIssuer
}

// Server is the OAuth 2.1 authorization server for agents. It authenticates
// registered clients and issues short-lived RS256 access tokens that the
// AgentMesh resource server validates unchanged.
type Server struct {
	cfg   Config
	keys  *KeySet
	store Store
	// trust validates delegation subject tokens against Config.SubjectIssuers.
	// Always non-nil; with no configured issuers it rejects everything.
	trust *TrustRegistry
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
	return &Server{
		cfg:   cfg,
		keys:  keys,
		store: store,
		trust: NewTrustRegistry(cfg.SubjectIssuers, nil),
	}, nil
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

// tokenResponse is the RFC 6749 §5.1 success body. IssuedTokenType is set only
// on token-exchange responses (RFC 8693 §2.2.1, where it is REQUIRED).
type tokenResponse struct {
	AccessToken     string `json:"access_token"`
	IssuedTokenType string `json:"issued_token_type,omitempty"`
	TokenType       string `json:"token_type"`
	ExpiresIn       int64  `json:"expires_in"`
	Scope           string `json:"scope,omitempty"`
}

// RFC 8693 grant and token-type identifiers.
const (
	grantTokenExchange   = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token"
	tokenTypeJWT         = "urn:ietf:params:oauth:token-type:jwt"
)

// handleToken implements the token endpoint: grant_type=client_credentials
// (an agent acting as itself) and RFC 8693 token exchange (an agent acting on
// behalf of a human, proven by a subject_token from a trusted IdP).
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "client_credentials":
		s.clientCredentials(w, r)
	case grantTokenExchange:
		s.tokenExchange(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"only client_credentials and token-exchange are supported")
	}
}

// authenticateClient runs the shared client-authentication flow for every
// grant: extract credentials (Basic or body), look up the client, verify the
// secret in constant time, refuse disabled clients. On failure it writes the
// RFC 6749 §5.2 error and returns ok=false.
func (s *Server) authenticateClient(w http.ResponseWriter, r *http.Request) (Client, bool) {
	clientID, secret, ok := clientAuth(r)
	if !ok || clientID == "" || secret == "" {
		// RFC 6749 §5.2: when the client attempted Basic auth, answer 401 with a
		// challenge; otherwise 400. Either way, invalid_client.
		writeInvalidClient(w, r)
		return Client{}, false
	}

	c, err := s.store.GetClient(r.Context(), clientID)
	if err != nil {
		if errors.Is(err, ErrClientNotFound) {
			// Burn the same hash-compare work as the found path so an unknown
			// client_id is not distinguishable from a wrong secret by timing.
			verifySecret(secret, unknownClientHash)
			writeInvalidClient(w, r)
			return Client{}, false
		}
		s.cfg.Logger.Error("iam: client lookup failed", "err", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "client lookup failed")
		return Client{}, false
	}
	// Constant-time secret check regardless of disabled state, so a disabled
	// client with a wrong secret is indistinguishable from a wrong secret.
	secretOK := verifySecret(secret, c.SecretHash)
	if !secretOK || c.Disabled {
		writeInvalidClient(w, r)
		return Client{}, false
	}
	return c, true
}

// dpopBind implements the RFC 9449 §5 token-endpoint side of DPoP. If the
// request carries a DPoP header, the proof is verified against this endpoint
// (htm=POST, htu=the absolute token-endpoint URL) and the returned Confirmation
// carries the key thumbprint to bind into the token as cnf.jkt, with token_type
// "DPoP". With no DPoP header the request is a plain bearer flow: (nil,
// "Bearer", nil) — fully backward compatible. A present-but-invalid proof is an
// error the caller renders as invalid_dpop_proof (RFC 9449 §5.2, HTTP 400).
func (s *Server) dpopBind(r *http.Request) (*Confirmation, string, error) {
	proofJWT := r.Header.Get("DPoP")
	if proofJWT == "" {
		return nil, "Bearer", nil
	}
	// Absolute token-endpoint URL as the client saw it. r.URL on a server
	// request is path-only, so reconstruct scheme+host: direct TLS or a
	// terminating proxy (X-Forwarded-Proto) means https.
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	htu := scheme + "://" + r.Host + r.URL.Path
	proof, err := dpop.Verify(proofJWT, dpop.Params{
		HTM: http.MethodPost,
		HTU: htu,
		Now: s.cfg.Now,
		// No ExpectedATH: at the token endpoint the access token does not exist
		// yet (RFC 9449 §4.3 note); ath binding happens at the resource server.
	})
	if err != nil {
		return nil, "", err
	}
	return &Confirmation{JKT: proof.JKT}, "DPoP", nil
}

func (s *Server) clientCredentials(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticateClient(w, r)
	if !ok {
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
	cnf, tokenType, err := s.dpopBind(r)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_dpop_proof", "invalid DPoP proof")
		return
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
		Cnf:              cnf,
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

	logArgs := []any{
		"client", c.ClientID, "sub", c.Subject, "workspace", c.Workspace,
		"aud", resource, "scope", claims.Scope, "ttl", ttl.String(),
	}
	if cnf != nil {
		logArgs = append(logArgs, "dpop-bound", true, "jkt", cnf.JKT)
	}
	s.cfg.Logger.Info("iam: issued token", logArgs...)

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tokenResponse{
		AccessToken: token,
		TokenType:   tokenType,
		ExpiresIn:   int64(ttl.Seconds()),
		Scope:       claims.Scope,
	})
}

// tokenExchange is the RFC 8693 token-exchange grant: the delegation path.
// The AGENT authenticates as itself (same client auth as client_credentials)
// and presents a subject_token — a JWT from a trusted human IdP proving which
// human is delegating. The issued token keeps the agent as `sub` and stamps
// the human into the `act` claim (docs/agentiam-standards.md §3), scoped to
// the intersection of what was requested and what the client is allowed, and
// expiring no later than the human's own authorization.
func (s *Server) tokenExchange(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticateClient(w, r)
	if !ok {
		return
	}

	subjectToken := r.PostForm.Get("subject_token")
	if subjectToken == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request",
			"the 'subject_token' parameter is required")
		return
	}
	switch r.PostForm.Get("subject_token_type") {
	case tokenTypeJWT, tokenTypeAccessToken:
	default:
		writeOAuthError(w, http.StatusBadRequest, "invalid_request",
			"subject_token_type must be "+tokenTypeJWT+" or "+tokenTypeAccessToken)
		return
	}
	if rt := r.PostForm.Get("requested_token_type"); rt != "" && rt != tokenTypeAccessToken {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request",
			"only "+tokenTypeAccessToken+" can be issued")
		return
	}
	resource := r.PostForm.Get("resource")
	if resource == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target",
			"the 'resource' parameter (target audience) is required")
		return
	}

	// Scope narrowing: requested ∩ client's allowed — a delegation can never
	// broaden what the agent itself could be granted. Checked before the
	// (network-bound) subject verification: cheap local failures first.
	scopes, err := c.grantScopes(r.PostForm.Get("scope"))
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}

	// Verify the subject token against the trust registry: trusted issuer,
	// signature, expiry. One sentinel error — no oracle about which check failed.
	subject, err := s.trust.Verify(r.Context(), subjectToken)
	if err != nil {
		if errors.Is(err, ErrSubjectRejected) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "subject token rejected")
			return
		}
		s.cfg.Logger.Error("iam: subject token verification failed", "err", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "subject token verification failed")
		return
	}

	ttl := c.TokenTTL
	if ttl <= 0 {
		ttl = s.cfg.DefaultTTL
	}
	now := s.cfg.Now()
	// Expiry rule (standards §3): exp = min(client TTL from now, subject exp).
	// A delegated token must not outlive the human authorization backing it.
	exp := now.Add(ttl)
	if !subject.Expiry.IsZero() && subject.Expiry.Before(exp) {
		exp = subject.Expiry
	}

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
	cnf, tokenType, err := s.dpopBind(r)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_dpop_proof", "invalid DPoP proof")
		return
	}
	claims := Claims{
		Issuer:    s.cfg.Issuer,
		Subject:   c.Subject, // the AGENT does the acting; the human rides in `act`
		ClientID:  c.ClientID,
		Audience:  resource,
		Workspace: c.Workspace,
		Kind:      kind,
		Scope:     strings.Join(scopes, " "),
		Act: &Actor{
			Subject: subject.Subject,
			Issuer:  subject.Issuer,
		},
		Cnf:              cnf,
		BudgetDailyBytes: c.BudgetDailyBytes,
		IssuedAt:         now.Unix(),
		NotBefore:        now.Unix(),
		Expiry:           exp.Unix(),
		JTI:              jti,
	}
	token, err := s.keys.Sign(claims)
	if err != nil {
		s.cfg.Logger.Error("iam: token signing failed", "err", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "token signing failed")
		return
	}

	logArgs := []any{
		"client", c.ClientID, "sub", c.Subject, "act_sub", subject.Subject,
		"act_iss", subject.Issuer, "aud", resource, "scope", claims.Scope,
		"exp", exp.UTC().Format(time.RFC3339), "jti", jti,
	}
	if cnf != nil {
		logArgs = append(logArgs, "dpop-bound", true, "jkt", cnf.JKT)
	}
	s.cfg.Logger.Info("iam: issued delegated token", logArgs...)

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tokenResponse{
		AccessToken:     token,
		IssuedTokenType: tokenTypeAccessToken,
		TokenType:       tokenType,
		ExpiresIn:       int64(exp.Sub(now).Seconds()),
		Scope:           claims.Scope,
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
	// DPoPSigningAlgValuesSupported advertises DPoP support (RFC 9449 §5.1):
	// the JWS algorithms accepted for DPoP proof JWTs at the token endpoint.
	DPoPSigningAlgValuesSupported []string `json:"dpop_signing_alg_values_supported"`
}

func (s *Server) handleMetadata(w http.ResponseWriter, _ *http.Request) {
	base := strings.TrimRight(s.cfg.Issuer, "/")
	md := authServerMetadata{
		Issuer:                            s.cfg.Issuer,
		TokenEndpoint:                     base + "/token",
		JWKSURI:                           base + "/.well-known/jwks.json",
		GrantTypesSupported:               []string{"client_credentials", grantTokenExchange},
		TokenEndpointAuthMethodsSupported: []string{"client_secret_basic", "client_secret_post"},
		// OAuth 2.1 removes the implicit grant; this server issues tokens only
		// via the token endpoint, so no authorization-endpoint response types.
		ResponseTypesSupported:        []string{},
		DPoPSigningAlgValuesSupported: []string{"ES256", "RS256"},
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
