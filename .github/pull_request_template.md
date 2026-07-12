## What and why

<!-- What changes, and the reasoning. If you found a bug by testing against
     real Postgres (or by racing goroutines), say so — that's the part a
     future reader needs. -->

## Checklist

- [ ] `gofmt -l ./cmd ./internal` prints nothing; `go vet ./...` clean
- [ ] `go test -race ./...` passes (with `AGENTMESH_TEST_DATABASE_URL` set, so
      the Postgres contract suite actually runs)
- [ ] **Store changes land in both engines** (memory + Postgres) with contract
      tests in `internal/storetest`
- [ ] **Concurrency claims have a test that fails without the mechanism**
      (lock/lease/atomic update) — not one that passes either way
- [ ] Bug fix? The regression test that would have caught it is included
- [ ] `AGENTMESH_AUTH=off` (the zero-setup demo path) still works
- [ ] Migrations are additive (expand → migrate → contract)

<!-- Delete any line that genuinely doesn't apply, rather than leaving it
     unchecked. -->
