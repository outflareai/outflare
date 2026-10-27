# 11. Open core: tools open, loops in Cloud

Status: Decided (27 October 2026). Changes [ADR 5](0005-open-core-cloud.md): self-hosters no longer get every feature.

## Decision

The open engine is the whole product a team runs by hand or with its own agent. Outflare Cloud adds the agents that run it for you, toward a target, and what only a hosted service can offer.

| Open engine (this repository, AGPL-3.0) | Outflare Cloud only (private repository) |
| --- | --- |
| Every tool in every module, all data and reports, outcomes and cost per meeting | Loops: a goal with a target that keeps running, measures results and changes course; autopilots per module; the cross-channel optimiser |
| Missions, the approval gate, limits and the built-in agent | Enterprise controls: SSO, SCIM, audit log export, approval rules across teams |
| Teams, custom roles, brands and agency mode | The scaled, managed version of heavy features: scheduled AI visibility across many models, crawling big sites, ClickHouse-scale analytics, session replay |
| The API, SDK, MCP server, CLI and the connector manifests in `integrations/` | Running the service: sign-up, billing, metering, the staff console |

The rule for a new feature: if a person does it or measures it, it belongs here. If it decides what to do next and acts on it, again and again, toward a target, it belongs in Cloud.

- **Self-hosters see Cloud features.** The engine keeps a catalog of Cloud features (name, one line, where it sits, the capability that unlocks it). Self-hosted servers show them as locked, with an upgrade prompt; Cloud lists the capability in `/v1/meta` and the real screen loads.
- **Paid features go through the same gate.** A loop acts through the public API as an agent inside a mission, so every step passes the same approval gate, limits, audit log and events.
- **Cloud only, for now.** There is no licence key that turns paid features on in a self-hosted server.
- **Nothing moves back.** A feature in the open engine never becomes paid.

## Why

Self-hosters keep a complete, agentic product: their own agent can drive every tool over MCP. People pay for work done for them, which suits bring your own keys: Cloud charges for software, never for AI. Hosting the autonomous parts also keeps abuse controls on them.

## Consequences

- Each change goes to the repository the routing list in AGENTS.md ("Open core") picks.
- The engine gains the catalog, the locked state and the hooks loops need; Cloud gains the loops.
- Licences stay as in ADR 1: AGPL-3.0 core, Apache-2.0 SDKs, MCP and `integrations/`, a CLA for contributors.
