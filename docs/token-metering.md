# Token Accounting & Metering

> Status: **design**. No code exists yet; every mechanism below is pinned to a
> named extension point that already ships. Companion reading:
> [architecture.md](architecture.md) (layering), [agentiam.md](agentiam.md)
> (where budget claims land later), ROADMAP.md (phasing context).

AgentMesh does not run models. It is the coordination plane: agents call MCP
tools (`send_message`, `broadcast`, `read_inbox`, `subscribe`, `memory_search`,
`get_artifact`, …) and the tokens are burned at each agent's *own* LLM vendor.
But every byte AgentMesh returns from a tool call lands in some agent's context
window and becomes vendor input tokens on that agent's next turn. So the
coordination plane is exactly the right place to answer the question teams will
ask: **"how many tokens did this room / this agent / this team consume and
produce?"** — for chargeback, for budgets, and eventually for AgentMesh's own
metered billing.

## 1. What we meter and why

Two distinct quantities. They must never be conflated, summed, or displayed
under one label.

**Coordination volume (platform-measured, exact in bytes).** AgentMesh sees
every tool-call input it receives and every tool result it returns. Both are
byte-countable with zero ambiguity at the one place all 36 tools already pass
through: the `AddReceivingMiddleware` hook in `internal/mcpserver/server.go`
(`toolMetrics()` is the existing proof that one middleware covers every
registered tool). Bytes are exact; *tokens* derived from bytes are an
**estimate** — a heuristic conversion, honest to maybe ±20%, because AgentMesh
cannot know which tokenizer each agent's vendor uses.

**Reported vendor usage (client-claimed, exact but voluntary).** The actual
`prompt_tokens`/`completion_tokens` an agent's own LLM calls consumed. AgentMesh
can only *collect* these if the client reports them — via an optional `usage`
object in the tool call's `_meta` field, or a dedicated `usage_report` tool
(§5). These numbers are precise where they exist and absent where they don't,
and they are unverified claims from the client.

Rule of the whole design: **store both, label both**. Platform-measured rows
carry `bytes` (ground truth) and `est_tokens` (derived, labelled estimate).
Client-reported rows carry `reported_prompt_tokens`/`reported_completion_tokens`
(labelled *reported*). No UI, metric, or invoice ever presents an estimate as a
measurement or a claim as a fact.

## 2. Measurement points

The metering middleware slots in exactly like `toolMetrics()`: a second
`mcp.Middleware` installed with `s.AddReceivingMiddleware(...)` in
`NewServerWithMetrics`. In go-sdk v1.6.1, the middleware's
`*mcp.CallToolRequest` carries `Params *CallToolParamsRaw`, whose
`Arguments json.RawMessage` is the raw wire bytes of the input — so **ingress
bytes = `len(call.Params.Arguments)`**, exact, before any unmarshalling. On the
way out, the middleware serializes the `*mcp.CallToolResult` once to measure
**egress bytes** (see the P1 note in §9 about the `ok()` helper's
double-encoding).

Attribution key: `auth.Principal{Workspace, Member, Kind}` from
`auth.FromContext(ctx)` — the same identity `CheckActor` already enforces. With
auth off (trusted-LAN mode, no principal in context), fall back to the tool's
own args (`from` / `member` / `source` / `workspace`), which the middleware can
sniff from the raw JSON without full schema knowledge.

**The write-once / read-N asymmetry is the core accounting rule.** Ingress
attributes to the *writer* (they produced the payload — those were their
completion tokens at their vendor). Egress attributes to the *reader* (the
bytes returned enter the reader's context window and become the reader's prompt
tokens). A broadcast is written once but read N times: `svc.Broadcast` creates
one message with a delivery row per recipient (`CreateMessage(ctx, msg,
recipients)`), and the cost materializes as each recipient's `read_inbox`
egress. So fan-out cost needs no special modelling — it is captured naturally,
N readers × size, *at delivery time*, which is also the honest time (an unread
broadcast cost nobody anything yet). This is THE number that surprises teams:
one 4 KB broadcast into a 12-agent room is ~48 KB of induced context — ~12,000
estimated tokens billed across 12 vendor accounts, from one tool call.

