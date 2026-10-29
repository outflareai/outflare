---
kind: improvement
area: partners
---
Partners: reward webhooks now carry the sale behind them (its amount, the charge id your backend sent, and the customer's own user id), plus when the hold ends, and `partner_payout.paid` lists the rewards it paid. A backend that handled Tolt's commission webhooks finds the same facts without extra calls.
