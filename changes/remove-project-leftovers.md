---
kind: improvement
area: api
---
The API drops everything left from projects and agency accounts: `project_id` fields, the `project.*` webhook events, the old scope names `projects.read` and `projects.manage` (use `profiles.read` and `profiles.manage`) and the whoami token `kind`. Inbox filters use `tag` (not `label`), and report rows use `name`.

Upgrade: migration 0316 renames stored `projects.*` grants on tokens and roles to `profiles.*`, removes project events from webhook subscriptions, and drops `project_id` from the query views.
