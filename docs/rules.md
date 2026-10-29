# Rules

Rules are the rules engine of the [customer data platform](customers.md). A rule watches what happens to people and other records and, when its condition becomes true, emits a business event of its own on the record's timeline, such as `customer.reactivated` or `customer.stalled`. Anything that listens to events acts on it: a [journey](lifecycle-email.md#journeys) can start on it, a webhook can deliver it, a smart list can count it, and Query can read it.

A rule never sends anything itself. "What happened" (the event) and "what we did about it" (the journey's email, the webhook) stay separate, so one business event can drive several automations, and each can be changed on its own.

Everything here works the same from the REST API, the MCP server and the web app (Lifecycle → Rules).

## In the app

Each rule is a card you read as a sentence: "**When** someone does **signed_up** **and doesn't** do **product_activated** **within** **7 days**, **if** plan is not enterprise, **emit** customer.stalled." Pick the kind of trigger from the first blank and fill in the rest. The card also has:

- **Dry run**: what it would have done over the last 7, 30 or 90 days (below).
- **Where its event goes**: the journeys that start on its event, or end when it happens.
- **When this fires, do…**: pick a first step, and a draft journey that starts on the rule's event opens in the [journey builder](lifecycle-email.md#journeys).

A rule changes while it is a draft or paused.

## Triggers

| Kind | Fires when | Example |
| --- | --- | --- |
| `event` | An event happens on a person's timeline. | `product_activated` with `plan` = `pro` emits `customer.activated_pro`. |
| `change` | A record's fields come to meet the conditions. It fires when they become true, not while they stay true, and again only after they stop being true and become true again. | `lifecycle_stage` becomes `customer`, or a deal's `stage` becomes `won`. |
| `absence` | An event doesn't happen within a time of a start event. Without a start event, each occurrence of the event starts the clock again, so it fires after a stretch with none (inactivity). | `signed_up`, then no `product_activated` within 7 days; or no `api_called` for 14 days. |
| `audience` | Someone joins (`on: enter`) or leaves (`on: leave`) a [smart list](lists.md#smart-lists). | Joined "Paying, no login in 30 days". |

Event, absence and audience rules are about people. A change rule can watch any record type, such as `deal`.

```sh
curl -X POST "$OUTFLARE_URL/v1/workspaces/$WS/rules" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name": "Stalled after sign-up",
       "trigger": {"kind": "absence", "start_event": "signed_up", "event": "product_activated", "within_seconds": 604800},
       "conditions": [{"attribute": "billing_plan", "op": "neq", "value": "enterprise"}],
       "emit_event": "customer.stalled"}'
```

## Conditions

Conditions are the same tests journeys use, with `match` `all` (the default) or `any`:

- `attribute`: a field on the record, such as `plan` or `address.country`.
- `property`: a property of the event that set the rule off. For an absence rule, a property of the start event.

The ops are `eq`, `neq`, `gt`, `lt`, `contains`, `exists` and `not_exists`. Change and audience rules test attributes only. An absence rule tests attributes when its time runs out, so a person who became an enterprise customer in the meantime is left out.

## Firing once

Every firing is recorded once per rule, record and episode. A retry, a replayed job or a duplicate event never fires the rule twice.

| What | Episode |
| --- | --- |
| `event` | Each event: one firing per event. |
| `change` | Each time the condition goes from false to true. |
| `absence` | Each start event: its clock fires at most once. |
| `audience` | Each join or leave. |

`fire: once` fires once per record, ever. `cooldown_seconds` keeps at least that long between two firings for one record.

## What never fires a rule

- **What happened before the rule went live.** A rule acts only on events from its `activated_at`. Activating it again after a pause starts afresh: timers that ran out while it was paused are dropped.
- **Late data.** An event that arrives more than a day after it happened updates history but fires nothing. A backfill or an import never sets automations off.
- **Browser events.** Anyone with a site key can send them, so they never fire a rule, as with journeys.
- **Events not approved in the catalog.** A name the workspace hasn't approved arrives quarantined ([customers.md](customers.md#product-events)) and drives nothing.
- **Changes someone made by hand.** A correction in the app or a `PATCH` with source `manual` doesn't fire a change rule unless the rule sets `manual_edits: true`. Changes from your product (`first_party`), a CRM sync, enrichment or an import do.
- **Merged and archived records.**

## Rules that set off rules

A rule can listen to another rule's event; it then runs one deeper. Chains stop at depth 5. Activating a rule is refused (`409`) when its event would set off active rules that, in the end, set it off again. A rule can't emit an event it listens to.

## The event a rule emits

The emitted event goes on the record's timeline with source `rule`, and is approved in the catalog straight away. Its properties are the triggering event's properties, plus `rule_id` and `rule_version`. A change rule adds `field`, an absence rule adds `start_event` and `within_seconds`, and an audience rule adds `list_id` and `on`. Journeys see them as `event.*`.

## Trying a rule first

`POST .../rules/{id}/simulate` (MCP `simulate_rule`) is a dry run. It shows what the rule would have done over the last `days` (default 30, at most 90): how many records it would have fired for, how many times, and up to 20 of them. Nothing is stored or sent.

- Event and absence rules replay the events Postgres keeps (see the event retention in [analytics storage](analytics-storage.md)).
- Change and audience rules look at records and list members as they are now.
- A simulation looks at up to 50,000 events or records. `truncated` says there were more, and then the numbers are a lower bound. It stops after 15 seconds with `query_timeout`.

## Managing rules

| | |
| --- | --- |
| `GET/POST .../rules`, `GET/PATCH .../rules/{id}` | List, create, read and change rules (scopes `customers.read` and `customers.manage`) |
| `POST .../rules/{id}/activate`, `pause`, `archive` | Turn a rule on or off, or stop it for good |
| `POST .../rules/{id}/simulate` | The dry run |
| `GET .../rules/{id}/firings` | When it fired and for whom, newest first, with the event it emitted and the one that set it off |

A rule is a draft until activated. A draft or paused rule changes freely, and a change to what it does raises its `version`, which each firing records, and starts its timers and change tracking afresh (its firings stay, so `fire: once` still holds). An active rule only changes its name and description, so pause it first. A brand's rule covers that brand's records and the shared ones. People limited to some brands see and change only their brands' rules.

The MCP tools are `list_rules`, `get_rule`, `create_rule`, `update_rule`, `simulate_rule`, `activate_rule`, `pause_rule`, `archive_rule` and `list_rule_firings`.

## Webhooks and events

Each firing is the `rule.fired` webhook event, with `record_id`, `event` (the emitted name), `event_id`, `trigger`, `depth` and `rule_version`. Subscribe a [webhook](api-conventions.md) to it to act on rules from your own systems. Making and changing rules are `rule.created`, `rule.updated`, `rule.activated`, `rule.paused` and `rule.archived`.

## How it runs

Rules are evaluated in jobs on the Postgres queue, so nothing slows down the request that stored an event or changed a record. Each check reads only the record and the one event or change in hand, never its history, so the cost doesn't grow with how much a workspace has stored.

- **Events and changes.** When an event is stored, or a field changes, that a rule listens to, a `rule.event` or `rule.change` job evaluates the rules that care.
- **Absence timers.** These are kept per rule and record. A minute's sweep fires the ones whose time ran out; workers on other servers skip the ones one server holds.
- **Audiences.** A smart list's refresh hands who joined and left to a `rule.audience` job, but only when an active rule watches the list.

The Grafana dashboard's Rules row and three alerts cover it ([monitoring.md](monitoring.md)).
