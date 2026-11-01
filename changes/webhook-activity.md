---
kind: improvement
area: api
---
Webhook deliveries keep every try, not only the last: open a delivery to see its payload and each attempt's status code, error and answer time, and send it again from there. Each endpoint shows its error rate over the last 24 hours, and its deliveries page shows the last 7 days of tries and failures.
