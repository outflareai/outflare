---
kind: fix
area: records
---
Importing people now adds a missing user ID when an email matches an existing person, so later events and transactions find the same record. Rows whose email belongs to a person with a different user ID are skipped with a clear reason.
