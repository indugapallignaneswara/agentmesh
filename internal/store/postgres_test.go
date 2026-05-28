package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/storetest"
)

// TestPostgresStore runs the full store contract against a real Postgres.
//
// It is gated on AGENTMESH_TEST_DATABASE_URL so unit runs stay hermetic. Point
// it at a throwaway database — the test truncates the relevant tables before
// each subtest and the suite assumes an empty store.
//
//	AGENTMESH_TEST_DATABASE_URL=postgres://agentmesh:agentmesh@localhost:5432/agentmesh_test?sslmode=disable \
//	    go test ./internal/store/ -run TestPostgresStore
func TestPostgresStore(t *testing.T) {
	dsn := os.Getenv("AGENTMESH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set AGENTMESH_TEST_DATABASE_URL to run Postgres integration tests")
	}

	storetest.RunSuite(t, func(t *testing.T) store.Store {
		ctx := context.Background()
		st, err := store.NewPostgres(ctx, dsn)
		if err != nil {
			t.Fatalf("connect postgres: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if err := st.TruncateAll(ctx); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return st
	})
}
