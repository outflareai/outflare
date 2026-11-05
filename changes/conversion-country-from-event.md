---
kind: improvement
area: paid-ads
---
A conversion sent only from your server, for someone your website never saw, now takes its country from the event itself (`context.location.country` or `context.ip`), so it is no longer skipped as "country unknown".
