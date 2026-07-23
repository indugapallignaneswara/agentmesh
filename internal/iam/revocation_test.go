package iam_test

// Revocation tests (P3, authorization-server side): a behavioural contract run
// against both RevocationStore implementations, plus endpoint tests for RFC
// 7009 POST /revoke and the GET /revocations poll feed.

import (
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

// rvPGStore returns a PGRevocationStore when a test database is configured AND
// reachable; otherwise it skips (same posture as stPGStore in store_test.go).
func rvPGStore(t *testing.T) *iam.PGRevocationStore {
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
	s, err := iam.NewPGRevocationStore(ctx, dsn)
	if err != nil {
		t.Skipf("test database configured but unreachable: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestRevocationStoreContractMem(t *testing.T) {
	rvRunContract(t, iam.NewMemRevocationStore())
}

func TestRevocationStoreContractPG(t *testing.T) {
	rvRunContract(t, rvPGStore(t))
}

// rvRunContract is the shared behavioural contract for any RevocationStore.
// JTIs are randomised (stUnique, store_test.go) so PG re-runs never collide.
func rvRunContract(t *testing.T, store iam.RevocationStore) {
	ctx := context.Background()
	now := time.Now().UTC()

	t.Run("revoke then is-revoked", func(t *testing.T) {
		jti := "rv-" + stUnique(t)
		if err := store.Revoke(ctx, jti, now.Add(10*time.Minute)); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		revoked, err := store.IsRevoked(ctx, jti, now)
		if err != nil {
			t.Fatalf("IsRevoked: %v", err)
		}
		if !revoked {
			t.Error("IsRevoked = false after Revoke, want true")
		}
	})

	t.Run("unknown jti is not revoked", func(t *testing.T) {
		revoked, err := store.IsRevoked(ctx, "rv-unknown-"+stUnique(t), now)
		if err != nil {
			t.Fatalf("IsRevoked: %v", err)
		}
		if revoked {
			t.Error("IsRevoked = true for a jti never revoked, want false")
		}
	})

	t.Run("expired entry is inert and absent from the feed", func(t *testing.T) {
		jti := "rv-expired-" + stUnique(t)
		exp := now.Add(-1 * time.Minute)
		if err := store.Revoke(ctx, jti, exp); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		revoked, err := store.IsRevoked(ctx, jti, now)
		if err != nil {
			t.Fatalf("IsRevoked: %v", err)
		}
		if revoked {
			t.Error("IsRevoked = true past the entry's exp, want false")
		}
		entries, err := store.ListActive(ctx, now)
		if err != nil {
			t.Fatalf("ListActive: %v", err)
		}
		for _, e := range entries {
			if e.JTI == jti {
				t.Errorf("ListActive contains expired entry %s", jti)
			}
		}
	})

	t.Run("list-active returns active entries", func(t *testing.T) {
		jti := "rv-active-" + stUnique(t)
		exp := now.Add(5 * time.Minute).Truncate(time.Millisecond)
		if err := store.Revoke(ctx, jti, exp); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		entries, err := store.ListActive(ctx, now)
		if err != nil {
			t.Fatalf("ListActive: %v", err)
		}
		found := false
		for _, e := range entries {
			if e.JTI == jti {
				found = true
				if !e.Expiry.Equal(exp) {
					t.Errorf("entry expiry = %v, want %v", e.Expiry, exp)
				}
			}
		}
		if !found {
			t.Errorf("ListActive is missing active entry %s", jti)
		}
	})

	t.Run("double revoke is idempotent", func(t *testing.T) {
		jti := "rv-double-" + stUnique(t)
		exp := now.Add(5 * time.Minute)
		if err := store.Revoke(ctx, jti, exp); err != nil {
			t.Fatalf("first Revoke: %v", err)
		}
		if err := store.Revoke(ctx, jti, exp); err != nil {
			t.Fatalf("second Revoke of the same jti: %v", err)
		}
		revoked, err := store.IsRevoked(ctx, jti, now)
		if err != nil {
			t.Fatalf("IsRevoked: %v", err)
		}
		if !revoked {
			t.Error("IsRevoked = false after double Revoke, want true")
		}
	})
}

// --- endpoint tests ---

// rvFixture is a running server whose revocation store the test can inspect.
type rvFixture struct {
	*srvFixture
	revocations *iam.MemRevocationStore
}

// rvNew mirrors srvNew (server_test.go) but passes an inspectable
// MemRevocationStore via Config.Revocations.
func rvNew(t *testing.T) *rvFixture {
	t.Helper()
	key, err := iam.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	keys := iam.NewKeySet(key)
	store := iam.NewMemStore()
	revocations := iam.NewMemRevocationStore()

	// Same bootstrap as srvNew: the issuer must equal the httptest URL, which
	// only exists once the server is listening, so route through a late-bound
	// handler.
	var srv *iam.Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	srv, err = iam.NewServer(iam.Config{Issuer: ts.URL, Revocations: revocations}, keys, store)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return &rvFixture{
		srvFixture:  &srvFixture{ts: ts, store: store, keys: keys, issuer: ts.URL},
		revocations: revocations,
	}
}

// rvIssueToken obtains a client_credentials token and returns it with its jti.
func rvIssueToken(t *testing.T, f *rvFixture, id, secret string) (token, jti string) {
	t.Helper()
	res, body := srvPostToken(t, f.srvFixture, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {secret},
		"resource":      {"https://mesh.example/mcp"},
	}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("token status = %d (body %v)", res.StatusCode, body)
	}
	token, _ = body["access_token"].(string)
	return token, rvJTI(t, token)
}

// rvJTI extracts the jti claim from a compact JWT (payload only, no verify —
// the test just needs the id).
func rvJTI(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", token)
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims struct {
		JTI string `json:"jti"`
	}
	if err := json.Unmarshal(pb, &claims); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if claims.JTI == "" {
		t.Fatal("token has no jti")
	}
	return claims.JTI
}

// rvPostRevoke POSTs token to /revoke with HTTP Basic client auth.
func rvPostRevoke(t *testing.T, f *rvFixture, id, secret, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.ts.URL+"/revoke",
		strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(id, secret)
	res, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /revoke: %v", err)
	}
	res.Body.Close()
	return res
}

func TestRevokeEndpointHappyPath(t *testing.T) {
	f := rvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{Workspace: "team", Subject: "deployer"})
	token, jti := rvIssueToken(t, f, id, secret)

	res := rvPostRevoke(t, f, id, secret, token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("revoke status = %d, want 200", res.StatusCode)
	}

	revoked, err := f.revocations.IsRevoked(context.Background(), jti, time.Now())
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revoked {
		t.Errorf("jti %s not revoked in the store after POST /revoke", jti)
	}

	// The public feed must now contain the jti, in the LOCKED RevocationFeed shape.
	feedRes, err := f.ts.Client().Get(f.ts.URL + "/revocations")
	if err != nil {
		t.Fatalf("GET /revocations: %v", err)
	}
	defer feedRes.Body.Close()
	if feedRes.StatusCode != http.StatusOK {
		t.Fatalf("feed status = %d, want 200", feedRes.StatusCode)
	}
	if cc := feedRes.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("feed Cache-Control = %q, want no-store", cc)
	}
	var feed iam.RevocationFeed
	if err := json.NewDecoder(feedRes.Body).Decode(&feed); err != nil {
		t.Fatalf("decode feed: %v", err)
	}
	if feed.AsOf.IsZero() {
		t.Error("feed as_of is zero")
	}
	found := false
	for _, e := range feed.Entries {
		if e.JTI == jti {
			found = true
			if !e.Expiry.After(time.Now()) {
				t.Errorf("feed entry expiry %v is not in the future", e.Expiry)
			}
		}
	}
	if !found {
		t.Errorf("feed does not contain revoked jti %s (entries %v)", jti, feed.Entries)
	}
}

