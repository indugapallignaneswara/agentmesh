package iam

// Admin console (P5): a self-contained read-only web UI over the fleet —
// registered clients, the audit trail, and active revocations — following the
// internal/dashboard pattern: one embedded HTML page polling one JSON endpoint.
//
// Gating decision: GET /console serves the HTML shell UNCONDITIONALLY. The
// shell is pure markup + JS — it contains no fleet data and no secrets, and it
// must render before a token exists so the operator has somewhere to type the
// admin token. All actual data flows through GET /console/data, which is
// firmly gated by adminAuthed (and therefore disabled entirely when no
// AdminToken is configured — fail closed, like /audit).

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

//go:embed console.html
var consoleHTML []byte

// consoleClient is the SAFE projection of a Client for the console payload.
// It exists so the secret hash can never leak: Client.SecretHash is not
// carried here, by construction, rather than relying on a json:"-" tag.
type consoleClient struct {
	ClientID         string            `json:"client_id"`
	Workspace        string            `json:"workspace"`
	Subject          string            `json:"subject"`
	Kind             string            `json:"kind"`
	AllowedScopes    []string          `json:"allowed_scopes"`
	BudgetDailyBytes int64             `json:"budget_daily_bytes,omitempty"`
	Entitlements     map[string]string `json:"entitlements,omitempty"`
	Disabled         bool              `json:"disabled"`
	CreatedAt        time.Time         `json:"created_at"`
}

// consoleData is the payload GET /console/data returns; the page polls it.
type consoleData struct {
	Clients     []consoleClient `json:"clients"`
	Audit       []AuditEvent    `json:"audit"`
	Revocations []Revocation    `json:"revocations"`
	AsOf        time.Time       `json:"as_of"`
}

// consoleAuditLimit bounds the "recent activity" panel — a console page, not a
// SIEM export (that is /audit's job).
const consoleAuditLimit = 100

// handleConsole serves the embedded console shell. Deliberately ungated: see
// the package comment above — the shell holds no data, and the operator needs
// it rendered to enter the admin token that unlocks /console/data.
func (s *Server) handleConsole(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(consoleHTML)
}

// handleConsoleData assembles the console payload: safe client fields, recent
// audit events (optionally narrowed by ?client_id= / ?type=), and the active
// revocation list. Admin-only.
func (s *Server) handleConsoleData(w http.ResponseWriter, r *http.Request) {
	if !adminAuthed(r, s.cfg.AdminToken) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "admin authorization required"})
		return
	}
	ctx := r.Context()
	now := s.cfg.Now()

	clients, err := s.store.ListClients(ctx, "") // all workspaces: fleet view
	if err != nil {
		s.cfg.Logger.Error("iam: console client list failed", "err", err)
		http.Error(w, "client store unavailable", http.StatusServiceUnavailable)
		return
	}
	// Stable order regardless of store iteration: newest registration first.
	sort.Slice(clients, func(i, j int) bool {
		if !clients[i].CreatedAt.Equal(clients[j].CreatedAt) {
			return clients[i].CreatedAt.After(clients[j].CreatedAt)
		}
		return clients[i].ClientID < clients[j].ClientID
	})
	safe := make([]consoleClient, 0, len(clients))
	for _, c := range clients {
		safe = append(safe, consoleClient{
			ClientID:         c.ClientID,
			Workspace:        c.Workspace,
			Subject:          c.Subject,
			Kind:             c.Kind,
			AllowedScopes:    emptyIfNil(c.AllowedScopes),
			BudgetDailyBytes: c.BudgetDailyBytes,
			Entitlements:     c.Entitlements,
			Disabled:         c.Disabled,
			CreatedAt:        c.CreatedAt,
		})
	}

	filter := AuditFilter{
		ClientID: r.URL.Query().Get("client_id"),
		Type:     r.URL.Query().Get("type"),
		Limit:    consoleAuditLimit,
	}
	audit, err := s.audit.Query(ctx, filter)
	if err != nil {
		s.cfg.Logger.Error("iam: console audit query failed", "err", err)
		http.Error(w, "audit store unavailable", http.StatusServiceUnavailable)
		return
	}
	// Belt-and-braces narrowing: the AuditStore contract applies the filter,
	// but the console must stay correct against any store, so re-check the two
	// fields it forwards. A no-op when the store already filtered.
	audit = narrowAudit(audit, filter.ClientID, filter.Type)

	revocations, err := s.revocations.ListActive(ctx, now)
	if err != nil {
		s.cfg.Logger.Error("iam: console revocation list failed", "err", err)
		http.Error(w, "revocation store unavailable", http.StatusServiceUnavailable)
		return
	}

	// Normalise nil slices to [] so every list field is a JSON array, never null.
	if audit == nil {
		audit = []AuditEvent{}
	}
	if revocations == nil {
		revocations = []Revocation{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(consoleData{
		Clients:     safe,
		Audit:       audit,
		Revocations: revocations,
		AsOf:        now,
	})
}

// narrowAudit keeps only events matching the non-empty clientID/type.
func narrowAudit(events []AuditEvent, clientID, typ string) []AuditEvent {
	if clientID == "" && typ == "" {
		return events
	}
	out := make([]AuditEvent, 0, len(events))
	for _, e := range events {
		if clientID != "" && e.ClientID != clientID {
			continue
		}
		if typ != "" && e.Type != typ {
			continue
		}
		out = append(out, e)
	}
	return out
}

// emptyIfNil normalises a nil string slice to an empty one for JSON.
func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
