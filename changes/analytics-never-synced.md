---
kind: fix
area: analytics
---
Analytics syncs are scheduled again when a connected analytics account has never synced: one such account stopped the scheduler from queueing syncs for every analytics connection on the server.
