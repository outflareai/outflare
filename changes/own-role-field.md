---
kind: improvement
area: api
---
Your own role in `GET /v1/me` and `GET /v1/workspaces/{ws}` now reads like a member's: `role` is the exact role as `GET /roles` lists it (a starting role's key or your workspace's own role's id), with its name in `role_name`. `role_id` is gone. The workspace switcher shows the role's name.
