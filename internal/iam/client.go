package iam

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
)

// ErrClientNotFound is returned by a Store when no client matches an id.
var ErrClientNotFound = errors.New("client not found")

// Client is a registered agent credential. It is the machine analogue of a user
// account: an admin registers it once, and the agent exchanges its id+secret for
// short-lived access tokens. Only the secret's hash is stored, so a database
// leak never exposes a usable credential — the same posture as amt_ tokens.
type Client struct {
	// ClientID is the public identifier (prefix "agt_").
	ClientID string
	// SecretHash is the SHA-256 hash of the client secret (hex).
	SecretHash string
	// Workspace and Subject are the room and member name a token for this
	// client authenticates as — they become Principal.Workspace / .Member.
	Workspace string
	Subject   string
	// Kind is almost always "agent"; carried onto the token's kind claim.
	Kind string
	// AllowedScopes bounds what a token for this client may request. An empty
	// list means the client may request no scopes (least privilege by default).
	AllowedScopes []string
	// TokenTTL is how long issued tokens live. Zero means use the server default.
	TokenTTL time.Duration
	// Disabled clients are refused at the token endpoint without being deleted
	// (immediate, reversible revocation).
	Disabled  bool
	CreatedAt time.Time
}

// Store persists agent clients. Like AgentMesh's Store it is an interface with
// an in-memory and (later) a Postgres implementation behind one contract, so
// the two can never drift.
type Store interface {
	CreateClient(ctx context.Context, c Client) error
	GetClient(ctx context.Context, clientID string) (Client, error)
	ListClients(ctx context.Context, workspace string) ([]Client, error)
	SetClientDisabled(ctx context.Context, clientID string, disabled bool) error
}

// GenerateClientCredentials mints a new client id and secret. The secret is
// returned once (to hand to the operator) alongside the hash to store; the
// server keeps only the hash.
func GenerateClientCredentials() (clientID, secret, secretHash string, err error) {
	idRaw := make([]byte, 9)
	if _, err = rand.Read(idRaw); err != nil {
		return "", "", "", fmt.Errorf("client id entropy: %w", err)
	}
	secRaw := make([]byte, 32)
	if _, err = rand.Read(secRaw); err != nil {
		return "", "", "", fmt.Errorf("client secret entropy: %w", err)
	}
	clientID = "agt_" + base64.RawURLEncoding.EncodeToString(idRaw)
	secret = "ags_" + base64.RawURLEncoding.EncodeToString(secRaw)
	return clientID, secret, HashSecret(secret), nil
}

// HashSecret is the canonical storage hash for a client secret.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// verifySecret compares a presented secret against a stored hash in constant
// time (compare the hashes, not the secrets, so length isn't leaked either).
func verifySecret(presented, storedHash string) bool {
	got := HashSecret(presented)
	return subtle.ConstantTimeCompare([]byte(got), []byte(storedHash)) == 1
}

// allows reports whether the client may be granted scope s.
func (c Client) allows(s string) bool {
	for _, a := range c.AllowedScopes {
		if a == s {
			return true
		}
	}
	return false
}

// grantScopes intersects a requested scope string with the client's allowed
// scopes. A request for scopes the client doesn't hold is an error (invalid_scope),
// never a silent downgrade. An empty request grants the client's full allowance.
func (c Client) grantScopes(requested string) ([]string, error) {
	req := normalizeScopes(requested)
	if len(req) == 0 {
		return append([]string(nil), c.AllowedScopes...), nil
	}
	for _, s := range req {
		if !c.allows(s) {
			return nil, fmt.Errorf("scope %q not permitted for this client", s)
		}
	}
	return req, nil
}

// ParseScopeList turns a comma/space separated CLI scope argument into a slice.
func ParseScopeList(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
	return f
}