func TestRevokeRefusesAnotherClientsToken(t *testing.T) {
	f := rvNew(t)
	idA, secretA := srvRegister(t, f.store, iam.Client{Workspace: "team", Subject: "agent-a"})
	idB, secretB := srvRegister(t, f.store, iam.Client{Workspace: "team", Subject: "agent-b"})
	tokenA, jtiA := rvIssueToken(t, f, idA, secretA)

	// Client B tries to revoke A's token: 200 (no oracle) but nothing stored.
	res := rvPostRevoke(t, f, idB, secretB, tokenA)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("cross-client revoke status = %d, want 200 (no oracle)", res.StatusCode)
	}
	revoked, err := f.revocations.IsRevoked(context.Background(), jtiA, time.Now())
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if revoked {
		t.Error("client B revoked client A's token — ownership check failed")
	}
}

func TestRevokeGarbageTokenIsNotAnOracle(t *testing.T) {
	f := rvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{Workspace: "team", Subject: "deployer"})

	for _, garbage := range []string{"not-a-jwt", "a.b.c", "eyJhbGciOiJub25lIn0.e30."} {
		res := rvPostRevoke(t, f, id, secret, garbage)
		if res.StatusCode != http.StatusOK {
			t.Errorf("revoke of %q: status = %d, want 200 (RFC 7009 §2.2)", garbage, res.StatusCode)
		}
	}

	entries, err := f.revocations.ListActive(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("garbage tokens produced denylist entries: %v", entries)
	}
}

func TestRevokeRequiresClientAuth(t *testing.T) {
	f := rvNew(t)
	id, secret := srvRegister(t, f.store, iam.Client{Workspace: "team", Subject: "deployer"})
	token, jti := rvIssueToken(t, f, id, secret)

	res := rvPostRevoke(t, f, id, "wrong-secret", token)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoke with a bad secret: status = %d, want 401", res.StatusCode)
	}
	revoked, err := f.revocations.IsRevoked(context.Background(), jti, time.Now())
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if revoked {
		t.Error("an unauthenticated caller revoked a token")
	}
}

func TestMetadataAdvertisesRevocationEndpoint(t *testing.T) {
	f := rvNew(t)
	res, err := f.ts.Client().Get(f.ts.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatalf("GET metadata: %v", err)
	}
	defer res.Body.Close()
	var md map[string]any
	if err := json.NewDecoder(res.Body).Decode(&md); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if got, _ := md["revocation_endpoint"].(string); got != f.issuer+"/revoke" {
		t.Errorf("revocation_endpoint = %q, want %q", got, f.issuer+"/revoke")
	}
}
