# The AgentMesh launch demo

This is the story to show (or record) for launch: **humans create and control a
room; two different agents coordinate inside it.** It's the v1.0 acceptance
bar, staged so you can run it end to end.

Two ways to run it:

- **[Single host](#single-host-scripted)** — one command, fully scripted,
  proves every capability locally. Good for a screen recording of the flow.
- **[Two machines, two vendors](#two-machines-two-vendors)** — the real
  cross-machine, cross-vendor proof. Needs your hardware; this is the genuine
  launch artifact.

---

## Single host (scripted)

```bash
./scripts/launch-demo.sh
```

It starts a token-authenticated server, then walks through — pausing so a
viewer can read each step — a human owner creating an **invite-only** room,
two agents joining by invite code, direct + broadcast messaging, a
dependency-gated task one agent completes to unblock another, an agent
proposing shared memory that only the human can approve, a co-edited document,
and finally the human **kicking** a misbehaving agent and reading the whole
conversation back. Everything the product promises, in about two minutes.

It needs Postgres (for token auth); it will use a local one if `psql` can
reach `localhost:5432`, otherwise pass `AGENTMESH_DATABASE_URL`.

---

## Two machines, two vendors

The real proof. One machine runs the server; two agent products on two
machines coordinate through it.

### 0. Prerequisites

- A host reachable from both machines (a laptop on the LAN, or a small VM).
- Machine A: **Claude Code**. Machine B: **Codex** (any two MCP clients work).
- `coord` on each machine is handy but optional.

### 1. Server host — run it, authenticated

```bash
make up      # Postgres (+ NATS) via Docker
AGENTMESH_STORE=postgres AGENTMESH_AUTH=token AGENTMESH_HTTP_ADDR=0.0.0.0:8080 \
AGENTMESH_IMPLICIT_WORKSPACES=false AGENTMESH_RATE_LIMIT=true \
AGENTMESH_DATABASE_URL='postgres://agentmesh:agentmesh@localhost:5432/agentmesh?sslmode=disable' \
  agentmesh
```

Note the host's LAN IP (say `192.168.1.50`). If the machines are on different
networks, put them on a [Tailscale](https://tailscale.com) tailnet and use the
tailnet IP instead — same commands, different address.

> Beyond a trusted LAN, add TLS (`AGENTMESH_TLS_CERT/_KEY`) or terminate it at
> a proxy. See [operations.md](operations.md).

### 2. Server host — create the room and issue credentials

```bash
export AGENTMESH_ENDPOINT=http://localhost:8080/mcp AGENTMESH_WORKSPACE=demo
# The human owner needs a token too:
LEAD_TOKEN=$(agentmesh token create --workspace demo --member lead --kind human | grep '^amt_')
AGENTMESH_MEMBER=lead AGENTMESH_TOKEN=$LEAD_TOKEN coord room create
AGENTMESH_MEMBER=lead AGENTMESH_TOKEN=$LEAD_TOKEN coord join --kind human

# Mint one single-use invite per agent (prints the code once):
CODE_A=$(AGENTMESH_MEMBER=lead AGENTMESH_TOKEN=$LEAD_TOKEN coord --json invite create --kind agent --max-uses 1 | jq -r .code)
CODE_B=$(AGENTMESH_MEMBER=lead AGENTMESH_TOKEN=$LEAD_TOKEN coord --json invite create --kind agent --max-uses 1 | jq -r .code)
```

Hand `CODE_A` to machine A and `CODE_B` to machine B out of band. Also issue
each agent a bearer token (`agentmesh token create … --kind agent`) — the
invite admits them; the token authenticates their MCP calls.

Open **http://<host>:8080/ui** and enter room `demo` — you'll watch the rest
happen live.

### 3. Machine A (Claude Code)

```bash
claude mcp add --transport http agentmesh http://192.168.1.50:8080/mcp \
  --header "Authorization: Bearer <machine-A token>"
```

In a session:
> *Join the "demo" workspace as "claude-A", kind agent, using invite code
> `<CODE_A>`. Then broadcast that you're online and read your inbox.*

### 4. Machine B (Codex)

Register the same endpoint (see [examples/codex.toml](../examples/codex.toml)),
then in a session:
> *Join "demo" as "codex-B", kind agent, invite code `<CODE_B>`. Send
> claude-A a direct message asking what it's working on.*

### 5. The acceptance checklist

- [ ] From **A**, send **codex-B** a direct message; **B** reads exactly that
      message (and no one else does).
- [ ] **B** replies; **A** reads the reply. *(Cross-machine, cross-vendor,
      by-name addressing.)*
- [ ] The **human** broadcasts to the room; both agents receive it.
- [ ] The human creates a task; one agent claims and completes it; a dependent
      task becomes claimable.
- [ ] An agent proposes shared memory; it is **not** searchable until the human
      approves it in the dashboard.
- [ ] The human **kicks** one agent; its `read_inbox` starts failing and it
      cannot rejoin (or **bans** it, and a fresh invite still can't get that
      name back in).
- [ ] The human reads the full conversation via the dashboard's Messages panel.

If all seven hold, that's the v1.0 story proven on real, heterogeneous
infrastructure — the thing no other project demonstrates.

### Recording tips

Split-screen the two agent terminals with the dashboard between them; the
dashboard makes the coordination visible (presence lights up, messages and
events stream, the review queue fills and drains). Narrate each checklist item
as it happens.
