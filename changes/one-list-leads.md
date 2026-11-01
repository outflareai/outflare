---
kind: improvement
area: api
---
A lead list's leads now come from one place, `GET /lead-lists/{id}/members`, which also takes the `suppressed` and `verification` filters. `GET /leads` no longer takes `list_id` and says where to look instead.
