# Developer setup

**People → Developer setup** (`GET /v1/brands/{brand}/developer-setup`, MCP `get_developer_setup`) is a live checklist of what one brand's developer sends. Each brand has its own, since each brand's integration is separate. The page refreshes every 30 seconds while it is open.

Each check says **OK**, **Check** (it works, but something should be fixed) or **Not yet** (nothing has arrived), what it found, and what to send to fix it, with an example to copy:

| Check | OK when |
| --- | --- |
| Sign-ups arrive from your server | A `Signed Up` event from the server in the last 7 days. |
| Orders arrive and count as payments | An `Order Completed` from the server in the last 7 days, and every recent one had a known person (`userId`) and revenue above zero, so each [counts as a payment](transactions.md#one-payment-counted-once). |
| Payments are counted once | A payment in the last 7 days, and every recent one has a person, so it counts toward revenue and ads. A brand with at least five payments in 60 days also gets a warning when none has come for three times the usual gap between payments (at least two days). |
| Orders carry their payment's id | Every recent server `Order Completed` has an `order_id` (or `transaction_id`, or `payment_id`), so it can be matched to its transaction by id. |
| Payments sent two ways share an id and an amount | Every payment that arrived both as an order and as a transaction in the last 30 days matched by id, with the same amount and currency. |
| No payment is saved twice | No payment or refund is kept twice, once from an order event and once as a transaction with the same id. **Merge them** fixes those (see below). |
| Each charge has its own id | No payment id came again a day or more after the payment it first named. |
| A retry keeps its payment's id | No payment came minutes after another from the same person, of the same amount and from the same way, under a different id. |
| Amounts are in cents | No recent payment is 50 or more times smaller than the brand's usual payment in its currency (from five payments, with a usual amount of at least 1000 minor units). |
| Refunds find their payment | Every refund names a payment Outflare has. |
| Website and server copies agree | The website tag's sign-ups and orders match the server's copies by `messageId` or `order_id`. A copy matched only by person and time is a warning: it is still [counted once](paid-media.md#sending-conversions-back), but the id should be added. |
| Refunds lower the conversions sent to ad networks | Shown once a refund has been sent to an ad network: every one was taken. |

## Refunds to match

A refund whose `refund_of` (or Order Refunded `order_id`) names no payment of the brand is kept but doesn't count. The page lists them, newest first. Enter the payment's order id, its order's `messageId`, or Outflare's transaction id, and **Link**: the refund then counts, comes off that payment's revenue and lowers its conversion in ad networks (`POST /v1/brands/{brand}/transactions/{transaction}/refund-of` with `{"payment": "ch_1042"}`, MCP `link_refund`; the audit event is `transaction.refund_linked`). A refund that arrives before its payment needs nothing: it counts when the payment comes.

## Payments sent from your server

When a brand's backend sends its payments, every payment should carry **one id**, the charge's own (an invoice id), wherever it is sent: `source_id` on a transaction, and `order_id` on its `Order Completed` if it sends both. Then each payment is kept once, whatever the timing and however often it is retried, and nothing is left to a guess ([One payment, counted once](transactions.md#one-payment-counted-once)). The checks above show where that isn't happening, and what it costs:

- **No `order_id`, or different ids both ways.** The two copies can only be matched by a guess: the same person, amount and currency within 15 minutes. A guess misses when the amounts differ or the calls are far apart (the payment counts twice), and joins two real payments of the same amount (one is left out). Each merge is the audit event `transaction.merged`, with `matched_by` `id` or `guess`.
- **Different amounts both ways.** The copy that arrived first keeps its amount. Usually `amount_minor` was sent in dollars (`49`) instead of cents (`4900`).
- **The same id on a later date.** Outflare keeps the first payment and leaves the new one out (`transaction.resent`). Usually a subscription's id sent for every renewal: every renewal after the first is missing from revenue. Send each charge's own id.
- **A retry under a new id.** Each copy counts. Make the id when the payment happens and send it on every retry.
- **Amounts far below the usual.** Usually dollars sent as `amount_minor`.
- **Gone quiet.** A payment that is never sent can't be seen any other way.

Also send `user_id` on every transaction (the same id as `userId` on events), `occurred_at` for anything that didn't just happen (a backfill), and refunds and chargebacks with `refund_of` set to the payment's id.

## Payments saved twice

Before both ways took turns on an id, a payment sent as an order event and as a transaction at the same moment could be kept twice. **Merge them** on the **No payment is saved twice** check (`POST /v1/brands/{brand}/developer-setup/merge-duplicates`, MCP `merge_duplicate_payments`) keeps each once: the copy already counted stays (else the one with a person, else the first), takes the other's person when it has none, and counts once. The other copy goes to Recently deleted for 7 days, where it can be restored. No partner commission is taken back. Each is `transaction.deleted` and `transaction.merged` (`matched_by` `duplicate`) in the audit log.
