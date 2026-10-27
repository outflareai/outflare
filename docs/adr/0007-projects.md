# 7. Projects are the unit of a motion, and own their infrastructure

Status: Accepted (23 October 2026)

## Decision

A project is one go-to-market motion: an offer sold to one ideal customer in one market. The hierarchy is workspace > project > sequence (the campaign record). A project holds:

- The brief: offer, audience and market as text, the ideal customer as structured filters (`icp`), and a Markdown brief. `brief_version` is bumped on every real change, and an edit carrying an older version fails with 409 instead of overwriting.
- Ownership of records through a nullable `project_id` on `sending_domains`, `mailboxes`, `lead_lists` and `sequences`. Null means shared by the workspace.
- A pause switch and a daily send limit, both enforced in the sending policy check like the domain and token limits.

Rules:

- A mailbox in a project serves only that project's sequences, in the planner and in manual assignment. Shared mailboxes serve any sequence, after the sequence's own project's mailboxes.
- An API token may be bound to one project (`api_tokens.project_id`). Routes refuse project tokens unless registered with `inProject`, whose handlers keep the token to its project. New routes are closed to project tokens by default.
- Deleting a project leaves its records shared, deletes its tokens (so none widens to the workspace) and is refused while live tokens exist.

## Why

Agents need one place to read what they are selling, to whom and where, and people need to stop one motion's mailboxes, budget and agents leaking into another's. Keeping ownership as a column on existing tables, not a new join table, makes every list filterable by project with one predicate and keeps "shared" as the simple default. Failing closed for project tokens means a route added later can never leak another project's data by accident.
