# Shared-Workspace Coordination for AI Coding Agents: Landscape Survey & Architecture Blueprint

**Bottom line up front:** Build this as a **single self-hosted "coordination workspace" exposed primarily as a Streamable-HTTP MCP server**, backed by **NATS JetStream (event bus)**, **Postgres + pgvector (shared memory, task state, locks, audit)**, **Yjs CRDTs (co-edited shared artifacts)**, and a thin **per-agent adapter layer** (MCP tools + session hooks + a CLI fallback) that plugs into existing Claude Code, Codex, Cursor, and Aider sessions without replacing them. No existing open-source project delivers your specific combination — *cross-machine, multi-human, multi-vendor, any-to-any addressing through a shared blackboard* — so the gap is real, but every layer should reuse mature infrastructure rather than inventing protocols. The genuinely hard problems are not the plumbing (messaging, vector search, locks are solved) but **shared-memory consistency, cross-agent prompt-injection/poisoning propagation, and reliable task-claim semantics across heterogeneous agents that don't share a runtime.**

## TL;DR

- **Reuse, don't reinvent the protocol layer.** MCP is the de-facto agent-to-tool standard (Linux Foundation-governed, first-class in Claude Code, Codex, Cursor, Windsurf, OpenCode, Goose as of 2026) and is the right *integration surface* — every target agent can register one MCP server as a shared tool. A2A (agent-to-agent, 150+ supporters) and ACP (REST-native) are complementary discovery/delegation standards to add later. But none of MCP/A2A/ACP provide pub/sub broadcast, shared semantic memory, or blackboard state — you must build those coordination semantics yourself on NATS JetStream + Postgres.
- **The closest prior art validates the gap rather than filling it.** Claude Code's "Agent Teams" (Feb 2026) implements your exact shared-task-list + mailbox + any-to-any model — but is **Claude-only, single-machine, single-lead, filesystem-based**. CCB/`claude_code_bridge` is **multi-vendor but local tmux-only**. ruflo's "federation" adds cross-machine WebSocket transport but stays Claude-centric and is largely alpha/stubbed. Google Scion (April 2026) is the strongest overall match (cross-vendor, cross-machine, shared workspace, broadcasts) — study it as a reference.
- **Adopt an event-driven blackboard, not direct agent-to-agent links.** Your instinct (agents talk to a shared workspace, not each other) is the correct, well-established pattern (blackboard architecture, Hearsay-II, 1970s). It decouples agents, enables many-to-many fan-out and asynchronous participation, and lets you add/remove agents freely. Pair it with optimistic per-key locking (Postgres `SELECT … FOR UPDATE SKIP LOCKED` / advisory locks) to prevent duplicated work.

---

## A) Protocols & Standards: What Exists and Where It Falls Short

