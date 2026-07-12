# Operating AgentMesh

Everything you need to run a node you can safely expose. Read
[`docs/validation.md`](validation.md) for the acceptance test, and
[`ROADMAP.md`](../ROADMAP.md) for what is and isn't built.

## Production checklist

Do all of these before exposing a node beyond a trusted LAN:

- [ ] **Auth on** — `AGENTMESH_AUTH=token` (or `oauth`). Never `off`.
- [ ] **TLS on** — `AGENTMESH_TLS_CERT`/`_KEY`, or terminate at a trusted
      reverse proxy. Bearer tokens over plaintext are recoverable by anyone on
      the path; the server warns loudly if auth is on without TLS.
- [ ] **Explicit rooms** — `AGENTMESH_IMPLICIT_WORKSPACES=false`, so a typo in
      a workspace name cannot silently spawn a room.
- [ ] **Rate limits on** — `AGENTMESH_RATE_LIMIT=true`, so a looping agent
      throttles itself and a human still has budget to kick it.
- [ ] **Postgres, not memory** — `AGENTMESH_STORE=postgres` (required by auth).
- [ ] **Probes wired** — liveness `/healthz`, readiness `/readyz` (it pings the
      store, so a node with a dead database stops receiving traffic).
- [ ] **Metrics scraped** — `/metrics` (Prometheus text format).
- [ ] **Backups running** — see below.

## Deploy

### Docker

```bash
docker build -t agentmesh:local --build-arg VERSION=$(git describe --tags --always) .
docker run --rm -p 8080:8080 --env-file deploy/agentmesh.env agentmesh:local
```

The image is distroless and runs as non-root: no shell, no package manager.
Use `--entrypoint coord` for the CLI, and `agentmesh token …` for credentials.

### systemd

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin agentmesh
sudo install -m 0755 agentmesh coord /usr/local/bin/
sudo install -d -o root -g agentmesh -m 0750 /etc/agentmesh
sudo install -m 0640 -o root -g agentmesh deploy/agentmesh.env /etc/agentmesh/
sudo $EDITOR /etc/agentmesh/agentmesh.env      # DSN, auth, TLS
sudo cp deploy/agentmesh.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now agentmesh
```

The unit is hardened (`NoNewPrivileges`, `ProtectSystem=strict`,
`MemoryDenyWriteExecute`, restricted address families) — the server needs
nothing beyond a socket and its config.

### Reverse proxy

If you terminate TLS upstream, forward `X-Forwarded-Proto: https` so the A2A
agent card advertises the correct scheme. Proxy these paths:

| Path | Purpose | Auth |
|---|---|---|
| `/mcp` | MCP endpoint (agents) | required |
| `/ui`, `/ui/api` | dashboard (humans) | required (`/ui` shell is open) |
| `/.well-known/*` | agent card, OAuth metadata | **must stay open** — discovery |
| `/healthz`, `/readyz`, `/metrics` | ops | open to your infra only |

## Credentials

```bash
# opaque token for an agent (shown once)
agentmesh token create --workspace team --member backend --kind agent --ttl 720h
agentmesh token list   --workspace team
agentmesh token revoke --id tok_...          # immediate
```

In-band admission (no DB shell) once a room exists:

```bash
coord invite create --kind agent --max-uses 1 --ttl 24h   # prints the code once
coord room policy --join invite --broadcast moderators
```

With `AGENTMESH_AUTH=oauth`, humans authenticate with IdP-issued JWTs
(validated against the issuer's JWKS, audience-bound to this server's
canonical URI per RFC 8707) while agents keep opaque tokens. Clients discover
the authorization server at `/.well-known/oauth-protected-resource`.

## Migrations

Migrations are embedded in the binary and applied automatically on boot, under
a Postgres advisory lock — so rolling several replicas at once is safe: one
migrates, the others wait and find the work done.

**Policy: expand → migrate → contract.** Every migration to date is additive
(new tables/columns with defaults), which means a new binary can run against
the old schema and vice versa during a rollout. Keep it that way: never drop
or rename a column in the same release that stops using it. Ship the code that
tolerates both shapes first, then remove the column a release later.

Rollback is therefore a binary rollback — the schema stays compatible.

## Backup and restore

The store is the single source of truth (messages, tasks, memory, artifacts,
rooms, credentials). NATS is best-effort fan-out and holds nothing you cannot
lose.

```bash
# backup (nightly; keep off-host)
pg_dump --format=custom --no-owner "$AGENTMESH_DATABASE_URL" > agentmesh-$(date +%F).dump

# restore into an empty database
createdb agentmesh
pg_restore --no-owner --dbname agentmesh agentmesh-2026-01-01.dump
```

Test the restore path on a throwaway database before you need it. After a
restore, start the server once so any pending migrations apply.

**Rotating credentials after a leak:** revoke the tokens
(`agentmesh token revoke --id …` — takes effect immediately, no restart) and,
for a compromised agent, `coord mod ban --target <name>` so the name cannot
rejoin even with a fresh credential.

## Observability

`/metrics` exposes per-tool call counts, error counts and latency histograms,
HTTP responses by path and status, and uptime. Useful alerts:

- `rate(agentmesh_tool_errors_total[5m]) / rate(agentmesh_tool_calls_total[5m])`
  — a rising tool error ratio usually means a misconfigured client.
- `/readyz` failing — the database is unreachable; the node is live but should
  receive no traffic.
- A spike in `agentmesh_tool_calls_total{tool="broadcast"}` from one room —
  someone's agent is looping. Rate limits contain it; a human should kick it.

## Known limitations

- **Inbox delivery is at-most-once by default.** Use `ack_mode` (and
  `ack_messages`) for at-least-once; unacknowledged messages redeliver after
  `AGENTMESH_ACK_VISIBILITY`.
- **Single node.** Multiple replicas share a database safely (claims use
  `SKIP LOCKED`, migrations take a lock), but there is no leader election and
  no cross-node federation yet.
