# Agent-IAM — an identity provider for agents

> Status: **phases 1 and 2 implemented.** Phase 1 (client-credentials
> foundation) and phase 2 (RFC 8693 delegation with the `act` claim) are built
> and tested under `internal/iam` and `cmd/agentiam`. It is a separate product
> that happens to live in this repo today, built on the seam `internal/auth`
> was designed around. Product/market/standards direction:
> `agentiam-product.md`, `agentiam-market.md`, `agentiam-standards.md`.

## Why a separate thing

AgentMesh is already an OAuth 2.1 **resource server** (see
[architecture.md](architecture.md) §5 and `internal/auth/oauth.go`): it
validates a bearer JWT against an issuer's JWKS, binds the audience (RFC 8707),
and maps the token to a `Principal`. What it has *not* had is something to
**issue** those tokens for agents. Human identities come from an enterprise IdP
(Okta, Entra, Keycloak, Auth0). Agents have no interactive login, so today they
carry long-lived opaque `amt_` tokens minted by an admin.

Agent-IAM fills that gap: it is an OAuth 2.1 **authorization server** purpose-built
for machine/agent identities. The design constraint that makes it tractable and
immediately useful:

> **Agent-IAM issues exactly the tokens AgentMesh already accepts.** The very
> first milestone is an authorization server whose access tokens validate
> through AgentMesh's *unchanged* `JWTAuthenticator`. No new verification code,
> no new trust path — the loop is closed on day one.

An "Okta for agents" differs from Okta not in the protocol (still OAuth 2.1 /
OIDC-shaped) but in what it optimizes for: no human login UI, short-lived
credentials by default, machine-to-machine as the primary grant, and — the part
that makes it *agent* IAM rather than generic M2M — **scoped, time-boxed
delegation** so a human can authorize an agent to act on their behalf without
handing over their own identity.

## The integration contract (do not break)

Agent-IAM's access tokens MUST satisfy what `internal/auth/oauth.go` verifies,
because that verifier is the whole point. Concretely:

**JWT header**

| field | value |
|-------|-------|
| `alg` | `RS256` (in the RS allowlist; no `none`, no HMAC) |
| `kid` | key id of the active signing key (matches a JWKS entry) |
| `typ` | `at+jwt` |

**JWT claims**

| claim | meaning | RS check |
|-------|---------|----------|
| `iss` | the Agent-IAM issuer URL | must equal the RS's configured issuer |
| `aud` | the target resource's canonical URI (the AgentMesh node) | must **contain** it — RFC 8707 |
| `sub` | the agent's member name in the room | → `Principal.Member` |
| `workspace` | the room the token is scoped to | → `Principal.Workspace` |
| `kind` | `agent` (or `human` for delegated human identity) | → `Principal.Kind` |
| `scope` | space-delimited scopes (future authz granularity) | carried, not yet enforced by the RS |
| `iat`/`nbf`/`exp` | issued-at / not-before / expiry | `exp` required; short-lived |
| `jti` | unique token id | enables future revocation/audit |

**JWKS** published at `/.well-known/jwks.json` in the exact shape the RS parses:
`{ "keys": [ { "kty":"RSA", "kid":"…", "n":"<base64url>", "e":"<base64url>" } ] }`.
Multiple keys may appear during rotation; the RS picks by `kid` and refreshes on
an unknown `kid`.

Because the RS already does audience binding, an Agent-IAM token minted for node
A cannot be replayed against node B — the same property that defeats token
passthrough for enterprise IdPs now protects agent tokens.

## Grants

### 1. `client_credentials` — machine-to-machine *(the foundation, built first)*

An agent client is registered once (out of band, by an admin) and given a
`client_id` and a `client_secret` (shown once; only its hash is stored). At
runtime the agent exchanges them for a short-lived access token:

```
POST /token
Content-Type: application/x-www-form-urlencoded

grant_type=client_credentials
&client_id=agt_abc123
&client_secret=<secret>
&resource=https://mesh.example.com        (RFC 8707 — the audience it wants)
&scope=room:team messages:write            (optional; subset of allowed)
```

The server authenticates the client, intersects the requested scope with the
client's allowed scope, and returns:

```json
{ "access_token": "<RS256 JWT>", "token_type": "Bearer",
  "expires_in": 900, "scope": "room:team messages:write" }
```

The agent then calls AgentMesh with `Authorization: Bearer <access_token>`.
Errors follow RFC 6749 §5.2 (`invalid_client`, `invalid_grant`,
`invalid_scope`, `unsupported_grant_type`) with the right HTTP status.

### 2. Delegation — human authorizes an agent *(phase 2, implemented)*

