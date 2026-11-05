---
kind: improvement
area: analytics
---
Each payment counts once, whether it arrives as a transaction from the API, an Order Completed event with revenue or a billing connector's charge, matched on the order id (or the same person, amount and currency within 15 minutes). Transactions also take your own `user_id`.
