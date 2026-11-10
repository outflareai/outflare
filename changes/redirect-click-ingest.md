---
kind: improvement
area: analytics
---
Redirect links answer faster and take far more clicks: each redirect no longer waits on the database, and clicks are written in batches a moment later. A server takes about a million clicks a day per workspace. Each click also keeps a keyed hash of the visitor's address, never the address itself, so repeat clicks can be told apart.

Upgrade: runs migration 0508.
