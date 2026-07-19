# Agent-IAM as a standalone product — "Okta for agents"

> Status: product design. This document argues that the Agent-IAM code living in
> this repo (`internal/iam`, `cmd/agentiam`) is the seed of a product sellable
> independently of AgentMesh, and lays out what it takes to get there: the
> extraction path, the agent-native feature ladder, deployment shapes, the
> anti-roadmap, and an effort map. Companion to [agentiam.md](agentiam.md) (the
> current technical design); every claim about existing code below was verified
> against the source as of this writing.

## 1. The product thesis

In an agentic organization, agents outnumber humans by 10–100x — and every
identity assumption the last twenty years of IAM was built on fails for them.
Agents have no interactive login, so there is nothing to put a password or MFA
prompt in front of. They have no offboarding day: an employee who leaves gets
their account disabled by HR-driven SCIM, while an agent that finished its task
last Tuesday keeps whatever credential it was spawned with until someone
remembers. The industry's de-facto answer — a long-lived API key pasted into an
environment variable, shared across a fleet, rotated never — is exactly where
human authentication was before Okta: passwords in spreadsheets, one secret per
vendor, revocation by hope. That gap was a product then, and it is a product
now, because the fix is not a feature of any one agent platform: it is a
dedicated issuer of short-lived, scoped, auditable, revocable machine identity
that every service an agent touches can verify the same way. The workforce IdPs
will not fill it quickly — their entire product surface (login UIs, MFA,
password policy, SCIM-for-people) is dead weight for a principal that is
spawned by an orchestrator and dies when the task ends.

## 2. What exists today (the seed)

The working code is a genuine OAuth 2.1 authorization server for agents, not a
mock. An honest inventory:

**Production-shaped already:**

- **Hashed credentials.** Client secrets are shown once at registration and
  stored only as SHA-256 hashes, verified with a constant-time compare
  (`internal/iam/client.go`); a database leak leaks no usable credential. The
  disabled-client check is deliberately ordered after the secret check so a
  disabled client is indistinguishable from a wrong secret.
- **Audience binding end to end.** The token endpoint refuses to issue without
  an RFC 8707 `resource` parameter (`invalid_target`), and the resource-server
  side (`internal/auth/oauth.go`) rejects any token whose `aud` doesn't contain
  its own URI. A token minted for service A is dead on arrival at service B —
  token passthrough fails by construction.
- **Algorithm allowlist.** The verifier accepts exactly RS256/384/512 and
  ES256/384/512 — no `none`, no HMAC, no alg-confusion surface.
- **Rotation-aware JWKS.** `KeySet` signs with one active key and publishes
  active + retired public keys; `kid` is derived from the public key hash so it
  is stable across restarts; the verifier refreshes its JWKS cache on an
  unknown `kid` (at most once a minute). The rotation *mechanics* exist on both
  sides.
- **Scope discipline.** Requested scopes are intersected with the client's
  allowance; requesting an unheld scope is a hard `invalid_scope`, never a
  silent downgrade; an empty allowance means no scopes (least privilege by
  default).
- **Entitlements in the credential.** The `budget_daily_bytes` claim flows
  register-flag → JWT claim → `Principal.BudgetDailyBytes` → enforcement, and
  ROADMAP M8 verified it end to end against a live flooding agent. This is the
  prototype of the whole P5 entitlements story.
- **Standards surface.** RFC 8414 discovery, RFC 6749 §5.2 error semantics
  (401-with-challenge vs 400, `invalid_client`/`invalid_scope`/…), `typ:
  "at+jwt"` per RFC 9068, per-token random 128-bit `jti`. Store interface with
  memory and Postgres implementations under one contract; the PG store owns its
  own single table and migrates itself, independent of AgentMesh's schema.

**Demo-grade, and honestly so:**

- **Key management is a process detail.** One active signing key loaded from a
  PEM path (or generated ephemerally, with a loud warning); the retired-key
  slot exists in the type but there is no rotation workflow, no CLI, no
  KMS/HSM backing. Rotation today means "restart with new code that populates
  `retired`".
- **No revocation of issued JWTs.** Disabling a client stops *new* tokens
  immediately, but every already-issued token lives until `exp`. The `jti` is
  minted precisely so a denylist can exist later; it does not exist yet. The
  only mitigation is the short default TTL (15 minutes).
- **No audit log.** Issuance is `slog` lines, not a queryable, exportable
  record. An IAM product's core artifact — "who got what token when, and what
  did it authorize" — is currently grep.
- **One client = one (workspace, member).** The registry binds a client to a
  single AgentMesh room and member name. Fine for the first relying party;
  not a multi-service, multi-tenant model.
- **No admin surface.** Registration is a CLI that talks straight to the
  database; no API, no console, no dynamic client registration.
- **Single-tenant, single-issuer process.** No org/project hierarchy, TLS
  delegated to a reverse proxy, no rate limiting on `/token`.

The load-bearing observation: the *hard-to-retrofit* properties (hashed
secrets, audience binding, alg strictness, stable kids, claims-to-principal
mapping) are the ones already done. The missing pieces are additive.

## 3. The extraction path

