---
kind: improvement
area: connectors
---
The product account lookup and outreach blocklist connectors now copy their whole lists into the opt-out list on a schedule (new accounts every 5 minutes, the blocklist exports every 30), so companies are checked without a call and people who become customers are stopped. See how fresh each copy is with `GET .../guard-list-syncs`.

Upgrade: runs migration 0680. A connected account lookup reads its whole account feed on the first sync, in runs of a few minutes each.
