# Transactions

A brand's transactions are its payments, refunds, chargebacks and credits, one row each. They come from two places:

- **Billing connectors** (Stripe and the others under Settings → Connections). Each charge and refund the sync reads after its first full read becomes a transaction of the payer's brand. Transactions start when the connection does: nothing from before is rebuilt.
- **The API**, for your own billing: `POST /v1/brands/{brand}/transactions` (MCP `record_transaction`).
- **Order Completed events** with revenue, sent from your servers ([product events](customers.md)).

However a payment arrives, it is kept once and counted once (see [One payment, counted once](#one-payment-counted-once)).

```http
POST /v1/brands/{brand}/transactions
{
  "kind": "payment",
  "amount_minor": 4900,
  "currency": "EUR",
  "source_id": "inv_1042",
  "record_id": "…",
  "product": "pro",
  "status": "succeeded"
}
```

- `kind` is `payment`, `refund`, `chargeback` or `credit`. `amount_minor` is in the currency's minor unit (cents) and never negative: the kind says which way the money went. A refund or chargeback names the payment it gives back with `refund_of` (see "Refunds, counted once").
- `source_id` is your id for it. Sending the same one again never adds it twice: the answer is the transaction already saved (200 instead of 201), with its `status` updated when it changed (`succeeded`, `pending`, `failed` or `reversed`).
- `record_id` is the person or company who paid. Without it, `user_id` is your own id for them (the `userId` your events send), and Outflare finds the person who has it. `product` is one of the brand's [products](product-lifecycle.md) by id or key. All three are optional, but a payment without a person counts for nothing beyond the brand's totals: send `record_id` or `user_id` so it reaches revenue by channel and your ad networks.
- `occurred_at` is when it happened, now by default.

## One payment, counted once

Revenue, [attribution](attribution.md) and the [conversions sent back to ad networks](paid-media.md#sending-conversions-back) count payments as `payment.succeeded` events, and refunds as `payment.refunded`. Every payment becomes exactly one of them, whichever way it arrives:

- **A transaction from the API.** A `succeeded` payment with a person (`record_id` or `user_id`) is a `payment.succeeded` event, and a refund or chargeback is a `payment.refunded` for the payment it names in `refund_of`. One that is `pending` counts when it is sent again as `succeeded`. Credits never count.
- **An Order Completed event.** A track event named `Order Completed` (stored as `order_completed`), sent from your servers for a known person, with `revenue` (or `total`, or `value`) above zero in major units (`49.00`) and a `currency` (the brand's when left out), is a payment. It is saved as a transaction (`source` `events`, `source_id` its `order_id`) and counted as a `payment.succeeded` event. Events from the website snippet never count as money, since anyone can send them.
- **A billing connector**, such as Stripe, as before.

**When the same payment arrives twice**, from two of these, it is kept once and counted once. Outflare takes them as the same payment when:

1. **They share an id.** The event's `order_id` (or `transaction_id`, or `payment_id`) is the transaction's `source_id`, or a connector's charge id. This is the reliable way: **send the same id in both.**
2. **Or, with no shared id,** the same person paid the same amount in the same currency within 15 minutes.

The one that arrives second is answered as the first: the API returns the transaction already saved (`200`), a billing sync skips the charge, and the order event gets the property `outflare_payment`, the message id of the `payment.succeeded` it is counted as. A transaction sent without a person learns it from its order event, and counts from then on.

So that each payment counts once in your reports and ad networks, count **`payment.succeeded`** (valued at `amount_minor`, in minor units) in [conversion definitions](conversions.md) and ad conversion rules, not `order_completed`. Attribution already leaves out an order that was counted as a payment whenever it counts `payment.succeeded`.

An Order Completed event, sent from your server to `POST /v1/brands/{brand}/events`:

```json
{"type": "track", "userId": "u_123", "event": "Order Completed", "messageId": "order-ch_1042",
 "properties": {"order_id": "ch_1042", "revenue": 49.00, "currency": "USD"}}
```

The same payment as a transaction, to `POST /v1/brands/{brand}/transactions`, counted once with the event above:

```json
{"kind": "payment", "amount_minor": 4900, "currency": "USD", "source_id": "ch_1042", "user_id": "u_123"}
```

Send every payment that brings money in: a top-up of a prepaid balance is a payment, but spending that balance on a plan is not, or the same money counts twice.

## Refunds, counted once

Refunds follow the same rule as payments: send them as a transaction, as an event, or both, and each counts once.

- **A refund transaction** has `kind` `refund` (or `chargeback`) and names its payment in `refund_of`. Any id the payment has works: its `source_id` (an order's `order_id`), its Order Completed event's `messageId` (`order-ch_1042`), or Outflare's transaction id. It is saved as the payment's `source_id`.
- **An Order Refunded event**, from your server for a known person, is a refund of the payment its `order_id` (or `transaction_id`, or `payment_id`) names, the same forgiving way. Its amount is `amount` (or `revenue`, `total`, `value`) in major units, and the whole payment when none is given. Its own id is `refund_id`, else its `messageId`, and its properties get `outflare_refund`, the refund's transaction id.
- **When the same refund arrives both ways**, it is kept once: the same refund id (`source_id` = `refund_id`), or else a refund of the same payment of the same amount and currency within 15 minutes.

A refund counts as `payment.refunded` once its payment is known. One that arrives before its payment waits and counts when the payment comes. One that names an id no payment has is listed on [Developer setup](developer-setup.md) under **Refunds to match**, to link to its payment by hand (`POST /v1/brands/{brand}/transactions/{transaction}/refund-of`). A counted refund comes off its payment's revenue and [lowers its conversion in ad networks](paid-media.md#refunds).

```json
{"kind": "refund", "amount_minor": 4900, "currency": "USD", "source_id": "re_2001", "refund_of": "ch_1042", "user_id": "u_123"}
```

The same refund as an event, counted once with the transaction above:

```json
{"type": "track", "userId": "u_123", "event": "Order Refunded", "messageId": "refund-re_2001",
 "properties": {"order_id": "ch_1042", "refund_id": "re_2001", "amount": 49.00, "currency": "USD"}}
```

## Currencies

Each amount is kept in its own currency and converted to the brand's (`currency` on the brand, USD by default; `PATCH /v1/workspaces/{ws}/brands/{brand}`) at that day's European Central Bank rate: `brand_amount_minor`. When the day's rate isn't known yet it is null, and filled in within the hour once it is.

## Reading them

- `GET /v1/brands/{brand}/transactions` lists them newest first: `?record=` for one person or company, `?kind=refund,chargeback` for some kinds (MCP `list_transactions`). `GET .../transactions/{transaction}` reads one.
- Query has them as `transactions`, for example revenue by month: `SELECT date_trunc('month', occurred_at), sum(brand_amount_minor) FILTER (WHERE kind = 'payment') FROM transactions GROUP BY 1`.
- Webhooks: `transaction.created` once per transaction, `transaction.updated` when its status changes.

## Deleting and restoring

A wrong or test transaction can be deleted: `POST /v1/brands/{brand}/transactions/{transaction}/delete` (MCP `delete_transaction`), with the `customers.manage` scope. It goes to Recently deleted, like everything in the [7-day recovery window](customers.md):

- While it is there, the list, Query and the record card leave it out.
- The same `source_id` sent again doesn't add it back: a billing sync skips it, and the API answers 409 `transaction_deleted`. Restore it instead.
- Restoring it (`POST .../recently-deleted/{deletion}/restore`) within 7 days puts it back as it was. `transaction.created` is not sent again; `deletion.restored` is. If its person or company was merged since, it comes back on the record kept.
- After 7 days it is purged for good, and the same `source_id` can be sent again.
- Webhook: `transaction.deleted`.

A transaction is the business's record of money: merging two records moves their transactions to the one kept, and erasing a person keeps their transactions, unlinked from them, including any waiting in Recently deleted.

Operators see what was saved, and amounts not converted, on the Grafana row "Transactions" ([monitoring](monitoring.md)).
