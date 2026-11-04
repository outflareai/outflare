# Agent actions

The action layer decides what an agent may change in your workspace on its own, holds the rest for a person, and keeps every change it made. It works the same for any agent: your own AI app over MCP, a script with an API token, or an agent running in Outflare Cloud. [ADR 13](adr/0013-action-layer.md) explains why.

## Turning it on for a token

Tick **It's an agent: send its changes through the action layer** when you create a token under API tokens, or later when editing it, or with the API:

```sh
curl -X POST "localhost:8080/v1/workspaces/$WS/api-tokens" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"name": "Growth agent", "scopes": ["sequences.read", "sequences.manage"], "action_gate": true}'

curl -X PATCH "localhost:8080/v1/workspaces/$WS/api-tokens/$TOKEN" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" -d '{"action_gate": true}'
```

From then on the token's reads run as before, and each change it makes is an **action**. Only a signed-in person turns the action layer off for a token. A token with the action layer on doesn't need a mission for its changes.

## What happens to a change

| The gate | The agent gets | What happens |
| --- | --- | --- |
| A grant says **run** | The normal answer | It runs, and is kept as an executed (or failed) action |
| A grant says **hold**, no grant matches, or the change always needs a person | `202` with the held `action` | Nothing changes until a person approves it |
| A grant says **refuse** | `403 action_refused` | Nothing changes |

A grant that runs changes can have a **daily limit**: once it has run that many in a UTC day, the rest are held.

**Always a person.** These wait for a person whatever the grants say: mailboxes, sending servers, senders, sign-ins, API tokens, webhooks, connections, email providers, suppressions, company context, one-off emails, and the fixes [fix autonomy](fix-autonomy.md) holds.

Each action carries the brand it is about: the agent's brand when its token is kept to one, else the brand route's, `?brand=`, or the body's `brand_id`; none for a change about shared records. A person kept to some brands sees and decides only changes about one of them. A change that ran and failed keeps the API's answer in `result`.

## Grants

A grant says what happens to one token's changes that match it. Only a signed-in person with `actions.manage` sets them, on **Agents** in the app (What agents may do) or with the API:

```sh
# Let the agent create sequences on its own, at most 5 a day.
curl -X POST "localhost:8080/v1/workspaces/$WS/action-grants" \
  -H "Cookie: outflare_session=..." -H "Content-Type: application/json" \
  -d '{"token_id": "'$TOKEN'", "method": "POST", "path": "sequences", "mode": "run", "daily_limit": 5}'

# Never let it delete one.
curl -X POST "localhost:8080/v1/workspaces/$WS/action-grants" \
  -H "Cookie: outflare_session=..." -H "Content-Type: application/json" \
  -d '{"token_id": "'$TOKEN'", "method": "DELETE", "path": "sequences/*", "mode": "refuse"}'
```

- **path** is the part after `/v1/workspaces/{ws}/`. `*` matches one segment and a trailing `**` one or more segments under it: `sequences/*/enrollments`, `leads/**` (what is under leads, not `leads` itself), or `**` for everything. Brand routes start with `brands/*/`.
- **method** is `POST`, `PUT`, `PATCH` or `DELETE`, or left out for any.
- **mode** is `run`, `hold` or `refuse`.
- **brand** keeps a grant to a token bound to that brand; it must be the token's own brand.
- **daily_limit** is the most changes a run grant runs in a UTC day; the rest wait for a person.
- **daily_spend_cents**, **monthly_spend_cents** and **total_spend_cents** (for good: a budget for one job) cap what a run grant's changes spend on your own accounts (the spend ledger, `GET .../spend`): spend booked while a change runs is linked to it, each change shows it as `spend_cents`, and once the day's or month's spend plus what the next change is likely to cost (the average of the grant's recent changes) would pass the cap, the change waits for a person (`decided_by_rule: spend_limit`). A first change under a grant has no history and runs. A run grant on paid ads (`ads`, `ads/**`, or the same under `brands/*/`) gets caps of $50 a day and $500 a month when none are given, so a first autonomous grant on ad spend can't surprise anyone; set your own to change them.

**Paid ads** have rules of their own on top ([paid-media.md](paid-media.md), "Managing campaigns"): raising a campaign's daily budget by more than a fifth, or setting one where there was none, always waits for a person; a raise or a resume that would put the brand's active campaigns over the grant's daily cap, or the month over its monthly cap, is held with `spend_limit`; a pause spends nothing and runs.

When several grants match, the most specific decides: one naming the method beats one that doesn't, then the longest path. On a tie, the stricter mode wins. With no match, the change is held, so a new agent starts out asking for everything.

`GET .../action-grants` lists them; `DELETE .../action-grants/{id}` revokes one, from the agent's next change.

## Templates: approve a pattern once

A grant can carry a **template** (`match`): fields every matching change's body must contain with those values. Approving a pattern once lets every change that fits it run, and anything outside it still waits for you. A grant with a template beats one without.

