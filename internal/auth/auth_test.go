package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

func seedToken(t *testing.T, st store.Store, ws, member string, kind model.Kind, expires *time.Time) string {
	t.Helper()
	secret, id, hash, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAuthToken(context.Background(), model.AuthToken{
		ID: id, TokenHash: hash, Workspace: ws, Member: member, Kind: kind,
		CreatedAt: time.Now().UTC(), ExpiresAt: expires,
	}); err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestGenerateSecretShape(t *testing.T) {
	secret, id, hash, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "amt_") || len(secret) < 40 {
		t.Fatalf("secret shape: %q", secret)
	}
	if !strings.HasPrefix(id, "tok_") {
		t.Fatalf("id shape: %q", id)
	}
	if hash != auth.HashSecret(secret) {
		t.Fatal("hash mismatch")
	}
	// IDs must not leak secret material: the id derives from the hash.
	if strings.Contains(secret, id[len("tok_"):]) {
		t.Fatal("id appears inside the secret")
	}
}

func TestTokenAuthenticator(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	a := &auth.TokenAuthenticator{Store: st}

	good := seedToken(t, st, "team", "backend", model.KindAgent, nil)

	p, err := a.Authenticate(ctx, good)
	if err != nil {
		t.Fatal(err)
	}
	if p.Workspace != "team" || p.Member != "backend" || p.Kind != model.KindAgent {
		t.Fatalf("principal = %+v", p)
	}

	for name, secret := range map[string]string{
		"malformed":    "not-a-token",
		"wrong-prefix": "xyz_" + strings.Repeat("a", 40),
		"unknown":      "amt_" + strings.Repeat("a", 43),
		"empty":        "",
	} {
		if _, err := a.Authenticate(ctx, secret); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("%s: err = %v, want ErrUnauthenticated", name, err)
		}
	}

	// Expired and revoked tokens are indistinguishable from unknown ones.
	past := time.Now().Add(-time.Hour)
	expired := seedToken(t, st, "team", "old", model.KindAgent, &past)
	if _, err := a.Authenticate(ctx, expired); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("expired err = %v, want ErrUnauthenticated", err)
	}
}

func TestChecksWithoutPrincipalPass(t *testing.T) {
	ctx := context.Background() // no principal = auth off
	if err := auth.CheckActor(ctx, "team", "anyone"); err != nil {
		t.Fatal(err)
	}
	if err := auth.CheckWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if err := auth.CheckKind(ctx, model.KindHuman); err != nil {
		t.Fatal(err)
	}
}

func TestChecksWithPrincipal(t *testing.T) {
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{
		Workspace: "team", Member: "backend", Kind: model.KindAgent,
	})
	// Own identity passes.
	if err := auth.CheckActor(ctx, "team", "backend"); err != nil {
		t.Fatal(err)
	}
	// Spoofing another member is forbidden.
	if err := auth.CheckActor(ctx, "team", "alice"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("spoof err = %v, want ErrForbidden", err)
	}
	// Crossing workspaces is forbidden.
	if err := auth.CheckActor(ctx, "other", "backend"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("cross-ws err = %v, want ErrForbidden", err)
	}
	if err := auth.CheckWorkspace(ctx, "other"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("ws err = %v, want ErrForbidden", err)
	}
	// An agent claiming to be human is forbidden.
	if err := auth.CheckKind(ctx, model.KindHuman); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("kind err = %v, want ErrForbidden", err)
	}
}
