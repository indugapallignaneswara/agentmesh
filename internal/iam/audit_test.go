package iam_test

// Audit-trail tests (P5): a behavioural contract run against both AuditStore
// implementations, endpoint tests for the admin GET /audit query + JSONL
// export, and an integration test proving the /token issue path records an
// AuditTokenIssued event.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/iam"
)

// --- store contract, run against mem and (when reachable) pg ---

// auPGStore returns a PGAuditStore when a test database is configured AND
// reachable; otherwise it skips (same posture as rvPGStore / stPGStore).
func auPGStore(t *testing.T) *iam.PGAuditStore {
	t.Helper()
	dsn := os.Getenv("AGENTIAM_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("AGENTMESH_TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("no AGENTIAM_TEST_DATABASE_URL / AGENTMESH_TEST_DATABASE_URL set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := iam.NewPGAuditStore(ctx, dsn)
	if err != nil {
		t.Skipf("test database configured but unreachable: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestAuditStoreContractMem(t *testing.T) {
	auRunContract(t, iam.NewMemAuditStore())
}

func TestAuditStoreContractPG(t *testing.T) {
	auRunContract(t, auPGStore(t))
}

// auJTIs projects a result to its jti sequence for order-sensitive asserts.
func auJTIs(events []iam.AuditEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.JTI
	}
	return out
}

func auAssertJTIs(t *testing.T, got []iam.AuditEvent, want ...string) {
	t.Helper()
	g := auJTIs(got)
	if len(g) != len(want) {
		t.Fatalf("got %d events %v, want %d %v", len(g), g, len(want), want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("events[%d].JTI = %q, want %q (got %v, want %v)", i, g[i], want[i], g, want)
		}
	}
}

// auRunContract is the shared behavioural contract for any AuditStore. All
// identifiers are randomised per run (stUnique) so PG re-runs never collide;
// every query filters on a run-unique field so pre-existing PG rows are
// invisible.
func auRunContract(t *testing.T, store iam.AuditStore) {
	ctx := context.Background()
	base := stUnique(t)
	c1, c2 := "agt-a-"+base, "agt-b-"+base
	ws1, ws2 := "ws-1-"+base, "ws-2-"+base
	// Truncate to microseconds: timestamptz keeps microsecond precision, so
	// round-tripped timestamps compare exactly.
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Second) }

	events := []iam.AuditEvent{
		{TS: at(0), Type: iam.AuditTokenIssued, ClientID: c1, Subject: "alice", Workspace: ws1, Kind: "agent", JTI: "j0-" + base, Scope: "mesh:send", Audience: "https://mesh", Result: "ok", RemoteIP: "10.0.0.1"},
		{TS: at(1), Type: iam.AuditTokenIssued, ClientID: c2, Subject: "bob", Workspace: ws1, JTI: "j1-" + base, Result: "ok"},
		{TS: at(2), Type: iam.AuditTokenExchanged, ClientID: c1, Subject: "alice", Workspace: ws2, JTI: "j2-" + base, Actor: "human@corp", ActorIssuer: "https://idp.corp", DPoPBound: true, Result: "ok"},
		{TS: at(3), Type: iam.AuditTokenRevoked, ClientID: c1, Subject: "alice", Workspace: ws1, JTI: "j3-" + base, Result: "ok"},
		{TS: at(4), Type: iam.AuditTokenDenied, ClientID: c2, Subject: "bob", Workspace: ws2, JTI: "j4-" + base, Result: "denied", Reason: "invalid_scope"},
	}
	for _, e := range events {
		if err := store.Append(ctx, e); err != nil {
			t.Fatalf("Append(%s): %v", e.JTI, err)
		}
	}
	j := func(i int) string { return events[i].JTI }

	t.Run("newest first", func(t *testing.T) {
		got, err := store.Query(ctx, iam.AuditFilter{Workspace: ws1})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		auAssertJTIs(t, got, j(3), j(1), j(0))
	})

	t.Run("round-trips all fields", func(t *testing.T) {
		got, err := store.Query(ctx, iam.AuditFilter{ClientID: c1, Type: iam.AuditTokenExchanged})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d events, want 1", len(got))
		}
		if !got[0].TS.Equal(events[2].TS) {
			t.Errorf("TS = %v, want %v", got[0].TS, events[2].TS)
		}
		got[0].TS = events[2].TS // Equal-but-different-location is fine
		if got[0] != events[2] {
			t.Errorf("event round-trip mismatch:\n got  %+v\n want %+v", got[0], events[2])
		}
	})

	t.Run("filter by client_id", func(t *testing.T) {
		got, err := store.Query(ctx, iam.AuditFilter{ClientID: c1})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		auAssertJTIs(t, got, j(3), j(2), j(0))
	})

	t.Run("filter by type", func(t *testing.T) {
		got, err := store.Query(ctx, iam.AuditFilter{ClientID: c2, Type: iam.AuditTokenDenied})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		auAssertJTIs(t, got, j(4))
	})

	t.Run("filter by subject", func(t *testing.T) {
		got, err := store.Query(ctx, iam.AuditFilter{ClientID: c2, Subject: "bob"})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		auAssertJTIs(t, got, j(4), j(1))
	})

	t.Run("filter by workspace", func(t *testing.T) {
		got, err := store.Query(ctx, iam.AuditFilter{Workspace: ws2})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		auAssertJTIs(t, got, j(4), j(2))
	})

	t.Run("time window inclusive-exclusive", func(t *testing.T) {
		// From = t1 (inclusive), To = t3 (exclusive) over client c1 → only t2.
		got, err := store.Query(ctx, iam.AuditFilter{ClientID: c1, From: at(1), To: at(3)})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		auAssertJTIs(t, got, j(2))
		// From alone is inclusive: c1 events at ts >= t3.
		got, err = store.Query(ctx, iam.AuditFilter{ClientID: c1, From: at(3)})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		auAssertJTIs(t, got, j(3))
	})

	t.Run("limit truncates newest first", func(t *testing.T) {
		got, err := store.Query(ctx, iam.AuditFilter{Workspace: ws1, Limit: 2})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		auAssertJTIs(t, got, j(3), j(1))
	})

	t.Run("limit clamping", func(t *testing.T) {
		// A dedicated client with more events than DefaultAuditLimit, so both
		// clamps are observable.
		cBig := "agt-big-" + base
		n := iam.DefaultAuditLimit + 10
		for i := 0; i < n; i++ {
			if err := store.Append(ctx, iam.AuditEvent{
				TS: at(10 + i), Type: iam.AuditTokenIssued, ClientID: cBig, Result: "ok",
			}); err != nil {
				t.Fatalf("Append: %v", err)
			}
		}
		// Limit 0 → DefaultAuditLimit.
		got, err := store.Query(ctx, iam.AuditFilter{ClientID: cBig})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(got) != iam.DefaultAuditLimit {
			t.Errorf("Limit=0: got %d events, want DefaultAuditLimit=%d", len(got), iam.DefaultAuditLimit)
		}
		// Limit above the cap clamps to MaxAuditLimit (here: no error, all rows).
		got, err = store.Query(ctx, iam.AuditFilter{ClientID: cBig, Limit: iam.MaxAuditLimit + 1})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(got) != n {
			t.Errorf("Limit>max: got %d events, want %d", len(got), n)
		}
	})

	t.Run("empty result is empty slice", func(t *testing.T) {
		got, err := store.Query(ctx, iam.AuditFilter{ClientID: "agt-nobody-" + base})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if got == nil {
			t.Error("Query returned nil, want empty slice")
		}
		if len(got) != 0 {
			t.Errorf("got %d events, want 0", len(got))
		}
	})
}

