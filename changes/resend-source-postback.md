---
kind: improvement
area: analytics
---
Tracker logs: a postback to a traffic source can be sent again. **Send again** on a row of Postbacks out (or `POST .../traffic-sources/{id}/postbacks/{send}/resend`, MCP `resend_traffic_source_postback`) queues it with the status and payout it had, to the source's postback URL as it is now, so a send that failed on a wrong URL goes through once you fix the URL.
