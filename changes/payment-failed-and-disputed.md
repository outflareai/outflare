---
kind: launch
area: api
---
Failed payments and disputes are events: a transaction sent with the status failed writes `payment.failed`, and a chargeback writes `payment.disputed`, on the person's timeline. Neither counts as revenue.
