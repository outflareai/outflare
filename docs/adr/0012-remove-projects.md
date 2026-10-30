# 12. Remove projects

Status: Accepted (28 October 2026). Steps 1 and 2 shipped (step 2 in v0.33.0); step 3 follows. Changes [ADR 9 (brands)](0009-brands.md), which put projects inside brands.

## Decision

Outflare drops projects. The model is **workspace → brand → sequences**, as in Instantly, Smartlead, lemlist, EmailBison, Apollo, Outreach, Salesloft, Reply.io, Clay and HubSpot: none of them has a layer between the brand and the thing that sends. The owner, on comparing them: "Lets remove projects, its to confusing".

## Where each part of a project goes

| Project feature | After |
| --- | --- |
| Brief: audience, voice | The brand's context (`brand_context`) |
| Brief: customer filters (`icp`) | Ideal customer profiles (already linked by `profile_id`) |
| Brief: offer and market | Appended, with the whole brief, to the brand context notes (the company context when the project has no brand) |
| Status `paused` / `archived` | The project's sequences are paused; bulk pause of a brand's sequences |
| `daily_send_limit` across sequences | Split evenly, rounded down, over the project's sequences as their new `daily_send_limit` |
| Domains, mailboxes | The brand. Any sequence in a brand may send from any of its mailboxes; nothing stays kept for one project's sequences (the owner's choice) |
| Lead lists | The brand; a list scores against its own ideal customer profile (`profile_id`), copied from its project |
| Project API tokens | Become workspace tokens (the owner's choice) |
| Costs and results per project | Per brand or per sequence. No sequence tags for now (the owner's choice) |
| Missions, memories, reply rules, deals, meetings, spend | Keep their `brand_id`; `project_id` stops being read |

## How it ships

Expand-only migrations ([migrations](../migrations.md)), so each release runs beside the one before.

1. **Keep behaviour, hide projects** (migration 0266). Copies briefs, pauses the sequences of paused or archived projects (and sets those projects active, so the sequence is the only switch), splits the daily cap, links lists to profiles. The project's own cap stays until step 2, so a rollback still keeps to it. The web app stops showing projects, except to move a project's records between brands, which still go together.
2. **Remove from the API, MCP and SDK** (migration 0270). A catch-up migration copies briefs changed since step 1 (`brief_copied_version`), makes project tokens workspace tokens (each change is a `token.updated` audit event with `reason: projects_removed`), turns project reply rules into sequence rules, gives infrastructure plans a `brand_id` and costs their project's brand, and detaches records from projects, so a project's mailboxes serve every sequence in their brand. The project endpoints, MCP tools and `x-outflare-access: project_tokens` go; code stops reading `projects` and every `project_id` column. `project_id` on a write is accepted and ignored, so older clients keep working, except where ignoring it would widen access or change what a rule covers: a new API token or a reply rule set with `project_id` is refused with 400. Outcome reports group by `sequence` or `brand`. The `projects.read`/`projects.manage` scopes now cover customer profiles and moving records between brands; after step 3 they were renamed `profiles.read`/`profiles.manage`, with the old names still accepted wherever a scope is (tokens and roles that hold them keep working). `project.*` webhook events stay in the catalog but are no longer sent.
3. **Drop** (migration 0280). A release after nothing deployed reads them, a migration drops `projects`, every `project_id` column, the brand-from-project triggers and `query.projects`. Deals take their record's brand from their sequence only. The query views `sequences`, `deals`, `meetings` and `spend` keep a `project_id` column that is always null, so saved queries that name it still run. Before it, a prep change made `GET /query/tables` leave out a view the database no longer has and dropped the step 2 migration test, because CI runs the base branch's tests on the new schema.
