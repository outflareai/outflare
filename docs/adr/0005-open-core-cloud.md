# 5. Open core: one public product, a private Cloud layer

Status: Decided (23 October 2026). Changed by [ADR 11](0011-open-core-line.md): some features are Cloud only.

## Decision

Self-hosted Outflare and the hosted Cloud run the same code. There are two repositories:

| Repository | Visibility | Holds |
| --- | --- | --- |
| [`outflareai/outflare`](https://github.com/outflareai/outflare) | Public | The whole product: engine, web app, API contract, SDK, MCP server, Docker Compose |
| Outflare Cloud | Private | Only what running the hosted service adds, such as billing and plans |

The Cloud repository never copies files from this one. It depends on it:

- **Engine.** Cloud's `main` is `outflare.Main(cloudExtensions...)`. The [`engine/outflare`](../../engine/outflare) package lets an `Extension` add:
  - routes under its own prefix (`/v1/cloud/...`)
  - middleware, for example plan limits
  - background jobs on the same Postgres queue
  - capabilities in `/v1/meta`
  - accounts: create a user who owns a new workspace and sign them in, or read who is signed in (`Host.Accounts`), so invite-only sign-up and staff pages reuse the engine's users and sessions instead of copying their formats; and administer accounts (`SendVerification`, `SendPasswordReset`, `PlanUserDeletion`, `DeleteUser`, `DeleteWorkspace`) so a staff console deletes people and workspaces by the engine's own rules for what belongs to them
  - workspace features: routes under `/v1/workspaces/{ws}/cloud/...` behind the engine's own sign-in, workspace, scope, brand, mission and idempotency checks (`Host.HandleWorkspace`, with the caller's audit events through `Caller.Record`); the workspace's own connected accounts by capability, with their spend booked in its ledger (`Host.Connections`); the workspace's own AI model under its monthly AI limit (`Host.AI`); its company records (`EnsureCompanies`); and pipelines whose stages move items one at a time with shared rate limits and breakers (`Host.Pipeline`, [pipeline.md](../pipeline.md)). These only ever use the workspace's own keys, so a Cloud feature follows the same bring-your-own-keys rule as the engine

  It does this without changing open-source code.
- **Web app.** Cloud builds `web/` from this repository at the same commit. Cloud-only screens appear when `/v1/meta` lists their capability, so there is one web app, not a fork.
- **Database.** A Cloud extension keeps its tables in its own Postgres schema (`cloud`) and creates them in `Setup`. The open-source migrations stay the only ones in the `public` schema.

## Staying in sync

Outflare Cloud uses this repository as a pinned dependency and updates to each new commit automatically. Nobody ports changes by hand. A change that only the Cloud needs goes in the Cloud repository; a change everyone benefits from goes here first.

## Contributions

Outside contributions need the [CLA](../../CLA.md), checked on every pull request. As copyright holder we can run the AGPL core inside a service with private additions. Without a CLA, code contributed under the AGPL alone would oblige us to publish the Cloud layer too.

## Why

- **One product.** Self-hosters get every product feature, which is the point of an open-source control plane. Cloud sells running it: uptime, deliverability operations, managed mailboxes and support.
- **No drift.** Cloud consumes the public code as a dependency, so it cannot fall behind or diverge.
- **Clear line.** Anything a single team self-hosting would need is public. Anything only a multi-tenant paid service needs is private.

## Alternatives considered

- **Private fork of the whole repository.** Every public change becomes a merge into the fork, conflicts grow over time, and the two products drift.
- **Everything public, Cloud config included.** Workable, but exposes billing logic and infrastructure details that give no value to self-hosters.
- **Closed "enterprise" features in this repository** (an `ee/` folder under another licence). Adds a second licence to the public repo and blurs what self-hosters get. This stays available later for features like SSO or audit export if they become paid.
