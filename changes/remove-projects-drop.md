---
kind: improvement
area: self-hosting
---
The last of projects is gone from the database: briefs, daily caps, reply rules and tokens moved to brands and sequences in an earlier release, and nothing has read projects since.

Upgrade: this release deletes the projects table and every project link. Take a database backup first if you want to keep a copy of the old project rows.
