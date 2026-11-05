---
kind: improvement
area: analytics
---
Refunds work like payments: send one as a transaction, an Order Refunded event, or both, and it counts once. A refund can name its payment by its order id, its order's messageId or Outflare's id, and one that arrives before its payment counts when the payment comes.