**Model Context Protocol (MCP).** Open standard from Anthropic (Nov 2024), JSON-RPC 2.0 over two current transports: **stdio** (client launches server as subprocess; local, single-user, most common/interoperable) and **Streamable HTTP** (introduced spec 2025-03-26, replacing deprecated HTTP+SSE; single POST/GET endpoint, optional SSE streaming, `Mcp-Session-Id` sessions, multi-client, auth, resumability — the transport for a self-hosted multi-client server). Architecture is **host → client → server** (one client per connected server). Critically, **MCP is a 1-host-to-N-servers tool-access protocol, not agent-to-agent or pub/sub** — no native broadcast, topics, shared state, or discovery. The MCP community itself acknowledges stateful sessions conflict with stateless load balancing and recommends "externalize session events to a distributed pub-sub system" (GitHub issue #2000); AI21 had to *extend* MCP with custom workspace primitives because "stateful execution was left implicit." **Implication: MCP is your integration surface; coordination semantics live behind it on real infrastructure.**

**Agent2Agent (A2A).** Google, announced April 9 2025, donated to Linux Foundation June 2025 (Apache 2.0), 150+ supporters by 2026. HTTP/SSE/JSON-RPC 2.0; **Agent Cards** (JSON capability advertisements) for discovery; task lifecycle states; OAuth/API-key/mTLS. Connects *agents to agents* (complements MCP's agents-to-tools). Good for **discovery/addressing** in your network; still early-adoption. Support as a secondary interface (publish Agent Cards), not core transport.

**Other standards.** **ACP** (IBM/BeeAI + AGNTCY; Linux Foundation July 2025) — REST/OpenAPI-native ("if you can build a REST API, you can build an ACP agent"), merging toward A2A. **AGNTCY/Agent Connect** (Cisco "Internet of Agents" + OASF schema). **ANP** (peer-to-peer, **W3C DID identity**, JSON-LD, W3C working group). **Identity:** zero-trust agentic frameworks (arXiv 2505.19301) and W3C DIDs are the emerging direction.

**Net assessment:** The space consolidated by Q1 2026 to **MCP (tools) + A2A (agent-to-agent) + ACP (REST alternative)**. None provide pub/sub, persistent shared semantic memory, or blackboard state — exactly the layers you must supply.

---

## B) Orchestration Frameworks & the Blackboard Pattern

**Coordination models.** **LangGraph** — graph-based state machines, explicit/inspectable/checkpointed state, most production-mature (Klarna, LinkedIn, Uber), scales linearly; borrow its *persisted, inspectable shared state*. **CrewAI** — role-based + event Flows, fast prototyping, overhead grows past ~5 agents. **AutoGen/AG2** — conversational, scales poorly (each agent multiplies turns), but is the one framework noted to support "distributed agents across processes or machines." **OpenAI Agents SDK, Semantic Kernel/Agent Framework, Letta** — centralized/single-runtime. **Microsoft Agent Framework** uses MCP as a shared context interface with persistent session state — a useful MCP-centered reference. The dominant pattern everywhere is a **centralized orchestrator or conversational handoff** — neither matches your independent, heterogeneous, cross-machine, any-to-any requirement.

**The blackboard / shared-workspace pattern (your model — and the right one).** Dating to Hearsay-II (1970s): independent "knowledge sources" read/write a shared structure; control logic decides what's next. Properties you need: **indirect asynchronous communication** (agents communicate *through* the board), **decoupling** ("agents only know about the blackboard," so add/remove freely — your many-to-many requirement), **opportunistic non-linear solving**, and **concurrency with per-key locks/optimistic concurrency**. Confluent's event-driven multi-agent patterns show how to make the blackboard event-driven directly, removing point-to-point paths — adopt this **event-driven blackboard** synthesis. Reference: **claudioed/agent-blackboard** (blackboard + MCP for SWE agents, single-machine). (Treat vendor "2–4× faster" claims as marketing, not verified.)

**Takeaway:** Borrow LangGraph's explicit persisted shared state and the blackboard's decoupled indirect-communication model over an event bus. Do **not** adopt a centralized-orchestrator framework as your core.

---

## C) Real-Time Messaging & Event Bus (Self-Hostable)

| Option | Footprint/ops | Persistence & replay | Pub/Sub & fan-out | Fit |
|---|---|---|---|---|
| **NATS + JetStream** | ~20 MB Go binary, 10–50 MB RAM, trivial cluster | JetStream: durable streams, at-least/exactly-once, replay | Native subject hierarchies, queue groups, request-reply, scatter-gather, **account-based multi-tenancy** | **Strongest fit** |
| **Redis Streams/PubSub** | Tiny if Redis already present, sub-ms | Streams = durable log + consumer groups; PubSub ephemeral | Consumer groups; must stitch PubSub+Streams+Cluster | Good if Redis in stack; doubles as lock/cache |
| **Kafka** | Heavy (multi-broker, partition planning) | Best durability/retention/replay | Partitions; ordering per-partition | **Overkill** |
| **RabbitMQ** | Moderate, vhost tenancy | Durable queues | Exchanges/routing | Heavier than NATS |
| **MQTT** | Very light | Broker-dependent | Topic pub/sub | IoT-oriented, thin semantics |
| **Centrifugo** | Single Go binary, purpose-built client pub/sub (WS/SSE/HTTP-stream/WebTransport/gRPC) | Channel history, reconnect recovery, presence; scales on Redis/NATS/Postgres | Excellent fan-out, channel namespaces, JWT | **Best for human/UI layer** |

**Recommendation:** **NATS JetStream as the core agent bus** — one ~20 MB binary, low latency, subject hierarchies mapping to `workspace.{id}.events`, `workspace.{id}.tasks`, `workspace.{id}.agent.{name}.inbox` (broadcast *and* any-to-any direct addressing), durable streams for replay/late-joiners, account-based per-workspace tenancy, and request-reply for cheap "ask agent X." **Add Centrifugo for the human/UI real-time layer** (presence "who's in the workspace," history, reconnect). For a minimal MVP, stream events to humans over the MCP server's SSE or a simple WebSocket and skip Centrifugo. Avoid Kafka unless you later need long-retention event sourcing across many workspaces.

---

## D) Shared Semantic / Persistent Memory (the hardest, most differentiating layer)

**Vector store (self-hostable).** **pgvector** is the recommendation — "if your app already uses Postgres, pgvector is often the right choice; no separate DB" — co-locating semantic memory with task state, locks, and provenance in one transactional system (write a memory item and its provenance atomically); handles ~50M vectors (pgvectorscale: 471 QPS @ 99% recall on 50M). Escalate to **Qdrant** (Rust, best price-performance, $30/mo VPS for 10M+ vectors) if vector search becomes the bottleneck. **Weaviate** (hybrid search), **Chroma** (prototyping), **Milvus** (billions, overkill), **LanceDB** (embedded/local) round out options.

**Agent-memory frameworks.** **Letta/MemGPT** (OS-style tiered memory, agent self-manages, fully self-hostable; overkill if you just want "add memory"). **Mem0** (~48K★, user/session/agent scopes, self-edits dupes; graph behind $249/mo Pro; open-source core self-hostable). **Zep/Graphiti** (Apache-2.0, temporal knowledge graph timestamping every fact; **requires Neo4j**; token-heavy). **Cognee** (local-first, doc-ingestion + graph). **Markdown-vault + semantic index** path — the only one where "human-agent merge" (humans editing a folder / merging a PR) is first-class; Fountain City's 2026 analysis notes **no framework natively implements human-agent merge — all assume the agent has direct memory write authority.** For a developer tool, a **Git-backed shared knowledge store is a serious option.**

**Avoiding clobbering.** Give each agent a **private scratch namespace + a shared workspace namespace**; only promote vetted facts to shared. Security guidance: "implement agent-specific memory partitions with controlled synchronization points." Keep an **append-only episodic log** (events, naturally conflict-free) separate from **semantic memory** (distilled facts needing careful merge). Model decisions/artifacts as a **temporal knowledge graph** so contradictions are versioned, not overwritten.

**CRDTs for co-edited artifacts.** For shared *editable* state (living design doc, shared task board), **Yjs** (dominant/fastest CRDT, Y.Map/Y.Array/Y.Text, Awareness presence protocol, network-agnostic, Rust port) or **Automerge** (JSON-doc CRDT, Rust/WASM, automerge-repo sync) merge concurrent human+agent edits without locks. 2026 development: AI agents are now run as **first-class CRDT peers** (Electric's server-side Yjs agent with its own cursor/streaming edits). Reserve CRDTs for genuinely co-edited mutable state; use the event log + Postgres for append-only/transactional data.

**Recommendation:** **Postgres + pgvector** as unified backbone (semantic memory, task state, provenance); **append-only episodic event log** (JetStream/Postgres); **per-agent + shared namespaces, strictly partitioned**; **Yjs CRDTs for co-edited artifacts**; Letta/Mem0 as optional pluggable engines an agent *may* use, not the system of record. Strongly consider a **Git-backed shared knowledge layer** so humans review agent memory writes via PR.

---

## E) Task Coordination & Distributed State

**Duplicate prevention — use Postgres primitives (no heavy infra needed).** **`SELECT … FOR UPDATE SKIP LOCKED`** — canonical work-queue; Postgres hands different rows to concurrent workers and skips locked ones, so no two agents claim the same task. **Advisory locks** (`pg_try_advisory_lock`, two-arg form `pg_try_advisory_lock(workspace_id, task_id)` for namespacing) — "only one worker processes each job"; **session-level locks auto-release on connection end** (crash-safe). Libraries (`go-pglock`, `pals`) need "only PostgreSQL — no Redis, ZooKeeper." Model tasks with explicit states (pending → claimed → completed/failed), `assigned_agent`, dependencies, and a **`claim_expires_at` lease** so dead agents' tasks return to the pool (work-stealing); emit `tasks.{ws}.claimed` events so all participants see claims instantly.

**Leader election / distributed locks** — only if you scale to multiple server replicas (use Postgres advisory locks, or etcd/Redis). Unnecessary for a single self-hosted node.

**Durable workflow engines (Temporal, Restate) — useful later, overkill for MVP.** Temporal (open-source, ex-Uber Cadence; OpenAI/Snap/Netflix/JPMorgan customers; powers Mistral Workflows) guarantees crash-proof workflows via deterministic replay (Workflow = orchestration, Activities = LLM/tool calls). **But** your externally-running Claude Code/Codex sessions can't be rewritten as Temporal workflows, and it adds significant ops weight. **Start with Postgres task state + leases.** Introduce Temporal/Restate only when you build *first-party orchestration agents* needing long-running, durable, exactly-once recovery inside your system.

---

## F) Plugging Into Existing Agent Sessions

**MCP support status (2025–2026).** **Claude Code** — first-class MCP (stdio + Streamable HTTP/SSE) plus hooks (PreToolUse/PostToolUse/SessionStart/SessionEnd), skills/slash commands, subagents, plugins, Agent SDK; `claude mcp add`; works headless (`claude -p`). **Codex CLI** — first-class MCP with parallel tool calls, `~/.codex/config.toml` `[mcp_servers.*]`, experimental hooks, OAuth for HTTP (the 2025 experimental flag is gone). **Cursor** — first-class via `mcp.json` `mcpServers` (free tier can't add custom servers). **Aider** — weaker/partial MCP; integrate via **CLI/wrapper** or scripting API. **Ecosystem (May 2026):** MCP is "boringly universal" (URL + OAuth + JSON); most top agents support `mcpServers` — so **one Streamable-HTTP MCP server can be registered by every developer's agent on every machine to reach the same workspace.**

**Recommended surface: one MCP "coordination workspace" server** exposing a small stable toolset: `workspace_join`/`workspace_presence` (identity + A2A-style Agent Card), `publish_event`/`subscribe`, `send_message(to_agent)`/`read_inbox` (**any-to-any addressing**), `broadcast` (many-to-many), `create_task`/`claim_task`/`update_task`/`list_tasks` (SKIP LOCKED), `memory_write(scope)`/`memory_search`, `get_artifact`/`update_artifact` (Yjs). Because MCP can't *push* mid-turn, complement with **session hooks** (Claude Code SessionStart/PostToolUse, Codex hooks) that **pull** inbox messages/events into context at boundaries, and **slash commands/skills** (`/ask @backend-agent …`, `/sync`) for ergonomic human-to-agent addressing.

**Approaches compared:** (1) **Shared MCP server** — primary; native everywhere, zero agent modification, one endpoint for all machines/users; con: pull-not-push (mitigate with hooks). (2) **Hooks + thin CLI wrapper** — complement and the Aider path (agents are "far more comfortable with CLIs than MCP"); fallback for weak MCP support. (3) **Proxy/wrapper** (tmux-style, like CCB) — brittle, vendor-specific, local-only; avoid as core. (4) **SDK-built first-party agents** (Claude Agent SDK / OpenAI Agents SDK) — for *system* roles (memory librarian, task router), not developer sessions. **Recommendation: MCP server + hooks + CLI fallback + SDK system agents** — satisfies "plug in, don't replace" identically across machines/vendors.

---

## G) Identity, Security & Multi-Tenancy

**Identity & namespacing.** Two principal types: **humans** (OAuth/OIDC, or tokens for a small team) and **agents** (per-agent keys now; **W3C DID** longer-term). Every message/claim/memory-write carries a verifiable principal for attribution/audit. **Workspaces are the tenancy boundary** — namespace everything (NATS accounts/subjects `workspace.{id}.>`, Postgres row scoping, separate memory namespaces, Centrifugo channel namespaces + JWT). **Role/capability permissions**: who creates tasks, writes *shared* (vs private) memory, addresses which agents, broadcasts. Default agents to private namespace + a review queue for shared memory.

**Multi-agent-specific risks (the shared layer is a propagation vector).** Prompt injection is OWASP **LLM01:2025** ("fundamental architectural risk"). **Cross-agent propagation ("AI worm"/"viral prompt"):** a compromised agent propagates tainted instructions to peers ("Promptware Kill Chain"); "Prompt Infection" (arXiv 2410.07283) shows multi-agent systems "highly susceptible, even when agents do not publicly share all communications"; documented chain: poison source → trigger → write shared memory → tool discovery → **lateral movement to other agents** → exfiltration (cf. CVE-2025-53773, Copilot RCE 9.6). **Shared-memory poisoning is durable and contagious** — persists and "influences every future interaction"; "any agent with read access retrieves the malicious instructions," propagating "within hours"; MINJA >95% success, PoisonedRAG with "a handful of texts"; OWASP ASI06 (2026); **session isolation does not help** — "the feature that makes agents useful is the attack surface."

**Defenses to bake in:** memory partitioning + **shared writes go through a review/quarantine queue** (human PR approval or trusted librarian agent, never direct); **provenance on every memory item** (who/when/source) for trust-aware retrieval and forensics; **treat all inter-agent messages/shared content as untrusted input** + moderation/trust scoring (LLM Tagging "significantly mitigates infection spread"); **least-privilege tools + a thin owned write-path** (your coordination server *is* that path); **append-only audit log** (free from the event log) for rollback; **MCP transport hardening** (validate `Origin` for DNS-rebinding, bind local to 127.0.0.1, auth all remote connections); evaluate **Microsoft's Agent Governance Toolkit** (MIT, April 2026, deterministic sub-ms policy enforcement vs all 10 OWASP agentic risks, framework-agnostic).

---

## H) Prior Art & the Genuine Gap

| Project | Cross-vendor? | Cross-machine? | Multi-human? | Mechanism | Task list + mailbox? |
|---|---|---|---|---|---|
| **Claude Code "Agent Teams"** (Feb 2026, v2.1.32+) | **No (Claude-only)** | **No (local `~/.claude/`, tmux)** | No (single lead) | Filesystem + file locking | **Yes** |
| **CCB / `claude_code_bridge`** (~2.2K★, MIT) | **Yes** (Claude/Codex/Gemini/OpenCode/Droid) | No (local tmux/WezTerm) | Local | tmux panes + daemons reading JSONL | Partial |
| **AionUi** | Yes | Remote control via browser/phone | Limited | **ACP + "Team MCP Server"** | **Yes** (async mailbox + task board) |
| **Google Scion** (~April 2026) | Yes (Codex/OpenCode "partial") | **Yes** (local/VM/K8s) | Designed for it | Containers, git worktrees, **DMs + broadcasts** | Yes (task graph, chatrooms) |
| **ruflo / claude-flow** (~53K★) | No (ruflo↔ruflo, Claude-centric) | **Yes (claimed)** | Cross-org | **WebSocket (`ws`), future QUIC; mTLS + Ed25519** | Memory/task tools (much alpha/stubbed) |

**What the closest prior art teaches:**
- **Agent Teams is the strongest *semantic* match** and validates your model: a shared task list where "teammates claim and complete" work with "task claiming via file locking to prevent race conditions," a mailbox ("send a message to one specific teammate by name… to reach everyone, send one message per recipient"), and direct teammate addressing "without going through the lead." But teammates are "separate Claude Code instances" (no other vendors), state is local (`~/.claude/teams/`, tracks "tmux pane IDs"), "a lead can only manage one team," "no nested teams," "lead is fixed," "no session resumption with in-process teammates," and it "uses significantly more tokens." **Your project is essentially "Agent Teams, but cross-vendor, cross-machine, multi-human, networked over a real bus instead of the filesystem."** Borrow its task-list + mailbox + lock semantics directly.
- **CCB proves multi-vendor real-time collaboration is wanted/feasible** but is local tmux-only (agents are real provider CLIs in panes, coordinated via session-JSONL reads + local daemons); copy its token-frugal "lightweight prompts instead of full file history" pattern.
- **Google Scion is the closest overall** — cross-vendor "harnesses," git worktrees, shared workspace, DMs + broadcasts, runs local/VM/K8s; study as reference (Codex/OpenCode adapters "partial").
- **ruflo federation** shows the cross-machine networking pattern (Ed25519 per-node keys + mTLS + WebSocket/QUIC) worth emulating — **but** independent analysis reports the swarm layer is "single-process, EventEmitter-based, no inter-node transport… cross-machine swarm coordination does not yet work" in core; claims are largely self-published. Emulate the identity/transport pattern, not the marketing.
- **Academic prior art to mine:** EvoGit (Git phylogenetic graph, "asynchronously read/write the evolving repo," no central coordination), AgentMesh (Planner/Coder/Debugger/Reviewer), AWCP ("files-as-interface" Unix-philosophy delegation), OpenHands (multi-agent delegation). A USPTO patent (US 12,405,822) describes "multi-agent interactions using a shared workspace" as a command ledger — note the IP landscape.

**The genuine, unfilled gap:** (1) **cross-machine + cross-vendor + multi-human simultaneously** — no mature, neutral, self-hosted layer unifies all three; (2) a **vendor-neutral shared blackboard with durable, queryable semantic memory** accessed through a standard surface; (3) **human-and-agent co-membership with safe shared-memory merge** (unsolved by any memory framework); (4) **built on a real event bus** for replay/late-joiners/presence (vs tmux panes or filesystem polling). You are not reinventing messaging, vector search, or locks — you're assembling them into the coordination layer the ecosystem demonstrably wants and has only built in fragmented, single-vendor, single-machine forms.

---

## Recommended Architecture (Layered Blueprint)

```
┌──────────────────────────────────────────────────────────────────────┐
│  AGENT INTEGRATION / ADAPTER LAYER                                     │
│  • Streamable-HTTP MCP server (primary plug-in for every agent)        │
│  • Session hooks (SessionStart/PostToolUse) → pull inbox+events        │
│  • Slash commands/skills (/ask @agent, /sync) for human addressing     │
│  • Thin `coord` CLI (fallback; Aider path)                            │
│  • SDK-built first-party system agents (memory librarian, task router) │
├──────────────────────────────────────────────────────────────────────┤
│  IDENTITY, WORKSPACE & POLICY LAYER                                    │
│  • Human auth (OAuth/OIDC) + per-agent keys (→ W3C DID later)          │
│  • Workspace namespacing (NATS accounts, Postgres row scoping)         │
│  • RBAC/capabilities; shared-memory review queue; audit log            │
│  • Policy enforcement (e.g., Agent Governance Toolkit)                 │
├──────────────────────────────────────────────────────────────────────┤
│  COORDINATION SEMANTICS (the value you build — event-driven blackboard)│
│  • Pub/sub topics + any-to-any inboxes + broadcast                     │
│  • Task model: states, deps, leases, SKIP-LOCKED claiming             │
│  • Presence/discovery (A2A Agent Cards)                                │
├───────────────┬───────────────────────┬──────────────────────────────┤
│  EVENT BUS    │  SHARED MEMORY        │  CO-EDITED ARTIFACTS          │
│  NATS         │  Postgres + pgvector  │  Yjs CRDT docs               │
│  JetStream    │  (semantic + task     │  (task board, design notes)  │
│  (+Centrifugo │   state + provenance  │  synced over the bus         │
│   for UI)     │   + audit, per-agent  │                              │
│               │   + shared namespaces)│                              │
└───────────────┴───────────────────────┴──────────────────────────────┘
```

**Coordination model:** an **event-driven blackboard**. Agents never address each other directly at the transport level — they `publish_event`/`subscribe`, `send_message` to inboxes, `broadcast`, and read/write shared memory and the task board *through the workspace*. Any human or agent can address any present agent by name (NATS subject `workspace.{id}.agent.{name}.inbox`), and broadcasts fan out to all. Late-joining agents replay JetStream history to gain context. Task claiming via `SKIP LOCKED` + leases prevents duplicated work; the append-only event log is both the coordination substrate and the audit trail.

---

## Phased MVP Roadmap

**Phase 0 — Core loop ("multiple people + multiple agents in one shared workspace, any-to-any").** Self-hosted **NATS JetStream + Postgres**. Build the **Streamable-HTTP MCP server** with: `workspace_join`/`presence`, `send_message`/`read_inbox`, `broadcast`, `publish_event`/`subscribe`. Register it in Claude Code, Codex, and Cursor on two different machines/users; add **SessionStart/PostToolUse hooks** to pull new messages into context, and a `/ask @agent` slash command. **Success metric:** a developer on machine A can address an agent in a session on machine B by name and get a reply, and a broadcast reaches all present agents. This alone is novel (cross-machine, cross-vendor, multi-human) versus all prior art.

**Phase 1 — Shared task state (no duplicated effort).** Add the task table (states, deps, `assigned_agent`, leases) with `claim_task` using `SELECT … FOR UPDATE SKIP LOCKED`; emit claim/complete events; expire stale leases (work-stealing). **Metric:** two agents given the same goal never both execute the same task; a crashed agent's task returns to the pool.

**Phase 2 — Shared semantic memory + provenance.** Add pgvector `memory_write(scope)`/`memory_search` with **per-agent private + shared namespaces**, provenance on every item, and a **review/quarantine queue** for shared writes (human PR-style or librarian agent). **Metric:** an agent retrieves a decision another agent recorded earlier; no agent can silently write shared memory.

**Phase 3 — Co-edited artifacts + human UI.** Add **Yjs-backed shared task board and design notes** synced over the bus; add **Centrifugo** for a web dashboard with presence ("who/what is in the workspace now") and live event stream.

**Phase 4 — Hardening & interop.** A2A Agent Cards for discovery; CLI fallback for Aider/other agents; security defenses (trust scoring, LLM Tagging, MCP Origin/auth hardening, governance policy layer); optional Temporal/Restate for first-party durable orchestration agents; optional cross-node federation (Ed25519 + mTLS + WebSocket/QUIC, à la ruflo's identity pattern) if you outgrow a single coordination node.

**Thresholds that change the plan:** if pgvector saturates (>~50M vectors or latency-bound) → migrate semantic memory to **Qdrant**; if you need long-retention event sourcing across many workspaces → reconsider **Kafka**; if you build long-running recoverable first-party agents → add **Temporal**; if you must scale to many coordination-server replicas → add leader election (Postgres advisory locks or etcd) and externalize sessions to the bus.

---

## Caveats & Open Problems

- **Push vs. pull is the central UX risk.** MCP and most agents can't be *interrupted* mid-turn; "an agent was addressed and responds in real time" is approximated via hooks pulling context at boundaries. Real-time responsiveness will be bounded by each agent's turn cadence — validate this feels acceptable early (Phase 0).
- **Shared-memory consistency and human-agent merge are genuinely unsolved.** No existing framework does safe human-agent memory merge; you are building novel review/provenance machinery. Get the partitioning and review queue right before opening shared memory to many writers.
- **Cross-agent injection/poisoning propagation is a first-order threat, not a footnote** — the shared layer is precisely the propagation vector (documented "AI worm" chains, >95% MINJA success, persistent poisoning). Treat all inter-agent content as untrusted; never let agents write shared memory directly.
- **Token/cost amplification** — every additional participating agent is a full separate context (Agent Teams "uses significantly more tokens"); copy CCB's lightweight-prompt pattern to contain cost.
- **Fast-moving ecosystem / source quality** — several figures here come from vendor blogs, self-published author gists (especially ruflo's federation/QUIC claims, which independent analysis flags as partly stubbed and where QUIC is explicitly future), and benchmark posts; star counts and version numbers are 2026 snapshots that change frequently. Verify the latest MCP spec version, Claude Code/Codex hook capabilities, and Scion's design directly against primary docs before committing implementation details.
- **IP awareness** — a USPTO patent (US 12,405,822) covers "multi-agent interactions using a shared workspace" as a command ledger of operational transforms; review the IP landscape if commercializing.