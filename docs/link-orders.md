# Link orders

Link orders is how a brand buys paid placements from sites in its own donor catalog. The page lives in the SEO module, immediately after Backlinks. Amounts are the **donor price only**: what is paid to the site, with no commission or client markup. USD and EUR are never added together. An invoice can be in any common currency (GBP, PLN, CHF and others, see [Invoice currencies](#invoice-currencies)); it is kept as invoiced and shown beside its US-dollar equivalent. Recording Paid is a Coverage payment row; it never sends money.

The work sits on Coverage, not on a new money system: each catalog domain is a Coverage publisher with a donor profile beside it, a published order becomes a paid placement, and the budget is a Coverage budget tagged `link_orders`. Donor attributes (PBN, niche, casino, rel offered) stay on the profile, so Presence publishers and rates do not grow those fields. See [coverage.md](coverage.md).

Scopes follow Coverage: `coverage.read` to look, `coverage.manage` to edit the catalog and move an order, `coverage.approve` plus being the brand's designated approver to Approve or mark Paid, and `coverage.finance.manage` to record Paid or a credit.

## Catalog

The catalog is the list of sites the brand is willing to buy from (`GET /v1/brands/{brand}/link-orders/catalog`, MCP `list_link_order_catalog`). Add one site at a time, or import a **CSV file**. A Google Sheet is not imported, and there is no Sheet URL field. The first row of the CSV is the header and must include a domain column (`domain`, `website`, `site` or `url`). A repeated domain updates the existing profile; a bad row is listed in `errors` and does not abort the rest of the file (`POST .../catalog/import`, MCP `import_link_order_catalog`). Header columns the import does not read are listed in `ignored_columns`.

Each site has three fields of its own, kept out of notes and niches:

- **Intermediary**: the broker or contact the site is bought through. The Catalog search (and the API's `q`) matches it as well as the site and theme, and the **Intermediary** filter (`intermediary=` in the API) lists every site from one intermediary.
- **Theme**: the site's own theme. **Niches** stay a separate list.
- **Site age**: how long the domain has existed. A file or form may say `2012`, `2012-05-01` or `12 years` (also `8 months`); it is stored as the date the site exists since (`site_since`, 1 January for a bare year) and shown as "Since 2012 · 14 years" (`site_age_years`).

Add and edit them on the Catalog page (Add donor, or the donor card's **Edit**), through `PATCH .../catalog/items/{id}` (`intermediary`, `theme`, `site_age`), or in the CSV.

### Importing an intermediary's database

An intermediary's full export (11,000+ sites, dozens of columns) imports as it is. Headers are matched ignoring case, spaces and punctuation, so `Traffic / Organic`, `traffic-organic` and `Traffic (Organic)` are all traffic, and common spellings are understood (`Website`, `DR`, `Domain authority`, `Ref. domains / Dofollow`, `Outgoing links (all time)`, `Geo AhrefsTop 2`). Besides DR, traffic, country (or **Geo top 1** when there is no country column) and language (**Main language**), the import keeps the intermediary's metrics and shows them on the donor card:

- Date added; Geo top 1 and 2; Article type; Main category and Subcategory
- Organic and total keywords
- Referring domains (all, followed, not followed); referring IPs and subnets
- Backlinks (all, followed, not followed, redirects, internal)
- Outgoing domains and outgoing links (followed and all time)

Numbers may be written `12,500`, `12 500` or `12.5K`. A blank, `-` or `n/a` cell in a metric column clears that metric, so a newer export never leaves a stale number. A row with metrics records the import as its metrics source and time.

Prices: `price` (also `cost`, `amount`) is the donor price in dollars or euros as price lists write it (`150`, `$1,200.50`, `1.200,50 €`); `price_usd` and `price_eur` set each currency. The currency is the `currency` column, else the symbol or code in the cell, else USD. `amount_minor` stays in minor units (cents), as the export writes it. Only USD and EUR are accepted.

**Size.** One import request may be up to **32 MB** (it was 4 MB), which holds a full intermediary database. The server writes the rows in chunks of 500, each in its own transaction, so a big file never holds one long transaction. The Catalog page goes further and sends a large file in **batches** of about 1 MB (at most 1,000 rows), each with the header row, one after another, with a progress bar and one summary at the end: added, updated, rows with errors (numbered by their line in the whole file) and columns not imported. Rows upsert by domain, so if a batch fails the same file can simply be imported again. Through the API or MCP, send a file over 32 MB the same way: several requests that each repeat the header.

**Export** (Export CSV on the Catalog page) writes the intermediary, theme, site age (`site_since`), DR, traffic, price and every metric under column names the import reads back unchanged.

PBN is a **manual flag**. Nothing in this version detects or hides a PBN automatically. The catalog filter defaults to excluding PBN rows.

Domain rating and traffic on a catalog row come from the latest stored SEO snapshot, never from a fresh paid provider read.

### Cell actions (right-click)

Right-click any cell of a catalog row (or press the context-menu key on it) for that site's actions. Left-click works as before: the site opens the donor card. The menu lists:

- **Find email**: the addresses already stored for the site: people at its company record, contacts a link-building check found on its pages, the brand's Outreach leads at the domain, and addresses in the donor's or publisher's notes (such as the intermediary's). Each says where it came from, whether it is at the site's domain, suppressed, or already contacted. It reads stored data only: it does not search the web or call a paid provider, so when nothing is stored it says so and names what it looked through. To read a site's contact pages, add it as a link prospect and check it in Link building. Needs `coverage.read`.
- **Send to a sequence**: a stored or typed address becomes a lead of the brand (source `link_orders`, the donor's domain as its company and website) and joins one of the brand's Outreach sequences with Outreach's usual rules: suppressed, excluded and already-enrolled addresses are skipped, and so is one active in another sequence. The sequence sends from its own mailboxes on its own schedule; a draft or paused sequence sends nothing until it is started. An address at the site becomes one of its company's people, so Find email shows it next time. Needs `coverage.manage` and `sequences.manage`; without them the item is disabled and says which is missing.
- **Open donor**: the donor card.

The actions are one list in `web/src/pages/link-orders/catalogActions.ts`; a new action is a new entry there.

### Direct contact

Some sites in an intermediary's catalog are worth writing to directly, not only through the broker. The catalog marks them **Direct contact** (`direct_contact` on each row of `GET .../catalog` and `GET .../catalog/items/{id}`). The mark is computed on read from stored data, with three rules and no score or cut-off:

1. **Not a PBN**: the row's PBN flag is off.
2. **Metrics stored**: DR or traffic is stored and above zero. A missing value does not count as a pass.
3. **An email at the site is stored**: at least one address at the site's own domain (or a subdomain) is already where Find email looks (people at the site's company, link-building check contacts, the brand's Outreach leads, the notes) and is not suppressed for the brand. An intermediary's address is not a direct one.

`direct_contact` is `{ recommended, emails, missing }`: `emails` counts those addresses and `missing` lists the rules a site misses (`pbn`, `no_metrics`, `no_email`). The **Direct contact** filter on the Catalog page (`direct_contact=true` or `false` in the API, MCP `list_link_order_catalog`) shows only the recommendations or the rest. A recommended row shows **Find email** and **Send to a sequence** under the site: the same actions as the right-click menu, with the same permissions and Outreach rules. The mark never sends or enrolls anything; a site is written to only when someone sends an address to a sequence, which then sends on its own schedule. It reads no paid provider and starts no SEO read.

## Candidates

Candidates are referring domains from the **already-saved** competitor [link gap](seo.md#gaps): sites that link to tracked competitors and not to this brand (`GET /v1/brands/{brand}/link-orders/candidates`, MCP `list_link_order_candidates`). Each row is `not_in_catalog`, `in_catalog` or `ordered`. Traffic is not stored on the gap, so it is always empty (the page shows an em dash).

This list does **not** call Ahrefs or DataForSEO, and it does not `POST /seo-competitors/gaps/links`. That route is a paid referring-domain pull; Link orders never starts it. Listing candidates does not spend the SEO connection budget. The weekly snapshot that stores the gap is already priced like the keywords and links reports, and counts against that budget.

A connection that cannot produce a backlinks snapshot cannot fill this list. **SE Ranking** has no `backlinks` or `links` report, so it does not feed referring domains, the link gap, or Link orders candidates. Use DataForSEO or Ahrefs for that weekly read.

`POST .../candidates/{domain}/add-to-catalog` (MCP `add_link_order_candidate_to_catalog`) find-or-creates a catalog profile with `source=competitor_gap` for a domain that is already in the saved gap. A domain that is not in the gap is refused.

## Targets and anchors

The **Targets** tab lists brand pages to build links to, split into **articles** (blog posts, guides, comparisons) and **products** (product sections and product pages). The kind is guessed from the URL path (`/blog/`, `/guides/`, `how-to-…`, `…-vs-…` are articles; everything else is a product page) and can be changed on a saved page.

Suggestions (`GET /v1/brands/{brand}/link-orders/targets/suggestions?kind=&limit=`, MCP `suggest_link_order_targets`) are computed on request from **already-stored** data. Nothing is saved and no provider is called:

- **Keyword gaps.** From the newest stored `keywords` snapshot per connection, a keyword where a tracked competitor ranks in the top 20 and the brand ranks more than 3 places behind (or not at all) is credited to the brand page that ranks for it. The weight grows with search volume.
- **Competitor backlinks.** From the stored competitor link reads, the competitor pages that collect referring domains are mapped to the brand page that ranks for the same keywords (or, failing that, shares the slug words). The weight grows with the number of referring domains.

Every suggestion lists its reasons (at most eight), so the score can be checked by a person.

**Picking a page.** A ticked checkbox saves the page as a target (`POST /targets`, `source=suggested`, or `manual` when typed in). Unticking keeps the row but sets `selected=false` (`PATCH /targets/{id}`), and the page stops being suggested. When requesting an order, the top suggestion is pre-ticked; the person can tick another or type any page on the brand's domains. An order stores `target_id`, `target_kind` and `target_choice` (`suggested` or `manual`); a typed URL that differs from the saved target drops the link to it.

**Anchors.** An order carries two anchors:

- `anchor_primary` with `anchor_primary_source`: `se_ranking` when it came from a stored SE Ranking keyword for that page (the first choice offered), then `page_target` (the page's saved target keyword), then `seo_keywords` (another connection's stored keyword), or `manual` when typed.
- `anchor_secondary` with `anchor_secondary_type`: `brand` (the brand name), `url` (the page URL, host, or host and path) or `keyword` (another SE Ranking word for that page). The type is chosen per order; none is fixed.

`GET /targets/anchors?page=` (MCP `get_link_order_target_anchors`) returns these options for any brand page. Anchors are plain text up to 500 characters; a second anchor needs a type, a primary anchor, and must differ from the primary. Target and anchors can change until the order is published.

When no SE Ranking keyword snapshot is stored for the brand's site, the primary anchor falls back to the other sources and the Targets tab says so. SE Ranking still has no backlinks report, so the competitor-backlinks reason only appears with DataForSEO or Ahrefs link reads.

## Orders

An order is a request to buy a link on one catalog donor, to one target URL, with a status of its own (`requested`, `quoted`, `negotiating`, `approved`, `published`, `paid`, `verified`, `lost`). The machine is not Coverage's placement stage.

- **Approve** is reserved for the brand's designated approver (`coverage_link_order_settings.approver_user_id`, who also needs `coverage.approve`). Until an approver is set, Approve and Paid stay blocked. MCP has no tools to approve or pay.
- **Publish** records a Coverage placement with `arrangement=paid` (uniqueness is URL plus inventory) and queues a live-link check.
- **Paid** is the same approver, plus `coverage.finance.manage`, after the order is published **and** a live-link check has passed (the link is on an up page and the target matches). Spent is booked then, not when the order is approved.
- **Verified** is a later confirmation that the link is still in place. Indexed-on-a-third-party-URL is out of this version and does not block it.
- **Lost** is a person marking it lost, or two consecutive conclusive live or target failures. A network error is not conclusive, and a single failure is not enough.

A lost order can be replaced once, by opening a new order with `replaces_order_id`. There is one open replacement at a time, and replacements cannot form a cycle. `GET .../link-orders/{id}/equivalents` (MCP `get_link_order_equivalents`) suggests catalog donors in the same domain-rating band and a niche in common.

## Invoice currencies

A quote, and the payment Mark Paid records, are in the currency on the donor's invoice: USD, EUR, GBP, PLN, CZK, UAH, CHF, CAD, AUD, NZD, JPY, SEK, NOK, DKK, RON, HUF, BGN, TRY, INR, SGD, HKD, MXN, BRL or ZAR. There is no free-text currency. Amounts are in the currency's minor unit; JPY and HUF have none (¥75,000 is `75000`), as elsewhere in Outflare.

Each amount is shown twice, in plain words: as on the invoice (`£500.00`) and in US dollars (`$635.40`), with the rate line `1 GBP = 1.2708 USD, ECB reference rate on 10 Nov 2026`. The quote and the frozen amount (`quoted_invoice` and `invoice` on an order) each store the original amount and currency, the invoice date, the US-dollar equivalent, the rate (US dollars for one unit), the rate date and where the rate came from:

- `usd` — the invoice is in US dollars; no conversion, rate 1.
- `ecb` — every other currency, EUR included: the European Central Bank reference rate Outflare stores (the `exchange_rates` table, fetched by the server from the ECB's public feed) for the invoice date, or the last working day before it within ten days (weekends and holidays have no rate). It is always labelled "ECB reference rate".

Nobody types a rate: the API takes no rate in a quote or Mark Paid, and the forms have no rate field. When there is no ECB rate for the invoice date or the ten days before it, the quote is not saved (400) and says so. The ECB publishes about 30 currencies (not UAH, for one) and the stored feed covers about the last 90 days, so a quote in an unpublished currency or with an old invoice date is refused.

The invoice date defaults to today and cannot be in the future. Mark Paid keeps the frozen amount and can take the final invoice's date; the US-dollar equivalent is then worked out again at the ECB rate for that date (400 when there is none). The Coverage invoice and payment rows are written in the invoice currency (issued on the invoice date), with the US-dollar equivalent, rate, rate date and source beside the payment.

Budgets and totals keep their USD and EUR rows: `quoted_amount_minor` / `quoted_currency` and `donor_amount_minor` / `currency` hold the amount itself for USD and EUR, and the US-dollar equivalent in USD for any other currency, so a pound invoice counts against the USD budget at its dollar value. Orders from before this change read as they were (a USD row has rate 1; a EUR row has no US-dollar equivalent until Mark Paid finds an ECB rate, and is paid in euros as before if there is none). This is accounting only: nothing here sends money.

## Live checks

`coverage.check` already writes placement observations. After it finishes, matching published, paid or verified orders get `last_check_at`, `last_check_live`, `last_check_target_match`, `last_check_rel` and `last_check_anchor`. Checks also stay scheduled, about every seven days, while the order is in those statuses and the guarantee has not expired.

## Impact

`GET .../link-orders/{id}/impact` reads already-saved Search Console and ranking points around `published_at`. It does not call a connector, and it does not claim that the link caused a change.

## Budget

The budget is a Coverage budget with tag `link_orders`, one row per currency (`POST/PATCH /v1/brands/{brand}/coverage/budgets`). Committed is the donor amount of orders in `approved` or `published` that are not yet `paid`, in that currency. Spent is only `paid` (and not credited). USD and EUR stay in their own rows.

## Query

With `coverage.read`, Query exposes two tables ([query.md](query.md)):

- `coverage_donor_profiles` — the catalog, including the publisher's domain, the PBN flag, niches and stored DR and traffic. Notes, the intermediary, theme, site age and the intermediary's metrics are not columns yet; read them through the API.
- `coverage_link_orders` — each order's status, donor price (minor units and currency; the US-dollar equivalent for an invoice in another currency), live-check result and Coverage placement and payment ids. Notes, `lost_reason` and the invoice-currency fields are not columns yet; read them through the API.

```sql
select currency, status, count(*), sum(donor_amount_minor)
from coverage_link_orders
group by 1, 2
```

## API and MCP

Under `/v1/brands/{brand}/link-orders`:

- Catalog: `GET/POST /catalog` (`direct_contact=true` for Direct contact sites), `GET/PATCH /catalog/items/{id}`, `POST /catalog/import`, `GET /catalog/items/{id}/contacts` (stored email addresses), `POST /catalog/items/{id}/outreach` (add an address to a sequence)
- Candidates: `GET /candidates`, `POST /candidates/{domain}/add-to-catalog`
- Targets: `GET/POST /targets`, `PATCH /targets/{id}`, `GET /targets/suggestions`, `GET /targets/anchors`
- Settings: `GET/PATCH /settings` (approver and equivalent DR band)
- Summary and budget: `GET /summary`, `GET /budget`
- Orders: `GET/POST /`, `GET /{id}`, then `/quote`, `/negotiate`, `/approve`, `/publish`, `/check`, `/pay`, `/verify`, `/lost`, `/replace`, `/credit`, `/equivalents`, `/impact`, `/history`

MCP tools for agents: `list_link_order_catalog`, `import_link_order_catalog`, `list_link_orders`, `get_link_order`, `create_link_order`, `list_link_order_candidates`, `add_link_order_candidate_to_catalog`, `get_link_order_equivalents`, `get_link_order_summary`, `list_link_order_targets`, `suggest_link_order_targets`, `get_link_order_target_anchors`, `find_link_order_donor_emails`, `send_link_order_donor_to_sequence`. There are no tools to approve or pay.

Webhooks (category `link_orders`), each from the audit log: `link_order.approved`, `link_order.published`, `link_order.checked`, `link_order.paid`, `link_order.verified`, `link_order.lost`, `link_order.replacement_opened`.

## What this version does not do

- Import a Google Sheet.
- Look an email up on the web or with a paid provider from Find email: it shows stored addresses only.
- Write to a Direct contact site on its own: the mark is a suggestion, and nothing is enrolled until someone sends an address to a sequence.
- Show the Outreach thread, agreed price or targets next to an order.
- Call a paid Ahrefs or DataForSEO pull from the page, the candidates list, or impact.
- Auto-detect a PBN.
- Sum USD and EUR, or show a commission or client price.
- Use any rate but the ECB reference rate, or take a rate typed in by a person or sent by a client.
- Send money when Paid or a credit is recorded.
- Use SE Ranking as a source of referring-domain candidates.
- Order links automatically from suggestions, or check that the live link uses the ordered anchor.