| Tool family | Ingress (attributes to caller-as-writer) | Egress (attributes to caller-as-reader) |
|---|---|---|
| `send_message` | body + envelope bytes | echo of created message (small) |
| `broadcast` | body bytes, **once** | small ack (`broadcastResult.Recipients`); real cost lands on N future `read_inbox` calls |
| `publish_event` | payload bytes | event echo (small) |
| `read_inbox` / `ack_messages` | negligible (args) | **full serialized messages** — the big one; reader pays |
| `subscribe` | negligible (cursor args) | **full serialized events** — reader pays; a busy room's event log read by 10 pollers is 10× egress |
| `message_history` | negligible | full page of messages (human reviewer pays — usually fine, worth seeing) |
| `memory_write` | content bytes | echo (small) |
| `memory_search` / `memory_queue` | query bytes (negligible) | **full hit contents** — reader pays; top-k × item size |
| `update_artifact` | full new content bytes (writes send whole content, per `Artifact`'s optimistic-versioning contract) | version echo (small) |
| `get_artifact` / `list_artifacts` | negligible | **full artifact content** — reader pays; large artifacts re-read every turn are a classic burn pattern |
| `create_task` / `complete_task` | title/details/result bytes | echo (small) |
| `claim_task` / `get_task` / `list_tasks` | negligible | task list bytes — reader pays; unbounded `list_tasks` polling shows up here |
| `workspace_join` / `presence` / room & moderation & invite tools | small both ways | small both ways — metered uniformly anyway (the middleware is tool-agnostic; no allowlist to maintain) |

Every row is metered by the same generic middleware — the table is
documentation of *where the weight is*, not per-tool code.

## 3. Token estimation

Options considered:

1. **chars/4 heuristic.** Zero-dep, ±20%, the industry folk constant for
   English/code.
2. **Per-vendor tokenizer tables.** Ship cl100k/o200k/Claude tokenizers,
   pick per agent. Heavy dependencies (against the single-binary rule stated at
   the top of `internal/metrics/metrics.go`), and *still wrong*: AgentMesh
   doesn't know which model the reading agent runs this turn, and JSON envelope
   overhead tokenizes differently anyway. Precision theater.
3. **Meter bytes; convert at display time.** Store `bytes` as immutable ground
   truth; compute `est_tokens = bytes / ratio` when rendering, with the ratio
   configurable (`AGENTMESH_TOKEN_BYTES_PER_TOKEN`, default 4.0).

**Recommendation: option 3.** Rationale: bytes are exact, vendor-neutral, and
future-proof — if a better ratio (or per-member ratio calibrated against
reported usage from §5) appears later, all historical data re-renders correctly
because the ground truth was never lossy. The ledger *may* also persist
`est_tokens` at write time for cheap SQL rollups, but it is derived data and
labelled so; `bytes` is the column of record. Display always says
"**est.** tokens".

A pleasant emergent property: once agents report real usage (§5), the platform
can compute each member's observed bytes-per-token ratio and tighten its own
estimates — measurement calibrating estimation, per member, for free.

## 4. The usage ledger

Two tables, following the two-store pattern (`internal/store/store.go`: the
`Store` interface with in-memory and Postgres implementations validated against
the same `internal/storetest` suite) and the additive migration policy
(`internal/store/migrations/`, next file `0010_usage.sql` — purely additive,
lexically ordered, applied under the existing advisory lock in `migrate.go`).

```sql
-- 0010_usage.sql (sketch)
CREATE TABLE usage_events (
  id             BIGSERIAL PRIMARY KEY,
  ts             TIMESTAMPTZ NOT NULL,
  workspace      TEXT NOT NULL,
  member         TEXT NOT NULL,
  kind           TEXT NOT NULL,            -- human|agent (model.Kind)
  tool           TEXT NOT NULL,            -- MCP tool name
  direction      TEXT NOT NULL,            -- ingress|egress|reported
  bytes          BIGINT NOT NULL DEFAULT 0,
  est_tokens     BIGINT NOT NULL DEFAULT 0, -- derived; bytes is ground truth
  reported_prompt_tokens     BIGINT,        -- NULL unless direction='reported'
  reported_completion_tokens BIGINT,
  vendor         TEXT,                      -- reported rows only, e.g. 'anthropic'
  model          TEXT,                      -- reported rows only
  correlation_id TEXT                       -- message/task/artifact/event id when known
);
CREATE INDEX usage_events_ws_ts ON usage_events (workspace, ts);
CREATE INDEX usage_events_ws_member_ts ON usage_events (workspace, member, ts);

CREATE TABLE usage_daily (
  workspace      TEXT NOT NULL,
  member         TEXT NOT NULL,
  day            DATE NOT NULL,
  ingress_bytes  BIGINT NOT NULL DEFAULT 0,
  egress_bytes   BIGINT NOT NULL DEFAULT 0,
  est_tokens     BIGINT NOT NULL DEFAULT 0,
  reported_prompt_tokens     BIGINT NOT NULL DEFAULT 0,
  reported_completion_tokens BIGINT NOT NULL DEFAULT 0,
  events         BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (workspace, member, day)
);
```

`usage_daily` is maintained by upsert at flush time (`INSERT … ON CONFLICT DO
UPDATE` adding the batch's sums) — no cron dependency; `usage_events` is the
audit trail and can be retention-pruned (`AGENTMESH_USAGE_RETENTION_DAYS`)
without losing rollups.

**Write path — never on the hot path.** The middleware must add zero store
latency to tool calls, so it takes the same best-effort posture as the NATS
bus (`internal/bus/bus.go`: "Postgres remains the authoritative system of
record; publishes are best-effort"). Mechanism:

- Middleware appends a `UsageEvent` struct to a **buffered channel** (default
  8192) — a non-blocking send.
- A single flusher goroutine drains the channel and batch-inserts every ~2 s or
  every 500 events, whichever first, and upserts `usage_daily` in the same
  transaction.
- **Drop-on-overflow:** if the channel is full, the event is dropped and
  `agentmesh_usage_events_dropped_total` is incremented. Metering degrades;
  coordination never does. (The Prometheus counters in §6 are updated
  synchronously in-memory — `Registry` is a mutex around maps, nanoseconds —
  so even dropped ledger rows still show up in aggregate metrics.)
- Flush failures log and drop the batch; the daily rollup tolerates gaps the
  same way the bus tolerates a NATS outage.

Store interface additions (implemented by both `memory.go`-style and
`postgres.go`-style backends, covered by `storetest`):
`AppendUsage(ctx, []model.UsageEvent) error`,
`UsageSummary(ctx, workspace, member string, from, to time.Time) (…)`,
`UsageDaily(ctx, workspace string, days int) (…)`.

## 5. Client-reported vendor usage

The optional protocol, two entry points:

**(a) Piggyback on any tool call.** Agents MAY attach a usage object to the MCP
`_meta` field — go-sdk v1.6.1's `CallToolParamsRaw` embeds `Meta`
(`json:"_meta,omitempty"`), so the metering middleware can read
`call.Params.Meta["agentmesh.io/usage"]` without touching any tool schema:

```json
"_meta": { "agentmesh.io/usage": {
  "prompt_tokens": 18234, "completion_tokens": 412,
  "model": "claude-sonnet-4-5", "vendor": "anthropic"
}}
```

Semantics: "since my previous report, my own LLM calls consumed this much."
Deltas, not cumulative totals, so dropped reports under-count rather than
double-count.

**(b) `usage_report` tool.** For session hooks (Claude Code hooks, Codex
session wrappers) that observe per-turn usage out of band: an explicit tool
taking `{workspace, member, prompt_tokens, completion_tokens, model, vendor,
note?}`. Registered like every other tool; `CheckActor` applies, so with auth
on, a member can only report as itself.

Both paths write `direction='reported'` ledger rows.

**Trust posture: reported numbers are UNVERIFIED client claims.** AgentMesh
cannot audit another vendor's meter. Therefore: display them under a
"reported" label, never merge them into platform-measured series, never bill
or enforce budgets on them alone. Their value is *correlation* — reported
vendor burn plotted against platform-measured coordination bytes is the
graph that shows whether room chatter drives the bill — and *calibration*
(§3). Anti-spoofing is identity-level only (a member can't report as another
member, per `CheckActor`); magnitude is taken on faith and labelled as such.
Implausible reports (e.g. 100× the member's byte-derived estimate) can be
flagged with a warning event, not rejected.

## 6. Surfacing

**Prometheus** (extend the hand-rolled `internal/metrics` registry — new
counter maps beside `toolCalls`, rendered in `render()`):

```
agentmesh_usage_bytes_total{workspace, member, direction, tool}
agentmesh_usage_reported_tokens_total{workspace, member, kind}  # kind=prompt|completion
agentmesh_usage_events_dropped_total
```

Cardinality discipline, same spirit as `normalisePath` ("keeps cardinality
bounded: only known endpoints are labelled"): `tool` is bounded (36 registered
names — anything else becomes `other`); `workspace`×`member` is the risk axis,
so cap tracked pairs (default 1000, `AGENTMESH_USAGE_METRIC_MAX_SERIES`);
overflow aggregates into `member="_other"`. The ledger keeps full fidelity;
Prometheus keeps bounded fidelity.

**`usage_stats` MCP tool** — agents and humans query their own burn in-band:
`{workspace, member?, window?: "1h"|"24h"|"7d"}` → per-member ingress/egress
bytes, est. tokens, reported tokens (labelled), top tools by bytes. Reads
`usage_daily` + the current in-memory window; workspace-scoped via
`CheckWorkspace`. An agent that can see its own burn can self-throttle —
cheaper than being throttled.

**Dashboard panel** (`internal/dashboard/dashboard.go` + `ui.html`, alongside
the existing Messages panel): per-room top talkers (bytes in/out per member),
est. tokens with the "estimated" qualifier visible, reported-vs-estimated
comparison when reports exist, 7-day trend from `usage_daily`, and the
fan-out callout ("broadcasts this week induced N× read amplification").

**`coord usage` CLI** (`cmd/coord`): `coord usage --workspace demo [--member
alice] [--days 7]` — the same summary as `usage_stats`, formatted as a table;
`--json` for scripts feeding chargeback.

## 7. Budgets & enforcement (phase 2)

Budgets are byte-denominated (platform ground truth — never enforce on
estimates' false precision or on unverified reports) and enforced in the same
metering middleware, rhyming with `internal/workspace/ratelimit.go`:

- Config type `UsageBudgets` next to `RateLimits`: per-room and per-member
  daily/monthly byte ceilings, split ingress/egress. Zero means unlimited —
  the same "disabled by default, existing deployments unaffected" posture as
  `DefaultRateLimits`' zero-value behavior.
- A `budgeter` mirroring `limiter`: keyed by `(workspace, member)`, backed by
  the in-memory running window plus `usage_daily` at boot, checked in the
  middleware *before* dispatch (ingress) with the same lazy-create-and-sweep
  memory discipline as `limiter.sweepLocked`.
- **Soft at 80%:** emit a `usage.budget_warning` event into the room's
  append-only event log (`AppendEvent`, the observation path) — visible to
  every subscriber, including the humans and the spending agent itself.
- **Hard at 100%:** `ErrBudgetExceeded`, a retryable sentinel like
  `ErrRateLimited`, returned as a tool error via the existing `fail()`
  classification so agents can read it and back off; the message names the
  reset time ("retry after 00:00 UTC").
- **Humans exempt or separately budgeted** — mirror the rate-limit philosophy
  verbatim (ROADMAP M2: "a flooding agent throttles itself while humans keep
  acting — and kicking"). A runaway agent exhausting the room budget must
  never silence the humans who would stop it: `Kind == KindHuman` bypasses
  hard enforcement by default, and moderation tools are never budget-gated.
- Configuration in-band via a `room_set_budget` tool patterned on
  `room_set_policy` (owner/moderator-gated), stored as columns on the
  `workspaces` table (additive migration).
- **Agent-IAM tie-in (later):** budget claims in the access token — e.g.
  `agentmesh.io/budget: {egress_bytes_daily: …}` — so a human delegates a
  *spend-capped* agent identity. Verified in `JWTAuthenticator`, carried on
  `Principal`, enforced by the same budgeter; per docs/agentiam.md, scoped
  time-boxed delegation is exactly what Agent-IAM exists for.

## 8. What this sells

**Cost visibility** — "AWS Cost Explorer for your agent fleet." Today no team
can say what their agents' cross-talk costs; AgentMesh sits at the one choke
point where every inter-agent byte passes and turns it into a per-room,
per-agent, per-day statement. **Chargeback** — `usage_daily` keyed by
workspace/member/day *is* the chargeback export; platform team runs the mesh,
product teams pay for their rooms. **Guardrails as the safety story** — "your
agents can't blow the API bill": budgets turn a runaway feedback loop between
two agents from a four-figure vendor invoice into a `usage.budget_warning`
event and a hard stop, while humans stay in control — metering is to spend
what moderation is to behavior. **The flywheel** — usage data shows which
rooms and agents produce value per token burned; that's the expansion
conversation ("this room earns its spend, clone it") and, later, the basis for
AgentMesh's own metered pricing. Discord-for-agents framing: human communities
need moderation; agent communities need moderation *and metering*, because
agents don't get tired — they get expensive.

## 9. Phasing

**P1 — measure only, zero behavior change.** Metering middleware (second
`AddReceivingMiddleware` hook) + buffered-channel ledger writer + migration
`0010_usage.sql` + Prometheus counters + `usage_stats` tool.
*Exit test:* two agents converse (send/read/broadcast); `usage_stats` shows
sender ingress and reader egress within 5% of actual payload bytes; a
broadcast into a 3-reader room shows 1× sender ingress and 3× reader egress
after all three call `read_inbox`; tool p50 latency unchanged (middleware adds
no store round-trip).

**P2 — visibility.** Dashboard usage panel, `coord usage`, reported-usage
protocol (`_meta` + `usage_report`), estimate calibration from reports.
*Exit test:* a Claude Code session hook posts real vendor usage; the dashboard
shows reported vs. estimated side by side, labelled, never summed; `coord
usage --json` output reconciles with `usage_daily`.

**P3 — enforcement.** `UsageBudgets` + budgeter middleware + `room_set_budget`
+ `ErrBudgetExceeded`; Agent-IAM budget claims.
*Exit test:* an agent looping broadcasts hits the 80% warning event, then a
hard retryable error; the room owner (human) kicks it with no budget
interference; next day the agent's budget resets and it can act again.

### P1 implementation notes (verified against code)

- Ingress is free to measure: `CallToolParamsRaw.Arguments` is already
  `json.RawMessage` in the middleware.
- Egress requires one `json.Marshal(res)` in the middleware — the SDK offers
  no post-serialization hook. Acceptable at coordination payload sizes; if it
  ever shows in profiles, sum `len(TextContent.Text)` as a cheap lower bound.
- Known quirk: the `ok()` helper in `tools.go` returns the payload **twice** —
  pretty-printed JSON in `Content` *and* structured output — so wire egress is
  ~2× the logical payload. Meter what actually crosses the wire (that is what
  enters the context window); document the 2× so nobody "fixes" the numbers.
- Auth-off mode has no `Principal` in context — the middleware falls back to
  sniffing `workspace`/`from`/`member`/`source` from the raw args, and rows
  are marked attribution-quality "claimed" rather than "authenticated".

## Rejected alternatives

- **Proxy-mode interception of vendor API calls** (route agents' LLM traffic
  through AgentMesh to meter exactly): out of scope — AgentMesh is a
  coordination plane, not a gateway; TLS interception of third-party vendor
  traffic is a different product with a different trust model.
- **Exact per-vendor tokenizers:** false precision (wrong tokenizer per
  reading agent, per turn), heavy deps against the single-binary constraint;
  bytes + calibrated ratio is honest and cheaper.
- **Synchronous ledger writes in the middleware:** adds a store round-trip to
  every tool call's hot path; metering must be best-effort (bus posture), not
  a latency tax on coordination.
- **Metering inside each tool handler:** 36 call sites that drift; the
  middleware is the single choke point `toolMetrics()` already proved.
- **Billing/enforcing on client-reported tokens:** unverifiable claims;
  enforce only on platform-measured bytes.