From "package in the AgentMesh repo" to "standalone service", in order:

1. **Own repo, own module, own release.** `internal/iam` + `cmd/agentiam`
   move to their own Go module (e.g. `agentiam/`), with their own version
   line and release cadence. The code is already structured for this: the
   package doc calls itself "a separate product that happens to live in this
   repo", it imports nothing from AgentMesh's model/store packages, and the
   PG store deliberately owns its own table so it can run against its own
   database. The one thing to copy rather than import is the ~150-line JWT
   verifier from `internal/auth/oauth.go`, which becomes the seed of a small
   published client SDK (see step 3).

2. **Multi-tenant model: orgs → projects → agent clients.** Today's
   `Client{Workspace, Subject}` generalizes: a *tenant* (org) owns *projects*;
   a project registers *agent clients* and declares *relying services* (each a
   canonical audience URI). A client's grant becomes "which audiences it may
   request, and which claims are stamped per audience" — today's
   workspace+member binding is just the AgentMesh-specific instance of
   "audience-specific claims". The token issuance path barely changes: it
   already takes the audience from the request and stamps client-configured
   claims; what changes is the registry schema around it and issuer-per-tenant
   (or tenant claim) semantics.

3. **AgentMesh becomes merely the first relying party.** Any OAuth 2.1
   resource server can consume Agent-IAM tokens — that is the entire point.
   AgentMesh already proves it with zero integration code beyond
   configuration: issuer URL, audience, JWKS URL. Ship a tiny verifier SDK
   (Go first, extracted from `internal/auth/oauth.go`; then TS/Python) so any
   internal service, MCP server, or API gateway can become a relying party in
   an afternoon. The docs stop saying "AgentMesh" and start saying "your
   resource server", with AgentMesh as the worked example.

4. **What stays shared: nothing at runtime.** There is no shared library, no
   shared database, no RPC between AgentMesh and Agent-IAM. The seam is the
   JWKS URL and three config values on the relying side. That is the proof of
   product-ness: if the integration surface were any wider, this would be a
   feature; because the seam is a public, standardized discovery document,
   it is a service anyone can point at.

## 4. The agent-native feature ladder

Generic M2M auth (client credentials, JWTs, JWKS) is table stakes — Keycloak
does it, Auth0 does it. The product is the ladder above it, in this order:

**P1 — Delegation (RFC 8693 token exchange, `act` claim).** A human,
authenticated by the *enterprise* IdP, mints a delegation: "agent X may act
with scopes {s} in context C for 1 hour." Agent-IAM issues the agent a token
whose `sub` is the agent but which carries an `act` claim naming the human,
constrained scope, and a hard expiry. This is the audit answer to the question
every agent deployment eventually faces: *which human is responsible for what
this agent did?* No credential sharing, no impersonation, self-expiring. The
phase-2 sketch already exists in [agentiam.md](agentiam.md); the signing,
claims, and verification machinery it reuses is all built.

**P2 — Sender-constrained tokens (DPoP, RFC 9449; or mTLS, RFC 8705).** Name
the threat precisely, because it is *the* agent-specific one: an agent's
context window is an exfiltration channel. A prompt-injected agent can be
talked into printing its own bearer token into a reply, a log, a tool call to
an attacker's server. With bearer tokens, that transcript leak is a full
compromise until expiry. With DPoP, the token is bound to a private key held
in the agent's *runtime* — process memory, never the prompt — and every use
requires a fresh proof-of-possession signature. A token pasted out of a
context window is inert. No workforce IdP markets this, because no employee's
credentials leak through their own conversation; every agent's can.

**P3 — Short-lived everything + JIT credentials.** Invert the default:
standing credentials become the exception, per-task tokens the rule. The
orchestrator (or the agent itself, via its client credential) fetches a token
scoped to *this task* with a TTL matched to the task, not the day. The
15-minute default TTL and the scope-intersection logic already push this way;
what's added is task-scoped issuance policy and — for the tail risk — a `jti`
denylist so a specific issued token can be killed before expiry. Every token
already carries a unique `jti` for exactly this; the denylist is a small
store + one check in the verifier SDK path (or token introspection, RFC 7662,
for relying parties that prefer a callback).

**P4 — Agent lifecycle.** Identity tied to the agent's actual lifetime:
registration at spawn (dynamic client registration, gated by an orchestrator
credential), automatic credential expiry on task completion or TTL, and fleet
inventory as a first-class query: *what agents exist right now, who owns each
(the P1 delegation chain answers this), and what can each reach (the audience
+ scope grants answer this).* This is the "offboarding day" agents never had,
and it turns the client registry from an admin chore into the org's agent
asset inventory — the thing a CISO actually asks for.

**P5 — Policy and entitlements.** `budget_daily_bytes` is the proof that the
credential can carry enforceable entitlements, not just identity: it already
flows from registration flag to JWT claim to enforcement, verified live in
M8. Generalize it: rate entitlements, spend ceilings, data-scope claims
("may read tier-2 data, never tier-3"), tool allowlists — arbitrary
entitlement claims declared per client (or per delegation), stamped into
tokens, enforced by relying parties that read them from the one place they
already trust. On top: the admin console and audit export (SIEM-friendly
JSONL/syslog of every issuance, denial, delegation, and revocation) — the
surfaces that make it buyable by a security team rather than adoptable by one
engineer.

