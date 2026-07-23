package iam_test

// Console tests (P5): the shell is public, the data endpoint is admin-gated,
// the payload carries clients + audit + revocations, honours filters, and —
// critically — never leaks a client secret hash.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/iam"
)

const conAdminToken = "console-admin-token-for-tests"

// conFixture is a console-focused server: AdminToken set, and direct handles
// on the audit + revocation stores so tests can seed them without depending on
// the token-endpoint flows.
type conFixture struct {
	ts    *httptest.Server
	store iam.Store
	audit *iam.MemAuditStore
	rev   iam.RevocationStore
}

func conNew(t *testing.T) *conFixture {
	t.Helper()
	key, err := iam.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	store := iam.NewMemStore()
	audit := iam.NewMemAuditStore()
	rev := iam.NewMemRevocationStore()

	var srv *iam.Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	srv, err = iam.NewServer(iam.Config{
		Issuer:      ts.URL,
		AdminToken:  conAdminToken,
		Audit:       audit,
		Revocations: rev,
	}, iam.NewKeySet(key), store)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return &conFixture{ts: ts, store: store, audit: audit, rev: rev}
}

// conGet performs a GET with an optional bearer token and returns status+body.
func conGet(t *testing.T, f *conFixture, path, token string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.ts.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return res.StatusCode, string(body), res.Header
}

func TestConsoleShellIsPublic(t *testing.T) {
	f := conNew(t)
	status, body, hdr := conGet(t, f, "/console", "")
	if status != http.StatusOK {
		t.Fatalf("GET /console without token: status = %d, want 200 (shell is public)", status)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(body, "Agent-IAM Console") {
		t.Errorf("shell body does not contain the console title")
	}
	// The shell must be inert: no fleet data is ever embedded in it.
	if strings.Contains(body, conAdminToken) {
		t.Errorf("shell body leaks the admin token")
	}
}

func TestConsoleDataGatedAndComplete(t *testing.T) {
	f := conNew(t)
	ctx := context.Background()

	clientID, secret := srvRegister(t, f.store, iam.Client{
		Workspace: "ops", Subject: "deploy-bot", Kind: "agent",
		AllowedScopes:    []string{"read", "write"},
		BudgetDailyBytes: 1 << 20,
		Entitlements:     map[string]string{"tier": "gold"},
	})
	secretHash := iam.HashSecret(secret)

	if err := f.audit.Append(ctx, iam.AuditEvent{
		TS: time.Now().UTC(), Type: iam.AuditTokenIssued, ClientID: clientID,
		Subject: "deploy-bot", Workspace: "ops", JTI: "jti-issued-1",
		DPoPBound: true, Result: "ok",
	}); err != nil {
		t.Fatalf("audit append: %v", err)
	}
	if err := f.rev.Revoke(ctx, "jti-revoked-1", time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// No token and a wrong token are both 401.
	if status, _, _ := conGet(t, f, "/console/data", ""); status != http.StatusUnauthorized {
		t.Fatalf("GET /console/data without token: status = %d, want 401", status)
	}
	if status, _, _ := conGet(t, f, "/console/data", "wrong-token"); status != http.StatusUnauthorized {
		t.Fatalf("GET /console/data with wrong token: status = %d, want 401", status)
	}

	status, body, hdr := conGet(t, f, "/console/data", conAdminToken)
	if status != http.StatusOK {
		t.Fatalf("GET /console/data with admin token: status = %d, want 200 (body %s)", status, body)
	}
	if cc := hdr.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	var payload struct {
		Clients []struct {
			ClientID      string            `json:"client_id"`
			Workspace     string            `json:"workspace"`
			AllowedScopes []string          `json:"allowed_scopes"`
			Entitlements  map[string]string `json:"entitlements"`
		} `json:"clients"`
		Audit       []iam.AuditEvent `json:"audit"`
		Revocations []iam.Revocation `json:"revocations"`
		AsOf        time.Time        `json:"as_of"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Clients) != 1 || payload.Clients[0].ClientID != clientID {
		t.Errorf("clients = %+v, want the one registered client %s", payload.Clients, clientID)
	}
	if payload.Clients[0].Entitlements["tier"] != "gold" {
		t.Errorf("entitlements not carried: %+v", payload.Clients[0].Entitlements)
	}
	if len(payload.Audit) != 1 || payload.Audit[0].JTI != "jti-issued-1" {
		t.Errorf("audit = %+v, want the one appended event", payload.Audit)
	}
	if len(payload.Revocations) != 1 || payload.Revocations[0].JTI != "jti-revoked-1" {
		t.Errorf("revocations = %+v, want the one revoked jti", payload.Revocations)
	}
	if payload.AsOf.IsZero() {
		t.Errorf("as_of is zero")
	}

	// THE critical leak test: neither the secret-hash field name nor the hash
	// value itself may appear anywhere in the console payload.
	for _, needle := range []string{"secret_hash", "SecretHash", "secretHash", secretHash, secret} {
		if strings.Contains(body, needle) {
			t.Errorf("console payload leaks %q", needle)
		}
	}
}

func TestConsoleDataFilters(t *testing.T) {
	f := conNew(t)
	ctx := context.Background()
	now := time.Now().UTC()

	events := []iam.AuditEvent{
		{TS: now, Type: iam.AuditTokenIssued, ClientID: "agt_a", JTI: "j1", Result: "ok"},
		{TS: now, Type: iam.AuditTokenDenied, ClientID: "agt_a", JTI: "j2", Result: "denied", Reason: "bad scope"},
		{TS: now, Type: iam.AuditTokenIssued, ClientID: "agt_b", JTI: "j3", Result: "ok"},
	}
	for _, e := range events {
		if err := f.audit.Append(ctx, e); err != nil {
			t.Fatalf("audit append: %v", err)
		}
	}

	decode := func(body string) []iam.AuditEvent {
		var p struct {
			Audit []iam.AuditEvent `json:"audit"`
		}
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		return p.Audit
	}

	// type filter narrows to issued events only.
	status, body, _ := conGet(t, f, "/console/data?type=token.issued", conAdminToken)
	if status != http.StatusOK {
		t.Fatalf("filtered query status = %d, want 200", status)
	}
	got := decode(body)
	if len(got) != 2 {
		t.Fatalf("?type=token.issued returned %d events, want 2: %+v", len(got), got)
	}
	for _, e := range got {
		if e.Type != iam.AuditTokenIssued {
			t.Errorf("filtered result contains type %q", e.Type)
		}
	}

	// client_id filter combines with type.
	status, body, _ = conGet(t, f, "/console/data?type=token.issued&client_id=agt_b", conAdminToken)
	if status != http.StatusOK {
		t.Fatalf("filtered query status = %d, want 200", status)
	}
	got = decode(body)
	if len(got) != 1 || got[0].JTI != "j3" {
		t.Errorf("?type&client_id returned %+v, want just j3", got)
	}
}

func TestConsoleDataEmptyArraysNotNull(t *testing.T) {
	f := conNew(t)
	status, body, _ := conGet(t, f, "/console/data", conAdminToken)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	for _, want := range []string{`"clients":[]`, `"audit":[]`, `"revocations":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("empty payload missing %s (nil slice leaked as null): %s", want, body)
		}
	}
}
