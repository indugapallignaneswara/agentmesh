package store_test

import (
	"testing"

	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/storetest"
)

func TestMemoryStore(t *testing.T) {
	storetest.RunSuite(t, func(t *testing.T) store.Store {
		return store.NewMemory()
	})
}
