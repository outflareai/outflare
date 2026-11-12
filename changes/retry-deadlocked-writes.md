---
kind: fix
area: api
---
Sending transactions or customer events at the same time for the same people no longer fails now and then with a 500 error: a write that loses a database deadlock is retried instead.
