---
kind: improvement
area: self-hosting
---
Removes the database tables left over from agency accounts, which were removed in v0.26.0.

Upgrade: migration 0284 drops the `agencies`, `agency_members`, `agency_tokens` and `agency_events` tables and the `workspaces.agency_id` column. Take a backup first if you want to keep that data.
