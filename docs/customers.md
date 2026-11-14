# Customer records

Customer records are Outflare's one model of the people and companies a workspace knows, plus any other kind of record it tracks, such as deals, subscriptions or partners. The customer data platform, the CRM and lifecycle email all read and write the same records, so a fact about someone is stored once, whichever feature or connector wrote it.

This covers the customer data platform so far: records, their identities and links, each brand's catalog, product events, duplicates and merges, each record's timeline, and consent. [Search](workspace-search.md) finds records and their history by their words, [Query](query.md) reads it with SQL; [smart lists](lists.md#smart-lists) group it, and [rules](rules.md) turn what happens to records into business events of their own, which journeys and webhooks act on. [Products and lifecycle stages](product-lifecycle.md) keep where each company and person stands with each product the brand sells. [Metrics](metrics.md) keep live totals per company and person, such as spend in the last 30 days.

Everything here works the same from the REST API and the MCP server.

## Each brand's data, under its brand

A brand owns its customer data (`plans/brands/README.md`): the same email in two brands is two people, and nothing reads across brands. So the customer API lives under the brand, `/v1/brands/{brand}/...` with the brand's id, and a brand's URLs stay the same wherever the brand goes:

| What | Routes under `/v1/brands/{brand}` |
| --- | --- |
| Records (people, companies, the brand's own types) | `GET`/`POST /records`, `GET`/`PATCH`/`DELETE /records/{record}`, `POST /records/{record}/review`, `GET /records/lookup` |
| A record's history, identities, links, timeline and consent | `GET /records/{record}/history`, `GET`/`POST /records/{record}/identities`, `POST .../identities/{identity}/close`, `GET`/`POST /records/{record}/relationships`, `POST .../relationships/{relationship}/end`, `GET /records/{record}/timeline`, `GET`/`POST /records/{record}/consents` |
| Duplicates and merges | `GET /records/duplicates`, `POST /records/duplicates/{candidate}/dismiss` and `/link`, `POST /records/{record}/merge`, `GET /merges`, `POST /merges/{merge}/undo` |
| Product events | `POST`/`GET /events` (the inbox), `GET /customer-events` (the events stored) |
| Smart and static lists | `GET`/`POST /smart-lists`, `POST /smart-lists/preview`, `GET`/`PATCH`/`DELETE /smart-lists/{id}`, `GET`/`POST /smart-lists/{id}/members`, `POST .../members/remove`, `POST .../refresh` |
| Imports | `GET`/`POST /crm-imports` (`integration_id` names the connection, which must serve the brand), `GET /crm-imports/{import}`, `GET /batches` and the undo routes |
| The catalog, Recently deleted, privacy exports, Query | `/catalog/...`, `/recently-deleted/...`, `/privacy-exports/...`, `/query` and `/query/tables` ([Query](query.md)) |

Every row these routes read or write is the brand's: a record, list, pair or import of another brand is `404` there, a `brand_id` in a body can only be the route's brand, and `?brand=` can't widen a list. A token made for one brand, and a member kept to some brands, get `404` for any other brand's routes.

The workspace routes (`/v1/workspaces/{ws}/records/...`, `/smart-lists/...`, `/crm-imports/...`, `GET /customer-events`, `/query`) are deprecated and keep working until the brands contract step, for SDKs and scripts written before: a list reads the brand `?brand=` picks (a handle or id), else the workspace's first brand (a member or token kept to some brands gets its own), and `?brand=all` lists every brand you can see, as before; one record, list or import is read in its own brand; and changes act on the record's or list's brand, or the workspace's first. Install health warns while anything still calls them. `POST /v1/workspaces/{ws}/customer-events` stays as it is for now (see Product events).

In the app every customer page (People and Companies, a record's page, Duplicates, Events, Imports, Recently deleted and Lists) works inside the brand picked in the brand switcher.

## The catalog

The catalog says what records can hold. Each brand has its own: the record types, fields, option lists and event definitions belong to a brand, and a field one brand adds is never seen by another. Read it first (`GET /v1/brands/{brand}/catalog`, MCP `get_catalog`) and reuse what is there before adding anything.

- **Smart defaults.** A new brand starts with the default catalog: the built-in types, fields and option lists below, and the `deal` type with its stages and fields. It is never a copy of another brand's, since two brands are rarely the same business. From there the brand has full create, change and archive.
- **Record types.** Every brand has `person` and `account` (shown as Companies), and a `deal` type (Deals) it can change like its own. Add your own with `POST /v1/brands/{brand}/catalog/types`, such as `subscription`. A type's key never changes; archiving one (how a type is deleted) keeps its records, and it can be restored. Two brands may each have a type, field or option list with the same key; they are separate.
- **Fields.** Each type has fields, each the one definition of a fact for every feature: imports, list filters, enrichment, the CRM and merge tags all use it. Built in: first and last name, job title, seniority, companies, country, city, time zone, LinkedIn and lifecycle stage for people; website, industry, company size, country, LinkedIn and lifecycle stage for companies. Both have **tags** (labels such as `vip` or `churn-risk`, made lowercase, up to 50 characters each) and an **owner** (a member of the workspace; alerts about the record go to them). For [digital PR](news-watch.md#journalists), people have **journalist** (true for someone who writes for an outlet) and **beats** (the topics they cover), and companies have **outlet tier** (`national`, `trade`, `regional`, `local` or `newsletter`). Add your own with `POST .../catalog/types/{type}/fields`; archive one to delete it.
- **Value types.** `text`, `number`, `currency` (`{"amount_minor": 1999, "currency": "USD"}`), `date`, `timestamp`, `checkbox`, `select` and `status` (an option key), `rating` (0 to 5), `email`, `phone` (international format), `url`, `location`, `record_reference` (another record, such as a deal's company) and `actor_reference` (a member of the workspace). `multiple` fields take a list. A field's key, value type, option list and target never change; anything else can, and a field can be archived.
- **Option lists.** `select` and `status` fields share option lists, so the industry of a lead, a CRM company and an import are the same values. Built in: country (ISO 3166 codes), industry, company size, seniority and lifecycle stage. Add your own, such as deal stages, with `POST .../catalog/option-sets`. Options are archived, never deleted, so old values keep their meaning.
- **Field labels.** Every field, built in or your own, and every event carries four labels for your team: **sensitivity** (`public`, `internal` by default, `confidential` or `restricted`), an **owner** (`owner_id`, the team member who looks after it), how long its data is meant to be **kept** (`retention_days`, forever by default) and where it **may be sent** (`destinations`: `webhooks`, `exports`, `ai`, `published_queries` and `email`, all of them by default; null resets to all). They are labels only: nothing is blocked, hidden or deleted because of them. Set them when adding or changing a field or event (`PATCH .../catalog/types/{type}/fields/{field}`, `PATCH .../catalog/events/{event}`, or Edit labels in the CRM settings). Of a built-in field, only these labels change.
- **Product events.** `POST .../catalog/events` defines an event your product sends, such as `product.activated`, with its properties. Only approved events will drive segments, rules and journeys. Each brand has its own catalog: two brands of one workspace can each define and approve the same name, and one brand's approval never affects another's.

Changing the catalog needs `catalog.manage`, which owners and admins have by default, because a field is shared by every feature that uses it. Approving a held event name (`POST /v1/brands/{brand}/catalog/events/{event}/approve`, MCP `approve_event_name`) needs only `customers.manage`, since it is part of fixing the brand's data and changes nothing else about the event.

The brand routes are under `/v1/brands/{brand}/catalog` (`types`, `types/{type}`, `types/{type}/fields`, `types/{type}/fields/{field}`, `option-sets`, `option-sets/{set}`, `events`, `events/{event}`, `events/{event}/approve`). The workspace routes (`/v1/workspaces/{ws}/catalog/...`) answer 410 `route_moved`: each brand has its own catalog.

## Records

`POST /v1/brands/{brand}/records` adds a record to the brand with its `type`, `name`, `attributes` and `identities`; `PATCH .../records/{record}` changes one. Attributes are checked against the type's fields: an unknown key is refused with a pointer to adding the field, and values are normalized (emails lowercase, phones in E.164).

Each record also has:

- **origin**: where it came from (`first_party` for your own product, billing or forms, `prospect` for lead lists and data providers, `crm_sync`, `import` or `manual`). With consent it sets each person's contact basis, which decides what may go out to them ([docs/opt-outs.md](opt-outs.md)). `acquired_via` names the tool the record came from.
- **brand_id**: the brand the record belongs to: the route's, or on the deprecated workspace route the one given, else the workspace's first. A record never moves to another brand through a brand route. Its identities are matched in that brand only (see Identities), and its fields are that brand's catalog's. People limited to some brands see and change only their brands' records.
- **version**: goes up on every change. Send the version you read with a `PATCH` (or a merge) and an edit made against an old copy is refused with `record_changed`. Agents must send it: a change from the MCP server, a connected AI app or a mission without a version is refused with `version_required`, so two agents working on one record never overwrite each other. People in the app and scripts with a plain API token may leave it out.
- **history**: `GET .../records/{record}/history` lists every change to the name and attributes, with who made it and from which source.

### Which source wins

Every value keeps the source that set it: `manual`, `first_party`, `crm_sync`, `enrichment` or `import` (send `source` with the write; the default is `manual`). Each field has a precedence, by default manual, then first party, then CRM sync, then enrichment. A write from a lower source leaves a higher source's value alone and lists the field in `skipped`, so enrichment never overwrites what someone typed and a CRM sync never overwrites your product's own data. A field's `write_policy` can also keep it for its source system only (`source_owned`) or for Outflare to work out (`computed`).

### Where a value comes from

Every source's latest value of each field is kept, whether or not it won: `GET /v1/brands/{brand}/records/{record}/field-values` (MCP `list_record_field_values`) lists them, with `current` on the one the record holds. On a record's page each field shows what the other sources say, with **Use this**: that saves the value as your own edit.

A person's edit (source `manual`) doesn't win forever. It wins for the brand's `manual_edit_days` (180 by default; `PATCH /v1/workspaces/{ws}/brands/{brand}`), or the field's own (`PATCH .../catalog/types/{type}/fields/{field}`, `null` for the brand's); `0` means forever. Built-in fields follow the brand's setting.

- **Pin** an edit (`POST .../records/{record}/fields/{field}/pin` with `{"pinned": true}`, MCP `pin_record_field`) to keep it for good. Unpinning starts its time again from now. Only a value set by hand can be pinned.
- **When its time runs out**, the next source to write the field replaces it. A sweep every 5 minutes also hands the field to the best value another source already gave (by the field's precedence, then the newest), as a normal change: it is in the record's history with that source, rules hear of it, and the audit log has `record.hand_edit_expired`.
- **A field only people write**, such as a note or an owner, keeps its value when the edit expires: there is nothing to replace it with. It is only marked expired.
- Typing the same value again restarts its time. Edits made before this release keep winning until someone edits them again.
- Pins and unpins are in the record's history (source `pinned` or `unpinned`) and the audit log (`record.field_pinned`, `record.field_unpinned`).

### Revenue from your payment provider

Connect Stripe and each customer becomes a person with a `stripe.customer` identity, their payments, refunds and subscription changes become events on their timeline, and `billing_plan`, `mrr`, `lifetime_revenue`, `customer_since` and `billing_status` are computed fields on their record. See [Payments](connectors.md#payments).

### Moving from another CRM

Connect Attio and import it once with `POST .../integrations/{id}/crm-imports`: its people, companies, deals and custom objects become records here, its fields become catalog fields and its stages option lists, with each record's Attio id kept as an `attio.record` identity so the import can run again. See [Moving from another CRM](connectors.md#moving-from-another-crm).

### Leads are people

Every lead is a person record. A new lead joins the person who already has its email, or gets a record of its own with origin `prospect`, its email as an unverified identity (so it never claims an address someone proves they own), its lead id as an `outflare.lead` identity, and its name, job title and LinkedIn as attributes. `record_id` on a lead links to it. Imports of up to 1,000 leads are linked as they land; bigger ones, and leads from before this, are linked by the `customers.link` job within moments.

A lead's details live on its person. Whenever a lead is added, imported, edited or enriched, its first and last name, job title and LinkedIn are copied to the person's fields, its phone becomes an unverified phone identity, and its company becomes the person's company (found or added by the website's domain, or the email's when that is a company's own, never a personal mailbox such as Gmail). They are written with the source `import`: they fill what is empty and update what a lead set before, but never replace a value a person typed, a CRM sync or enrichment set. The `customers.lead_fields` job copies what older leads know. Cold email's merge tags read the person back, so a correction made on the record (a job title, a first name) is what goes out, and the person's other text fields work as merge tags by their field key.

Cold email, replies, meetings and deals carry the person's `record_id` too: from their lead, or else from the person who has the address (a reply's sender, a meeting's attendee). Lifecycle email to one address links to its verified owner. Merging two records moves all of it to the one kept.

### Meetings as events

A meeting tied to a person is also an event on their timeline, so a booked or held call can be a conversion like a sign-up or a payment ([attribution](attribution.md)), start a journey, or feed a rule. Whatever booked or changed it (Google Calendar, Outlook, Calendly, Cal.com, HubSpot, the app or the API), the person gets:

| Event | When |
| --- | --- |
| `meeting.booked` | When it was booked (every meeting gets this first) |
| `meeting.held` | When it ended (or started, without an end time), once its status is held |
| `meeting.no_show` | The same, for a no-show |
| `meeting.canceled` | When it was canceled |

Each has `meeting_id`, `source`, `attribution`, `starts_at` and, when the meeting has them, `sequence_id` and `mission_id`, with source `meetings`. They are recorded within a minute of the change, or of the meeting being tied to the person, and each only once. Meetings from before this were recorded too, with the times they happened.

### Reply labels as events

When a cold email reply settles on a label, the person who sent it gets a `reply.labeled` event, so "a lead replied interested" can start a journey (such as handing them to sales or opening a deal), feed a rule or count as a conversion. A label settles when the rules or the AI are sure of it, or when a person or agent sets or confirms it; a label the AI isn't sure of waits for a person and records nothing until then.

The event has `inbound_id`, `category` (such as `interested` or `not_now`), `group` (`hot`, `later`, `closed` or `automatic`), `mailbox_id`, `source` (`mailbox` or the sequencer's connector) and, for a reply to a sequence, `sequence_id`, with source `replies`. It never carries the reply's text. Each label a reply settles on is recorded once; relabelling it records the new label. The sender is the reply's person, else its lead's, else the person with the sender's address; a reply from someone Outflare doesn't know records nothing. Filter a journey's trigger on `category` or `group` to start it on the replies you want.

## Identities

Identities say who a record is: `email`, `phone`, `device_token` (with `properties.platform`: ios, android or web), `user_id` (your product's own id), `anonymous_id`, `domain` for companies, or a source's own, such as `stripe.customer`.

Identities belong to their record's brand, and are matched only within it: **the same email or user id in two brands is two people**, never merged and never linked. A product event, identify call, payment customer, CRM or CSV import, or record made with identities in one brand finds that brand's person, or makes a new one there, whatever another brand holds. Only a **verified** identity claims its value, and in each brand only one record can hold a value verified:

- An email or phone is unverified unless you say the person proved they own it (`"verified": true`, only from your servers). Your own ids, payment customers and domains are trusted as sent.
- Someone who signs up with another person's email and never confirms it gets their own record. They never take over the other person's record, and the address stays the other person's.
- A verified value another record of the same brand already holds is refused with `identity_taken`, naming that record when you can see it. Nothing is moved or merged. Another brand's person with the same value never blocks it.
- When a person changes their email, add the new one with `replaces` set to the old identity's id: the old one is closed as `changed` and kept in the history, so the address can later be claimed by whoever proves they own it.
- `GET /v1/brands/{brand}/records/lookup?namespace=email&value=...` finds the brand's records an identity points at: `owner` is the one holding it verified, and the rest hold it unverified. The workspace route `GET .../records/lookup` does the same for the brand `?brand=` picks; under all brands it lists every brand's, and `owner` is the first brand's (else another's).
- A CSV import of leads links each lead to a person of its list's brand (the first brand for a list of no brand), and a payments or CRM import to the person of its connection's brand. Outreach, replies and email clicks that find a person by address look in every brand until their step of the brands project moves them, preferring the workspace's first brand.

## Links

Records link to each other: a person to their companies, a deal to its company. Record reference fields (such as a person's `companies`) are links too, so there is one graph. Add other links with `POST .../records/{record}/relationships` (`kind` such as `partner_of`) and end them with `.../relationships/{relationship}/end`; ended links are kept.

## Deals

Every deal is one record of the `deal` type, whether it was made in the CRM, by a reply rule, by an import, logged for outreach (`POST .../deals`) or synced from HubSpot. The deal record and the deal in outreach outcomes share an id, so `GET .../records/{id}` and `GET .../deals/{id}` are the same deal, and a change on either side shows on the other.

- **Fields.** Every workspace starts with Attio's shape, so a deal type brought over from Attio already fits: `stage` (a status field on the `deal_stage` list: lead, qualified, meeting, proposal, negotiation, won, lost), `value` (currency), `close_date`, `associated_people` and `associated_company`. A workspace that made its own `deal` type keeps it, with any of these fields it lacked added, and `won` and `lost` added to its stages.
- **Won and lost** close a deal: outcomes count it as won or lost, and its close date is the day it closed. Any other stage is open.
- **People and company.** A deal logged for a lead gets the lead's person, and their company when the deal has none.
- **Outreach.** A deal made in the CRM is tied to the outreach that reached its first person, as a synced deal is: their lead's sequence and latest meeting before the deal opened (the `deals.attribute` job, `deal.attributed` event).
- **HubSpot.** HubSpot stays the source of truth for a synced deal: its next sync of that deal writes its name, stage, value and close date again. A HubSpot stage the CRM has no match for keeps the record's open stage.
- **Archiving** a deal record takes it out of outcomes and reports; deleting a deal through `DELETE .../deals/{id}` archives its record. A deal from HubSpot stays in outcomes until HubSpot closes or removes it.
- **Timelines.** A deal is on the timelines of its people and company, and its own timeline shows it and its changes.

## Product events

Visits to your own websites come in through [website tracking](website-tracking.md): a public site key and a snippet that send anonymous page views with their campaign tags, click ids and referral codes. [Website visitors](website-visitors.md) shows which known companies and people came, and who is hot.

Send what people do in your product to a brand: `POST /v1/brands/{brand}/events` (MCP `send_brand_events`), in Segment's format: one event, or `{"batch": [...]}` with up to 500 (5 MB). A product already sending to Segment, RudderStack or a similar library can send the same calls here, in camelCase or snake_case. Send them from your servers with an API token that has `customers.manage`, since it can change customer records; a token made for one brand can only send to that brand.

```json
{"source": "segment", "batch": [
  {"type": "identify", "userId": "u_123", "anonymousId": "a_9f2", "messageId": "m_1",
   "traits": {"email": "ada@example.com", "email_verified": true, "firstName": "Ada", "plan": "pro"}},
  {"type": "track", "userId": "u_123", "event": "Order Completed", "properties": {"total": 42}, "messageId": "m_2"},
  {"type": "group", "userId": "u_123", "groupId": "org_7", "traits": {"name": "Acme", "website": "acme.example"}}
]}
```

- **Saved first.** Every event is saved as sent, and the answer is `202` at once with the batch's `batch_id` and a receipt `id` per event. Only the envelope can be refused (not JSON, over 5 MB, more than 500 events, a `source` that isn't a short lowercase label); an event is never refused for its content.
- **Processed in the background.** A job processes the brand's events in the order they arrived, usually within seconds. Each ends as one of:
  - `accepted`: stored, with the person it matched (`record_id`) and the product event (`customer_event_id`)
  - `duplicate`: a `messageId` the brand already has, so it was stored once
  - `rejected`: something to fix, with a `code` and a `reason` that says what (`missing_id`: every event needs `userId` or `anonymousId`; `invalid_timestamp`; `property_type`: a property isn't the type the brand's catalog defines for that event; `identity_taken` and the other record conflicts; `internal`: it failed five times for a reason of ours)
  - `held`: a track name someone paused in the brand's catalog. It is kept, but changes nobody and drives nothing until the name is approved again. New names are never held.
- **Follow them.** `GET /v1/brands/{brand}/events` lists them newest first, filtered by `status`, `code`, `source`, `batch_id`, `name` or when they arrived; `GET .../events/{id}` shows one with the event as sent (`raw`); `GET .../events/summary` counts the last 24 hours and 7 days by status, and lists the rejection codes, the new names that look like a typo (`name_checks`) and any paused names with held events. The brand's **Events** page in the app shows the same, with what to do about each.
- **Fix and replay.** Fix what the reason says (approve a paused name with `POST /v1/brands/{brand}/catalog/events/{name}/approve` or the Events page's Approve and replay, change a property's type, free an identity), then `POST .../events/replay` with the events' `ids`, or a filter such as `{"status": "held"}` or `{"status": "rejected", "code": "property_type"}`. They go back to `pending` and are processed again. Accepted and duplicate events are never replayed.
- **Kept for a while.** Accepted and duplicate events stay in this log for 30 days, rejected and held ones for 90, so there is time to fix them; each shows its `expires_at`. The product events themselves (`GET .../customer-events`, the timeline) are kept as before.
- **Types.** `track` (something happened), `identify` (who someone is), `page` and `screen` (stored as `page.viewed` and `screen.viewed`), `group` (the company they belong to) and `alias` (an id they had before).
- **Names.** A track's `event` becomes its catalog name: `Order Completed` is `order_completed`, `checkoutStarted` is `checkout_started`, and dotted names such as `product.activated` stay as they are. A name the catalog has not seen is added to it **approved**: its events are stored straight away and can be used at once in segments, lists, journeys, rules and reports. Nobody approves names. See [New event names](#new-event-names).
- **Retries.** Send a `messageId`: a second event with the same one comes back as `duplicate`.
- **Orders are payments.** An `Order Completed` track event from your server, for a known person, with `revenue` (or `total`, or `value`) in major units and a `currency`, is also saved as a [transaction](transactions.md) and counted as a `payment.succeeded` event. Put the payment's id in `order_id`: when the same payment also arrives as a transaction (with that id as its `source_id`) or from a billing connector, it counts once ([One payment, counted once](transactions.md#one-payment-counted-once)).
- **Refunded orders are refunds.** An `Order Refunded` event from your server, with the order's `order_id`, is a refund of that payment, counted once with a refund transaction for the same refund ([Refunds, counted once](transactions.md#refunds-counted-once)). **People → Developer setup** checks that sign-ups, orders and refunds arrive and count ([developer-setup.md](developer-setup.md)).
- **The brand.** Events belong to the brand in the URL; a `brand_id` in the body is ignored. People are found by `userId`, verified email and `anonymousId` in that brand only: the same `userId` sent to two brands makes a person in each, and neither ever changes the other. The catalog that approves names and types properties is the brand's own.

### New event names

A new track name is usable as soon as it arrives. Within a minute, the name check (job `event_names.check`) compares each new name with the names the brand already sends, and asks only when one looks like a typo or a second spelling of another: "Is `sign_up` a typo of `signed_up`?". It uses the workspace's own AI model when one is connected (AI tool **Event name check**, on the cheap model); without one, or when the call fails, it compares spellings: the same words with other separators, a prefix more or less (`user.signed_up` and `signed_up`), a letter or two added or dropped, or one letter changed or swapped. A name that differs only in its numbers (`step_1`, `step_2`) is its own event.

- **Where it asks.** The brand's **Events** page (People → Events) shows "Is this a typo?" with both names and how many events each has; `GET /v1/brands/{brand}/events/summary` lists them as `name_checks`, the status shows `event_names_to_check`, and each catalog entry carries `name_check` (`pending`, `ok` or `asked`), `looks_like` and `check_reason`.
- **Answering.** `POST /v1/brands/{brand}/catalog/events/{name}/name-check` (MCP `answer_event_name_check`, scope `customers.manage`) with `{"same": true}` moves the name's stored events to the name it looks like, stores the ones sent with it from now on under that name, and keeps the name in the catalog deprecated, pointing there (`same_as`). `{"same": false}` keeps both. Either way its events were usable while the question waited.
- **Pausing a name.** Setting a name's status to `quarantined` in the catalog pauses it: its new events are `held` until it is approved again (`.../approve`) and replayed.
- **From browsers.** A brand takes at most 100 new names a day from its website's snippet; past that, the rest of the day's new names are refused and names it already has keep working.

The workspace route `POST /v1/workspaces/{ws}/customer-events` (MCP `track_customer_events`) still works for now: it processes the events while you wait and answers each one's result in `results`, keeping rejected events only in that answer. It moves to the brand route in a later release.

### Who an event is about

1. **`userId`**, your product's own id for the person, is the strongest key. A `userId` no record holds makes a new person (origin `first_party`).
2. Without one, a **verified email** in identify's traits (`email_verified: true`, only once they proved they own it) finds the person who holds it.
3. Otherwise **`anonymousId`** finds the person it was linked to. An anonymous visitor's events wait unmatched (`GET .../customer-events?matched=false`) and join the person once an identify sends their `userId` with that `anonymousId`.

Two different `userId`s are never merged, and an email someone only typed in never matches anyone: it is added to the person unverified. A verified email another person holds stays theirs, and the result says so in `warnings`.

### Traits

identify's traits fill in the person; group's fill in the company. Each trait that matches a field in the catalog (`firstName` is `first_name`, `title` is `job_title`, `address.city` is `city`) is written with source `first_party`, so it never overwrites a manual edit. `email` and `phone` become identities, `name` (or first and last name) the record's name, and a company's `website` its domain. Every other trait is kept on the event and listed in `ignored_traits`: add a field to the catalog to keep it on the record from then on. group finds the company by `groupId` (or makes it) and adds it to the person's companies.

## Duplicates

Records of one brand that share an email or phone may be the same human; records of two brands never are, and are never paired or merged. Outflare lists each such pair once (`GET .../records/duplicates`, MCP `list_record_duplicates`), including pairs that existed before the upgrade, with both records side by side, for someone to answer "same human?":

- **Merge** (`POST .../records/{record}/merge` with `from`, MCP `merge_records`): the record in the path is kept. The other's identities, links, product events, consent and payment subscriptions move to it, and its billing fields are worked out again from both; its attributes fill in what the kept record lacks, or replace a value a lower-precedence source set (a CRM value never replaces one someone typed), and lists such as companies are joined. The merged record is kept, archived, with `merged_into` set, so its history and old links still lead to the one kept.
- **Link** (`.../duplicates/{candidate}/link`): the same human, kept as two records, joined by a `same_human` link.
- **Dismiss** (`.../duplicates/{candidate}/dismiss`): different people. The pair is not asked about again.

Two records that both have their own `user_id` are two accounts in your product. They are listed as `blocked` and are never merged (`merge_refused`); link them instead.

Outflare merges on its own in one case only: when your product sends a verified email (identify with `email_verified: true`) that belongs to a record with no user id of its own. The email belongs to the record holding it verified, such as one from an import or a CRM sync. When no record holds it verified, it belongs to the only record holding it at all, such as a lead or someone who unsubscribed, as long as that record holds no other email. That record is merged into the person, with its consent and opt-outs, and the event's result lists it in `merged`. When several records hold it unverified, or the only one has its own user id or another email, they are listed as possible duplicates instead, and a pair someone dismissed or linked stays as it is.

## Timeline

`GET .../records/{record}/timeline` (MCP `get_record_timeline`) is one timeline per person or company across every module, newest first:

| Kind | What |
| --- | --- |
| `event` | Product events (for a company, the group calls that named it). Lifecycle email (`email.sent`, `email.delivered`, `email.opened` and so on, source `lifecycle`; see [docs/lifecycle-email.md](lifecycle-email.md)), payments and [meetings](#meetings-as-events) (`meeting.booked`, `meeting.held` and so on) arrive as events too. |
| `attribute_change` | A field changed: old and new value, who changed it and from which source |
| `identity` | An email, phone or other identity added or closed |
| `consent` | Consent given or withdrawn |
| `message` | Cold email sent to them and their replies (`direction` `out` or `in`) |
| `meeting`, `deal` | Their meetings and deals |
| `visit` | A visit to your website: landing page, pages seen, referrer, campaign tags, country and device |
| `opt_out` | An entry on the opt-out list for them: why, how and from which tool (see [docs/opt-outs.md](opt-outs.md)) |
| `journey` | A journey entered, and finished or left |

`kinds=visit,message` narrows it. A company's timeline includes its people's (everyone linked to it), and each item's `record_id` says whose it is; `people=false` shows only the company's own. On the brand route it holds the brand's items and the ones no brand owns (an email sent outside a sequence); on the workspace route `brand=<id or handle>` does the same, and people limited to some brands only see their brands' items and shared ones. Pages are 50 items (up to 200 with `limit`); pass `next_cursor` as `cursor` for the next. Kind names don't change once shipped; new kinds may be added.

## Before you reach out

Every card shows a **relationship check** at the top of its record (`GET /v1/brands/{brand}/records/{record}/relationship`, MCP `get_record_relationship`, `customers.read`), so nobody pitches a partnership to a customer or cold-emails someone a colleague already knows. It works from the card's company: the company itself, a partnership's `company` field, or a person's company, together with that company's people. It says:

- **Already a customer**: an active or at-risk product in the lifecycle, or a won deal.
- **Open deal**: the deals with the company or its people, open, won and lost.
- **In another pipeline**: the brand's other records that point at the company, such as its SEO or affiliate partnership, with their stage.
- **Warm intro**: who on the team has emailed the company's people from their own mailbox, how often and when. It counts only email the person asking may see, the same as the email tab (their own, shared with them, or shared with the team and checked to be about work), never says what was written, and isn't given to API tokens.
- **Stale thread**: when anyone last sent outreach, heard back, met or emailed them, and whether a two-way conversation (a reply, a meeting or email) has been quiet for 60 days.

## Email with your contacts

A person can save their own email with a contact onto that contact: imported by hand, or captured from their Gmail once they connect it (`/v1/brands/{brand}/crm/mail/...`). It is stored encrypted, and only for whole conversations with that contact: mail with colleagues, mixed internal threads, blocked addresses and domains (the brand's and the person's own) and mail under a private Gmail label (Personal, HR, Legal, Finance and Medical unless the brand or person changes `private_labels`) are never saved. The filters run again on every read, so a new exclusion hides older mail at once.

**Reading everyone's mail through Google Workspace.** Instead of each person connecting Gmail, an admin can add the company's own [Google Workspace connection](connectors.md#google-workspace-team-email-into-the-crm) (a service account with domain-wide delegation for read-only Gmail) and pick it in the brand's rollout (`workspace_integration_id`). Every enabled member's mailbox is then read with it, starting from `history_days` back (0, the default, reads the whole history), under exactly the same filters, sharing rules and AI privacy check. Their connection says `via: workspace`; they can delete what was captured but only an admin stops it, by taking them off the rollout. Anyone who connected their own Google account keeps it. Clearing the setting removes the Workspace-read mailboxes at once. Their calendars are read through the same connection too (`calendar.readonly`, the last 90 days and everything ahead, then only what changes, every 15 minutes). A meeting is kept only when everyone from outside the company on it belongs to one of the brand's contacts or companies, under the same filters as email (internal-only meetings, blocked and private senders are left out), and never when the calendar marks the event private or confidential. A kept meeting is its owner's, exactly like their email: private by default, shown to colleagues only when the owner shares that contact (who and when, or in full with the title and guests), or under team sharing once the AI check has read its title and found it to be about work (`GET .../crm/mail/records/{record}/meetings`). A meeting deleted in Google is marked canceled. The rest of a calendar is never copied.

**Sending as yourself.** Every module that sends an email from a person's own address uses one send service ([ADR 15](adr/0015-google-layer.md)): `POST /v1/brands/{brand}/crm/mail/sends` from your own signed-in session, with the record it's about, `to`, `cc`, `subject`, a plain-text `body` and, to reply in a thread, `in_reply_to` (a captured email you can see). The recipients pass the same policy check as any reply (suppressions and withdrawn consent), the send runs in a job, and `GET .../sends/{send}` says how it went (`queued`, `sending`, `sent` or `failed` with an `error_code`). The sent email is kept on the record like captured mail. A retry looks for the email in your Sent mail first, so it is never sent twice. Today it sends from mailboxes read through the Google Workspace connection, which needs `gmail.send` delegated (the connection lists what Google allows in its `scopes`); a person's own Google sign-in gets sending with the shared sign-in, a later step of ADR 15.

**Who can read it.** By default only its owner. The owner can share a contact's mail with chosen people or the whole brand, as "who and when" only or in full. No role, owner or admin included, reads someone's mail without that.

**Capturing every contact.** By default each person picks the contacts and companies whose mail their Gmail capture saves. An owner or admin can switch on `capture_all_contacts` in the brand's policy instead: each person enabled for capture then saves their mail with any contact or company in the brand's CRM. An email is saved only when everyone outside the company in it belongs to one record: the contact whose address it is, or else the company whose domain it is (public mailbox services such as Gmail never count as a company). Mail that fits two contacts or two companies isn't saved, Gmail's promotions, social and forums tabs are skipped, and every filter above still applies. Switching it on or off syncs every connected mailbox again (`crm_mail.capture_all_changed`), and every member sees it in their own email settings.

**Team email sharing.** An owner or admin can switch on `team_sharing` in the brand's policy, so everyone who works on the brand can read each other's work email with CRM contacts and the history with a customer, partner or supplier isn't stuck in one inbox. It starts 48 hours after it's switched on (`team_sharing_from`), and every member of the brand gets a `crm_mail.team_sharing` alert at once; switching it off is immediate and tells them again. While it's on:

- A contact whose owner made no choice is shared in full with the brand, and its sharing setting says so; a contact its owner set private stays private.
- Mail from payroll, HR, benefits, banking and expense services (such as Gusto, Deel, Rippling, Wise or Revolut) is never saved; add your own accountant and law firm as blocked domains.
- Before a colleague sees an email, the workspace's own AI model (the cheapest of its provider, the "Email privacy check" task, within its AI limits) reads its subject and first lines. One about pay, a job offer, HR, a legal dispute, health or family stays its owner's, and so does one the model was unsure about or hasn't read yet. Without an AI connection colleagues see no shared email; emails a person shares on purpose aren't checked.
- Each time someone reads a colleague's email it's recorded as `crm_mail.viewed` (who, whose and which contact, never what).

Every member sees whether it's on, and from when, in their own email settings.

## Consent

Consent is recorded per person, channel (`email`, `sms`, `push`, `whatsapp`, `in_app`), topic (a subscription topic's key such as `product_updates`, or none for the whole channel) and purpose (default `marketing`), with where it happened (`source`: `signup_form`, `preference_center`, `unsubscribe_link`, `import`) and what proves it (`evidence`, such as the form and its wording's version).

- `POST .../records/{record}/consents` with `status` `granted` or `withdrawn` records one. Nothing is overwritten: the latest one counts, and `GET .../consents?history=true` lists them all.
- `GET .../records/{record}/consents` is the current state for each channel, topic and purpose.
- A topic's own consent wins over the whole channel's, except a later withdrawal from the whole channel, which covers every topic.
- Lifecycle email sends marketing to people who agreed, never to third-party leads (origin `prospect`) without their consent, and to everyone else unless they said no or the workspace's contact rule asks for consent first ([docs/opt-outs.md](opt-outs.md)).

### Unsubscribes

An unsubscribe from any email, whether a cold sequence's link, a reply asking to stop, the lifecycle unsubscribe link or one imported from another sequencer, does two things: it adds the address to the suppression list, and it records a withdrawn consent to all marketing email on the person's record (adding a prospect record if nobody has the address). So the person is out of every module's marketing, in every brand. Transactional email they need, such as receipts and password resets, still reaches them.

Only the person opting in again clears it: a grant to all marketing email (no topic) whose `source` is one of `signup_form`, `signup`, `opt_in`, `double_opt_in`, `checkout`, `form`, `in_app` or `preference_center`, such as when they create an account and agree to receive email. That grant removes the unsubscribe suppressions on the person's addresses and records `suppression.removed` with `via: opted_in`. A grant from an import, a CRM sync, the API without a source, or an agent never clears an unsubscribe, and bounces, complaints and manual blocks always stay.

## Privacy export

When someone asks what you hold about them, a privacy export gathers it into one file: the record with its fields, and every row in the brand that points at it, which is its emails and ids, links, field history, merges, product events (and the events as sent to the brand), consent, emails, messages, replies, meetings, deals, payments, list memberships, journey runs and the rest. The list is the one merges use (`customers.Follows` and `customers.Owned`), so anything new that points at a person is exported too.

- **One brand.** Privacy requests are per brand: an export covers only the brand the record belongs to. The same email in another brand is another person, and never in the file. Rows linked to the person that belong to no brand (shared by every brand, from before brands owned their data) are theirs too, so they are included.
- **Ask for one** on the record's page (Privacy export), with `POST /v1/brands/{brand}/records/{record}/privacy-exports` or the MCP tool `request_privacy_export`. It needs `customers.manage`. It answers at once, pending, and a background job builds the file, usually in seconds.
- **Download it** from the record's page or `download_url` (`GET .../privacy-exports/{export}/download`), for 7 days. The file is gzipped JSON: `{"exported_at", "brand": {"id", "name"}, "record": {...}, "tables": {"emails": [...], ...}}`, each row with all of its columns. Downloading needs `customers.manage` as well, since it is someone's personal data in bulk; `customers.read` can see that an export exists, its status and counts.
- **No secrets.** Columns holding tokens, secrets, passwords, hashes, sealed values or credentials, and binary columns, are never included.
- **Limits.** Over 100 MB uncompressed, an export fails with `too_large`. After 7 days the file is deleted and the export shows as expired (a download answers 410); ask for a new one.
- **Kept on record.** Asking (`privacy_export.requested`), the result (`privacy_export.ready` or `privacy_export.failed`) and every download (`privacy_export.downloaded`) are in the audit log; `privacy_export.ready` is also a webhook event. Erasing the record deletes its exports with it.

## Deleting and restoring

Bad data can be deleted and, for 7 days, brought back as it was: the recovery window. Legal erasure is separate.

- **What can be deleted.** A person or company (with their emails and ids, links and product events), one field's value, one identity, one link, one product event, or one [transaction](transactions.md). Payments and refunds are product events, so a test payment is deleted the same way; a transaction (a payment row from billing or the API) is deleted from Transactions, and a billing sync won't add it back while it is deleted. Each delete is one *deletion*, restored together.
- **In the app.** On a record's page, Delete value next to a field, Delete next to an email or id, and Delete next to a link each delete just that one thing, after a confirmation, and it shows up in Recently deleted.
- **Hidden everywhere at once.** A deleted record is gone from the record API, lists (also Archived), search, Query, smart lists, rules, journeys and sends; its active journey runs end (`exit_reason` `deleted`) and stay ended. Its identities, links and events, and a deleted identity, link or event on its own, move out of their tables while deleted, so nothing reads them, ClickHouse drops them, and their values (an email, an event's message id) are free: a sync or event with the same email makes a new person. A deleted field value leaves the record without firing rules or journeys.
- **Deleting events changes reports.** Reports, attribution, A/B test results and rule history that counted a deleted event change while it is deleted; a payment's lifetime revenue is worked out again. Nothing already sent because of it is taken back. The delete flow says so.
- **Recently deleted.** People → Recently deleted, `GET /v1/brands/{brand}/recently-deleted` or the MCP tool `list_recently_deleted` lists the brand's deletions, newest first, with who deleted each and when it goes for good. `customers.read` sees them; deleting and restoring need `customers.manage`.
- **Restoring** (`POST /v1/brands/{brand}/recently-deleted/{deletion}/restore`, `restore_deletion`) puts everything back with its ids, history and links, and fires nothing: no rule, journey, webhook or email for what came back, and a person kept their place on smart lists, so they don't join again. What can't come back stays in the deletion with a reason, and the rest is restored: `taken` (another record verified the email since, or the same event arrived again), `gone` (the other end of a link was erased), `changed` (the field was set again) or `record_deleted` (restore the record first). Fix it and restore again.
- **Purged after 7 days.** Then the deletion goes for good, and a deleted person or company is erased as below (with their leads, so the link job can't make them again). A restore after that answers 410 `deletion_expired`.
- **Erasure is permanent.** For a request to erase someone's data, use Erase permanently on the record's page, `DELETE /v1/brands/{brand}/records/{record}` or `delete_record`: it is final at once, also for a record in Recently deleted (its deletions go with it), and `suppress=true` adds their emails to the do-not-contact list so a later import or sync can't bring them back.
- **On record.** `record.soft_deleted`, `field_value.deleted`, `identity.deleted`, `relationship.deleted`, `customer_event.deleted`, `transaction.deleted`, `deletion.restored` and `deletion.purged` are in the audit log; `record.soft_deleted`, `transaction.deleted` and `deletion.restored` are also webhook events. A privacy export includes what is in Recently deleted, since it is still held.

## Who can edit records

By default everyone whose role allows changing customer records (`customers.manage`) can edit them. An owner or admin can turn on **Only owners and admins can edit records** (Settings → Workspace, or `PATCH /v1/workspaces/{ws}` with `records_edit_owners_only`). Then only owners and admins, and API tokens they made, change people, companies and other records: creating, editing, deleting, merging and undoing merges, emails and ids, and links. Everyone else can still see them and gets 403 `records_locked` when they try. A token follows its maker's role, so an admin's token stops editing when they become a member. Imports, CRM syncs, product events and other automatic writes are not affected. Turning it on or off is recorded as `workspace.records_editing_changed`.

## Bulk edits

Owners can change many people or companies at once, from a list, a saved query or ids picked by hand: set an owner or a field, add or remove tags, move them in or out of a static list, or archive them. They preview it first and can undo it for 7 days. Every change is in each record's own history with the bulk edit's id. See [Bulk edits](bulk-edits.md).

## Undoing a merge or a batch

A wrong merge or a bad import can be undone in one step.

### Undoing a merge

- **Where.** A record's page lists its merges under Merges, with Undo merge; `GET /v1/brands/{brand}/merges?record=` and `POST /v1/brands/{brand}/merges/{merge}/undo` (MCP `list_merges`, `undo_merge`). Reading needs `customers.read`, undoing `customers.manage`.
- **What goes back.** The merged record is a record of its own again (not merged, and not archived unless it was before). Every row the merge moved goes back to it: its emails and ids, links, product events, consents, emails, replies, meetings, deals, list places, journey runs, payments and the rest. The copies of its identities and links on the kept record go; the fields the merge filled on the kept record go back to what they were; other records whose reference fields the merge pointed at the kept record point at the merged one again; and the duplicate pair is open again. Billing fields and first and last touch are worked out again for both.
- **What stays.** Whatever reached the kept record after the merge stays there. A field changed since the merge keeps its new value (the answer lists it as `changed`), and an identity someone else holds now stays closed (`taken`). A journey the merged record left because the kept one was already in it stays ended.
- **Chained merges undo newest first.** After A was merged into B and then B into C, A into B can't be undone until B into C is: it answers 409 `merge_not_latest`. `undoable` on each merge says whether it can be undone now.
- **Older merges.** A merge made before Outflare kept a record of what merges move can't be undone: 409 `merge_not_reversible`. Split those records by hand.
- **Fires nothing.** No rule, journey, email or webhook is sent for the rows that move back. `record.unmerged` is in the audit log and is a webhook event, as `record.merged` is.

### Undoing a batch

- **What a batch is.** Every CSV import of leads, every CRM import and every send of product events to a brand (`POST /v1/brands/{brand}/events`, whose answer has its `batch_id`) is a *batch*. Everything it writes carries it: the people and companies it creates, field values it sets (their history), emails and ids, links, product events, list places and leads. Data written before batches existed carries none, so it can't be undone this way. A batch belongs to the brand its records are made in (a CSV import's list's brand, a CRM import's connection's, else the workspace's first), and undoing it never touches another brand's data.
- **Where.** People → Imports lists a brand's batches with what each made; `GET /v1/brands/{brand}/batches` (MCP `list_batches`). Undo… shows the preview first.
- **The preview** (`GET .../batches/{batch}/undo-preview`, `preview_batch_undo`) counts what the batch made, and lists what changed since: records someone else edited, emailed, that have payments or other activity since, records merged since, fields it set that were changed since, and leads already in a sequence. It says what each way of undoing would do.
- **You choose each time** (`POST .../batches/{batch}/undo` with `mode`, `undo_batch`):
  - **Undo only what's untouched** (`untouched`) removes the records nobody changed since, with their identities, links, events and list places; removes what the batch added to other records; puts back field values it set (only while they still hold its value); and removes the leads it made that no sequence used. The changed records stay, with what the batch put on them, listed in the preview so you can remove them by hand.
  - **Undo everything** (`everything`) also removes the changed records.
  - Neither undoes a record merged since (undo the merge first), a field someone changed since (the batch's value is already gone) or a lead already in a sequence: emails already sent can't be unsent. Their history stays, and on the timeline an email sent to someone an undone batch made carries `detail.undone_batch_id`.
- **Restorable for 7 days.** Everything an undo removes is one deletion of kind `batch` in Recently deleted, restored in one step; then the batch can be undone again. After 7 days the records it removed are erased.
- **Large batches** (over 2,000 rows) are undone in the background: the answer is 202 and the batch's `undo.status` goes `queued`, then `done` or `failed` (a failed undo changes nothing; try again). Undoing twice answers 409 `batch_already_undone`, or `batch_undo_running` while it runs.
- **Someone's edits** are changes made by a person or an API token (field history with an actor); fields Outflare works out itself (billing, first and last touch, a lead's details) don't count as an edit.
- **Fires nothing.** No rule, journey, email or webhook is sent for what it removes or puts back. `batch.undone` is in the audit log and is a webhook event.

## Scopes

| Scope | Allows | Roles |
| --- | --- | --- |
| `customers.read` | Read records, their identities, links, history, timeline and consents, product events, and the catalog | Owner, admin, operator, viewer |
| `customers.manage` | Add and change records, identities and links, merge duplicates, send product events, approve a held event name, record consent, and ask for and download privacy exports | Owner, admin, operator |
| `catalog.manage` | Add, change and archive record types, fields, option lists and event definitions | Owner, admin |
