package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/indugapallignaneswara/agentmesh/internal/iam"
)

// openStore returns a Client store and a close function. It uses Postgres when
// AGENTIAM_DATABASE_URL is set, otherwise an in-memory store (demo only — client
// registrations vanish on restart, so `client register` is meaningless there).
func openStore(ctx context.Context, log *slog.Logger) (iam.Store, func(), error) {
	if dsn := os.Getenv("AGENTIAM_DATABASE_URL"); dsn != "" {
		pg, err := iam.NewPGStore(ctx, dsn)
		if err != nil {
			return nil, func() {}, err
		}
		return pg, pg.Close, nil
	}
	log.Warn("no AGENTIAM_DATABASE_URL set; using in-memory client store — " +
		"registrations are lost on restart. Set a database URL for real use.")
	return iam.NewMemStore(), func() {}, nil
}
