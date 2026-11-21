---
kind: fix
area: analytics
---
A repeat postback that only changes a conversion's status now keeps the payout the network first reported (it was replaced by the offer's own payout, or 0). Paging the conversions and cost update lists reaches rows that share a timestamp, and a later cost update for the same days always wins over an earlier one.
