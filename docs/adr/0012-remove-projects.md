# 12. Remove projects

Status: Proposed (28 October 2026). Changes [ADR 9 (brands)](0009-brands.md), which put projects inside brands.

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
2. **Remove from the API, MCP and SDK.** A catch-up migration copies briefs changed since step 1 (`brief_copied_version`), makes project tokens workspace tokens, turns project reply rules into sequence rules, and detaches records from projects, so a project's mailboxes serve every sequence in their brand. The project endpoints and `x-outflare-access: project_tokens` go; code stops reading `projects` and every `project_id` column. `project.*` webhook events stay in the catalog but are no longer sent.
3. **Drop.** A release after nothing deployed reads them, a later migration drops `projects`, the `project_id` columns, their triggers and `query.projects`.
