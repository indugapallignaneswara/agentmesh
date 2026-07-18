package iam_test

// Store contract test: one behavioural suite run against both the in-memory
// store and (when a test database is reachable) the Postgres store, so the two
// implementations can never drift.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/iam"
)

// stUnique returns a random hex suffix so PG re-runs never collide on ids or
// workspaces.
func stUnique(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("entropy: %v", err)
	}
	return hex.EncodeToString(b)
}

// stPGStore returns a PGStore when a test database is configured AND reachable;
// otherwise it skips. It never fails a run for lack of a database.
func stPGStore(t *testing.T) *iam.PGStore {
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
	s, err := iam.NewPGStore(ctx, dsn)
	if err != nil {
		t.Skipf("test database configured but unreachable: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestStoreContractMem(t *testing.T) {
	stRunContract(t, iam.NewMemStore())
}

func TestStoreContractPG(t *testing.T) {
	stRunContract(t, stPGStore(t))
}

// stRunContract is the shared behavioural contract for any iam.Store.
func stRunContract(t *testing.T, store iam.Store) {
	t.Helper()
	ctx := context.Background()
	uniq := stUnique(t)
	ws := "st-ws-" + uniq

	c := iam.Client{
		ClientID:      "agt_st_" + uniq,
		SecretHash:    iam.HashSecret("ags_st_" + uniq),
		Workspace:     ws,
		Subject:       "deployer",
		Kind:          "agent",
		AllowedScopes: []string{"mesh:send", "mesh:read"},
		TokenTTL:      90 * time.Second,
		Disabled:      false,
		// timestamptz keeps microseconds; truncate so round-trip compares equal.
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}

	t.Run("create and get round-trips all fields", func(t *testing.T) {
		if err := store.CreateClient(ctx, c); err != nil {
			t.Fatalf("CreateClient: %v", err)
		}
		got, err := store.GetClient(ctx, c.ClientID)
		if err != nil {
			t.Fatalf("GetClient: %v", err)
		}
		if got.ClientID != c.ClientID ||
			got.SecretHash != c.SecretHash ||
			got.Workspace != c.Workspace ||
			got.Subject != c.Subject ||
			got.Kind != c.Kind ||
			got.TokenTTL != c.TokenTTL ||
			got.Disabled != c.Disabled {
			t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", got, c)
		}
		if !reflect.DeepEqual(got.AllowedScopes, c.AllowedScopes) {
			t.Errorf("AllowedScopes = %v, want %v", got.AllowedScopes, c.AllowedScopes)
		}
		if !got.CreatedAt.Equal(c.CreatedAt) {
			t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, c.CreatedAt)
		}
	})

	t.Run("get unknown id returns ErrClientNotFound", func(t *testing.T) {
		_, err := store.GetClient(ctx, "agt_missing_"+uniq)
		if !errors.Is(err, iam.ErrClientNotFound) {
			t.Fatalf("GetClient(unknown) = %v, want ErrClientNotFound", err)
		}
	})

	t.Run("duplicate create errors", func(t *testing.T) {
		if err := store.CreateClient(ctx, c); err == nil {
			t.Fatal("CreateClient accepted a duplicate client id")
		}
	})

	t.Run("list filters by workspace", func(t *testing.T) {
		other := c
		other.ClientID = "agt_st_other_" + uniq
		other.Workspace = ws + "-other"
		if err := store.CreateClient(ctx, other); err != nil {
			t.Fatalf("CreateClient(other): %v", err)
		}

		got, err := store.ListClients(ctx, ws)
		if err != nil {
			t.Fatalf("ListClients(%q): %v", ws, err)
		}
		if len(got) != 1 || got[0].ClientID != c.ClientID {
			t.Errorf("ListClients(%q) = %d clients, want exactly the one in %q", ws, len(got), ws)
		}

		all, err := store.ListClients(ctx, "")
		if err != nil {
			t.Fatalf("ListClients(all): %v", err)
		}
		found := map[string]bool{}
		for _, cl := range all {
			found[cl.ClientID] = true
		}
		if !found[c.ClientID] || !found[other.ClientID] {
			t.Errorf("ListClients(\"\") missing this run's clients (got %d rows)", len(all))
		}
	})

	t.Run("set disabled flips flag and errors on unknown", func(t *testing.T) {
		if err := store.SetClientDisabled(ctx, c.ClientID, true); err != nil {
			t.Fatalf("SetClientDisabled(true): %v", err)
		}
		got, err := store.GetClient(ctx, c.ClientID)
		if err != nil {
			t.Fatalf("GetClient after disable: %v", err)
		}
		if !got.Disabled {
			t.Error("client not disabled after SetClientDisabled(true)")
		}
		if err := store.SetClientDisabled(ctx, c.ClientID, false); err != nil {
			t.Fatalf("SetClientDisabled(false): %v", err)
		}
		got, err = store.GetClient(ctx, c.ClientID)
		if err != nil {
			t.Fatalf("GetClient after re-enable: %v", err)
		}
		if got.Disabled {
			t.Error("client still disabled after SetClientDisabled(false)")
		}
		if err := store.SetClientDisabled(ctx, "agt_missing_"+uniq, true); !errors.Is(err, iam.ErrClientNotFound) {
			t.Errorf("SetClientDisabled(unknown) = %v, want ErrClientNotFound", err)
		}
	})
}
