# Lists

The Lists page holds two kinds of list side by side. A **lead list** is a fixed group of leads you picked, imported or found with a search, ready to add to a sequence. A **smart list** is a group of people on your customer records that keeps itself up to date from rules, such as "paid in the last 90 days" ([below](#smart-lists)).

## Lead lists

A lead list is a group of leads you can add to a sequence. Make one by hand (CSV, API, an agent), or let Outflare build it from a search at a data provider you already pay for. Outflare never sells or stores contact data of its own: every person comes from your own provider account, and the provider bills you directly.

Everything here works the same from the web app (Lists), the REST API and the MCP server.

## Connect a data provider

Paste an API key in Connectors (or from the Lead lists page). Each provider says what it can do:

| Provider | Search people | Find emails | Cost, roughly |
| --- | --- | --- | --- |
| Apollo | yes | yes | Search is free; revealing an email costs one credit |
| People Data Labs | yes | yes | One credit per record returned |
| MoltSets | yes | yes | Flat monthly plans with fair-use limits (free tier: 1,000 tokens); a lookup that finds nothing is free |
| Blitz | yes | yes | Flat monthly plans counted in records: one for each person or email found, nothing for a miss. People are searched one company at a time, by company domain; emails are found from a LinkedIn profile only ([email-finding.md](email-finding.md#blitz)) |
| Prospeo | | yes | One credit per email found |
| LeadMagic | | yes | One credit per email found |
| Findymail | | yes | One credit per verified email found |
| Treg | | yes | Pay per call from your Treg balance. It asks up to four providers in order for one person ([email-finding.md](email-finding.md#treg)), at most $0.06 in all; each billed call counts as one credit here. The same token also verifies emails |
| Hunter | | yes | One request per lookup; the same key also verifies emails |

`GET /v1/workspaces/{ws}/data-providers` lists them with the filters each search understands and the accounts you have connected. Prices are the providers' and change; check your plan.

## Build a list from a search

1. **Pick who to find.** Job titles, seniority, company size, industries, person and company location, company domains and keywords. A list with an ideal customer profile can copy the profile's filters into its search in one click.
2. **Preview.** Ten people from the search, with the total number of matches. Nothing is saved and no credits are spent.
3. **Choose how to find emails.** The waterfall: providers are asked in order until one returns an email, so you only pay the next provider for people the first one missed. Results are cached for 180 days, so a refresh never pays twice for the same person.
4. **Set the guard rails.**
   - *Most leads per run* (`max_leads`, default 500) and a *credit cap per run* (`credit_cap`). A run stops cleanly at either.
   - *Minimum fit score* (`min_score`): people scored below it are left out before any credits are spent.
   - *Verify emails* before adding (on by default, through your verification service or the built-in checks), and optionally *only verified emails*.
   - *Leave out* people emailed in the last N days (`skip_contacted_days`, default 90), people already in an active sequence, and anyone on the suppression list. This works across every list and campaign in the workspace.
5. **Build, then refresh.** A build runs in the background. Refresh adds people who are new since last time, by hand or every N days (`refresh_every_days`). People already in the list are skipped for free. A scheduled refresh that failed is tried again after 15 minutes, then 1 and 2 hours, then 4, up to 5 failures in a row; after that the list waits for its next scheduled refresh.

Each run reports what it found, added and enriched, what it left out and why (`already_in_list`, `low_score`, `no_email`, `unverified`, `undeliverable`, `suppressed`, `contacted`, `in_sequence`), the credits it spent per provider and why it stopped early, if it did. Credits go into the spend ledger, so cost per meeting includes data costs.

```sh
# Preview a search (free)
curl -X POST "localhost:8080/v1/brands/$BRAND/lead-lists/preview" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"search_integration_id": "'$APOLLO'", "filters": {"titles": ["Head of Growth"], "locations": ["Australia"], "employee_ranges": ["51-200"]}}'

# Create the list and build it
curl -X POST "localhost:8080/v1/brands/$BRAND/lead-lists" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"name": "AU growth leaders", "profile_id": "'$PROFILE'", "search_integration_id": "'$APOLLO'",
       "filters": {"titles": ["Head of Growth"], "locations": ["Australia"], "employee_ranges": ["51-200"]},
       "enrich_integration_ids": ["'$APOLLO'", "'$PROSPEO'"], "max_leads": 300, "credit_cap": 400,
       "min_score": 40, "refresh_every_days": 7, "build": true}'

# Follow the run, then read the leads, best fit first
curl "localhost:8080/v1/brands/$BRAND/list-runs/$RUN" -H "Authorization: Bearer $OUTFLARE_TOKEN"
curl "localhost:8080/v1/brands/$BRAND/lead-lists/$LIST/members" -H "Authorization: Bearer $OUTFLARE_TOKEN"

# Refresh now, or fill in missing fields on the leads already there
curl -X POST "localhost:8080/v1/brands/$BRAND/lead-lists/$LIST/runs" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" -d '{"kind": "refresh"}'
```

An *enrich* run (`{"kind": "enrich"}`) fills empty fields (name, title, company, LinkedIn, location) on the leads already in a list. It never changes an email address. It works on manual lists too.

## Fit score

Every lead in a searched list gets a 0 to 100 fit score, with the reasons. It is scored against the list's [ideal customer profile](ideal-customers.md) (`profile_id`), where any part the list's own search sets wins; a list without one is scored against its search. The parts: title (35), seniority (20), company size (15), industry (15) and location (15). Only the parts the profile sets count, and seniority is guessed from the title when the provider does not say. It is plain rules, so the same lead always gets the same score.

## Where a lead came from

`GET /v1/brands/{brand}/leads/{id}/sources` shows each provider that supplied or filled in a lead, its id there and when. Useful for data requests and for knowing which provider's data replies best.

## Importing a CSV into a list

On a manual list's page, **Import leads** opens the CSV import right there, and every lead in the file goes into that list. On **Leads**, the import's **Add them to a list** menu offers the brand's manual lists, **New list…** to name a new one, or **No list**; it starts on the list the table is filtered to. Lists built from a search fill themselves, so they aren't offered.

## Clay, spreadsheets and other tools

Anything that can send an HTTP request can add leads: a Clay HTTP API column, Zapier, n8n or a script can `POST /v1/brands/{brand}/leads` with `"list_id"` set, or `POST /v1/brands/{brand}/leads/import?list=<name>` with a CSV. Upserts are by email within the brand, so sending the same person twice updates them.

## Permissions

Reading lists and runs needs `leads.read`; creating, editing, running and deleting needs `leads.manage`.

## Brands

Leads, lead lists and their builds belong to one brand (`plans/brands/README.md`), and live under `/v1/brands/{brand}/` (`leads`, `lead-lists`, `list-runs`). A list holds its brand's leads only, a build fills its list's brand, and a sequence enrols its brand's leads only. A lead is unique per brand: the same address in two brands is two people. Until the brands contract step drops the older per-workspace index, an address is a lead in one brand of a workspace at a time: adding it in another brand answers `409 lead_in_other_brand`, and imports and builds skip it (`other_brand`). The `/v1/workspaces/{ws}/` lead and list routes are deprecated: lists default to the workspace's first brand (`?brand=` picks one, `all` every brand), and new leads and lists go to the brand named in `brand_id`, else the first brand.

## Agents

MCP tools: `list_data_providers`, `preview_lead_search`, `create_lead_list`, `update_lead_list`, `run_lead_list`, `get_list_run`, `list_list_runs`, `cancel_list_run`, `list_lead_lists`, `get_lead_list`, `list_lead_list_members`, `get_lead_sources`. The tools that can spend credits are marked as reaching the outside world, so MCP clients can ask before running them.

## Static lists

A static list holds the people someone, the API or a journey puts in it, such as webinar attendees or beta testers. Nothing refreshes it. Make one with `POST .../smart-lists` and `"kind": "static"`. Put people in with `POST .../smart-lists/{id}/members` and `{"record_ids": [...]}`, and take them out with `POST .../smart-lists/{id}/members/remove` (MCP `add_to_static_list` and `remove_from_static_list`). A journey's `add_to_list` and `remove_from_list` steps do the same.

Only people count: not companies, merged or archived records, or, for a brand's list, another brand's people. A static list is used wherever a smart list is: [rules](rules.md) acting on people joining or leaving it hear of each change.

## Lists from a saved query

A list can take its members from a saved [Query](query.md) instead of rules: `POST .../smart-lists` with `"kind": "query"` and `"saved_query_id"` (in the app: New smart list, then "Use a saved query"). Its members are the records in the query's `record_id` column, or its `id` column when it has none, for example:

```sql
SELECT id AS record_id FROM people WHERE attributes->>'plan' = 'pro'
```

- It refreshes every 15 minutes like a smart list, running the saved query's current version, so editing the query changes the list at the next refresh. Joins and leaves are kept in its history like any list's.
- It reads customer data only, inside the list's brand, and holds up to 50,000 records of its kind (`record_type`), live and in its brand: anything else the query returns is left out.
- When the query fails, the list keeps the members it had and `error_code` says why: `query_missing` (the saved query was archived), `no_record_id` (no `record_id` or `id` column), `too_many` (over 50,000 rows), `query_timeout`, or the query's own error code. It tries again at the next refresh.
- Change its query with `PATCH .../smart-lists/{id}` and `saved_query_id`: a saved query of the list's brand that you can see. It has no rules.
- It works wherever a smart list does. A [flow kit](playbooks.md) exports it as an empty static list, since its query belongs to the workspace.

## Save a copy

`POST /v1/brands/{brand}/smart-lists/{id}/copy` (the list's menu, "Save a copy"; MCP `copy_smart_list`) saves who is in a list of people now as a new static list: a snapshot that never changes on its own, named after the list and today's date unless you pass `name`. Each person's history says they joined it as a copy. The audit log has `smart_list.copied`.

Lists also work as exclusions: a journey, broadcast or sequence can leave out everyone on chosen lists (see Exclusions in [lifecycle-email.md](lifecycle-email.md)).

## Smart lists

A smart list is a group of people on your [customer records](customers.md) that keeps itself up to date: Outflare adds people when they start matching its rules and removes them when they stop. Make one from **Lists → New list → Smart list**, pick the rules, watch the count change as you go, and save.

Each rule tests one thing about a person:

- **A field** on their record, such as `billing_plan` or `country`: is, is not, is one of (`in`, a list), is none of (`not_in`), is more than, is less than, contains, starts with, is set or is not set, and for dates, in the last N days (`in_last_days`) or more than N days ago (`more_than_days_ago`). Numbers compare as numbers, text ignoring case, and dates as text (so `2026-01-01` style dates sort); a value that isn't a date never matches a date test. For a field that holds a list, contains checks its items.
- **An event** they did, such as `payment_succeeded`: did or didn't, ever, in the last N days (`within_days`) or more than N days ago (`before_days`), and at least N times (`min_count`; with didn't, fewer than N times). **Where** tests the event's own properties, up to five, such as `{"property": "amount_minor", "op": "gt", "value": 10000}` for payments over $100 (amounts are in cents). Events from the website tracker don't count, because anyone with the site key can send them.
- **A page they viewed** (`page`) on one of the brand's sites: a site (`host`; `www.` is the same site), a path that is, starts with or contains something, or both, with the same time and count options as events. Only views from a browser tied to the person by something they did count: signing in to your product, clicking a tracked link in an email Outflare sent, or a [visitor link](website-visitors.md). So visitors who never gave their email or signed in are in no list, and nobody can put someone in a list by sending page views. `{"page": {"host": "app.example.com"}, "op": "not_exists"}` is people who never opened the app.
- **Another list** they are in, or aren't (`in_smart_list`): a smart, static or query list of the same brand and kind, as it stood at its last refresh. A list can't depend on itself, even through other lists, and a list another list names can't be deleted until that list's rules change.
- **Marketing email** (`marketing_email`): whether they may get marketing email, as the People list's Marketing email column shows it.
- **A lead list** they are in, or aren't: a fixed list can feed a smart list.
- **A news search** they wrote an article for, or didn't, ever or in the last so many days: a media list of the [journalists](news-watch.md#journalists) who cover a topic.
- **A company field** on a company they are on (`company_attribute`), such as a journalist's outlet tier: `{"company_attribute": "outlet_tier", "op": "eq", "value": "national"}`. It passes when any of their companies passes; is not and is not set pass when none of them has the value.
- **A lifecycle stage** for a product (`product_stage`, [products and lifecycle stages](product-lifecycle.md)): `{"product_stage": "churned", "product": "residential", "op": "eq"}` holds people in that stage, `neq` people who aren't. `level` picks billing (`company`) or usage (`person`) stages; by default a people list reads usage stages and a company list billing stages.

A person must pass all the rules, or any of them (`match`). With no rules a list holds everyone. In the app, the rule builder starts with what kind of rule you want (Visited a page, Did something, Has a field, Is in a list, Their company, Lifecycle stage, Can be emailed), offers the brand's events, properties, sites and pages in searchable pickers (`GET /v1/brands/{brand}/smart-lists/choices`, MCP `get_smart_list_choices`), and reads the rules back as one sentence above the live count. An event's filters offer the properties its recent events carry, also ones nobody has described in the catalog, and take money in whole units for a property whose name ends in `_minor` (100 for $100; rules store cents). Time windows and counts take any number, beside the common ones. When the rules that decide who is in only leave people out ("didn't", "isn't"), every rule when all must match or any one of them when any may, it warns that the list will hold nearly everyone.

### Describe it in words

**Describe who should be in it** writes the rules for you from a sentence, such as "people who paid in the last 90 days but never opened the app". The workspace's own AI model (AI tool **Smart list rules**, on the workspace's own key; with none connected the box says so and links to Connectors) reads the brand's field, event, page, list and product names with their labels and descriptions, never a person or their values, and writes rules, which Outflare checks like any list's. They open in the builder with a sentence on who they pick, for you to check, change and save. Nothing is saved until you save it. When the description can't be written as rules (it needs an event the brand doesn't send, say), it says what is missing.

```sh
# Ask for rules; read the draft until it is ready or failed
curl -X POST "localhost:8080/v1/brands/$BRAND/smart-list-drafts" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"description": "People who paid in the last 90 days but never opened the app"}'
curl "localhost:8080/v1/brands/$BRAND/smart-list-drafts/$DRAFT" -H "Authorization: Bearer $OUTFLARE_TOKEN"
```

### Groups: mixing "and", "or" and "not"

A rule can be a **group** of rules with its own match: all of them, any of them, or **none** of them (in the app, the builder shows this as two parts: **Include people who**, with "and" or "or" between conditions and either/or sets, and **But leave out people who**, which is stored as a "none" group). A group can hold groups, two levels below the list's own rules at most, with up to 20 rules in a group and 50 in a list. For example, people who signed up and have neither paid nor been refunded:

```json
{"match": "all", "rules": [
  {"event": "signed_up", "op": "exists"},
  {"match": "none", "rules": [{"event": "payment.succeeded", "op": "exists"}, {"event": "payment.refunded", "op": "exists"}]}
]}
```

A group sets only `rules` and `match`; every other rule needs its `op`. Groups work wherever smart list rules do: smart lists, their preview, and audiences in review requests and playbooks.

A list holds people unless it is a [list of companies](#lists-of-companies); merged and archived records never count. A list is one brand's and holds only that brand's people.

Lists fill straight away when saved, and refresh on their own every 15 minutes, which also moves people in and out of time windows such as "in the last 90 days". **Refresh now** does it at once. If a refresh takes over two minutes, it stops and the list shows `query_timeout`: narrow the rules, for example with a time window on an event.

```sh
# How many people would these rules pick? Nothing is saved.
curl -X POST "localhost:8080/v1/brands/$BRAND/smart-lists/preview" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"rules": [{"attribute": "billing_plan", "op": "eq", "value": "pro"}, {"event": "payment_succeeded", "op": "exists", "within_days": 90}]}'

# Save it; it fills in a few seconds
curl -X POST "localhost:8080/v1/brands/$BRAND/smart-lists" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"name": "Paying customers", "brand_id": "northpeak", "rules": [{"attribute": "billing_plan", "op": "eq", "value": "pro"},
       {"event": "payment_succeeded", "op": "exists", "within_days": 90}]}'

# Who is in it now, newest to join first
curl "localhost:8080/v1/brands/$BRAND/smart-lists/$LIST/members" -H "Authorization: Bearer $OUTFLARE_TOKEN"
```

**Edit name and description** on a list's page renames it and says what it is for, for any kind of list (`PATCH` with `name` and `description`); a name another list already has is refused.

A list's page shows who is in it. Each name opens that person's record, and beside it are their email, lifetime value (everything they have paid, less refunds), the day they signed up (their first `signed_up` event) and when they joined the list. **Columns** picks which of these show, with the day the record was first seen as one more; **Save as my default** keeps your pick for every list with your account, so it is the same on every device you sign in on (`GET /v1/me/table-views`), and **Back to standard columns** drops it. The members API returns the same values as `lifetime_revenue`, `signed_up_at` and `created_at`. Lifetime value is the record's Lifetime revenue field when a billing connection keeps one, and otherwise their `payment.succeeded` events less `payment.refunded`, in the currency they paid most in.

Reading smart lists needs `customers.read`; making, changing, refreshing and deleting them needs `customers.manage`. A token or member limited to some brands sees lists for its brands and those for every brand, but only its brands' people in them, and can only change its brands' lists. Deleting a list never deletes the people.

MCP tools: `list_smart_lists`, `get_smart_list`, `list_smart_list_members`, `list_smart_list_changes`, `preview_smart_list`, `get_smart_list_choices`, `draft_smart_list`, `get_smart_list_draft`, `create_smart_list`, `update_smart_list`, `refresh_smart_list`, `delete_smart_list`.

### Lists of companies

A smart list can hold companies instead of people: make it with `"record_type": "account"`. Its rules test the company: a company field (`attribute`), an event (the company's own events and those of its people), a lifecycle stage per product (`product_stage`, billing stages by default) and another list of companies (`in_smart_list`). Preview it with `"record_type": "account"` too. A list of companies can't be static, and its kind of record never changes.

- **Excluding a list of companies leaves out everyone at those companies.** An exclusion on a journey, broadcast or sequence that names a company list skips each person whose company is in it.
- **Rules on joining and leaving** work for companies: an [audience rule](rules.md) with `record_type: account` on a company list fires for the company.
- **Review requests go to people**, so they refuse a list of companies.
- Its members list shows each company's main domain in `email`.

### History

Every list keeps who joined and left it, and when: each refresh of a smart list and each change to a static one. `GET /v1/brands/{brand}/smart-lists/{id}/changes` (MCP `list_smart_list_changes`) lists them newest first, `?change=enter` or `leave` narrows it, and a person's or company's [timeline](customers.md#timeline) shows the lists they joined and left (kind `list`). Query has the history as `smart_list_changes`. The history starts when this release does; it holds nothing from before. A person in Recently deleted keeps their place, so restoring them neither leaves nor joins.

Coming next: sending to a smart list from email broadcasts, starting journeys when someone joins one, and switching a smart list on for each ad network.
