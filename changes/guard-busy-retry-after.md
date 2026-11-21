---
kind: improvement
area: connectors
---
The account lookup and outreach blocklist connections now pass on the wait a service names with HTTP 503 and Retry-After, so callers can treat it as busy rather than down.
