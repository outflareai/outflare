---
kind: fix
area: analytics
---
When one person makes two payments of the same amount minutes apart, a copy of the second that arrives before its twin is now kept as its own payment. Before, a guess could join it to the first payment, which already had both copies, and Developer setup reported a payment matched only by a guess.
