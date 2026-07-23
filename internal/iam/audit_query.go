package iam

// GET /audit — the admin query and SIEM-export endpoint (P5). Admin-only: it
// exposes the whole fleet's activity, so it is gated by the configured admin
// token (empty token = surface disabled, fail closed — see adminAuthed).
//
// Two output shapes from the same query:
//   - format=json (default): {"events":[...]} for the console and ad-hoc use.
//   - format=jsonl: application/x-ndjson, one AuditEvent object per line —
//     the SIEM export a log shipper tails.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !adminAuthed(r, s.cfg.AdminToken) {
		http.Error(w, "admin authorization required", http.StatusUnauthorized)
		return
	}

	q := r.URL.Query()
	f := AuditFilter{
		ClientID:  q.Get("client_id"),
		Subject:   q.Get("subject"),
		Workspace: q.Get("workspace"),
		Type:      q.Get("type"),
	}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "invalid 'from': want RFC3339", http.StatusBadRequest)
			return
		}
		f.From = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "invalid 'to': want RFC3339", http.StatusBadRequest)
			return
		}
		f.To = t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "invalid 'limit': want a non-negative integer", http.StatusBadRequest)
			return
		}
		f.Limit = n
	}
	format := q.Get("format")
	switch format {
	case "", "json":
		format = "json"
	case "jsonl":
	default:
		http.Error(w, "invalid 'format': want json or jsonl", http.StatusBadRequest)
		return
	}

	events, err := s.audit.Query(r.Context(), f)
	if err != nil {
		s.cfg.Logger.Error("iam: audit query failed", "err", err)
		http.Error(w, "audit store unavailable", http.StatusServiceUnavailable)
		return
	}
	if events == nil {
		events = []AuditEvent{}
	}

	w.Header().Set("Cache-Control", "no-store")
	if format == "jsonl" {
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w) // Encode terminates each object with \n
		for _, e := range events {
			if err := enc.Encode(e); err != nil {
				return // client gone mid-stream; nothing sensible to write
			}
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Events []AuditEvent `json:"events"`
	}{Events: events})
}
