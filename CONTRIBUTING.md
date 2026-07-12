# Contributing to AgentMesh

Thanks for wanting to help. This document is short and specific — it tells you
how to get a change merged, and the two or three rules that actually matter
here.

## Getting set up

You need Go (see the `go` directive in `go.mod`) and, for the full test suite,
a throwaway Postgres.

```bash
git clone https://github.com/indugapallignaneswara/agentmesh
cd agentmesh
make test          # hermetic: in-memory store, no Postgres needed
make demo          # runs a server + simulated members, asserts the whole flow
```

To run the Postgres contract suite (what CI runs):

```bash
# any throwaway database will do
export AGENTMESH_TEST_DATABASE_URL='postgres://user@localhost:5432/agentmesh_test?sslmode=disable'
make test-integration
```

Before opening a PR:

```bash
gofmt -l ./cmd ./internal   # must print nothing
go vet ./...
go test -race ./...
```

## The rules that matter

**1. Every store feature lands in *both* engines, under the shared contract
suite.** There are two `store.Store` implementations — in-memory and Postgres —
and one test suite (`internal/storetest`) that both must pass. This is what
keeps them from drifting, and drift is how silent data bugs get in. If you add
a store method, add it to both and add contract tests. A memory-store-only
feature will not be merged.

**2. Concurrency claims need adversarial tests.** If you write something whose
correctness depends on a lock, a lease, or an atomic update, write the test
that *fails without it*. The no-double-claim test is the model: it races 16
goroutines for 50 tasks, and if you delete `FOR UPDATE SKIP LOCKED` it reports
233 duplicate claims. A test that passes whether or not the mechanism exists
proves nothing.

**3. Inter-agent content is untrusted.** Anything an agent writes that another
agent can read is a prompt-injection vector. Shared memory stays review-gated;
message bodies stay labelled as data, not instructions. If you add a surface
where one agent's output reaches another's context, say how it's contained.

**4. Don't break `AGENTMESH_AUTH=off`.** It's the zero-setup demo path and the
reason someone can try this in two minutes. New features default to off or
degrade gracefully.

**5. Migrations are additive.** Expand → migrate → contract. Never drop or
rename a column in the same release that stops using it — a rolling deploy runs
old and new binaries against one schema. See `docs/operations.md`.

## Style

Match the surrounding code. Comments explain *why* — constraints, trade-offs,
the attack a check prevents — not what the next line does. If a decision looks
odd, the comment should say what would break if you did it the obvious way.

## Pull requests

- One logical change per PR. A bug fix and a refactor are two PRs.
- The commit message should explain the *reasoning*, not just the diff. If you
  found the bug by testing against real Postgres, say so — that's the part a
  future reader needs.
- If you fixed a bug, add the regression test that would have caught it.
- Tests pass, `gofmt` clean, `go vet` clean. CI enforces all of it, including
  a guard that the Postgres suite actually ran rather than silently skipping.

## Reporting security issues

**Do not open a public issue.** See [SECURITY.md](SECURITY.md).

## Questions and ideas

Open a GitHub issue. If you're proposing something large, open an issue before
writing the code — the roadmap in [ROADMAP.md](ROADMAP.md) has opinions about
sequencing, and I'd rather align first than reject work you've already done.
