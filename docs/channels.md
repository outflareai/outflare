# Channels: the framework every channel follows

The brand agent works across channels: paid ads, content on the brand's website, social, communities, email outreach, link building, press, SEO, reviews, newsletters (broadcasts to the brand's own subscribers), website conversion (A/B tests, experiments and feature flags), competitor watch, and, through accounts and browser workers, listings and marketplaces and forums and launch sites, with more to come. Every channel is described the same way (`engine/internal/channels`), so the agent's tool selection, the readiness checks and the dashboard pick a new channel up by themselves, and the test kit holds every channel to the same bar. Channel code is open; deciding when to use a channel is the agent's.

## A channel's manifest

| Part | What it is |
| --- | --- |
| Needs | The connections (by capability, with recommended connectors) and the data the workspace must have, in the same shape as a playbook's needs ([playbooks.md](playbooks.md)). Bring your own keys: only the workspace's own connections count. |
| Actions | Every kind of change the agent can make, named by the API operation that makes it, with its approval category: `routine` (runs within the agent's grants, else held for a person), `spend` (a run grant carries daily and monthly caps and the largest change allowed) or `always_person` (never runs by itself). Each says whether it can be undone and what a run grant allows a day by default. |
| Reads | The operations the agent decides from: current state, performance, what is new. |
| Measures | How the channel's results are judged in the shared outcomes and attribution, so channels compare. |
| Playbooks | The proven plays on it, from `playbooks/`. |
| Docs, dashboard, metrics, alerts | Its page, its row on the Grafana dashboard, the metrics the engine exports for it and the rules that watch them ([monitoring.md](monitoring.md)). |
| Tests | The test functions that run it against recorded provider responses and the shared action-layer cases: its test kit. |

## Readiness

`GET /v1/brands/{brand}/channels` (scope `workspace.read`) lists the channels with their readiness for one brand (the workspace route answers 410 `route_moved`):

- `ready`: every required connection is connected and the data is there.
- `connect_first`: a required connection is missing; `missing_connections` names it with recommended connectors. An optional connection (`missing_optional`) never holds a channel back.
- `needs_data`: connected, but the data the channel works on isn't there yet (`missing_data`): an ad account read from the platform, a social account, a site added to SEO, mailboxes, the brand's context.

The MCP tool `list_channels` gives the same. The brand agent reads it before choosing what to work on; a person sees what to connect next.

## The test kit

`go test ./engine/internal/channels/` runs the kit over every channel: each action is a real write in `api/openapi.yaml` with a scope and a `202` answer (so the action layer can hold it), each read is a real operation, each playbook ships, each capability has a connector, each data check exists, the docs page, the dashboard row, the metrics (exported and on a panel), the alerts and the tests it names exist. A channel that fails the kit is not one the agent may use. The engine's own tests for a channel (recorded provider responses, the agent's writes held and approved) are the ones its manifest names.

## Adding a channel

1. Connectors for its platforms, with the workspace's own keys (`engine/internal/connectors`, [connectors.md](connectors.md)).
2. Its routes, with `x-outflare-access` and a `202 MissionHeld` answer on every write, and its jobs for side effects.
3. Measurement into outcomes and attribution; events, metrics, a dashboard row and alerts (AGENTS.md).
4. Playbooks for its proven plays.
5. Tests against recorded responses and an agent's held change, named in the manifest.
6. The manifest in `engine/internal/channels/channels.go`; the kit tells you what is missing.

Channels with no API (`listings`, `forums`) go through accounts and browser workers ([accounts.md](accounts.md), [browser-workers.md](browser-workers.md)): their actions record the brand's account, queue a bounded browser task, ask a person to take the account over, and record the appearance in Coverage; they are ready once the brand's context is written and an account is recorded (`brand_accounts`).
