package iam

// Audit trail (P5): every security-relevant decision at the authorization
// server — a token issued, delegated, revoked, or denied — is recorded as a
// structured AuditEvent, not just an slog line. The trail answers "which
// credential got what authority, when, on whose behalf, and what was refused",
// and it is the feed a SIEM ingests. The admin console reads the same store.
//
// SPINE NOTE: AuditEvent, AuditStore, AuditFilter, the event-type constants,
// and adminAuthed below are locked. The store implementations, the /audit
// query+export handler, and the console are filled in by the P5 build.

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

// Audit event types.
const (
	AuditTokenIssued    = "token.issued"    // client_credentials grant
	AuditTokenExchanged = "token.exchanged" // RFC 8693 delegation grant
	AuditTokenRevoked   = "token.revoked"   // RFC 7009 revocation
	AuditTokenDenied    = "token.denied"    // a grant/exchange/revoke refused
)

// AuditEvent is one recorded decision. Fields are populated best-effort per
// event type; empty fields simply don't apply. It never carries secrets or a
// full token — only the jti and the metadata a reviewer or SIEM needs.
type AuditEvent struct {
	TS        time.Time `json:"ts"`
	Type      string    `json:"type"`
	ClientID  string    `json:"client_id,omitempty"`
	Subject   string    `json:"subject,omitempty"`
	Workspace string    `json:"workspace,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	JTI       string    `json:"jti,omitempty"`
	Scope     string    `json:"scope,omitempty"`
	Audience  string    `json:"audience,omitempty"`
	// Actor / ActorIssuer name the delegating human on an exchanged token
	// (the RFC 8693 act claim).
	Actor       string `json:"actor,omitempty"`
	ActorIssuer string `json:"actor_issuer,omitempty"`
	DPoPBound   bool   `json:"dpop_bound,omitempty"`
	// Result is "ok" or "denied"; Reason explains a denial.
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
	// RemoteIP is the caller's address (best-effort, honours X-Forwarded-For).
	RemoteIP string `json:"remote_ip,omitempty"`
}

// AuditFilter narrows an audit query. Zero-value fields are ignored; From/To
// bound the time range; Limit caps the result (0 → a sensible default).
type AuditFilter struct {
	ClientID  string
	Subject   string
	Workspace string
	Type      string
	From      time.Time
	To        time.Time
	Limit     int
}

// AuditStore persists the audit trail. Two implementations (memory + Postgres)
// behind one contract, like every store in the project. Appends are best-effort
// from the caller's view (a full trail must never block issuing a token), but
// the store itself is durable.
type AuditStore interface {
	// Append records one event. Newest-first ordering is the store's job.
	Append(ctx context.Context, e AuditEvent) error
	// Query returns events matching the filter, newest first.
	Query(ctx context.Context, f AuditFilter) ([]AuditEvent, error)
}

// DefaultAuditLimit bounds a query that sets no Limit.
const DefaultAuditLimit = 200

// MaxAuditLimit is the hard cap on a single query/export page.
const MaxAuditLimit = 5000

// adminAuthed reports whether a request carries the admin bearer token. The
// audit query/export and the console are admin-only: they expose the whole
// fleet's activity. An empty configured token means admin surfaces are
// DISABLED (fail closed), never open.
func adminAuthed(r *http.Request, adminToken string) bool {
	if adminToken == "" {
		return false
	}
	got := bearerToken(r)
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(adminToken)) == 1
}

// bearerToken extracts a Bearer token from the Authorization header.
func bearerToken(r *http.Request) string {
	const p = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):]
	}
	return ""
}

// clientIP returns the best-effort caller IP for an audit record.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}
	return r.RemoteAddr
}
