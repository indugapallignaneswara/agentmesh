// Package auth is AgentMesh's identity layer — and the seed of a standalone
// agent-IAM service. It deliberately knows nothing about HTTP routing or MCP:
// it defines principals, credentials, and the checks the service layer calls.
// The extraction seams are the two interfaces: Authenticator (swap opaque
// tokens for OAuth/OIDC validation, or a remote IAM service, without touching
// callers) and TokenReader (any storage backend).
//
// v1 credentials are opaque bearer tokens ("amt_" + 256 bits, URL-safe
// base64). Only the SHA-256 hash is stored, so a database leak does not leak
// credentials; lookups are O(1) by hash; revocation is immediate.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// Sentinel errors. Unauthenticated means "no/invalid credential"; Forbidden
// means "valid credential, but not allowed to act as the claimed principal".
var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
)

// Principal is an authenticated identity: one member of one workspace.
type Principal struct {
	Workspace string
	Member    string
	Kind      model.Kind
}

// Authenticator validates a presented secret and returns its principal.
type Authenticator interface {
	Authenticate(ctx context.Context, secret string) (Principal, error)
}

// TokenReader is the storage subset the token authenticator needs.
type TokenReader interface {
	GetAuthTokenByHash(ctx context.Context, hash string, now time.Time) (model.AuthToken, error)
}

// TokenAuthenticator authenticates opaque amt_ bearer tokens against a store.
type TokenAuthenticator struct {
	Store TokenReader
	Now   func() time.Time
}

const secretPrefix = "amt_"

// Authenticate hashes the presented secret and looks it up. Malformed, missing,
// revoked and expired tokens are all ErrUnauthenticated — no oracle.
func (a *TokenAuthenticator) Authenticate(ctx context.Context, secret string) (Principal, error) {
	if !strings.HasPrefix(secret, secretPrefix) || len(secret) < len(secretPrefix)+20 {
		return Principal{}, ErrUnauthenticated
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	t, err := a.Store.GetAuthTokenByHash(ctx, HashSecret(secret), now())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, err
	}
	return Principal{Workspace: t.Workspace, Member: t.Member, Kind: t.Kind}, nil
}

// GenerateSecret returns a new credential: the secret to hand to the member,
// its public ID (for list/revoke), and the hash to store.
func GenerateSecret() (secret, id, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", fmt.Errorf("entropy: %w", err)
	}
	secret = secretPrefix + base64.RawURLEncoding.EncodeToString(raw)
	// The ID is a short, non-secret handle derived from the hash (not the
	// secret) so logs and list output never contain credential material.
	hash = HashSecret(secret)
	id = "tok_" + hash[:12]
	return secret, id, hash, nil
}

// HashSecret is the canonical storage hash for a token secret.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Equal is a constant-time string comparison, exported for tests and future
// non-hashed comparisons.
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// --- request context ---

type ctxKey struct{}

// WithPrincipal returns a context carrying the authenticated principal.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the authenticated principal, if any. Absence means the
// server is running with auth off — checks then pass (trusted-LAN mode).
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// CheckWorkspace verifies the principal (if any) belongs to the workspace.
func CheckWorkspace(ctx context.Context, workspace string) error {
	p, ok := FromContext(ctx)
	if !ok {
		return nil // auth off
	}
	if p.Workspace != workspace {
		return fmt.Errorf("%w: token is for workspace %q, not %q", ErrForbidden, p.Workspace, workspace)
	}
	return nil
}

// CheckActor verifies the principal (if any) is exactly the claimed actor in
// the claimed workspace. This is the anti-spoofing rule: with auth on, a
// member can only act as itself.
func CheckActor(ctx context.Context, workspace, actor string) error {
	p, ok := FromContext(ctx)
	if !ok {
		return nil // auth off
	}
	if p.Workspace != workspace {
		return fmt.Errorf("%w: token is for workspace %q, not %q", ErrForbidden, p.Workspace, workspace)
	}
	if p.Member != actor {
		return fmt.Errorf("%w: token is for member %q; cannot act as %q", ErrForbidden, p.Member, actor)
	}
	return nil
}

// CheckKind verifies the principal (if any) has the claimed kind. Joining as a
// different kind is forbidden — an agent token must never join as a human,
// which would grant review authority over shared memory.
func CheckKind(ctx context.Context, kind model.Kind) error {
	p, ok := FromContext(ctx)
	if !ok {
		return nil // auth off
	}
	if p.Kind != kind {
		return fmt.Errorf("%w: token is for kind %q; cannot join as %q", ErrForbidden, p.Kind, kind)
	}
	return nil
}
