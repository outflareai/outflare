# Conversions

A conversion is whatever a workspace counts as one: a sign-up, a payment, a booked or held call, a qualified lead, an order. Outflare defines conversions once, on the customer events stream ([customers.md](customers.md#product-events)), and never per integration. A call is a conversion because a `call.completed` or `meeting.held` event says so, whichever tool sent it. Modules read the same definitions:

- [Attribution](attribution.md) credits channels with them.
- Each definition also carries every ad network's own name for the conversion, for sending it back ([paid media](paid-media.md#sending-conversions-back) still sends by its own conversion rules today).
- Modules in the engine, such as partner programs, read them through `engine/internal/conversions`.

Nothing here is particular to a kind of business. Presets are starting points a workspace copies and changes.

In the web app they live under **Analytics → Channels → Conversions**: add a preset, define a conversion with its conditions, value and ad network names, edit or archive one. The API and MCP do the same.

## A definition

| Field | What it is |
| --- | --- |
| `key`, `name` | `qualified_call`, "Qualified call". The key never changes. |
| `event` | The customer event, such as `signed_up`, `payment.succeeded`, `meeting.held` or `call.completed`. |
| `filters` | Conditions on the event's properties, all of which must match: `[{"property": "duration_s", "op": "gt", "value": 60}]`. Ops are `eq`, `neq`, `gt`, `gte`, `lt`, `lte`, `in`, `contains`, `exists` and `not_exists`. A property can be a dotted path (`plan.tier`). Text compares ignoring case. |
| `value_mode` | What one conversion is worth: `none`, `fixed` (`fixed_value_minor`, in cents, with a `currency`), or `property` (`value_property`, such as `revenue` in major units or `amount_minor` in minor units, set by `value_unit`). |
| `currency` | A fixed currency, or empty for the event's own `currency` property. |
| `network_events` | Each ad network's name for it, by network id: `{"meta": "Purchase", "tiktok": "CompletePayment"}`. |
| `attribution` | Whether attribution credits channels with it (on by default). |

```json
{"key": "qualified_call", "name": "Qualified call", "event": "call.completed",
 "filters": [{"property": "duration_s", "op": "gt", "value": 60}],
 "value_mode": "fixed", "fixed_value_minor": 5000, "currency": "USD"}
```

Anything without an integration posts its events through the API (`POST .../customer-events`) or a webhook, and a definition counts them like any other.

## Presets

`GET .../conversion-definitions/presets` lists them, and `POST .../conversion-definitions/presets/{preset}` adds the definitions whose keys the workspace doesn't use yet. Adding one twice changes nothing.

| Preset | Definitions |
| --- | --- |
| `saas` | Sign-up (`signed_up`), Trial started (`trial_started`), Payment (`payment.succeeded`, valued at `amount_minor`) |
| `booked_calls` | Booked call (`meeting.booked`), Held call (`meeting.held`), Deal won (`payment.succeeded`) |
| `ecommerce` | Purchase (`order_completed`, valued at `revenue`), Account created (`signed_up`, not credited) |
| `lead_gen` | Lead (`form_submitted`), Qualified lead (`lead_qualified`) |

Network events in presets use Meta's, TikTok's and Reddit's standard event names. Add other networks' names to a definition yourself.

## How attribution uses them

Attribution counts two kinds of events:

- the events in its settings' `conversion_events` (`signed_up` and `payment.succeeded` unless changed), each as it is, with the value the event carries
- events that match a definition with `attribution` on, with the definition's value

When an event matches several definitions, the first by key counts. A definition's event that its filters leave out isn't a conversion. Each credited conversion records the definition it matched (`definition_id`). Every change to a definition credits all conversions again in the background, within a few minutes.

## API and tools

| Call | MCP | What it does |
| --- | --- | --- |
| `GET .../conversion-definitions` | `list_conversion_definitions` | The workspace's definitions (`archived=true` for archived ones too) |
| `POST .../conversion-definitions` | `create_conversion_definition` | Define one |
| `GET`, `PATCH`, `DELETE .../conversion-definitions/{key}` | `update_conversion_definition`, `archive_conversion_definition` | Read, change (fields left out keep their values) or archive one |
| `GET .../conversion-definitions/presets`, `POST .../presets/{preset}` | `list_conversion_presets`, `add_conversion_preset` | The presets, and adding one |

Reading needs `analytics.read`; changing needs `analytics.manage`. The audit actions are `conversion_definition.created`, `conversion_definition.updated`, `conversion_definition.archived` and `conversion_definitions.preset_added`.

For modules in the engine, `engine/internal/conversions` has the same thing as Go:

- `List`, `Get` and `ForAttribution` read definitions.
- `Definition.Match(props)` says whether an event's properties pass the filters.
- `Definition.Value(props)` gives its value in minor units and its currency.
- `For(defs, event, props)` finds the definition that counts an event.

## Storage

Migration 0253 adds `conversion_definitions`, one row per definition with its filters and network events as JSON, and `attribution_conversions.definition_id`.
