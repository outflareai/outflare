---
kind: improvement
area: api
---
Every list in the API now pages one way: pass `next_cursor` back as `cursor` until it is null. The per-list `before`, `after` and `offset` parameters and their `next_before`, `next_after` and `next_offset` fields are gone. The events feed and following logs start from now with `start=latest`.
