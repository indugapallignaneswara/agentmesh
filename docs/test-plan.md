# AgentMesh master test plan

Black-box QA of every element through the real surfaces (coord CLI, HTTP, MCP),
independent of the unit/contract suites. Each element lists the behaviours to
verify; executors record PASS/FAIL with the actual output, plus enhancement
observations (UX friction, unclear errors, missing capabilities).

## Environments

| Env | Purpose | Config |
|---|---|---|
| E1 `:8080` | core flows, data layer, dashboard | memory store, auth off |
| E2 `:8102` | task lease/work-stealing | memory store, `AGENTMESH_TASK_LEASE=3s` |
| E3 `:8101` | authentication | postgres store, `AGENTMESH_AUTH=token` |

## Element 1 — Server surface & discovery (E1)
- `/healthz` returns `ok`; `/.well-known/agent-card.json` returns valid JSON
  with name/version/supportedInterfaces (URL host matches request), skills,
  and NO securitySchemes when auth is off.
- MCP `tools/list` exposes exactly 20 tools with descriptions.

## Element 2 — Membership, presence, messaging, events (E1)
- join (human/agent), re-join refreshes; presence lists active members; bad
  names rejected with readable errors.
- direct message → only the named recipient receives it, exactly once
  (consume-on-read); `sender_kind` tags human vs agent senders.
- broadcast reaches all others, not the sender; send-to-self rejected;
  unknown recipient rejected.
- publish_event + subscribe with cursor paging; events isolated per workspace.

## Element 3 — Task board incl. lease (E1 + E2)
- create (title required, dangling depends_on rejected), oldest-first claim,
  dependency gating, claimable=false on empty pool, complete with result,
  non-assignee completion rejected, status filters.
- E2: claim then wait past the 3s lease → another agent steals; the original
  assignee's completion is then rejected.

## Element 4 — Shared memory & artifacts & dashboard (E1)
- memory: private invisible cross-agent; shared quarantined until a human
  approves; agents denied queue/review; author self-review denied; reject
  stays hidden; approved is searchable by all; FTS relevance sane.
- artifacts: create v1, concurrent edit v2, stale write rejected with
  actionable guidance naming the current version, merge to v3, list.
- dashboard: `/ui` serves HTML; `/ui/api?workspace=` reflects presence/
  tasks/queue/artifacts; `since` cursor returns only new events; bad
  workspace → 400.

## Element 5 — Authentication (E3)
- no token → 401 + `WWW-Authenticate`; bogus/revoked/expired token → 401;
  `/healthz` + agent card stay open and the card advertises the bearer scheme.
- valid token: join/messaging work as the bound principal.
- enforcement: actor spoof, inbox theft, kind spoof (agent→human), workspace
  escape — all rejected with readable `forbidden` errors.
- `agentmesh token list/revoke` behave; revocation is immediate.

## Reporting format

Per test: `element / test / expected / actual / PASS|FAIL`. Conclude each
element with enhancement notes. A FAIL must include the exact command and
output.

## Results — QA pass (June 2026, Sonnet black-box agents)

All elements executed against the live surfaces. **Every functional check
passed** (E1/E2 ≈ 28, task board 20 + 13 adversarial probes, memory/artifacts/
auth/dashboard all green). Auth enforcement was probed hard — actor spoof,
kind spoof, inbox theft, workspace escape all rejected, and a rejected inbox
read left the message unconsumed (no side effect).

### Fixed as a result
- **CLI flags after a positional arg were silently ignored** (e.g.
  `coord memory search "q" --limit 1` ignored `--limit`, because Go's
  `flag.Parse` stops at the first positional). Fixed with `parsePositional`
  (interspersed parsing) across send/broadcast/memory write+search/task
  create/artifact put; regression test in `cmd/coord/parse_test.go`.
- **Dashboard `/ui/api` returned `"events": null`** when empty — now always
  `[]` (and all list fields normalised) so clients iterate without nil-guards.

### Logged as enhancements (not yet built)
- Memory **rejection note** isn't visible to the author (no status lookup).
- No task `cancel`/`reassign`/`priority`/`list --assignee`; no `list` paging.
- `WWW-Authenticate` emitted as `Www-Authenticate` (valid per RFC 7230, but
  non-canonical casing).
- Per-workspace event sequence numbers are sparse (global counter) — document
  or make per-workspace.

## Results — code audit pass (multi-agent, verified against source)

A 5-dimension audit (concurrency, security, correctness, protocol, product)
read the actual code. The adversarial-verify phase was cut short by a rate
limit, so findings were verified by hand against the source. Outcome:

### Fixed
- **Failed-dependency dead-end** (confirmed, also seen in QA): a task
  depending on a failed task was unclaimable forever. Added `RetryTask`
  (failed → pending, clears assignee/result/lease) in both stores, the
  `retry_task` MCP tool, `coord task retry`, and a `task_retried` event.
- **Postgres CreateTask store divergence** (confirmed): Postgres returned
  duplicate `DependsOn` and inserted the task before validating deps (so it
  would have accepted a self-dependency that the memory store rejects). Now
  dedupes and validates-before-insert, matching the memory store.
- **Unbounded write inputs** (confirmed DoS hygiene): message body, task
  details/result, event payload and agent card were uncapped while memory
  (16KB) and artifacts (256KB) were bounded. Added 64KB caps on each.

### Known limitation (documented, not fixed)
- **Postgres event-cursor commit-order race**: `events.seq` is a bigserial
  drawn at INSERT but rows become visible at COMMIT, so under concurrent
  appends a poller can advance its cursor past a lower seq that commits later
  and skip it. This affects only the observation log (`subscribe`), never
  inbox delivery or task claiming. A safe-watermark cursor is the proper fix.

## Roadmap — "Discord for agents, humans control the rooms"

The product-lens finder surfaced the gap between "coordination workspace" and
that vision. Ranked by impact, all grounded in current code:

1. **Room lifecycle** — workspaces are implicit (a typo makes a new room, none
   can be closed/listed). Add a `workspaces` table + `room_create/close/list`.
2. **Moderator role + moderation** — only kind (human/agent) exists; no role,
   no kick/ban/remove-member. Add `role` (owner/moderator/member), a `bans`
   table, `RemoveMember`, and `room_kick/ban/set_role` (human-gated).
3. **Human message history** — reads are consume-once per recipient; humans
   controlling a room can't see the conversation. Add `ListMessages` +
   `message_history` (human/moderator-gated) + a dashboard Messages panel.
4. **Invites / join-requests** — joining is open (auth off) or out-of-band
   (token CLI). Add an `invites` table + `room_invite`/`join`-with-code.
5. **Channels/threads** — one flat firehose per room. Add an optional
   `channel` field + `channels` table; broadcast scoped to channel members.
6. **Rate limiting** — no flood control; an agent loop can spam broadcasts.
   Add a per-principal token bucket in the middleware.
7. **Explicit leave + richer presence** — no leave/kick; departed members
   accrue undelivered rows forever. Add `workspace_leave` + status field.
