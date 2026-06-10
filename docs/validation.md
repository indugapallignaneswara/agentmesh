# Phase 0 validation

The Phase 0 success metric (from the blueprint):

> A developer on machine A can address an agent in a session on machine B by
> name and get a reply, and a broadcast reaches all present agents — across
> machines, across vendors, with humans and agents co-resident.

There are two levels of validation. The first runs anywhere and is automated;
the second needs real hardware and real agent installs and is therefore a manual
acceptance test.

## Level 1 — loopback simulation (automated)

`scripts/loopback-demo.sh` runs one server and two CLI "members" (standing in
for a Claude session and a Codex session) on a single host, with an in-memory
store so there are no external dependencies:

```bash
./scripts/loopback-demo.sh
```

It asserts: presence lists all members, any-to-any direct addressing delivers to
exactly the named recipient, consume-on-read works, a broadcast fans out to all
others, and the observation log records the activity.

**What this proves:** the coordination semantics end to end over real HTTP/MCP.
**What it does NOT prove:** that two *different physical machines* and two
*different agent products* interoperate. That is Level 2.

## Level 2 — cross-machine, cross-vendor (manual acceptance test)

This is the real metric. It cannot be run in CI; it needs two machines on a
shared network and two agent products installed. Do it once to sign off Phase 0.

### Prerequisites

- A host reachable from both machines (a laptop on the LAN, a small VM, etc.)
  that will run the AgentMesh server.
- Machine A with **Claude Code** installed; Machine B with **Codex CLI**
  installed (any two MCP-capable agents work; these are the reference pair).
- The `coord` binary on each machine (optional but handy):
  `go build -o ~/.local/bin/coord ./cmd/coord`.

### Step 1 — run the server on the shared host

Use Postgres for a real run (or `AGENTMESH_STORE=memory` for a quick smoke):

```bash
# durable
make up   # starts Postgres + NATS via docker compose
AGENTMESH_HTTP_ADDR=0.0.0.0:8080 \
AGENTMESH_DATABASE_URL='postgres://agentmesh:agentmesh@localhost:5432/agentmesh?sslmode=disable' \
AGENTMESH_NATS_URL='nats://localhost:4222' \
  go run ./cmd/agentmesh
```

Note the host's LAN IP (say `192.168.1.50`). The endpoint is
`http://192.168.1.50:8080/mcp`. (For anything beyond a trusted LAN, put it
behind TLS and auth — see the security notes in the README/roadmap; Phase 0
ships no auth.)

### Step 2 — register the server with each agent

**Machine A — Claude Code:**

```bash
claude mcp add --transport http agentmesh http://192.168.1.50:8080/mcp
```

**Machine B — Codex:** add to `~/.codex/config.toml` (see
[`examples/codex.toml`](../examples/codex.toml)):

```toml
[mcp_servers.agentmesh]
url = "http://192.168.1.50:8080/mcp"
```

### Step 3 — join from each session

In the Claude Code session on A:

```
Use the workspace_join tool with workspace "team", name "claude-A", kind "agent".
```

In the Codex session on B: join as `codex-B` the same way.

Optionally add the inbox-pull hook on each machine
([`examples/claude/`](../examples/claude/README.md)) so messages arrive
automatically at turn boundaries.

### Step 4 — the actual test

1. **Any-to-any across machines.** From the Claude session on A, send a direct
   message to `codex-B`:
   *"Use send_message: workspace team, from claude-A, to codex-B, body 'what is
   the build status?'"*
2. **Reply.** On B, the Codex session calls `read_inbox` (or its hook pulls it),
   sees the message, and replies with `send_message` back to `claude-A`.
3. **Confirm.** On A, `read_inbox` shows the reply. ✅ *Cross-machine,
   cross-vendor, by-name addressing works.*
4. **Broadcast.** From either session (or a human via `coord broadcast`), send a
   broadcast and confirm both other members receive it via `read_inbox`.
5. **Human co-membership.** From a third terminal, join as a human and
   participate with the `coord` CLI:
   `AGENTMESH_MEMBER=alice coord join --kind human && coord broadcast "ship it"`.

### Pass criteria

- [ ] A message sent from A is read by the named recipient on B (and by no one
      else).
- [ ] A reply from B is read back on A.
- [ ] A broadcast reaches every other present member.
- [ ] A human (CLI) and agents (MCP) coexist in one workspace.

### Known Phase 0 limits to keep in mind while testing

- **Pull, not push.** An agent sees messages at its next turn boundary (hook or
  explicit `read_inbox`), not instantly mid-turn. Latency is bounded by each
  agent's cadence.
- **Consume-on-read / at-most-once.** A message is delivered once; if a session
  reads then crashes before acting, that message is gone. (A read/ack split is a
  later-phase candidate.)
- **Auth.** By default (`AGENTMESH_AUTH=off`) there is no authentication —
  only run on a trusted network. For anything beyond that, set
  `AGENTMESH_AUTH=token`, issue per-member tokens with
  `agentmesh token create`, and pass them via `coord --token` /
  `claude mcp add --header "Authorization: Bearer ..."`.

## Status

Level 1 (loopback) is automated and passing. Level 2 (cross-machine,
cross-vendor) is **not yet executed** — it requires the operator's hardware and
agent installs. Run the checklist above to sign off the Phase 0 metric.
