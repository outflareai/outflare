# Products and lifecycle stages

A brand often sells more than one product, and a customer can be in a different place with each one: still paying for one, cancelled on another, trying a third. Outflare keeps a **stage per product** for every company and person, so a rule or journey reacts to the right product. A company that cancels one product while it keeps paying for another is not "churned", and it doesn't get a "welcome back" email when it uses the product it never left.

This is part of the [customer data platform](customers.md). Everything here works the same from the REST API, the MCP server and the web app (People → Products, and a Products card on each person and company). It belongs to one brand: the API lives under `/v1/brands/{brand}/`.

## Products

A product is something the brand sells, such as "Residential proxies" or "Datacenter proxies". Each has:

- **an id and a key.** The key is made from the name (`residential-proxies`) unless you choose one. Neither ever changes, so renaming a product never breaks a rule or an event.
- **aliases**: the names your own systems use for it. `event:resi` is a name your product sends in its events; `billing:prod_123` is the product id in your billing system. A bare name is an event name.
- **a churn mode.** `cancellation` (the default): a customer churns when they cancel. `inactivity`: a customer churns after no usage for `churn_days` (default 30), which suits pay-as-you-go products nobody ever "cancels".
- **`at_risk_days`** (default 14): no usage for this long moves an active customer to *at risk*.

`POST /v1/brands/{brand}/products` adds one (scope `catalog.manage`, MCP `create_product`). `PATCH .../products/{product}` changes its name, churn mode, days and aliases (in the app: Edit on its row in People → Products). `POST .../archive` archives it: it keeps its history, but its events no longer move anyone and it leaves companies' overall status. `POST .../restore` brings it back.

### Which product an event is about

An event names its product in its properties:

```json
{"type": "track", "userId": "u_42", "event": "Proxy Request", "properties": {"product": "resi"}}
```

