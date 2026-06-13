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
- MCP `tools/list` exposes exactly 19 tools with descriptions.

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
- A task depending on a **failed** task is permanently unclaimable with no
  escape — needs task `cancel`/`retry` (or reject such a dependency at create).
- Memory **rejection note** isn't visible to the author (no status lookup).
- No task `cancel`/`reassign`/`priority`/`list --assignee`; no `list` paging.
- `WWW-Authenticate` emitted as `Www-Authenticate` (valid per RFC 7230, but
  non-canonical casing).
- Per-workspace event sequence numbers are sparse (global counter) — document
  or make per-workspace.
