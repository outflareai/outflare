# Link orders

Link orders is how a brand buys paid placements from sites in its own donor catalog. The page lives in the SEO module, immediately after Backlinks. Amounts are the **donor price only**: what is paid to the site, with no commission or client markup. USD and EUR are never added together. An invoice can be in any common currency (GBP, PLN, CHF and others, see [Invoice currencies](#invoice-currencies)); it is kept as invoiced and shown beside its US-dollar equivalent. Recording Paid is a Coverage payment row; it never sends money.

The work sits on Coverage, not on a new money system: each catalog domain is a Coverage publisher with a donor profile beside it, a published order becomes a paid placement, and the budget is a Coverage budget tagged `link_orders`. Donor attributes (PBN, niche, casino, rel offered) stay on the profile, so Presence publishers and rates do not grow those fields. See [coverage.md](coverage.md).

Scopes follow Coverage: `coverage.read` to look, `coverage.manage` to edit the catalog and move an order, `coverage.approve` plus being the brand's designated approver to Approve or mark Paid, and `coverage.finance.manage` to record Paid or a credit.

## Catalog

The catalog is the list of sites the brand is willing to buy from (`GET /v1/brands/{brand}/link-orders/catalog`, MCP `list_link_order_catalog`). Add one site at a time, or import a **CSV file**, an **Excel workbook** (.xlsx) or a **Google Sheet link**. The first row is the header and must include a domain column (`domain`, `website`, `site` or `url`). A repeated domain updates the existing profile; a bad row is listed in `errors` and does not abort the rest of the file (`POST .../catalog/import`, MCP `import_link_order_catalog`). Header columns the import does not read are listed in `ignored_columns`.

Each site has three fields of its own, kept out of notes and niches:

- **Intermediary**: the broker or contact the site is bought through. The Catalog search (and the API's `q`) matches it as well as the site and theme, and the **Intermediary** filter (`intermediary=` in the API) lists every site from one intermediary.
- **Theme**: the site's own theme. **Niches** stay a separate list.
- **Site age**: how long the domain has existed. A file or form may say `2012`, `2012-05-01` or `12 years` (also `8 months`); it is stored as the date the site exists since (`site_since`, 1 January for a bare year) and shown as "Since 2012 · 14 years" (`site_age_years`).

Add and edit them on the Catalog page (Add donor, or the donor card's **Edit**), through `PATCH .../catalog/items/{id}` (`intermediary`, `theme`, `site_age`), or in the CSV.

### Recommended donors

Under the link target, **Recommended donors** ranks the catalog for that page, from 0 to 100, and says why. It weighs the topic (the categories you prefer, starting from the categories of donors already ordered and of catalog sites that link to competitors), the page's language (from a locale path such as `/fr/`, else English), DR, organic traffic, price per 1,000 visits, and whether the site links to the competitors that rank for the page (the saved competitor gap). A site labelled **Best link** gets a bonus. It subtracts for link farms (over 1,000 followed outgoing domains) and for donors already ordered for the brand, and leaves out PBNs and sites with the **Likely PBN?** signs, unpriced sites, gambling and adult sites unless that topic is chosen, and donors already ordered for this page. It follows the catalog filters, so a language, price or category filter narrows the picks. It only reads stored data, never a paid provider, and orders nothing: a ticked pick is in the request for **Request quotes**.

**Show** sets how many picks the list holds: 10, 25, 50, 100 or 200 (remembered per browser and brand); with no page chosen they are shared between the top suggested pages. Each pick has a checkbox (a ticked pick is in the request, like a ticked catalog row) and the list has **Select all**. Beside the domain are the two numbers to read first: the **light** (see [The green light](#the-green-light)) and the share of the site's search traffic on its **top page**.

A site where someone is already enrolled in one of the brand's sequences leaves the picks and is listed under **Sent to a sequence**, with the address, the sequence and the enrollment's status (a draft or paused sequence shows as not sending). Each catalog row carries this as `outreach` (`sequence_id`, `sequence_name`, `sequence_status`, `status`, `email`, `enrolled_at`), computed on read: the latest enrollment of a brand lead whose address is at the site's domain or a subdomain, or whose company is the site (Find email files a free-mail address that way), in a sequence of the brand that is not archived. Stopped enrollments don't count, so stopping someone, or archiving the sequence, brings the site back.

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

**Excel and Google Sheets.** On the Catalog page, Import donors takes a CSV or .xlsx file, or a Google Sheet link. A workbook or a sheet is first read into CSV by `POST .../catalog/table` (MCP `read_link_order_catalog_table`), which writes nothing, and that CSV is then imported in batches like a file, with the same column matching and the intermediary for every row. A workbook reads its first tab (or the one named in `sheet`): cell text and cached values, so a formula gives its result and a date arrives as an Excel serial number. A Google Sheet is read as its tab's CSV export with no credentials and no Google connection, so share it as **Anyone with the link** (Viewer) or **Publish to web**; a private sheet is refused with a message saying so. The tab is the one in the link (`#gid=`), else the first. Only docs.google.com and Google's download hosts are reached, over public addresses. Blank rows are dropped, and a sheet may have up to 50,000 rows.

**Export** (Export CSV on the Catalog page) writes the intermediary, theme, site age (`site_since`), DR, traffic, price and every metric under column names the import reads back unchanged.

PBN is a **manual flag**: nothing flags or hides a site on its own. The catalog filter defaults to excluding flagged rows.

**Likely PBN?** The catalog points out sites whose stored metrics look like a PBN, as a suggestion to review (a red badge, with the reasons in its tooltip and on the donor card). The core sign is authority without an audience: DR 30 or more with under 1,000 organic visits, or with under 100 ranking keywords (a lot of traffic from so few keywords is a second warning). Signs of a link network add to it: few referring subnets for the referring domains (under a quarter), thousands of followed outgoing domains, and over 200 backlinks per referring domain. A site is "likely" with a core sign and enough weight; a big news site that only links out a lot is not. More filters → PBN signs → Likely PBN (to review) lists them, and Flag as PBN (right-click a cell, or on the donor card) sets the flag. It reads stored metrics only.

**Best link** is a manual label for the sites the brand most wants links from (`best_link` on the donor; a `best_link` or `Best link` CSV column sets it). Right-click a catalog cell, or open the donor, to mark or remove it. With no sort chosen the catalog shows best links first, and More filters → Best link → Best links only keeps just them. Nothing sets it automatically.

Domain rating and traffic on a catalog row come from the latest stored SEO snapshot, never from a fresh paid provider read.

### Traffic and links: which sites to skip

Every catalog row has a `quality` verdict, by the link-building team's base minimum:

- **Skip** a site under DR 30 or under 5,000 organic visits a month; a site with more than 45% of its search traffic on its top page or on its home page, or 80% or more on its three top pages (all of it on one to three pages, often a doorway or one viral post); and a site that links out to more than 0.3 domains for every domain that links to it.
- **Check** a site whose top page has 35% to 45%: close to the limit.
- **RD/LD** (referring ÷ linked domains) is graded beside it: under 1 a link farm, 1 to 2 weak, 2 to 5 normal, 5 or more premium.

Recommended donors never pick a site to skip, rank a site to check lower and a premium link profile higher. The catalog hides sites to skip by default (*Traffic and links*), and a site's card shows the numbers and reasons.

The link ratio works from the intermediary's file (its followed outgoing domains). Where the traffic sits needs an **Ahrefs check** (`POST .../catalog/ahrefs-check`, MCP `check_link_order_donors_in_ahrefs`, or *Check in Ahrefs* under Recommended donors) on the brand's own Ahrefs connection. It reads only five numbers, to keep it cheap: Domain Rating, worldwide search traffic, live referring and linked domains, and the three pages with the most search traffic (about 250 API units a site, within the connection's monthly budget; a site checked successfully in the last 30 days is not checked again, its stored numbers are used). They replace the row's DR and traffic and go in its metrics (`linked_domains_live`, `top_page_url`, `top_page_traffic`, `top3_traffic`, `home_traffic`). A file with these columns (such as an export with `top_page_url` and `top_page_traffic`) fills them too.

### Finding emails and sending picks to a sequence

**Find emails and send to a sequence** under Recommended donors takes the ticked picks, has OutflareBot search each site for who to write to, and adds the best address of each to the Outreach sequence you choose.

The draft a new sequence starts from never names the brand, so the site does not price the link by who is buying. The dialog opens on the sequence to choose (**1. Choose the sequence to send from**): its steps are the emails the donors get. It only runs on ticked picks, any number of them: the search goes to the API 50 sites a request, and the dialog waits on a few sites at a time. When it closes, the page reloads, so the sites just added move to Sent to a sequence.

- **The search** (`POST .../catalog/find-emails`, MCP `search_link_order_donor_emails`, up to 50 rows) reads the site's home page and up to five of its contact, advertising, imprint, write-for-us and about pages, in English and the languages donor lists are in (German, Slovenian, Croatian, Spanish, Italian, French, Turkish, Portuguese, Dutch, Hungarian and more). It reads mailto links and addresses written in the text, also those hidden with "[at]" and "(dot)".
- **What it keeps.** Up to three addresses at the site's own domain (or a free-mail address the site lists, ranked lower), best first: the advertising desk (werbung@, ads@, advertising@, marketing@, sales@), then the editors (redaktion@, editor@, press@), then a general mailbox (contact@, info@). Mailboxes that must never get a placement pitch are dropped: data protection (datenschutz@, privacy@, dpo@), abuse, legal, copyright, jobs, billing, newsletter and no-reply. They become person records on the site's company, so they show under Find email too.
- **Each row's `email_search`** says how the last search ended: `found`, `none`, `blocked` (robots.txt), `opted_out`, `unreachable` or `failed`. It obeys robots.txt and crawl opt-outs, costs nothing, and runs at most once a day per row.
- **The sequence.** The dialog preselects the sequence used last time for donors, else one named for link building, else an active one. *+ New link-building sequence* makes one right there, as a draft, with an editable first email and a follow-up four days later in the same thread; it sends nothing until a mailbox is connected and it is started in Outreach.
- **Sending.** The best address joins the sequence through the donor's outreach (`POST .../catalog/items/{id}/outreach`), with Outreach's usual rules: suppressed, excluded and already-enrolled addresses are skipped, and the sequence sends from its own mailboxes on its own schedule. When a site lists nothing, an address already stored for it is used.

### One catalog for every brand

Each brand has its own catalog (brands own their data), but a broker's database is the same for all of them, so it is kept once and copied:

- **Import into every brand.** Import donors has *Import into every brand*, on by default when the workspace has more than one brand: each batch of the file goes to every active brand.
- **Copy an existing catalog.** *One catalog for every brand* on the Catalog tab copies this brand's catalog to every other brand, or copies another brand's catalog here. `POST /v1/brands/{brand}/link-orders/catalog/copy` (MCP `copy_link_order_catalog`) writes up to 500 of `from_brand`'s live donors per call into `{brand}`; send `next_cursor` back as `cursor` until it is null.

A copy carries the site, prices, metrics, niches, intermediary, theme, site age, notes and the PBN and Best link flags. Archived donors are not copied. Donors upsert by domain, so copying again updates and never doubles. Each brand's publishers, orders, outreach and history stay its own, and nothing reads across brands afterwards.

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

- **Near the top (SE Ranking).** A keyword SE Ranking (its stored snapshots and the keywords its projects track) has the brand's page at positions 4 to 20 for is credited to that page: a link most likely moves it onto the first page or into the top 3. Positions 4 to 10 weigh 1.5 times more, and the weight grows with search volume. When two pages rank for the keyword, only the better one is credited.
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

### The green light

Before money is committed and before a link is recorded, the order shows the donor's **light**: DR, traffic, the share on the top page, the top three and RD/LD, and when Ahrefs last checked it.

- **Green light**: the verdict is OK (see [Traffic and links](#traffic-and-links-which-sites-to-skip)) and Ahrefs checked the site in the last 30 days.
- **Check** or **Re-check** (amber): the top page holds 35% to 45%, or the Ahrefs check is missing or older than 30 days.
- **Skip** (red): below the base minimum.
- **Not checked** (grey): nothing is known about where its traffic sits.

Without a green light, **Approve** and **Mark published** stay disabled until the site is checked (**Check in Ahrefs now**, on the brand's own Ahrefs) or the person ticks **Go ahead without a green light**. A site checked successfully in the last 30 days is never pulled again: its stored numbers are used, the button isn't offered, and the API skips it (counted in `fresh`), so only an older, failed or missing check spends Ahrefs units. The gate is in the web app; the API does not enforce it.

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

### The conversation with the donor

An order shows the brand's emails with its donor under **Conversation** (`GET .../link-orders/{id}/thread`, MCP `get_link_order_thread`): Outreach sends to, and Inbox mail from, an address at the site's domain (or a subdomain) or one of the donor's stored contacts, such as an intermediary's address in its notes. Newest first, at most 100, with each reply's Inbox label and the prices written in it (`$150`, `120 EUR`) to help fill in the quote; those prices are read from the text, not confirmed. Only this brand's mail is read, nothing is sent, and replies are written from the Inbox. It needs `inbox.read` as well as `coverage.read`.

### Recording a link bought elsewhere

**Record a bought link** on the Orders tab is one form for a link that was already bought outside Outflare: the published article, the page it links to, the anchors, the price and its currency, the invoice date (for the exchange rate) and, optionally, the intermediary and proof. It runs the normal steps with the person's own permissions: it adds the site to the catalog if it is missing, opens the order with a manual target, saves the quote, approves it when the person is the brand's designated approver, and records the publication, which queues a live-link check. For anyone else the order waits at Quoted for the approver. Paid is still marked on the order after the check passes, and nothing sends money.

**Past orders from CSV** does the same for many links, one row at a time: `target_url`, `published_url` and `price` are needed; `domain` (else taken from the article), `intermediary`, `anchor`, `anchor_secondary`, `currency` (default USD, or from a `Price (EUR)` header), `invoice_date`, `proof_url` and `notes` are optional. A bad row is listed with the step it stopped at, and the rest go on.

## Live checks

`coverage.check` already writes placement observations. After it finishes, matching published, paid or verified orders get `last_check_at`, `last_check_live`, `last_check_target_match`, `last_check_rel`, `last_check_anchor` and `last_check_anchor_match`. The anchor is compared with the ordered primary and second anchors, ignoring case, spacing and punctuation: `exact`, `partial` (one contains the other, or most of the ordered words are there) or `different`; empty when no anchor was ordered or no link to the target was found. When the page links to the target more than once, the link closest to an ordered anchor is judged. A different anchor is a warning on the Checks tab only: it never blocks Paid or marks the order lost. Checks also stay scheduled, about every seven days, while the order is in those statuses and the guarantee has not expired.

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

- Catalog: `GET/POST /catalog` (`direct_contact=true` for Direct contact sites), `GET/PATCH /catalog/items/{id}`, `POST /catalog/import`, `POST /catalog/table` (a Google Sheet link or an .xlsx workbook as CSV), `GET /catalog/items/{id}/contacts` (stored email addresses), `POST /catalog/items/{id}/outreach` (add an address to a sequence)
- Candidates: `GET /candidates`, `POST /candidates/{domain}/add-to-catalog`
- Targets: `GET/POST /targets`, `PATCH /targets/{id}`, `GET /targets/suggestions`, `GET /targets/anchors`
- Settings: `GET/PATCH /settings` (approver and equivalent DR band)
- Summary and budget: `GET /summary`, `GET /budget`
- Orders: `GET/POST /`, `GET /{id}`, then `/quote`, `/negotiate`, `/approve`, `/publish`, `/check`, `/pay`, `/verify`, `/lost`, `/replace`, `/credit`, `/equivalents`, `/impact`, `/history`

MCP tools for agents: `list_link_order_catalog`, `read_link_order_catalog_table`, `import_link_order_catalog`, `list_link_orders`, `get_link_order`, `create_link_order`, `list_link_order_candidates`, `add_link_order_candidate_to_catalog`, `get_link_order_equivalents`, `get_link_order_summary`, `list_link_order_targets`, `suggest_link_order_targets`, `get_link_order_target_anchors`, `find_link_order_donor_emails`, `send_link_order_donor_to_sequence`. There are no tools to approve or pay.

Webhooks (category `link_orders`), each from the audit log: `link_order.approved`, `link_order.published`, `link_order.checked`, `link_order.paid`, `link_order.verified`, `link_order.lost`, `link_order.replacement_opened`.

## What this version does not do

- Look an email up on the web or with a paid provider from Find email: it shows stored addresses only.
- Write to a Direct contact site on its own: the mark is a suggestion, and nothing is enrolled until someone sends an address to a sequence.
- Write an email from the order: replies are written from the Inbox.
- Call a paid Ahrefs or DataForSEO pull from the page, the candidates list, or impact.
- Flag a PBN on its own: Likely PBN? is only a suggestion.
- Sum USD and EUR, or show a commission or client price.
- Use any rate but the ECB reference rate, or take a rate typed in by a person or sent by a client.
- Send money when Paid or a credit is recorded.
- Use SE Ranking as a source of referring-domain candidates.
- Order links automatically from suggestions.
