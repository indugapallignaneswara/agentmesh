package iam

import (
	"context"
	"sync"
	"time"
)

// MemRevocationStore is the in-memory RevocationStore: a jti -> token-expiry
// map under a mutex. It backs tests and the zero-config demo; because it is
// process-local, revocations reset on restart — production passes a
// PGRevocationStore instead.
type MemRevocationStore struct {
	mu      sync.RWMutex
	revoked map[string]time.Time // jti -> the token's own expiry
}

// NewMemRevocationStore returns an empty in-memory revocation store.
func NewMemRevocationStore() *MemRevocationStore {
	return &MemRevocationStore{revoked: map[string]time.Time{}}
}

func (s *MemRevocationStore) Revoke(_ context.Context, jti string, exp time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Opportunistic prune: entries past their expiry are dead weight — the
	// token they name is already invalid on its own.
	s.pruneLocked(time.Now())
	s.revoked[jti] = exp
	return nil
}

func (s *MemRevocationStore) IsRevoked(_ context.Context, jti string, now time.Time) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	exp, ok := s.revoked[jti]
	return ok && now.Before(exp), nil
}

func (s *MemRevocationStore) ListActive(_ context.Context, now time.Time) ([]Revocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	out := make([]Revocation, 0, len(s.revoked))
	for jti, exp := range s.revoked {
		if now.Before(exp) {
			out = append(out, Revocation{JTI: jti, Expiry: exp})
		}
	}
	return out, nil
}

// pruneLocked drops entries at or past their expiry. Callers hold s.mu.
func (s *MemRevocationStore) pruneLocked(now time.Time) {
	for jti, exp := range s.revoked {
		if !now.Before(exp) {
			delete(s.revoked, jti)
		}
	}
}
