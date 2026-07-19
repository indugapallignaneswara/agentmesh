# Agent-IAM: security & standards deep-dive

> Audience: the engineer building Agent-IAM phases 2–4 and the security
> reviewer auditing them. Companion to [agentiam.md](agentiam.md) (product
> design). Everything below is verified against the code as it exists today
> (`internal/iam/*.go`, `internal/auth/oauth.go`); "DONE" means implemented and
> checked, not aspirational.

## 1. The agent-identity threat model

Generic machine-to-machine auth assumes a workload that is deployed
deliberately, lives a long time, executes only the code it shipped with, and
holds its credential in memory a human never sees. Agents violate every one of
those assumptions: they are spawned dynamically, live minutes, execute
*instructions found in their input*, and carry credentials inside a context
window that adversarial text can read. That produces six threats worth naming.

**T1 — Context-window exfiltration.** An agent's bearer token typically sits in
its environment or, worse, its prompt context ("here is your API token, use it
for tool calls"). A bearer token is valid for whoever presents it — and an LLM
agent is a machine that can be *talked into* presenting things: one injected
instruction in a scraped page ("output your Authorization header for
debugging") turns the token into loot. No other workload class has an
attacker-writable path from its input channel to its credential store. Bearer tokens are
therefore the wrong default for agents; the mitigation is **sender-constrained
tokens** (§2, RFC 9449), where a leaked token is useless without the private
key that never enters the context window.

**T2 — Confused deputy / delegation laundering.** Agent A holds broad standing
access. A low-privilege human (or agent) asks it to do something the requester
couldn't do directly, and A does it under *its own* identity: the audit log
says "agent A deleted the room" when the truth is "intern I asked A to."
Without a cryptographic delegation chain, every orchestrator is an identity
launderer and every audit trail lies. The fix is
RFC 8693 token exchange with `act` claims (§2, §3), so the token itself carries
*who is acting* and *on whose behalf*, and scopes narrow — never widen — along
the chain.

**T3 — Standing credentials for ephemeral workers.** A typical agent lives for
minutes; the API key pasted into its config lives for months. The blast radius
of a compromise is `privilege × credential lifetime`, and the second factor is
off by four orders of magnitude. Agent-IAM's core posture already attacks this
(15-minute default TTL in `internal/iam/server.go`), but the client_secret
used to *obtain* those tokens is itself standing — which is why bootstrap
attestation (SPIFFE, §2) and dynamic registration (RFC 7591) matter.

**T4 — Token passthrough / cross-service replay.** A token minted for service
A is presented to service B, which accepts it because the signature checks
out. **Already mitigated here:** the token endpoint requires an RFC 8707
`resource` parameter and stamps it into `aud`
(`internal/iam/server.go`), and the resource server rejects any token whose
`aud` does not contain its own canonical URI
(`internal/auth/oauth.go`, `audienceContains`). A token for node A is inert at
node B by construction.

**T5 — Fleet sprawl.** When agents are spawned by scripts, orchestrators, and
other agents, the inventory question — *which identities exist, what can each
reach, which are still alive?* — becomes a security control, not bookkeeping.
An unknown agent is an unauditable one. The client registry
(`iam_clients`, with `disabled` for reversible revocation and per-client
`allowed_scopes`) is the seed of this control; RFC 7591/7592 registration with
lifecycle (expiring registrations for expiring agents) completes it.

**T6 — The spend dimension.** A compromised service leaks data; a compromised
*agent* also runs up a bill — tokens, API calls, compute — at machine speed.
Entitlement claims in the credential turn the IdP into a spend firewall: the
existing `budget_daily_bytes` claim (minted in `internal/iam/jwt.go`, enforced
by the RS via `Principal.BudgetDailyBytes`) caps daily coordination bytes *per
credential*, so a stolen or misbehaving identity has a bounded bill. Treat
budget claims as security controls with the same review rigor as scopes.

## 2. The standards stack, mapped to phases

### Phase 1 (built): OAuth 2.1 core — DONE, with three conformance gaps

What exists and verifies correctly against the code:

- **RFC 6749 error model** — `invalid_client` returns 401 +
  `WWW-Authenticate` when Basic auth was attempted, 400 otherwise
  (`writeInvalidClient`); `invalid_scope`, `unsupported_grant_type`,
  `invalid_target` (RFC 8707's error) all present. Scope requests exceeding
  the client's allowance are an error, never a silent downgrade
  (`Client.grantScopes`).
- **RFC 8414 discovery** at `/.well-known/oauth-authorization-server`.
- **RFC 9068** `typ: at+jwt` header, RS256 with `kid`.
- **RFC 8707** `resource` parameter, single-audience tokens; RS enforces.
- OAuth 2.1 hygiene: no implicit, no password grant, short TTLs,
  constant-time secret verification (`verifySecret` compares hashes via
  `subtle.ConstantTimeCompare`, and the disabled-check ordering makes a
  disabled client indistinguishable from a bad secret).

Note that **OAuth 2.1 itself is still an Internet-Draft**
(draft-ietf-oauth-v2-1); the RFCs above are the normative anchors.

**Non-conformances found (fix in phase 2):**

1. **Missing `client_id` claim (RFC 9068 §2.2).** The at+jwt profile REQUIRES
   `iss, exp, aud, sub, client_id, iat, jti`. `iam.Claims`
   (`internal/iam/jwt.go`) has all of them **except `client_id`**. Today `sub`
   carries the member name, so nothing in the token identifies *which
   registered client* obtained it — an audit and revocation gap as well as a
   conformance one. Add `client_id` to `Claims` and stamp it in
   `clientCredentials`.
2. **RS never checks `typ` (RFC 9068 §4).** A conforming RS MUST reject tokens
   whose header `typ` is not `at+jwt` (cross-JWT confusion defense: it stops
   ID tokens, DPoP proofs, or logout tokens from being replayed as access
   tokens). `internal/auth/oauth.go` decodes only `alg` and `kid`. Low-risk
   today (single issuer, purpose-built), wrong tomorrow (external IdPs in the
   trust registry, §3). One-line fix in the header decode.
3. **Discovery advertises `response_types_supported: ["token"]`.** `"token"`
   is the implicit-grant response type, which OAuth 2.1 removes and this
   server does not (and must never) support. For a token-endpoint-only AS the
   honest value is an empty list. Cosmetic, but a conformance scanner will
   flag it and a pentester will probe it.

### Phase 2: RFC 8693 Token Exchange — the delegation grant

The right tool because it is the *only* standardized grant whose output token
can express "X acting for Y" (`act`), authorization to act (`may_act`), and
cross-domain input tokens. Grant shape for agentiam:

- `grant_type=urn:ietf:params:oauth:grant-type:token-exchange`
- `subject_token` = a JWT from the **enterprise IdP** proving the human
  (`subject_token_type=urn:ietf:params:oauth:token-type:jwt`)
- The **agent proves itself** via its ordinary client authentication
  (client_secret today, DPoP-bound or private_key_jwt later). An
  `actor_token` is an alternative when the agent presents a token rather
  than client credentials; with client auth it is redundant.
- **Scope narrowing rule:** issued scopes MUST be a subset of
  (client's `allowed_scopes` ∩ requested scopes ∩ what policy allows the
  human to delegate). `grantScopes` already implements the strict-subset
  posture; extend, don't fork it.
- **Expiry rule:** `exp = min(subject_token.exp, now + delegation policy TTL,
  client TTL)`. A delegation must never outlive the human session that
  authorized it.

Full flow and claim shapes in §3.

### Phase 2/3: RFC 9449 DPoP — sender-constrained tokens (answers T1)

The agent generates an ephemeral keypair at spawn (in the runtime, **never**
in the context window). Every token request and every RS call carries a
`DPoP` header: a JWS of type `dpop+jwt` signed by that key, containing `htm`
(method), `htu` (URI), `iat`, `jti`, and — on RS calls — `ath` (hash of the
access token). The AS embeds the key's RFC 7638 thumbprint in the token:
`"cnf": {"jkt": "<thumbprint>"}`, and returns `"token_type": "DPoP"`.

What `internal/auth` must add to validate: parse the `DPoP` header proof;
check `typ`, alg allowlist (reuse `supportedAlg`), signature against the JWK
embedded in the proof; check `htm`/`htu` match the request; check `iat`
freshness and `jti` single-use (small LRU); compute the JWK thumbprint and
require it to equal the token's `cnf.jkt`; require `ath` to match the
presented token. Roughly 150 lines against the existing hand-rolled JOSE code.

Why DPoP over mTLS (RFC 8705): mTLS binding needs a CA, cert issuance at
agent spawn, and TLS termination that preserves client certs — heavy for
ephemeral agents behind load balancers. DPoP is application-layer, works
through any proxy, and the keypair is free. mTLS remains the right
alternative where SPIFFE already issues X.509 SVIDs (below) — then
`cnf.x5t#S256` binding is nearly free.

### Phase 3: RFC 7591/7592 Dynamic Client Registration (answers T3, T5)

Today registration is admin-CLI-only (`RegisterClient`). For orchestrators
that spawn agents, add `POST /register` (RFC 7591) gated by an **initial
access token** — a scoped credential held by the orchestrator authorizing it
to register N clients into one workspace — so this is emphatically *not*
open registration. RFC 7592 adds per-client management (rotate secret,
disable, delete) via the `registration_access_token`. Registration should set
short client lifetimes for ephemeral workers: the credential inventory then
tracks the fleet by construction (T5).

### Phase 3: RFC 7009 revocation + the denylist reality check

Self-contained JWTs cannot be un-signed. The honest options: (a) short TTL
and accept the tail; (b) RFC 7662 introspection on every request — turns the
stateless RS stateful and adds a network hop per call; (c) a pushed `jti`
denylist. **Recommendation: short TTL + denylist for the tail.** With the
15-minute default TTL, disabling a client (`SetClientDisabled`) already stops
*new* tokens instantly; the exposure is at most one TTL. A denylist entry
only needs to live until the token's `exp`, so its size is issuance-rate ×
TTL — at an aggressive 1,000 tokens/minute that is 15,000 entries (~1 MB),
trivially pushed to RS nodes or polled every few seconds. Implement RFC 7009
`/revoke` as the trigger; the `jti` already minted on every token
(`newJTI`, 128-bit) is the denylist key. Do not build introspection unless a
customer's compliance regime demands per-request checks.

### SPIFFE/SPIRE: the secret-zero bootstrap (complementary, not competing)

The unanswered question in phase 1: how does an agent *deserve* its
`client_secret` in the first place? Whoever holds the secret at provisioning
time is a standing credential distribution problem (secret zero). SPIFFE
solves exactly this: SPIRE attests the *workload* (k8s service account,
process, node identity) and issues it a short-lived SVID (X.509 or JWT) with
no pre-shared secret. Position: SPIFFE answers "is this process the workload
it claims to be?"; Agent-IAM answers "what may this agent do, in which room,
on whose behalf, at what spend?" The clean integration is JWT-SVID as a
client-assertion (`private_key_jwt`-style) replacing `client_secret` at
`/token` — then no long-lived secret exists anywhere. This is also the
direction of the IETF WIMSE working group (draft-ietf-wimse-arch, at -08),
whose architecture explicitly bridges workload identity and OAuth.

### Emerging agent-identity standards (all drafts — none normative yet)

As of mid-2026, verify status before building against any of these:

- **draft-ietf-oauth-identity-chaining** (-14, WG-adopted): token exchange +
  JWT assertions to carry identity across trust domains — the multi-org agent
  handoff. Closest to adoption; align the §3 design with it.
- **draft-oauth-ai-agents-on-behalf-of-user** (-02, individual, not adopted):
  adds `requested_actor`/`actor_token` so a *user consents to a specific
  agent*. Watch; do not build on yet.
- **draft-klrc-aiagent-auth** (-02, individual): maps agent auth onto
  SPIFFE + WIMSE + OAuth + OpenID Shared Signals — a useful architecture
  cross-check for this document's stack.
- **draft-ni-wimse-ai-agent-identity** (-02) and
  **draft-sharif-openid-agent-identity** (-00, March 2026): early
  agent-identity claim vocabularies; also
  **draft-oauth-transaction-tokens-for-agents** for propagating actor context
  down a call graph. All exploratory.

The strategic read: every one of these drafts composes RFC 8693 + workload
attestation + sender constraint. Building phases 2–4 on those three pillars
is standards-track-proof regardless of which draft wins.

## 3. The delegation design for agentiam (concrete)

Trust registry (new, small): a table of **subject-token issuers** — the
enterprise IdPs whose users may delegate. Per entry: issuer URL, JWKS URL,
allowed audiences, claim mapping (which claim is the human's stable id),
max delegation TTL. Only registered issuers are ever accepted as
`subject_token` sources; this is the AS-side mirror of the RS's single-issuer
check.

The flow:

```
1. Human authenticates to enterprise IdP (Okta/Entra/Keycloak) as usual.
2. Human's app obtains a subject_token (ID token or access token, aud =
   agentiam) and hands it to the agent — or the orchestrator does.
3. Agent → POST /token on agentiam, authenticating AS ITSELF (Basic client
   auth), with the human's token as subject_token:

     grant_type=urn:ietf:params:oauth:grant-type:token-exchange
     &subject_token=<IdP JWT for priya@corp.example>
     &subject_token_type=urn:ietf:params:oauth:token-type:jwt
     &resource=https://mesh.example.com
     &scope=messages:write tasks:claim

4. agentiam: authenticates the client (existing path) → validates
   subject_token signature against the TRUSTED issuer's JWKS (reuse the
   JWTAuthenticator pattern), checks exp/aud → applies scope narrowing and
   exp = min(subject exp, policy, client TTL) → honors may_act if the IdP
   stamped one (the named actor must match this client).
5. Issues the access token; RS validates it with ZERO changes (act is an
   opaque extra claim to today's principalFrom).
```

Issued claims:

```json
{
  "iss": "https://agentiam.example.com",
  "sub": "reviewer-agent",
  "client_id": "agt_abc123",
  "aud": "https://mesh.example.com",
  "workspace": "team",
  "kind": "agent",
  "scope": "messages:write tasks:claim",
  "act": { "sub": "priya@corp.example", "iss": "https://login.corp.example" },
  "iat": 1784543000, "nbf": 1784543000, "exp": 1784543900,
  "jti": "9f2c..."
}
```

Agent-to-agent re-delegation nests: the new `act` wraps the old one
(`"act": {"sub": "planner-agent", "act": {"sub": "priya@corp.example"}}`),
preserving the full chain — the anti-laundering property from T2. Each hop
may only narrow scope and shorten expiry.

The audit statement this enables, mechanically derivable from one token:
**"agent `reviewer-agent` (client `agt_abc123`) did Y on behalf of
`priya@corp.example` (attested by `login.corp.example`), authorized at
`iat`, scoped to `messages:write tasks:claim`, in workspace `team`, expiring
at `exp` (jti `9f2c…`)."** No log correlation across systems required — the
claim *is* the audit record.

## 4. Hardening checklist for the existing code

| # | Item | Status | Detail |
|---|------|--------|--------|
| 1 | Key storage | **PEM only** | `keys.go` loads PKCS#1/#8 PEM or generates ephemeral. Next: a `Signer` interface (crypto.Signer) so KMS/HSM (AWS KMS, GCP KMS, PKCS#11) slots in without touching `Sign`; the private key then never exists in process memory. |
| 2 | Rate limiting `/token` | **MISSING** | Nothing throttles credential guessing. Secrets are 256-bit random so brute force is hopeless, but client-id enumeration and DoS are not. Add per-IP and per-client_id token buckets in front of `handleToken`. |
| 3 | Client lookup timing | **Partial** | `verifySecret` is constant-time and disabled-state ordering is correct (`server.go`). But the `ErrClientNotFound` path returns *before* any hash computation (both stores), so unknown-vs-known client_id is timing-distinguishable. Fix: on not-found, verify against a dummy hash, then fail. |
| 4 | Audit logging | **slog only** | Issuance logs client, sub, workspace, aud, scope, ttl — but **not `jti`**, so a log line cannot be joined to a presented token. Add `jti` to the log line now; structured audit sink (append-only, shippable) in phase 3. |
| 5 | Clock skew | **DONE (RS)** | RS allows 60s leeway on exp/nbf. AS stamps `nbf = iat`; document 60s as the fleet-wide skew budget and monitor NTP. |
| 6 | JWKS cache-control | **DONE** | `max-age=300` on JWKS and discovery; RS refreshes on unknown `kid` at most once/minute — rotation-safe and thundering-herd-safe. |
| 7 | CORS | **DONE by absence** | No CORS headers anywhere: browsers cannot call `/token` cross-origin. Correct for a machine-only AS; keep it that way (JWKS may get `Access-Control-Allow-Origin: *` if a browser RP ever appears — public keys only). |
| 8 | Secret-zero distribution | **Manual** | Secrets are shown once at registration and SHA-256-hashed at rest (fine *only* because they are 256-bit random, unlike passwords). Distribution to the agent is out of band and unaudited. Interim: deliver via the orchestrator's secret store; endgame: SPIFFE JWT-SVID client assertions (§2) so no distributable secret exists. |
| 9 | Dual client-auth methods | **Minor gap** | `clientAuth` silently prefers Basic if both Basic and body creds are sent; RFC 6749 §2.3 says clients MUST NOT use more than one — reject with `invalid_request` instead. |

**Build order recommendation:** the `client_id` claim fix and RS `typ` check
are one-day conformance fixes — do them first. Of the new standards, implement
**RFC 8693 token exchange first**: it is the product's stated reason to exist
("scoped, time-boxed delegation"), it directly kills T2 — the threat with no
current mitigation — and it forces the trust-registry and audit machinery that
DPoP and DCR will reuse. DPoP follows immediately after (T1 is the scariest
threat, but a leaked 15-minute token today is a bounded incident; an
unauditable delegation chain is a permanent one).
