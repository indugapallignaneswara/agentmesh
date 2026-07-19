# Agent-IAM: Market Assessment for a Standalone "Okta for Agents"

> Question under test: is machine/agent identity — specifically a self-hosted
> OAuth 2.1 authorization server purpose-built for AI agents — a viable
> standalone product, given what incumbents, startups, and standards bodies
> shipped through mid-2026?
>
> Method note: web research July 2026. Every load-bearing claim carries a URL.
> Where a number comes from a third-party summary rather than a primary vendor
> page, it is flagged. This is a market read, not a business plan.

## §1 Market map

| Player | Category | What it ships for agents/NHI | The agent-shaped gap |
|---|---|---|---|
| **Okta** | Incumbent IdP | "Auth for GenAI" on Auth0 (dev preview Apr 2025): Token Vault, async auth, user auth for agents ([okta.com](https://www.okta.com/newsroom/press-releases/auth0-platform-innovation/)); "Okta for AI Agents" — ISPM, Universal Directory registration, Privileged Access — announced EA and reported GA Apr 30 2026 ([okta.com blog](https://www.okta.com/blog/ai/okta-ai-agents-early-access-announcement/)); Cross App Access (XAA) protocol, EA ([okta.com](https://www.okta.com/newsroom/press-releases/okta-introduces-cross-app-access-to-help-secure-ai-agents-in-the/)) | Governance and posture of agents, not agent-native token *issuance*; SaaS-only; no spend/budget claims; XAA is a protocol bid, adoption unproven |
| **Auth0 (Okta)** | Incumbent CIAM | client_credentials M2M with metered token quotas: ~1,000 M2M tokens/mo on Free/Essentials, ~5,000 on Professional (third-party pricing summaries: [costbench](https://costbench.com/software/identity-access-management/auth0/), [Auth0 community](https://community.auth0.com/t/m2m-tokens-pricing/98636) — verify against current auth0.com/pricing) | M2M is priced as an overage trap, not a product; no delegation-chain or budget semantics; not self-hostable |
| **Microsoft Entra Agent ID** | Incumbent IdP | First-class "agent identities" distinct from workload identities; blueprints, federated credentials (no static secrets), MCP/A2A support, third-party agent federation ([learn.microsoft.com](https://learn.microsoft.com/en-us/entra/agent-id/what-is-microsoft-entra-agent-id)) | Deep but Microsoft-gravity: value concentrates inside Entra/Azure tenancy; no budget claims; not self-hosted |
| **WorkOS** | Dev-first auth | AuthKit becomes an MCP-compliant OAuth 2.1 authorization server "with one configuration value"; free to 1M MAU, then $2,500/mo per additional million ([workos.com/mcp](https://workos.com/mcp), [provider roundup](https://workos.com/blog/best-mcp-server-authentication-providers)) | Closest to the MCP-AS wedge — but SaaS-only, MAU-priced for humans, no agent-native claims (budget, delegation actor) |
| **Keycloak** | OSS IdP | Service accounts via client_credentials, token exchange (RFC 8693) support; free self-host ([keycloak.org docs](https://www.keycloak.org/docs/latest/server_admin/index.html#_service_accounts)) | General-purpose IdP heft; nothing agent-specific; e.g. Keycloak 26.6 reportedly blocks token exchange for DPoP-bound tokens ([keymate](https://keymate.io/blog/dpop-proof-of-possession)) |
| **AWS IAM Roles Anywhere** | Cloud workload identity | X.509-based temporary AWS credentials for non-AWS workloads; the feature itself is free (Private CA ~$400/mo if used) ([aws.amazon.com](https://aws.amazon.com/iam/roles-anywhere/)) | Only mints *AWS* credentials; no OAuth/JWT surface for arbitrary resource servers; nothing agent-aware |
| **SPIFFE/SPIRE, Teleport Machine ID** | Workload identity infra | Standard workload identities (SVIDs), attestation; Teleport is a commercial SPIFFE-compatible implementation ([goteleport.com](https://goteleport.com/docs/machine-workload-identity/introduction/), [spiffe.io](https://spiffe.io/docs/latest/spire-about/spire-concepts/)) | Infra-heavy; practitioners note SPIRE's attestation infra and X.509 issuance model fit poorly with ephemeral, dynamically spawned agents ([solo.io](https://www.solo.io/blog/agent-identity-and-access-management---can-spiffe-work), [riptides.io](https://riptides.io/blog/how-to-deliver-spiffe-identity-to-ai-agents/)) |
| **Astrix Security** | NHI security startup | Discovery, governance, least-privilege and audit for NHIs/agents; ~$85M raised; Cisco announced intent to acquire for ~$400M ([astrix.security](https://astrix.security/), [bankinfosecurity](https://www.bankinfosecurity.com/blogs/cisco-eyeing-buy-non-human-identity-startup-astrix-p-4105)) | Governs credentials that already exist; does not *issue* agent tokens; now being absorbed into a platform vendor |
| **Aembit** | Workload/agent IAM startup | "IAM for Agentic AI" GA: Blended Identity (agent + delegating human in one credential) and an MCP Identity Gateway doing policy + token exchange; free starter tier ([aembit.io](https://aembit.io/press-release/aembit-introduces-identity-and-access-management-for-agentic-ai/), [GA post](https://aembit.io/blog/aembit-iam-for-agentic-ai-is-now-generally-available/)) | Closest functional competitor. SaaS control plane; no budget/spend claims; not a drop-in self-hosted AS you own |
| **Natoma** | MCP gateway startup | Governed MCP gateway: identity-aware policy, credential governance, shadow-MCP discovery; acquired by Snowflake, May 2026 ([snowflake.com](https://www.snowflake.com/en/news/press-releases/snowflake-announces-intent-to-acquire-natoma-providing-secure-connectivity-for-the-agentic-enterprise/)) | Gateway-in-the-path, not an authorization server; post-acquisition, gravity shifts to Snowflake's data estate |
| **Arcade.dev / Composio** | Agent tool-auth | Arcade: MCP runtime with action-time auth and URL elicitation co-developed with Anthropic ([arcade.dev](https://www.arcade.dev/), [zenml case study](https://www.zenml.io/llmops-database/secure-authentication-for-ai-agents-using-model-context-protocol)); Composio AgentAuth: managed OAuth/token vault for third-party tools ([composio.dev](https://composio.dev/content/ai-agent-integration-platforms)) | They broker the agent's access to *SaaS tools* (Gmail, Slack); they are not the agent's own IdP and issue no first-party agent identity |
| **Oasis / Entro / Token Security** | NHI governance | Discovery and posture for keys, secrets, service accounts ([NHI platform comparison](https://www.cremit.io/reports/rsac-2026-nhi)) | Same as Astrix: they observe and govern, they don't mint |

## §2 The opening — is there space?

**The bear case (incumbents bolt it on, startups crowd the rest).** Okta,
Microsoft, and WorkOS have all shipped real agent-identity product, not just
slideware: Entra Agent ID is a genuinely new identity construct with federated
credentials and MCP/A2A awareness ([Microsoft](https://learn.microsoft.com/en-us/entra/agent-id/what-is-microsoft-entra-agent-id)); WorkOS turned AuthKit into an
MCP-spec OAuth 2.1 AS with generous free tiers ([WorkOS](https://workos.com/mcp)); Okta is pushing its own
protocol (XAA) with a large partner list ([Okta](https://www.okta.com/newsroom/press-releases/okta-introduces-cross-app-access-to-help-secure-ai-agents-in-the/)). Meanwhile the startup layer is
already consolidating: Cisco moved on Astrix (~$400M) and Snowflake bought
Natoma inside roughly a year of those companies' agent pivots ([bankinfosecurity](https://www.bankinfosecurity.com/blogs/cisco-eyeing-buy-non-human-identity-startup-astrix-p-4105), [Snowflake](https://www.snowflake.com/en/news/press-releases/snowflake-announces-intent-to-acquire-natoma-providing-secure-connectivity-for-the-agentic-enterprise/)).
When a category consolidates before it commoditizes, late independents usually
become features.

**The bull case (the specific cell is empty).** Look at what each crowd
actually ships. The NHI startups (Astrix, Oasis, Entro, Token) do *discovery
and governance* of credentials that already exist. The tool-auth startups
(Arcade, Composio) broker the agent's access to *other people's* APIs. The
incumbents do governance (Okta), ecosystem-locked issuance (Entra), or SaaS
issuance priced for human MAUs (WorkOS, Auth0 — whose M2M quotas of ~1,000
tokens/month ([costbench](https://costbench.com/software/identity-access-management/auth0/)) are actively hostile to agents that re-fetch
short-lived tokens every 15 minutes). Aembit is the one player squarely on
"issue agent credentials with human context," and it is a SaaS control plane.
**Nobody ships a self-hosted, single-binary OAuth 2.1 authorization server
purpose-built for agents** — short-lived JWTs, audience binding, delegation
actor claims, budget claims — that a team can run next to its own
infrastructure. That cell is empty, and the MCP spec is simultaneously
manufacturing relying parties for it: since the June 2025 revision, and
hardened in the 2025-11-25 revision, every remotely-accessible MCP server is
required to act as an OAuth 2.1 resource server with RFC 9728 metadata and
RFC 8707 resource indicators ([modelcontextprotocol.io](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)) — and public registries
tracked roughly 10,000–20,000 MCP servers by mid-2026 (counts vary by
registry and overlap; treat as order-of-magnitude: [official registry ~9.6k records](https://www.digitalapplied.com/blog/mcp-ecosystem-h1-2026-retrospective-adoption-data-points), [ecosystem estimates](https://www.qcode.cc/mcp-servers-ecosystem-2026)).
Every one of those servers needs an authorization server; the spec
deliberately does not provide one.

**Position.** There is a real, narrow opening — but it is *not* "Okta for
agents" as a category claim. The governance/discovery layer is lost to
consolidation, and enterprise-wide agent identity fabric will belong to
Okta/Microsoft. The defensible opening is the bottom of the market the
incumbents structurally can't serve: teams who run their own agents and their
own MCP servers, won't put agent credentials in someone else's SaaS, and need
an AS that speaks the MCP spec's exact dialect out of the box. That is a
wedge, not yet a company; the market question is whether it expands upward.

## §3 Wedge assessment

The founder's actual assets: (a) a working self-hosted OAuth 2.1 AS for
agents (client_credentials, RS256, RFC 8707 audience binding, per-token
budget claims); (b) a real relying party — AgentMesh — proving the loop end
to end; (c) MCP-native positioning. Four candidate wedges, ranked:

1. **MCP-server auth provider (strongest).** The demand is manufactured by
   the spec itself: MCP servers MUST be OAuth 2.1 resource servers and MUST
   point clients at an authorization server ([spec](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)). Developers hit this wall
   the day they expose a server beyond localhost — Stack Overflow's own
   engineering blog covers the confusion ([stackoverflow.blog](https://stackoverflow.blog/2026/01/21/is-that-allowed-authentication-and-authorization-in-model-context-protocol/)). The competition
   here is WorkOS (SaaS, excellent DX) and DIY Keycloak (heavyweight,
   generic). "The single binary that makes your MCP server spec-compliant in
   ten minutes, on your own hardware" is a crisp, searchable, spec-driven
   value proposition. This is the wedge.

2. **Self-hosted-first for teams that won't SaaS their agent creds
   (strong, but it's a *property* of wedge 1, not a separate wedge).** The
   Zitadel/Ory/Phase Two comparables prove a durable buyer segment exists for
   self-hosted identity ([zitadel.com/pricing](https://zitadel.com/pricing), [phasetwo.io](https://phasetwo.io/pricing/hosting/)). Agent credentials
   are more sensitive than user logins — an agent token *does things*. Sell
   wedge 1 self-hosted-first; this is the differentiation against WorkOS.

3. **Budget/spend-bound tokens (differentiator, not wedge).** No surveyed
   player — Okta, Entra, Aembit, WorkOS — puts spend limits in the token
   itself. It is genuinely novel and demo-worthy, and AgentMesh proves
   enforcement end to end. But there is no evidence yet of buyers searching
   for it (uncertainty: absence of evidence, thin market signal), and a claim
   only works if resource servers enforce it — a two-sided adoption problem.
   Ship it as the headline demo inside wedge 1, not as the category.

4. **Delegation audit trail (right direction, early).** The standards are
   converging exactly here — RFC 8693 `act` claims, IETF drafts on
   on-behalf-of for agents ([draft-oauth-ai-agents-on-behalf-of-user](https://datatracker.ietf.org/doc/html/draft-oauth-ai-agents-on-behalf-of-user-02), [draft-klrc-aiagent-auth](https://datatracker.ietf.org/doc/draft-klrc-aiagent-auth/)),
   the OIDF whitepaper naming "impersonation vs. delegated authority" a top
   gap ([openid.net](https://openid.net/new-whitepaper-tackles-ai-agent-identity-challenges/)). But these are individual drafts, not adopted standards;
   building the product on them now means re-work risk. Track closely,
   implement `act` (already designed in agentiam.md), don't lead with it.

**Recommendation:** lead with **wedge 1 delivered as wedge 2** — the
self-hosted, MCP-spec-native authorization server — with budget claims as the
signature feature and delegation as the roadmap. AgentMesh is the proof
artifact: "here is a real MCP resource server accepting these tokens,
unchanged."

## §4 Pricing/packaging hypothesis

Grounded in the comparables:

- **Zitadel:** self-host free (AGPL), cloud from ~$100/mo ([zitadel.com/pricing](https://zitadel.com/pricing)).
- **Ory:** OSS core, usage-priced cloud ([comparison](https://openalternative.co/compare/ory/vs/zitadel)).
- **Phase Two (Keycloak):** open-source extensions, managed hosting priced
  per cluster / concurrent sessions, premium ~$499/mo — explicitly marketed
  as escaping the "identity tax" ([phasetwo.io](https://phasetwo.io/pricing/hosting/)).
- **Anti-pattern to exploit:** Auth0's per-M2M-token metering (1k/mo free)
  punishes exactly the short-lived-token hygiene agents should have ([costbench](https://costbench.com/software/identity-access-management/auth0/), [Auth0 community](https://community.auth0.com/t/m2m-tokens-pricing/98636)).

Hypothesis: **open-core, never per-token.**
- *Free (OSS):* single binary, unlimited agents and tokens, client_credentials,
  JWKS, budget claims — the whole spec-compliance story. Per-token pricing is
  the incumbent weakness; make "unlimited tokens" the marketing line.
- *Paid tier (~$50–200/mo per instance, self-hosted license or managed):*
  Postgres HA, admin console, delegation workflows + audit export, SIEM
  integration, enterprise-IdP federation (humans in Okta/Entra mint
  delegations for agents here).
- *Enterprise:* SSO for the admin plane, compliance reports, support SLA —
  the standard Zitadel/Phase Two ladder.
- Unit of pricing: per deployment/cluster (Phase Two model), because the
  buyer is a platform team, not a per-seat department. Flagged uncertainty:
  willingness-to-pay at the low end is unproven; the free tier may satisfy
  most early adopters for a long time — that is the open-core tradeoff, and
  the NHI market sizing (~$11–12B for NHI access management in 2025–26 per
  one industry report, [cremit/RSAC roundup](https://www.cremit.io/reports/rsac-2026-nhi) — third-party figure, treat with
  caution) mostly describes enterprise governance spend, not this segment.

## §5 Timing verdict

**On time for the issuance wedge; already late for everything else.**
Evidence for "on time": the MCP authorization mandate is under a year old in
its hardened form ([spec 2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)); the relying-party population (MCP servers)
is growing by ~1,000/month on one registry alone ([digitalapplied](https://www.digitalapplied.com/blog/mcp-ecosystem-h1-2026-retrospective-adoption-data-points)); the
standards for delegation are still drafts ([IETF datatracker](https://datatracker.ietf.org/doc/draft-klrc-aiagent-auth/)), so the deep
feature set is not yet commoditizable; and Aembit's GA was April 2026 —
the direct competition is months old, not years ([aembit.io](https://aembit.io/blog/aembit-iam-for-agentic-ai-is-now-generally-available/)). Evidence for
"late": Okta for AI Agents GA, Entra Agent ID shipping, WorkOS free to 1M
MAU, and two acquisitions closing the governance flank. The window that
remains is the self-hosted, spec-native, developer-first slice — real, but it
will not stay open long once WorkOS or Zitadel decides an "MCP AS in a box"
is worth packaging (Zitadel is one AGPL release away from it).

## §6 Top 5 risks, ranked

1. **WorkOS/Zitadel ship "MCP AS in a box" and erase the wedge.**
   Counterargument: WorkOS is structurally SaaS and MAU-priced; Zitadel is
   general-purpose and has shown no agent-specific claims work. A focused
   product can own the niche's mindshare first — but only with speed.
2. **The MCP auth pain gets absorbed upstream** — gateways (Natoma-in-
   Snowflake, API gateways) or the MCP SDKs themselves bundle enough auth
   that servers never shop for an AS. Counterargument: the spec explicitly
   separates AS from RS ([spec](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)); gateways still need a token issuer behind
   them, and self-hosters won't take a Snowflake dependency.
3. **No budget-token demand.** The signature differentiator may be a feature
   nobody asked for; no competitor shipping it could mean "empty niche" or
   "no market." Counterargument: token-metering docs in this very repo and
   Auth0's quota model show cost-control of machine credentials is a live
   concern; and the feature costs little to carry while wedge 1 is validated.
4. **Standards churn invalidates the delegation design.** The IETF drafts
   (`requested_actor`, actor tokens) are pre-adoption ([draft-oauth-ai-agents-on-behalf-of-user](https://datatracker.ietf.org/doc/html/draft-oauth-ai-agents-on-behalf-of-user-02));
   if OIDF/IETF land somewhere else, early delegation code is rework.
   Counterargument: the RFC 8693 `act` claim core is already a published RFC
   and every draft builds on it; the blast radius is the grant plumbing, not
   the token format.
5. **Solo-founder vs. consolidating giants** — Cisco, Snowflake, Okta, and
   Microsoft are spending hundreds of millions here; a single-binary OSS
   project may be permanently sub-scale. Counterargument: that same
   consolidation removes independent competitors from the niche, and
   self-hosted developer tools (Keycloak→Phase Two, Zitadel, Ory) have
   repeatedly sustained businesses in the shadow of Okta by serving buyers
   the giants structurally ignore. The honest floor: even if it never
   becomes a company, the product makes AgentMesh's security story
   best-in-class, so the downside is bounded.

---

*Uncertainty register: Auth0/WorkOS pricing from vendor pages and third-party
trackers as of July 2026 — re-verify before quoting to anyone; MCP server
counts are registry-dependent and double-counted across indexes; the NHI
market-size figure is a single third-party report; "Okta for AI Agents GA
April 30 2026" comes from Okta's own blog and was not independently
confirmed; IETF draft status checked via datatracker listings, WG adoption
not confirmed for any agent-specific draft.*
