package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

func mkToken(t *testing.T, s store.Store, id, hash, ws, member string, kind model.Kind, expires *time.Time) model.AuthToken {
	t.Helper()
	got, err := s.CreateAuthToken(context.Background(), model.AuthToken{
		ID: id, TokenHash: hash, Workspace: ws, Member: member, Kind: kind,
		CreatedAt: base, ExpiresAt: expires,
	})
	mustNoErr(t, err)
	return got
}

func testTokenCreateGetByHash(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkToken(t, s, "tk1", "hash-1", "ws1", "backend", model.KindAgent, nil)

	got, err := s.GetAuthTokenByHash(ctx, "hash-1", base)
	mustNoErr(t, err)
	if got.Member != "backend" || got.Kind != model.KindAgent || got.Workspace != "ws1" {
		t.Fatalf("got = %+v", got)
	}
	if _, err := s.GetAuthTokenByHash(ctx, "no-such-hash", base); err != store.ErrNotFound {
		t.Fatalf("unknown hash err = %v, want ErrNotFound", err)
	}
}

func testTokenRevocation(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkToken(t, s, "tk1", "hash-1", "ws1", "backend", model.KindAgent, nil)
	mkToken(t, s, "tk2", "hash-2", "ws1", "alice", model.KindHuman, nil)

	mustNoErr(t, s.RevokeAuthToken(ctx, "tk1", base.Add(time.Hour)))
	// Revoked tokens no longer authenticate...
	if _, err := s.GetAuthTokenByHash(ctx, "hash-1", base.Add(2*time.Hour)); err != store.ErrNotFound {
		t.Fatalf("revoked token err = %v, want ErrNotFound", err)
	}
	// ...double-revoke is NotFound, others are unaffected...
	if err := s.RevokeAuthToken(ctx, "tk1", base); err != store.ErrNotFound {
		t.Fatalf("double revoke err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetAuthTokenByHash(ctx, "hash-2", base.Add(2*time.Hour)); err != nil {
		t.Fatalf("unrevoked token err = %v", err)
	}
	// ...and the audit list keeps both, newest first.
	list, err := s.ListAuthTokens(ctx, "ws1")
	mustNoErr(t, err)
	if len(list) != 2 {
		t.Fatalf("list = %d, want 2 (revoked kept for audit)", len(list))
	}
}

func testTokenExpiry(t *testing.T, s store.Store) {
	ctx := context.Background()
	exp := base.Add(time.Hour)
	mkToken(t, s, "tk1", "hash-1", "ws1", "backend", model.KindAgent, &exp)

	if _, err := s.GetAuthTokenByHash(ctx, "hash-1", base.Add(30*time.Minute)); err != nil {
		t.Fatalf("before expiry err = %v", err)
	}
	if _, err := s.GetAuthTokenByHash(ctx, "hash-1", base.Add(time.Hour)); err != store.ErrNotFound {
		t.Fatalf("at/after expiry err = %v, want ErrNotFound", err)
	}
}