// --- endpoint: GET /audit ---

const auAdminToken = "test-admin-token"

// auFixture is a running server with the audit surfaces enabled.
type auFixture struct {
	ts    *httptest.Server
	store iam.Store
	audit *iam.MemAuditStore
}

// auNew stands up a Server with an AdminToken and a mem audit store the test
// can seed directly (same swap-in construction as srvNew in server_test.go).
func auNew(t *testing.T) *auFixture {
	t.Helper()
	key, err := iam.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	store := iam.NewMemStore()
	audit := iam.NewMemAuditStore()

	var srv *iam.Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	srv, err = iam.NewServer(iam.Config{
		Issuer:     ts.URL,
		Audit:      audit,
		AdminToken: auAdminToken,
	}, iam.NewKeySet(key), store)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return &auFixture{ts: ts, store: store, audit: audit}
}

// auGet GETs /audit with the given bearer token ("" = no Authorization).
func auGet(t *testing.T, f *auFixture, token, rawQuery string) *http.Response {
	t.Helper()
	u := f.ts.URL + "/audit"
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /audit: %v", err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func auSeed(t *testing.T, f *auFixture, n int, mutate func(int, *iam.AuditEvent)) {
	t.Helper()
	base := time.Now().UTC().Add(-time.Minute)
	for i := 0; i < n; i++ {
		e := iam.AuditEvent{
			TS: base.Add(time.Duration(i) * time.Second), Type: iam.AuditTokenIssued,
			ClientID: "agt_seed", JTI: "seed-" + string(rune('a'+i)), Result: "ok",
		}
		if mutate != nil {
			mutate(i, &e)
		}
		if err := f.audit.Append(context.Background(), e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

func TestAuditEndpointAuth(t *testing.T) {
	f := auNew(t)
	if res := auGet(t, f, "", ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", res.StatusCode)
	}
	if res := auGet(t, f, "wrong-token", ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", res.StatusCode)
	}
}

func TestAuditEndpointDisabledWithoutAdminToken(t *testing.T) {
	// Empty AdminToken must fail closed even with the right (empty) guess.
	key, err := iam.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	srv, err := iam.NewServer(iam.Config{Issuer: "https://iam.test"}, iam.NewKeySet(key), iam.NewMemStore())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/audit", nil)
	req.Header.Set("Authorization", "Bearer ")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /audit: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (admin surfaces disabled)", res.StatusCode)
	}
}

func TestAuditEndpointJSON(t *testing.T) {
	f := auNew(t)
	auSeed(t, f, 3, nil)

	res := auGet(t, f, auAdminToken, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var body struct {
		Events []iam.AuditEvent `json:"events"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Events) != 3 {
		t.Fatalf("got %d events, want 3", len(body.Events))
	}
	// Newest first: the last-seeded event leads.
	if body.Events[0].JTI != "seed-c" {
		t.Errorf("events[0].JTI = %q, want seed-c", body.Events[0].JTI)
	}
}

func TestAuditEndpointJSONL(t *testing.T) {
	f := auNew(t)
	auSeed(t, f, 4, nil)

	res := auGet(t, f, auAdminToken, "format=jsonl")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}
	var lines []iam.AuditEvent
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var e iam.AuditEvent
		if err := json.Unmarshal([]byte(sc.Text()), &e); err != nil {
			t.Fatalf("line %d is not a JSON object: %v (%q)", len(lines)+1, err, sc.Text())
		}
		lines = append(lines, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(lines) != 4 {
		t.Fatalf("got %d jsonl lines, want 4 (one per event)", len(lines))
	}
	if lines[0].JTI != "seed-d" {
		t.Errorf("first line JTI = %q, want seed-d (newest first)", lines[0].JTI)
	}
}

func TestAuditEndpointFilterNarrows(t *testing.T) {
	f := auNew(t)
	auSeed(t, f, 4, func(i int, e *iam.AuditEvent) {
		if i%2 == 0 {
			e.ClientID = "agt_even"
		}
	})

	res := auGet(t, f, auAdminToken, "client_id=agt_even")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var body struct {
		Events []iam.AuditEvent `json:"events"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Events) != 2 {
		t.Fatalf("got %d events, want 2 (filter did not narrow)", len(body.Events))
	}
	for _, e := range body.Events {
		if e.ClientID != "agt_even" {
			t.Errorf("event %q has client_id %q, want agt_even", e.JTI, e.ClientID)
		}
	}
}

func TestAuditEndpointBadParams(t *testing.T) {
	f := auNew(t)
	for _, q := range []string{
		"from=yesterday",
		"to=2026-13-99",
		"limit=abc",
		"limit=-5",
		"format=xml",
	} {
		if res := auGet(t, f, auAdminToken, q); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, res.StatusCode)
		}
	}
}

// --- integration: issuing a token writes an AuditTokenIssued event ---

func TestAuditRecordsTokenIssued(t *testing.T) {
	f := auNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{
		Workspace:     "team",
		Subject:       "deployer",
		Kind:          "agent",
		AllowedScopes: []string{"mesh:send"},
	})

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {secret},
		"resource":      {"https://mesh.example/mcp"},
		"scope":         {"mesh:send"},
	}
	res, err := f.ts.Client().PostForm(f.ts.URL+"/token", form)
	if err != nil {
		t.Fatalf("POST /token: %v", err)
	}
	defer res.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&tok); err != nil {
		t.Fatalf("decode /token: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	// Pull the jti out of the issued JWT's payload.
	parts := strings.Split(tok.AccessToken, ".")
	if len(parts) != 3 {
		t.Fatalf("access_token is not a JWT: %q", tok.AccessToken)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims struct {
		JTI string `json:"jti"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if claims.JTI == "" {
		t.Fatal("issued token has no jti")
	}

	// The lead's Append hook must have recorded the issue.
	events, err := f.audit.Query(context.Background(), iam.AuditFilter{
		ClientID: id, Type: iam.AuditTokenIssued,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d token.issued events for client, want 1", len(events))
	}
	e := events[0]
	if e.JTI != claims.JTI {
		t.Errorf("audit JTI = %q, want the issued token's jti %q", e.JTI, claims.JTI)
	}
	if e.ClientID != id || e.Subject != "deployer" || e.Workspace != "team" || e.Result != "ok" {
		t.Errorf("audit event fields off: %+v", e)
	}
}
