# Customer records

Customer records are Outflare's one model of the people and companies a workspace knows, plus any other kind of record it tracks, such as deals, subscriptions or partners. The customer data platform, the CRM and lifecycle email all read and write the same records, so a fact about someone is stored once, whichever feature or connector wrote it.

This covers the customer data platform so far: records, their identities and links, the shared catalog, product events, duplicates and merges, each record's timeline, and consent. [Search](workspace-search.md) finds records and their history by their words, [Query](query.md) reads it with SQL; segments and rules build on it.

Everything here works the same from the REST API and the MCP server.

## The catalog

The catalog says what records can hold. Read it first (`GET /v1/workspaces/{ws}/catalog`, MCP `get_catalog`) and reuse what is there before adding anything.

- **Record types.** Every workspace has `person` and `account` (shown as Companies). Add your own with `POST .../catalog/types`, such as `deal`. A type's key never changes; archiving one keeps its records.
- **Fields.** Each type has fields, each the one definition of a fact for every feature: imports, list filters, enrichment, the CRM and merge tags all use it. Built in: first and last name, job title, seniority, companies, country, city, time zone, LinkedIn and lifecycle stage for people; website, industry, company size, country, LinkedIn and lifecycle stage for companies. Add your own with `POST .../catalog/types/{type}/fields`.
- **Value types.** `text`, `number`, `currency` (`{"amount_minor": 1999, "currency": "USD"}`), `date`, `timestamp`, `checkbox`, `select` and `status` (an option key), `rating` (0 to 5), `email`, `phone` (international format), `url`, `location`, `record_reference` (another record, such as a deal's company) and `actor_reference` (a member of the workspace). `multiple` fields take a list. A field's key, value type, option list and target never change; anything else can, and a field can be archived.
- **Option lists.** `select` and `status` fields share option lists, so the industry of a lead, a CRM company and an import are the same values. Built in: country (ISO 3166 codes), industry, company size, seniority and lifecycle stage. Add your own, such as deal stages, with `POST .../catalog/option-sets`. Options are archived, never deleted, so old values keep their meaning.
- **Product events.** `POST .../catalog/events` defines an event your product sends, such as `product.activated`, with its properties. Only approved events will drive segments, rules and journeys.

Changing the catalog needs `catalog.manage`, which owners and admins have by default, because a field is shared by every feature that uses it.

## Records

`POST /v1/workspaces/{ws}/records` adds a record with its `type`, `name`, `attributes` and `identities`; `PATCH .../records/{record}` changes one. Attributes are checked against the type's fields: an unknown key is refused with a pointer to adding the field, and values are normalized (emails lowercase, phones in E.164).

Each record also has:

- **origin**: where it came from (`first_party` for your own product, billing or forms, `prospect` for lead lists and data providers, `crm_sync`, `import` or `manual`). With consent it sets each person's contact basis, which decides what may go out to them ([docs/opt-outs.md](opt-outs.md)). `acquired_via` names the tool the record came from.
- **brand_id**: a brand's record, or shared by every brand. People limited to some brands see their brands' records and the shared ones, and change only their brands'.
- **version**: goes up on every change. Send the version you read with a `PATCH` (or a merge) and an edit made against an old copy is refused with `record_changed`. Agents must send it: a change from the MCP server, a connected AI app or a mission without a version is refused with `version_required`, so two agents working on one record never overwrite each other. People in the app and scripts with a plain API token may leave it out.
- **history**: `GET .../records/{record}/history` lists every change to the name and attributes, with who made it and from which source.

### Which source wins

Every value keeps the source that set it: `manual`, `first_party`, `crm_sync`, `enrichment` or `import` (send `source` with the write; the default is `manual`). Each field has a precedence, by default manual, then first party, then CRM sync, then enrichment. A write from a lower source leaves a higher source's value alone and lists the field in `skipped`, so enrichment never overwrites what someone typed and a CRM sync never overwrites your product's own data. A field's `write_policy` can also keep it for its source system only (`source_owned`) or for Outflare to work out (`computed`).

### Revenue from your payment provider

Connect Stripe and each customer becomes a person with a `stripe.customer` identity, their payments, refunds and subscription changes become events on their timeline, and `billing_plan`, `mrr`, `lifetime_revenue`, `customer_since` and `billing_status` are computed fields on their record. See [Payments](connectors.md#payments).

### Moving from another CRM

Connect Attio and import it once with `POST .../integrations/{id}/crm-imports`: its people, companies, deals and custom objects become records here, its fields become catalog fields and its stages option lists, with each record's Attio id kept as an `attio.record` identity so the import can run again. See [Moving from another CRM](connectors.md#moving-from-another-crm).

### Leads are people

Every lead is a person record. A new lead joins the person who already has its email, or gets a record of its own with origin `prospect`, its email as an unverified identity (so it never claims an address someone proves they own), its lead id as an `outflare.lead` identity, and its name, job title and LinkedIn as attributes. `record_id` on a lead links to it. Imports of up to 1,000 leads are linked as they land; bigger ones, and leads from before this, are linked by the `customers.link` job within moments.

Cold email, replies, meetings and deals carry the person's `record_id` too: from their lead, or else from the person who has the address (a reply's sender, a meeting's attendee). Lifecycle email to one address links to its verified owner. Merging two records moves all of it to the one kept.

## Identities

Identities say who a record is: `email`, `phone`, `device_token` (with `properties.platform`: ios, android or web), `user_id` (your product's own id), `anonymous_id`, `domain` for companies, or a source's own, such as `stripe.customer`.

Only a **verified** identity claims its value, and only one record can hold a value verified:

- An email or phone is unverified unless you say the person proved they own it (`"verified": true`, only from your servers). Your own ids, payment customers and domains are trusted as sent.
- Someone who signs up with another person's email and never confirms it gets their own record. They never take over the other person's record, and the address stays the other person's.
- A verified value another record already holds is refused with `identity_taken`, naming that record when you can see it. Nothing is moved or merged.
- When a person changes their email, add the new one with `replaces` set to the old identity's id: the old one is closed as `changed` and kept in the history, so the address can later be claimed by whoever proves they own it.
- `GET .../records/lookup?namespace=email&value=...` finds the records an identity points at: `owner` is the one holding it verified, and the rest hold it unverified.

## Links

Records link to each other: a person to their companies, a deal to its company. Record reference fields (such as a person's `companies`) are links too, so there is one graph. Add other links with `POST .../records/{record}/relationships` (`kind` such as `partner_of`) and end them with `.../relationships/{relationship}/end`; ended links are kept.

## Product events

Visits to your own websites come in through [website tracking](website-tracking.md): a public site key and a snippet that send anonymous page views with their campaign tags, click ids and referral codes. [Website visitors](website-visitors.md) shows which known companies and people came, and who is hot.

Send what people do in your product to `POST /v1/workspaces/{ws}/customer-events` (MCP `track_customer_events`), in Segment's format: one event, or `{"batch": [...]}` with up to 500. A product already sending to Segment, RudderStack or a similar library can send the same calls here, in camelCase or snake_case. Send them from your servers with an API token that has `customers.manage`, since it can change customer records.

```json
{"batch": [
  {"type": "identify", "userId": "u_123", "anonymousId": "a_9f2", "messageId": "m_1",
   "traits": {"email": "ada@example.com", "email_verified": true, "firstName": "Ada", "plan": "pro"}},
  {"type": "track", "userId": "u_123", "event": "Order Completed", "properties": {"total": 42}, "messageId": "m_2"},
  {"type": "group", "userId": "u_123", "groupId": "org_7", "traits": {"name": "Acme", "website": "acme.example"}}
]}
```

- **Types.** `track` (something happened), `identify` (who someone is), `page` and `screen` (stored as `page.viewed` and `screen.viewed`), `group` (the company they belong to) and `alias` (an id they had before).
- **Names.** A track's `event` becomes its catalog name: `Order Completed` is `order_completed`, `checkoutStarted` is `checkout_started`, and dotted names such as `product.activated` stay as they are. A name the catalog has not seen arrives **quarantined**: it is stored and shown, but drives nothing until someone approves it (`PATCH .../catalog/events/{name}`).
- **Retries.** Send a `messageId`: a second event with the same one is stored once and comes back as `duplicate`.
- **Results.** Each event is `accepted`, `duplicate` or `rejected` on its own, with the person it matched (`record_id`) and its name, so one bad event never loses the rest of the batch.

### Who an event is about

1. **`userId`**, your product's own id for the person, is the strongest key. A `userId` no record holds makes a new person (origin `first_party`).
2. Without one, a **verified email** in identify's traits (`email_verified: true`, only once they proved they own it) finds the person who holds it.
3. Otherwise **`anonymousId`** finds the person it was linked to. An anonymous visitor's events wait unmatched (`GET .../customer-events?matched=false`) and join the person once an identify sends their `userId` with that `anonymousId`.

Two different `userId`s are never merged, and an email someone only typed in never matches anyone: it is added to the person unverified. A verified email another person holds stays theirs, and the result says so in `warnings`.

### Traits

identify's traits fill in the person; group's fill in the company. Each trait that matches a field in the catalog (`firstName` is `first_name`, `title` is `job_title`, `address.city` is `city`) is written with source `first_party`, so it never overwrites a manual edit. `email` and `phone` become identities, `name` (or first and last name) the record's name, and a company's `website` its domain. Every other trait is kept on the event and listed in `ignored_traits`: add a field to the catalog to keep it on the record from then on. group finds the company by `groupId` (or makes it) and adds it to the person's companies.

## Duplicates

Records that share an email or phone may be the same human. Outflare lists each such pair once (`GET .../records/duplicates`, MCP `list_record_duplicates`), including pairs that existed before the upgrade, with both records side by side, for someone to answer "same human?":

- **Merge** (`POST .../records/{record}/merge` with `from`, MCP `merge_records`): the record in the path is kept. The other's identities, links, product events, consent and payment subscriptions move to it, and its billing fields are worked out again from both; its attributes fill in what the kept record lacks, or replace a value a lower-precedence source set (a CRM value never replaces one someone typed), and lists such as companies are joined. The merged record is kept, archived, with `merged_into` set, so its history and old links still lead to the one kept.
- **Link** (`.../duplicates/{candidate}/link`): the same human, kept as two records, joined by a `same_human` link.
- **Dismiss** (`.../duplicates/{candidate}/dismiss`): different people. The pair is not asked about again.

Two records that both have their own `user_id` are two accounts in your product. They are listed as `blocked` and are never merged (`merge_refused`); link them instead.

Outflare merges on its own in one case only: when your product sends a verified email (identify with `email_verified: true`) that a record with no user id of its own holds verified, such as one from an import or a CRM sync. That record is merged into the person, and the event's result lists it in `merged`.

## Timeline

`GET .../records/{record}/timeline` (MCP `get_record_timeline`) is one timeline per person or company across every module, newest first:

| Kind | What |
| --- | --- |
| `event` | Product events (for a company, the group calls that named it). Lifecycle email (`email.sent`, `email.delivered`, `email.opened` and so on, source `lifecycle`; see [docs/lifecycle-email.md](lifecycle-email.md)) and payments arrive as events too. |
| `attribute_change` | A field changed: old and new value, who changed it and from which source |
| `identity` | An email, phone or other identity added or closed |
| `consent` | Consent given or withdrawn |
| `message` | Cold email sent to them and their replies (`direction` `out` or `in`) |
| `meeting`, `deal` | Their meetings and deals |
| `visit` | A visit to your website: landing page, pages seen, referrer, campaign tags, country and device |
| `opt_out` | An entry on the opt-out list for them: why, how and from which tool (see [docs/opt-outs.md](opt-outs.md)) |
| `journey` | A journey entered, and finished or left |

`kinds=visit,message` narrows it. A company's timeline includes its people's (everyone linked to it), and each item's `record_id` says whose it is; `people=false` shows only the company's own. `brand=<id or handle>` keeps one brand's items and the ones no brand owns, and people limited to some brands only see their brands' items and shared ones. Pages are 50 items (up to 200 with `limit`); pass `next_before` as `before` for the next. Kind names don't change once shipped; new kinds may be added.

## Consent

Consent is recorded per person, channel (`email`, `sms`, `push`, `whatsapp`, `in_app`), topic (a subscription topic's key such as `product_updates`, or none for the whole channel) and purpose (default `marketing`), with where it happened (`source`: `signup_form`, `preference_center`, `unsubscribe_link`, `import`) and what proves it (`evidence`, such as the form and its wording's version).

- `POST .../records/{record}/consents` with `status` `granted` or `withdrawn` records one. Nothing is overwritten: the latest one counts, and `GET .../consents?history=true` lists them all.
- `GET .../records/{record}/consents` is the current state for each channel, topic and purpose.
- A topic's own consent wins over the whole channel's, except a later withdrawal from the whole channel, which covers every topic.
- Lifecycle email sends marketing to people who agreed, never to third-party leads (origin `prospect`) without their consent, and to everyone else unless they said no or the workspace's contact rule asks for consent first ([docs/opt-outs.md](opt-outs.md)).

### Unsubscribes

An unsubscribe from any email, whether a cold sequence's link, a reply asking to stop, the lifecycle unsubscribe link or one imported from another sequencer, does two things: it adds the address to the suppression list, and it records a withdrawn consent to all marketing email on the person's record (adding a prospect record if nobody has the address). So the person is out of every module's marketing, in every brand. Transactional email they need, such as receipts and password resets, still reaches them.

Only the person opting in again clears it: a grant to all marketing email (no topic) whose `source` is one of `signup_form`, `signup`, `opt_in`, `double_opt_in`, `checkout`, `form`, `in_app` or `preference_center`, such as when they create an account and agree to receive email. That grant removes the unsubscribe suppressions on the person's addresses and records `suppression.removed` with `via: opted_in`. A grant from an import, a CRM sync, the API without a source, or an agent never clears an unsubscribe, and bounces, complaints and manual blocks always stay.

## Scopes

| Scope | Allows | Roles |
| --- | --- | --- |
| `customers.read` | Read records, their identities, links, history, timeline and consents, product events, and the catalog | Owner, admin, operator, viewer |
| `customers.manage` | Add and change records, identities and links, merge duplicates, send product events and record consent | Owner, admin, operator |
| `catalog.manage` | Add and change record types, fields, option lists and event definitions | Owner, admin |
