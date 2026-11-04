# Transactions

A brand's transactions are its payments, refunds, chargebacks and credits, one row each. They come from two places:

- **Billing connectors** (Stripe and the others under Settings → Connections). Each charge and refund the sync reads after its first full read becomes a transaction of the payer's brand. Transactions start when the connection does: nothing from before is rebuilt.
- **The API**, for your own billing: `POST /v1/brands/{brand}/transactions` (MCP `record_transaction`).

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

- `kind` is `payment`, `refund`, `chargeback` or `credit`. `amount_minor` is in the currency's minor unit (cents) and never negative: the kind says which way the money went. A refund or chargeback can name the payment it gives back with `refund_of` (that payment's `source_id`).
- `source_id` is your id for it. Sending the same one again never adds it twice: the answer is the transaction already saved (200 instead of 201), with its `status` updated when it changed (`succeeded`, `pending`, `failed` or `reversed`).
- `record_id` is the person or company who paid, and `product` one of the brand's [products](product-lifecycle.md) by id or key. Both are optional.
- `occurred_at` is when it happened, now by default.

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
