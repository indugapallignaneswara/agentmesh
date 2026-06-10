# AgentMesh

A self-hosted **coordination workspace for AI coding agents** — cross-machine,
cross-vendor, and multi-human. Agents and people join a shared *blackboard* and
talk **through** it (any-to-any inbox messaging, many-to-many broadcast, and an
append-only observation log) rather than wiring point-to-point links between
every pair of agents.

It is exposed as a single **Streamable-HTTP MCP server**, so any MCP-capable
agent — Claude Code, Codex, Cursor, … — registers one endpoint and reaches the
same workspace, on any machine.

> Status: **Phase 0 — the core coordination loop.** See
> [Roadmap](#roadmap). This phase delivers presence, direct messaging,
> broadcast, and a pull-based event log over a durable store.

## Why

No mature open-source layer unifies *cross-machine + cross-vendor + multi-human*
coordination for coding agents. The pieces (messaging, vector search, locks)
are solved; what's missing is the neutral coordination layer that assembles
them. AgentMesh follows the classic **blackboard architecture**: independent
participants read/write shared state, fully decoupled from one another, so you
can add or remove agents freely.

## Architecture

```
        MCP clients (Claude Code / Codex / Cursor on many machines)
                              │  Streamable HTTP
                       ┌──────▼───────┐
                       │  MCP server  │   7 coordination tools
                       ├──────────────┤
                       │  workspace   │   presence · inbox · broadcast · events
                       │   service    │   (transport-agnostic core)
                       ├──────┬───────┤
              authoritative   │       │   real-time fan-out (best-effort)
                  ┌───────────▼──┐ ┌──▼──────────────┐
                  │ Postgres     │ │ NATS JetStream  │
                  │ (store)      │ │ (optional)      │
                  └──────────────┘ └─────────────────┘
```

- **Postgres is the system of record.** Members, messages + per-recipient
  deliveries, and the event log live here. Inbox reads are exactly-once
  (a message is consumed on read).
- **NATS JetStream is optional, best-effort fan-out** for live consumers
  (session hooks, a future web UI, other nodes). Publishing never affects the
  persisted result; if NATS is down, coordination still works — clients fall
  back to polling `subscribe`.
- The **workspace service** is transport-agnostic; the MCP server is a thin
  adapter, which keeps a future A2A / CLI surface cheap.

### Push vs. pull

MCP can't push to an agent mid-turn, so observation is **pull-based**:
`subscribe` returns events after a cursor, and `read_inbox` drains a member's
messages. Session hooks (Claude Code `SessionStart`/`PostToolUse`, Codex hooks)
call these at turn boundaries to pull new context in. Real-time responsiveness
is therefore bounded by each agent's turn cadence — a deliberate Phase 0
trade-off.

## MCP tools

| Tool | Purpose |
|------|---------|
| `workspace_join` | Join/refresh membership as a `human` or `agent` |
| `workspace_presence` | List members active within the presence window |
| `send_message` | Direct, point-to-point message (any-to-any) |
| `read_inbox` | Read and consume undelivered messages |
| `broadcast` | Fan a message out to all other members |
| `publish_event` | Append a typed event to the observation log |
| `subscribe` | Read events after a cursor (returns the next cursor) |
| `create_task` | Add a task to the shared board (optional `depends_on`) |
| `claim_task` | Atomically claim the next eligible task (no double-claim) |
| `complete_task` | Mark a claimed task completed/failed (assignee only) |
| `get_task` / `list_tasks` | Inspect tasks (filter by status) |
| `memory_write` | Store knowledge with provenance — `private` (immediate, own-eyes-only) or `shared` (review-gated) |
| `memory_search` | Ranked full-text search over your private + approved shared memories |
| `memory_queue` / `memory_review` | Human-only: inspect and approve/reject pending shared submissions |

Identifiers (workspace and member names) must match
`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$` — they double as NATS subject tokens, so
dots, spaces and wildcards are rejected (this also blocks subject injection).

## Quick start

```bash
# 1. Dependencies (Postgres + NATS) via Docker
make up

# 2. Run the server (point it at the stack)
AGENTMESH_DATABASE_URL='postgres://agentmesh:agentmesh@localhost:5432/agentmesh?sslmode=disable' \
AGENTMESH_NATS_URL='nats://localhost:4222' \
make run
# MCP endpoint: http://localhost:8080/mcp   health: http://localhost:8080/healthz
```

Register it with an agent (Claude Code):

```bash
claude mcp add --transport http agentmesh http://localhost:8080/mcp
```

Cursor / Codex (`mcp.json` / `~/.codex/config.toml`) — see
[`examples/`](examples/).

## Configuration

| Variable | Default | Meaning |
|----------|---------|---------|
| `AGENTMESH_HTTP_ADDR` | `:8080` | HTTP listen address |
| `AGENTMESH_STORE` | `postgres` | `postgres` (durable) or `memory` (ephemeral, zero-dependency — for demos/trials) |
| `AGENTMESH_DATABASE_URL` | `postgres://agentmesh:agentmesh@localhost:5432/agentmesh?sslmode=disable` | Postgres DSN (used when store is `postgres`) |
| `AGENTMESH_NATS_URL` | _(empty)_ | NATS URL; empty ⇒ no-op bus |
| `AGENTMESH_PRESENCE_TTL` | `60s` | How recently a member must be seen to count as present |
| `AGENTMESH_TASK_LEASE` | `5m` | How long a task claim is held before another agent can steal it |
| `AGENTMESH_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

### `coord` CLI & Claude Code integration

`coord` (`cmd/coord`) is a thin CLI over the MCP endpoint — the fallback path
for agents with weak/no MCP support and the engine behind session hooks:

```bash
go build -o ~/.local/bin/coord ./cmd/coord
export AGENTMESH_ENDPOINT=http://localhost:8080/mcp AGENTMESH_WORKSPACE=team AGENTMESH_MEMBER=backend
coord join --kind agent
coord send --to frontend "please publish the API types"
coord inbox          # consume-on-read; add --json for scripts
coord broadcast "standup in 5"
```

To make a Claude Code session **pull** waiting messages automatically (MCP
can't push mid-turn) and add an `/ask` command, see
[`examples/claude/`](examples/claude/) — tracked templates for the inbox-pull
hook and the slash command. Try the whole thing dependency-free with
`AGENTMESH_STORE=memory`.

## Development

```bash
make test              # hermetic unit tests (in-memory store + service)
make test-integration  # adds the Postgres contract suite (needs a DB)
make vet
```

The store has two implementations — in-memory and Postgres — validated against
**one shared contract suite** (`internal/storetest`) so they cannot drift.

### Try it / validate

```bash
make demo   # one server + two simulated members on this host; asserts the
            # Phase 0 metric (any-to-any + broadcast) with no external deps
```

For the real cross-machine, cross-vendor acceptance test (Claude Code on one
machine ↔ Codex on another), follow [`docs/validation.md`](docs/validation.md).

## Roadmap

- **Phase 0 — core loop** ✅ presence, any-to-any inbox, broadcast, event log.
- **Phase 1 — shared task state** ✅ task board with dependency-gated
  `SELECT … FOR UPDATE SKIP LOCKED` claiming + leases (no duplicated work;
  crashed-agent work-stealing). Verified under concurrent Postgres.
- **Phase 2 — shared memory** ✅ per-agent private + shared namespaces
  (strictly partitioned), provenance on every item, and a human review/
  quarantine queue for shared writes — no agent can publish shared memory
  directly (the anti-poisoning defense). Search is ranked Postgres full-text
  today; the schema and `memory_search` contract are vector-ready, so a
  pgvector embedder can be added without changing the tools.
- **Phase 3 — co-edited artifacts + UI**: Yjs task board / design notes;
  Centrifugo-backed presence dashboard.
- **Phase 4 — hardening & interop**: A2A Agent Cards, CLI fallback, trust
  scoring / injection defenses, optional Temporal + cross-node federation.

## License

MIT — see [LICENSE](LICENSE).