`properties.product_id` (the product's id or key) or `properties.product` (an alias) both work, in any case. Billing connections (Stripe and the others) name the product on subscription events by its billing id, which matches a `billing:` alias. The answer to `POST /v1/workspaces/{ws}/customer-events` says which product each event matched (`product_id`).

Products come from wherever your brand already has them: a billing connection, the API, the names in your events, or by hand. You don't need Stripe. Choose which of these the brand takes names from with `sources` (`PATCH /v1/brands/{brand}/lifecycle`).

### Held names

A name no product has yet is **held** for review: it is listed with how often and when it was seen, and drives nothing until someone maps it. `GET /v1/brands/{brand}/product-names?status=held` (MCP `list_product_names`) lists them. `PATCH .../product-names/{name}` with `{"status": "mapped", "product_id": "..."}` maps one, and `{"status": "ignored"}` ignores it (scope `customers.manage`). You can also map or ignore a name before it is seen (`POST .../product-names`). Events already stored keep the product they had; events from then on use the mapping.

Names are held only once the brand has a product or has stages on, so a brand that doesn't use products never collects them.

## Stages

Stages are kept on two levels:

| Level | Kept on | Default stages | For |
| --- | --- | --- | --- |
| `company` | The company (a person with no company carries them on their own record) | lead → trial → active → at risk → churned | Billing: sales, win-back, alerts to the account owner |
| `person` | Each person | invited → activated → at risk → inactive | Usage: onboarding and nudges to that person only |

Each stage has a **category** (`lead`, `trial`, `active`, `at_risk`, `churned`) that says what it means, and a **rank** used for the company's overall status. A brand can rename, add and remove stages: in the app under People → Products → Lifecycle stages → Edit stages (people with `catalog.manage`), with `PUT /v1/brands/{brand}/lifecycle/stages` or MCP `set_lifecycle_stages`. A stage's key never changes once saved. A stage someone is in, or a move uses, can't be removed.

### Moves

A move takes someone from some stages to another, and **emits an event** on their timeline that [rules](rules.md) and [journeys](lifecycle-email.md#journeys) can start on. There are three kinds:

- **event**: an event happens for the product, such as `subscription.canceled`, with optional tests on its properties (`status` = `trialing`).
- **usage**: any event your product sends about itself. Billing syncs and Outflare's own events never count as usage.
- **inactivity**: no usage for the product's `at_risk_days` or `churn_days`. A sweep checks every minute.

The defaults:

| Level | Move | From | To | Emits |
| --- | --- | --- | --- | --- |
| company | `trial.started`, or `subscription.started` with status trialing | none, lead | trial | `product.trial_started` |
| company | `payment.succeeded`, `subscription.started` (active), or `subscription.changed` to active from a trial | none, lead, trial | active | `product.activated` |
| company | `payment.succeeded` or `subscription.started` (active) | churned | active | `product.reactivated` |
| company | `trial.ended` | trial | churned | `product.trial_expired` |
| company | `subscription.canceled` (cancellation products) | trial, active, at risk | churned | `product.churned` |
| company | usage | at risk | active | `product.recovered` |
| company | usage (inactivity products) | churned | active | `product.reactivated` |
| company | no usage for `at_risk_days` | active | at risk | `product.at_risk` |
| company | no usage for `churn_days` (inactivity products) | active, at risk | churned | `product.churned` |
| person | `user.invited` | none | invited | `product.user_invited` |
| person | usage | none, invited | activated | `product.user_activated` |
| person | usage | at risk, inactive | activated | `product.user_recovered` |
| person | no usage for `at_risk_days` | activated | at risk | `product.user_at_risk` |
| person | no usage for `churn_days` | activated, at risk | inactive | `product.user_inactive` |

Every emitted event carries `product_id`, `product_key`, `level`, `move`, `from_stage` and `to_stage`, so a journey can test which product it was. Each move is also the `lifecycle.moved` webhook event, so a CRM or warehouse can follow stages, and Query has them as `products`, `lifecycle_states`, `lifecycle_moves` and `lifecycle_overall` ([Query](query.md)). Moves are tried in order and the first that fits wins; change them in the app (Edit moves: the event or usage or time without usage that moves someone, where from, where to and the event it records, in order), with `PUT /v1/brands/{brand}/lifecycle/moves` or MCP `set_lifecycle_moves`. A move's property tests can be set only through the API; the app keeps them as they are. `from_stages` may include `none`, someone with no stage yet.

What keeps moves safe:

- **Once per cause.** The same event, retried or replayed, never moves anyone twice.
- **Late or old news only counts as usage.** An event that arrives over a day late, or that happened before the current stage began, moves nobody.
- **Held event names drive nothing**, as everywhere in Outflare.
- **A late sweep goes straight to the right stage.** If a product has gone unused past both waits, it moves to churned, not through at risk first.

## A company's overall status

Each company also has an **overall status** across its products: its highest-ranked billing stage, plus flags.

- `partly_churned`: one product churned while another is still in trial, active or at risk.
- `trialling_another`: paying for one product while trialling another.

Changes to it are events too, on the company:

| Event | When |
| --- | --- |
| `customer.became_customer` | Its first product went active (once per company; a later return is `product.reactivated`). |
| `customer.expanded` | Another product went active while it already paid for one. |
| `customer.partly_churned` | A product churned while another is still live. |
| `customer.fully_churned` | Every product is churned. |

`GET /v1/brands/{brand}/records/{record}/lifecycle` (MCP `get_record_lifecycle`) shows a company's or person's stage per product, the overall status and the latest moves.

## Turning it on

`PATCH /v1/brands/{brand}/lifecycle` with `{"enabled": true}` (scope `catalog.manage`, MCP `update_lifecycle`). A brand with no stages yet gets the defaults above.

Turning it on **starts from nothing**. Stages fill only from events after that moment; nothing is rebuilt from history, and nothing from before it ever moves anyone or sends anything. Turning it off stops all moves and keeps the stages as they are; turning it on again counts only events from then on.

## Monitoring

`outflare_lifecycle_evaluations_total` counts checks by level and outcome, `outflare_lifecycle_due` and `outflare_lifecycle_oldest_due_seconds` say how far the inactivity sweep is behind, and `outflare_lifecycle_sweep_seconds` how long a sweep takes ([monitoring](monitoring.md)).