The agent-native grant, via RFC 8693 token exchange. A human (already
authenticated via the enterprise IdP) hands the agent a `subject_token` — a JWT
from a **trusted** IdP proving who is delegating. The agent authenticates as
itself (same client auth as client credentials) and exchanges it:

```
POST /token
grant_type=urn:ietf:params:oauth:grant-type:token-exchange
&subject_token=<IdP JWT for the human>
&subject_token_type=urn:ietf:params:oauth:token-type:jwt
&resource=https://mesh.example.com
&scope=mesh:send            (optional; ⊆ client's allowed scopes)
```

Trusted IdPs come from `AGENTIAM_SUBJECT_ISSUERS` (`issuer=jwks_url,...`);
with none configured the grant is disabled and every exchange is refused. The
issued token keeps the AGENT as `sub` and stamps the human into the `act`
(RFC 8693 actor) claim — `{"act": {"sub": "priya@corp.example", "iss":
"https://login.corp.example"}}` — so a single token answers "which agent,
authorized by which human, attested by which IdP". Scopes are the requested ∩
client-allowed intersection (never broadened), and **`exp` = min(client TTL,
the human token's own `exp`)**: a delegation cannot outlive the human
authorization behind it. The response carries
`issued_token_type=urn:ietf:params:oauth:token-type:access_token`, and the
resource server validates the token unchanged (`act` rides along as an opaque
audit claim). Bad subject tokens — untrusted issuer, bad signature, expired —
all answer one `invalid_grant`, no oracle.

## Sender-constrained tokens (DPoP, RFC 9449) — *implemented*

The agent-specific threat: an access token lives in the agent's context window
and environment, where a **prompt-injection** attack can make the agent leak
it. A plain bearer token, once leaked, is fully usable by the thief. DPoP makes
a leaked token **inert**.

The agent holds a keypair whose private half never leaves its runtime. On the
`/token` request it sends a `DPoP:` proof header (a short JWS signed by that
key); the authorization server binds the issued token to the key with a
`cnf.jkt` thumbprint claim and returns `token_type: DPoP`. Thereafter every
call to AgentMesh must carry a **fresh** DPoP proof for that exact request
(method + URI + a hash of the token); the resource server checks the proof's
key thumbprint equals the token's `cnf.jkt`. A token scraped from an agent's
context is useless — the attacker cannot mint the proof without the private
key, and a captured proof is single-use (jti replay cache) and bound to one
method/URI. Opt-in and backward compatible: a request with no `DPoP` header
yields an ordinary bearer token, byte-identical to before. Bad proofs answer
`invalid_dpop_proof`; a bound token presented without a valid proof is rejected
by the resource server. `AGENTIAM`/mesh advertise
`dpop_signing_alg_values_supported: ["ES256","RS256"]`.

## Endpoints

| method | path | purpose |
|--------|------|---------|
| POST | `/token` | grant endpoint (client_credentials + RFC 8693 token-exchange; optional DPoP binding) |
| GET | `/.well-known/jwks.json` | public signing keys (RS validates against this) |
| GET | `/.well-known/oauth-authorization-server` | RFC 8414 discovery metadata |
| GET | `/healthz` | liveness |

## Keys & rotation

RSA-2048 signing keys. A key has a `kid` derived from its public-key SHA-256.
The server signs with one active private key and publishes **all** currently
valid public keys in the JWKS, so rotation is: add a new key → start signing with
it → keep the old public key in the JWKS until every token signed by it has
expired → drop it. This matches the RS's rotation-aware `kid` lookup (it
refreshes on an unknown `kid`). Keys load from PEM (config/secret mount) or are
generated on first run for the zero-config demo (with a loud "ephemeral keys"
warning, mirroring the in-memory store warning).

## Storage

The client registry needs the same two-store discipline as AgentMesh: an
in-memory implementation for tests/demo and a Postgres implementation for
production, behind one interface validated by a shared contract suite. A
registered client stores: `client_id`, `secret_hash`, `workspace`, `sub`
(member name), `kind`, `allowed_scopes`, `token_ttl`, `disabled`, timestamps.
Secrets are SHA-256 hashed exactly like `amt_` tokens — a DB leak leaks no
usable credential.

## What this is not (yet)

No human login UI, no consent screen, no refresh tokens (short-lived access
tokens are re-fetched via client_credentials), no dynamic client registration,
no admin console. Those are product surface for later. The first deliverable is
the smallest thing that is genuinely an IdP for agents: **register an agent →
it fetches a short-lived signed token → AgentMesh accepts it, audience-bound and
expiring — with zero changes to AgentMesh.**
