# Agent-IAM — an identity provider for agents

> Status: **foundation in progress.** This document is the design; the code
> under `internal/iam` and `cmd/agentiam` implements it incrementally. It is a
> separate product that happens to live in this repo today, built on the seam
> `internal/auth` was designed around.

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

### 2. Delegation — human authorizes an agent *(phase 2, designed here)*

The agent-native grant. A human (already authenticated via the enterprise IdP)
mints a **delegation**: "agent X may act as me in room team, scopes
{messages:write, tasks:claim}, for 1 hour." Agent-IAM issues the agent a token
whose `sub` is the agent but that carries an `act` (RFC 8693 actor) claim naming
the delegating human, plus the constrained scope and a hard `exp`. This gives
auditable "on behalf of" without the human ever sharing a credential, and it
expires on its own. Token exchange (RFC 8693) is the mechanism.

*Not built yet — the client-credentials foundation and the signing/JWKS/claims
machinery it requires come first, and delegation reuses all of it.*

## Endpoints

| method | path | purpose |
|--------|------|---------|
| POST | `/token` | grant endpoint (client_credentials now; token-exchange later) |
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
