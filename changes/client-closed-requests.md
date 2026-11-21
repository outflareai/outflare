---
kind: fix
area: general
---
A request the caller closed before the answer (a closed tab, a page left mid-load) is now logged as `client_closed` instead of a 500 server error, so the request log and error counts show only real failures.
