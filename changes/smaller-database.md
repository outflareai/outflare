---
kind: fix
area: self-hosting
---
The database no longer grows with every tracked website visit: finished background work that runs once per batch of events is now cleaned up after 14 days like other finished work, and the events workspace keeps successful API and MCP calls once an hour per person instead of every one.
