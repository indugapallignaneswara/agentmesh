package store_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// TestPostgresConcurrentMigrateNoRace is the acceptance test for the multi-
// replica rollout guarantee documented in docs/operations.md: several replicas
// booting at once against a fresh database must all come up cleanly, with the
// schema migrated exactly once. migrate() holds a session-level advisory lock
// for the whole run, so the first replica migrates while the rest block, then
// acquire the lock and find every version already applied.
//
// To confirm this test has teeth: remove the pg_advisory_lock/unlock calls in
// migrate.go and it fails — concurrent runs see the same version as un-applied,
// both apply it, and the loser errors on the duplicate schema_migrations insert
// (or a duplicate-object error from the migration body itself).
func TestPostgresConcurrentMigrateNoRace(t *testing.T) {
	dsn := os.Getenv("AGENTMESH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set AGENTMESH_TEST_DATABASE_URL to run Postgres migration test")
	}
	ctx := context.Background()

	// Create a throwaway database so we exercise migrations from empty, without
	// disturbing the shared test schema other tests rely on.
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	dbName := fmt.Sprintf("am_migrate_race_%d", os.Getpid())
	// A prior aborted run may have left it behind; drop then create.
	_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+dbName)
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+dbName); err != nil {
		admin.Close()
		t.Fatalf("create scratch db: %v", err)
	}
	t.Cleanup(func() {
		// Terminate any lingering backends, then drop.
		_, _ = admin.Exec(ctx,
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, dbName)
		_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+dbName)
		admin.Close()
	})

	scratchDSN, err := withDatabase(dsn, dbName)
	if err != nil {
		t.Fatalf("build scratch dsn: %v", err)
	}

	// Race N replicas' worth of NewPostgres (each runs migrate) at once.
	const replicas = 8
	var wg sync.WaitGroup
	errs := make([]error, replicas)
	stores := make([]*store.Postgres, replicas)
	start := make(chan struct{})
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			pg, err := store.NewPostgres(ctx, scratchDSN)
			stores[i], errs[i] = pg, err
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("replica %d failed to migrate: %v", i, err)
		}
	}
	for _, pg := range stores {
		if pg != nil {
			_ = pg.Close()
		}
	}
}

// withDatabase returns dsn with its database name replaced by db, preserving
// the userinfo, host, and query parameters (including a unix-socket host=).
func withDatabase(dsn, db string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.Path = "/" + db
	return u.String(), nil
}
