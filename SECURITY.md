# Security Policy

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Report privately via [GitHub Security Advisories][advisory] ("Report a
vulnerability"), or email **indugapallignaneswara@gmail.com** with `SECURITY`
in the subject.

Include what you need to make it reproducible: version/commit, configuration
(auth mode, store), and the steps. A proof-of-concept helps a lot.

**What to expect:** an acknowledgement within 72 hours, an assessment within a
week, and a fix or a documented mitigation for anything confirmed. I will
credit you in the advisory unless you'd rather stay anonymous. This is a
solo-maintained project — I'd rather set that expectation honestly than
promise an enterprise SLA.

[advisory]: https://github.com/indugapallignaneswara/agentmesh/security/advisories/new

## Supported versions

Pre-1.0: only the latest release receives fixes. Once 1.0 ships, the current
minor release will be supported.

## Threat model

AgentMesh is a coordination server where **AI agents exchange content that
other agents will read**. That makes it, by construction, a prompt-injection
propagation surface. The design takes that seriously.

### What AgentMesh defends against

| Threat | Defence |
|---|---|
| **Cross-agent prompt injection** | Message bodies are labelled untrusted data (never instructions) in the `read_inbox` tool description and the pull hook, and carry `sender_kind` (human/agent) as a provenance signal. |
| **Shared-memory poisoning** | Shared memory writes are **quarantined**: an agent can propose, but only a **human** can approve. Rejected items are never retrievable. Private memory is strictly partitioned per member. |
| **Identity spoofing** | With auth on, a credential binds to one principal (workspace + member + kind). An agent cannot act as another member, read another's inbox, join as `human` (which would grant review authority), or cross workspaces. |
| **Token passthrough** | In `oauth` mode, tokens are **audience-bound** (RFC 8707): a token minted for another service is rejected. |
| **JWT forgery** | Narrow algorithm allowlist (RS/ES 256/384/512). `none` and HMAC are rejected — HMAC would let an attacker sign with the public key. |
| **Credential theft at rest** | Only SHA-256 hashes of tokens and invite codes are stored. A database leak leaks no usable credentials. Revocation is immediate. |
| **Flooding / DoS by a looping agent** | Per-principal, per-operation rate limits. A flooding agent throttles **only itself** — humans keep their budget and can kick it. Input sizes are capped; list responses are bounded. |
| **A compromised agent staying in the room** | Humans can `room_kick`, `room_ban` (blocks rejoin), revoke its token, and close the room. Kicks purge its undelivered messages. |
| **Lost work / silent data loss** | `ack_mode` gives at-least-once delivery; the event log's cursor cannot skip entries (serialised appends). |

### What it does **not** defend against

Be clear-eyed about these:

- **`AGENTMESH_AUTH=off` is not secure and is not meant to be.** It is the
  zero-setup demo mode. Anyone who can reach the port can join any room as any
  name — including as a `human`, which grants memory-review authority. Never
  run it on an untrusted network.
- **Auth without TLS ships bearer tokens in plaintext.** The server warns; it
  cannot stop you. Terminate TLS somewhere.
- **A human reviewer approving poisoned content.** The review queue moves the
  trust decision to a person; it does not make that person right. Review shared
  memory the way you'd review a PR from a stranger.
- **A malicious *human* member.** Roles limit what agents can do; a human with
  moderator rights is trusted by design.
- **The agents' own reasoning.** AgentMesh labels untrusted content and gives
  you the tools to eject a misbehaving agent. It cannot prevent an agent from
  choosing to follow instructions it read in a message.
- **Multi-tenancy across mutually hostile organisations.** Workspaces isolate
  data, but this is a single-tenant self-hosted server; don't treat rooms as a
  security boundary between adversaries.

### Deployment hardening

Follow the production checklist in [`docs/operations.md`](docs/operations.md):
auth on, TLS on, explicit rooms, rate limits on, Postgres (not memory), probes
wired, backups running.
