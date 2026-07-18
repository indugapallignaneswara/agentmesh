package iam

import (
	"context"
	"fmt"
	"sync"
)

// MemStore is an in-memory Client store for tests and the zero-config demo. A
// Postgres implementation lands behind the same Store interface for production.
type MemStore struct {
	mu      sync.RWMutex
	clients map[string]Client
}

// NewMemStore returns an empty in-memory client store.
func NewMemStore() *MemStore {
	return &MemStore{clients: map[string]Client{}}
}

func (s *MemStore) CreateClient(_ context.Context, c Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[c.ClientID]; ok {
		return fmt.Errorf("client %q already exists", c.ClientID)
	}
	s.clients[c.ClientID] = c
	return nil
}

func (s *MemStore) GetClient(_ context.Context, clientID string) (Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.clients[clientID]
	if !ok {
		return Client{}, ErrClientNotFound
	}
	return c, nil
}

func (s *MemStore) ListClients(_ context.Context, workspace string) ([]Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Client
	for _, c := range s.clients {
		if workspace == "" || c.Workspace == workspace {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *MemStore) SetClientDisabled(_ context.Context, clientID string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[clientID]
	if !ok {
		return ErrClientNotFound
	}
	c.Disabled = disabled
	s.clients[clientID] = c
	return nil
}