## 5. Deployment shapes

**Self-hosted single binary — the wedge.** This is the current shape:
`agentiam serve`, one PEM key, one Postgres URL (or nothing, for the demo).
It matches AgentMesh's audience — teams that run their own infrastructure and
will not send their agents' credentials through a third party — and it is how
the first ten design partners deploy in a day. Keep it forever; it is also
the open-core distribution that seeds the funnel.

**Multi-tenant SaaS — the Okta-shaped business.** Recurring revenue, the
admin console as the product, per-agent pricing. It requires real tenancy
(the org/project model from §3), KMS/HSM-backed per-tenant signing keys with
automated rotation, availability SLAs (an IdP outage is everyone's outage),
`/token` rate limiting and abuse controls, and SOC 2 — none of which the
single binary needs on day one.

**Recommended sequencing: self-hosted first, SaaS second.** Three reasons.
First, credibility: an identity product with no customers cannot open with
"trust us with your keys"; self-hosted sidesteps the trust cliff. Second, the
feature ladder P1–P3 is deployment-shape-agnostic — building it in the binary
loses nothing and the SaaS inherits it. Third, the SaaS-blocking work
(tenancy, KMS, SLAs) is exactly the work §2 lists as missing, and it is
better funded by design-partner revenue than done on speculation. The bridge:
a managed single-tenant offering (we run your instance) can precede true
multi-tenancy and de-risk the operations story.

## 6. What NOT to build

No human workforce IdP features: no password auth, no MFA enrollment UIs, no
SCIM for people, no social login, no consent screens for end users. Humans
already have Okta, Entra, Google — and their IdP is wired into HR, device
posture, and compliance in ways no newcomer should re-fight.

Instead, Agent-IAM **federates** with the human IdP. The P1 delegation flow is
the federation point: the human proves who they are with an OIDC token *from
their enterprise IdP*, and Agent-IAM binds that verified human identity into
the agent credential's `act` claim. Agent-IAM never stores a password and
never becomes the system of record for people — it is the system of record
for agents, referencing humans by their existing identity.

This is also the answer to "won't Okta just do this?". Okta's machine
identity is generic M2M scoped to its own ecosystem; the ladder in §4 —
context-window exfiltration defense, spawn-time lifecycle, delegation chains,
entitlement claims — is a different product with different primitives. By
being the agent-side *complement* that federates with **all** the human IdPs
(Okta and Entra and Google and Keycloak), Agent-IAM is neutral in exactly the
way none of them can be to each other: nobody's Entra shop adopts Okta's
agent story, but anyone can adopt the vendor-neutral one that plugs into what
they already run. Interop, not replacement.

## 7. Effort map

| Rung | Size | Reuses | Riskiest unknown |
|------|------|--------|------------------|
| Extraction (repo/module/tenancy §3) | **M** | Everything: server, stores, key set, CLI move nearly intact; verifier copies out of `internal/auth/oauth.go` into an SDK | Getting the org→project→client→audience schema right the first time — it is the API everything else hangs off |
| P1 Delegation | **M** | Signing/claims/JWKS unchanged; `/token` grows a second grant arm (`handleToken` already dispatches on `grant_type` with token-exchange named as the planned second grant); `Claims` grows `act` | Validating the *human's* IdP token inside Agent-IAM: per-tenant trusted-issuer config, and UX for how a human actually mints a delegation (CLI first, console later) |
| P2 DPoP | **L** | Verifier alg plumbing (ES256 already supported); token issuance stamps `cnf`/`jkt` | Relying-party adoption cost — every RS must check proofs, so the verifier SDK must make it one flag; nonce/replay windows for high-frequency agent calls are easy to get subtly wrong |
| P3 JIT + jti denylist | **S/M** | `jti` already on every token; short TTLs already the default; store interface pattern for the denylist | Denylist propagation latency to relying parties (push vs poll vs introspection) — the gap between "revoked" and "refused" is the product claim |
| P4 Lifecycle | **M** | `RegisterClient`, `Disabled`, `CreatedAt`, list-by-workspace CLI are the primitives | Spawn-time registration trust: what authorizes an orchestrator to mint identities, without recreating the standing-credential problem one level up |
| P5 Entitlements + console + audit | **L** | `budget_daily_bytes` is the worked template for claim → Principal → enforcement; issuance logging points exist | Schema for arbitrary entitlements that relying parties can consume without bespoke code per claim; the console is a second product surface (different skills, different pace) |

The dependency order is the ladder order: P1 needs only the extraction; P2/P3
need the SDK from the extraction; P4 builds on P1's ownership chains; P5's
console wants P4's inventory to have something to show.

---

**The one-sentence pitch:** every service an agent touches already knows how
to verify an OAuth token against a JWKS URL — Agent-IAM is the issuer built
for principals that are spawned rather than hired, and the proof is that its
first relying party integrated with zero lines of code.
