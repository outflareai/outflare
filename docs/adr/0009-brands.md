# 9. Brands inside a workspace

Status: accepted (2026-10-26)

## Context

Companies market several businesses from one team: Geonode runs geonode.com, repocket.com and games, on web, mobile and TV, and each has its own website as its point of truth. Separate workspaces would mean reconnecting every connector and re-inviting every person, and nobody could see everything without switching accounts or API keys. But agents need each brand's own context, sending must not mix brands, and reports should split by brand.

## Decision

A workspace holds **brands**. A brand is a name, a handle, a website, other domains and platforms, plus its own versioned context (`brand_context`).

- **Shared by default.** Connectors, members, tokens, leads and suppressions stay the workspace's. Projects, domains, mailboxes, lead lists, sequences, missions and memories get a nullable `brand_id`; null is shared by every brand.
- **Projects decide.** A record in a project always has the project's brand. Triggers keep it so, and moving a project moves everything in it, so there is one rule and no drift.
- **Context in layers.** Company context, then brand context (rules add, other fields override), then project brief. `GET /context?brand=` returns the company and brand layers with their memories.
- **Sending stays apart.** Allocation only gives a sequence mailboxes of its own brand or shared ones.
- **Analytics map by domain.** A site reports for the brand whose domains include its domain; `analytics_site_brands` records exceptions.
- **One switch.** `?brand=` on list endpoints and analytics; the web app keeps the choice in the URL and per person; the MCP server reads `OUTFLARE_BRAND`.

- **Access by brand.** A member keeps one role plus an optional list of brands (`memberships.brand_ids`, empty for every brand; owners and admins always have every brand); invites carry the list to the membership. An API token may be bound to one brand (`api_tokens.brand_id`, never together with a project); OAuth MCP connections are tokens and inherit it. A brand-limited caller sees records of its brands and shared ones (null `brand_id`), changes only its brands' records (shared ones are `403`), and new records it makes take its project's or its only brand. Routes say whether they admit brand-limited callers (`inBrand`, `inProject`; `brand_limited: true` in `x-outflare-access`); every other workspace route answers `403`, so a route is closed to them until someone checks it filters by brand.

- **Contact across brands.** Leads and suppressions stay the workspace's, so an unsubscribe stops every brand. `workspaces.cross_brand_days` (off by default) adds a rule to the send policy: a brand's first email never goes to someone another brand emailed within that many days. Only the first step checks it, sequences with no brand don't count, and the enrollment ends `blocked` with `other_brand:<handle>`.
- **Reporting and events by brand.** A record's brand is its sequence's (or project's, or for spend and warm-up its mailbox's or domain's); reports and the cost-per-meeting report group and filter by it. Each audit event stores its target's brand (`audit_events.brand_id`), so the audit log, events feed, product events and webhook payloads carry `brand_id`, and a webhook endpoint can be kept to one brand.

## Consequences

Everything works unchanged for a workspace without brands. Every new workspace-owned record that a brand can own needs `brand_id`, the trigger, and a `?brand=` filter on its list endpoint.