```sh
# Run every sequence the agent creates in this timezone, at most 10 a day.
curl -X POST "localhost:8080/v1/workspaces/$WS/action-grants" \
  -H "Cookie: outflare_session=..." -H "Content-Type: application/json" \
  -d '{"token_id": "'$TOKEN'", "method": "POST", "path": "sequences", "mode": "run", "match": {"timezone": "Europe/London"}, "daily_limit": 10}'
```

The quickest way to make one is from a held change you approve: add `remember` with the body fields that define the pattern, and changes like it run from then on. Only a signed-in person with `actions.manage` can do this, since it changes what the agent may do.

```sh
curl -X POST "localhost:8080/v1/workspaces/$WS/actions/$ACTION/approve" \
  -H "Cookie: outflare_session=..." -H "Content-Type: application/json" \
  -d '{"note": "Good", "remember": {"fields": ["template_id"], "daily_limit": 20}}'
```

Leave `fields` empty to approve every change to that method and path. A template never lets through a change that always needs a person.

## Deciding

**Agents** in the app shows what waits for you, everything your agents changed, and what each may do; `GET .../actions?status=held` is the same approval inbox over the API. Each action shows the request, the agent's reason (its `Outflare-Reason` header), why it was held, and a `body_hash` of exactly what will run.

- **One at a time:** `POST .../actions/{id}/approve` runs it as the agent, through every check again, and answers with the result. `POST .../actions/{id}/reject` with a `note` turns it down.
- **A batch:** `POST .../actions/decide` with `{"approve": [...], "reject": [...], "note": "..."}`, up to 100 at once. Each is decided on its own, and the answer says what happened to each.

Approving needs `actions.approve`, and is for people and approver tokens: an agent token on the action layer never decides on changes, its own or another agent's. A held change nobody decides within 30 days expires, and the agent has to make it again.

## Outflare's agent on the action layer: agent runs

A playbook can run as an **agent run**: Outflare's own agent carries its steps out in turns on the workspace's AI model, as an agent token of its own on the action layer (`POST /v1/workspaces/{ws}/playbooks/{id}/agent-runs`, scope `agents.run`; `agents.read` reads runs, and the mission scopes stand for them while missions retire). The **mode** a person chooses is their approval of what the run may do on its own, set as the token's grants:

| Mode | Grants | What the agent sees |
| --- | --- | --- |
| `copilot` (default) | none: every change waits for a person | `202`, and the decision at its next turn |
| `autonomous` | `**` runs, with `max_spend_cents` as the run's whole budget (`total_spend_cents` on the grant); changes that always need a person still wait | the normal answer, or `202` |
| `dry_run` | `**` refuses: every change is refused and kept, so people see what it would have done | `403 action_refused`; it carries on as if the change had run |

The Agents page's **Runs** view lists runs and opens one: what each turn did, its changes (held ones to decide there), a box to write to it, and pause, resume and stop. The run (`GET .../agent-runs/{id}`) is the agent's check-in: the goal, plan and instructions, the brand's and the company's context, what its changes came to (`actions`, `held_actions`, `spend_cents`) and the latest updates. People write instructions to it (`POST .../agent-runs/{id}/updates`), which bring its next turn forward; the agent posts progress, questions and a report, and completes the run (`POST .../complete`). Pausing stops turns until a person resumes; stopping or completing revokes the run's token, so nothing more can act as it. Its held changes stay on the Agents page for a person to decide or expire. A run that can't carry on (no AI model, its token gone, three failed turns) pauses with the reason.

Runs replace missions, which are retired: what a mission did stays readable, and a change to one, or one sent with the `Outflare-Mission` header, answers `410 mission_retired`.

## Loops in extensions

A loop an extension runs (Outflare Cloud's agents) is an agent token too, made with `Host.Agents().Create`: its secret is never kept, the action layer is on, it holds at most an operator's scopes and never an approving one. Its calls go through `Agents().Call`, in process through the full handler, so it is held to exactly what an outside agent is, and its changes show on the same Agents page.

## Scopes

| Scope | Lets you |
| --- | --- |
| `actions.read` | See every agent change and the grants |
| `actions.approve` | Approve and reject held changes |
| `actions.manage` | Set and revoke grants (a signed-in person only) |

## Events and monitoring

Each decision is a product event, and `action.held`, `action.approved` and `action.rejected` can also be sent to webhooks, so an approver bot or a chat tool can pick up held changes: `action.held`, `action.approved`, `action.rejected`, `action_grant.created`, `action_grant.revoked` and `token.action_gate_changed`. A refused change is its call's `api.called` event with error code `action_refused`. The Grafana dashboard's **Agent actions** row shows what the gate decided, how changes ended, how many wait and for how long; the alerts are listed in [monitoring](monitoring.md#alerts).
