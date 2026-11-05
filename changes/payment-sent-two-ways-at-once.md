---
kind: fix
area: analytics
---
A payment or refund sent as an order event and as a transaction at the same moment is now kept once, with its person. Before, it could show twice on the Transactions page, and Developer setup could warn about payments with no person.
