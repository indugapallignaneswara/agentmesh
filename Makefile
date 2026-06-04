# AgentMesh developer tasks. Assumes Go on PATH.

GO        ?= go
BINARY    ?= bin/agentmesh
PKG       := ./...
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -X main.version=$(VERSION)

# Local Postgres used by integration tests (a throwaway database).
TEST_DATABASE_URL ?= postgres://agentmesh:agentmesh@localhost:5432/agentmesh_test?sslmode=disable

.PHONY: build run test test-integration demo vet fmt tidy up down clean

build: ## Compile the server binary
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/agentmesh

run: ## Run the server (expects Postgres; set AGENTMESH_NATS_URL for the bus)
	$(GO) run ./cmd/agentmesh

test: ## Run unit tests (hermetic; Postgres integration tests skip)
	$(GO) test $(PKG)

test-integration: ## Run all tests including Postgres integration
	AGENTMESH_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" $(GO) test -count=1 $(PKG)

demo: ## Run the loopback Phase 0 simulation (no external deps)
	./scripts/loopback-demo.sh

vet: ## go vet
	$(GO) vet $(PKG)

fmt: ## Format the tree
	$(GO) fmt $(PKG)

tidy: ## Tidy modules
	$(GO) mod tidy

up: ## Start local Postgres + NATS
	docker compose -f deploy/docker-compose.yml up -d

down: ## Stop local Postgres + NATS
	docker compose -f deploy/docker-compose.yml down

clean: ## Remove build artifacts
	rm -rf bin
