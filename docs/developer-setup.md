# Developer setup

**People → Developer setup** (`GET /v1/brands/{brand}/developer-setup`, MCP `get_developer_setup`) is a live checklist of what one brand's developer sends. Each brand has its own, since each brand's integration is separate. The page refreshes every 30 seconds while it is open.

Each check says **OK**, **Check** (it works, but something should be fixed) or **Not yet** (nothing has arrived), what it found, and what to send to fix it, with an example to copy:

| Check | OK when |
| --- | --- |
| Sign-ups arrive from your server | A `Signed Up` event from the server in the last 7 days. |
| Orders arrive and count as payments | An `Order Completed` from the server in the last 7 days, and every recent one had a known person (`userId`) and revenue above zero, so each [counts as a payment](transactions.md#one-payment-counted-once). |
| Payments are counted once | A payment in the last 7 days, and every recent one has a person, so it counts toward revenue and ads. |
| Refunds find their payment | Every refund names a payment Outflare has. |
| Website and server copies agree | The website tag's sign-ups and orders match the server's copies by `messageId` or `order_id`. A copy matched only by person and time is a warning: it is still [counted once](paid-media.md#sending-conversions-back), but the id should be added. |
| Refunds lower the conversions sent to ad networks | Shown once a refund has been sent to an ad network: every one was taken. |

## Refunds to match

A refund whose `refund_of` (or Order Refunded `order_id`) names no payment of the brand is kept but doesn't count. The page lists them, newest first. Enter the payment's order id, its order's `messageId`, or Outflare's transaction id, and **Link**: the refund then counts, comes off that payment's revenue and lowers its conversion in ad networks (`POST /v1/brands/{brand}/transactions/{transaction}/refund-of` with `{"payment": "ch_1042"}`, MCP `link_refund`; the audit event is `transaction.refund_linked`). A refund that arrives before its payment needs nothing: it counts when the payment comes.
