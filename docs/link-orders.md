# Link orders

Link orders is how a brand buys paid placements from sites in its own donor catalog. The page lives in the SEO module, immediately after Backlinks. Amounts are the **donor price only**: what is paid to the site, with no commission or client markup. USD and EUR are never added together. Recording Paid is a Coverage payment row; it never sends money.

The work sits on Coverage, not on a new money system: each catalog domain is a Coverage publisher with a donor profile beside it, a published order becomes a paid placement, and the budget is a Coverage budget tagged `link_orders`. Donor attributes (PBN, niche, casino, rel offered) stay on the profile, so Presence publishers and rates do not grow those fields. See [coverage.md](coverage.md).

Scopes follow Coverage: `coverage.read` to look, `coverage.manage` to edit the catalog and move an order, `coverage.approve` plus being the brand's designated approver to Approve or mark Paid, and `coverage.finance.manage` to record Paid or a credit.

## Catalog

The catalog is the list of sites the brand is willing to buy from (`GET /v1/brands/{brand}/link-orders/catalog`, MCP `list_link_order_catalog`). Add one site at a time, or import a **CSV file**. A Google Sheet is not imported, and there is no Sheet URL field. The first row of the CSV is the header and must include a `domain` column. A repeated domain updates the existing profile; a bad row is listed in `errors` and does not abort the rest of the file (`POST .../catalog/import`, MCP `import_link_order_catalog`). Amounts in the file are the donor price, in minor units.

PBN is a **manual flag**. Nothing in this version detects or hides a PBN automatically. The catalog filter defaults to excluding PBN rows.

Domain rating and traffic on a catalog row come from the latest stored SEO snapshot, never from a fresh paid provider read.

## Candidates

Candidates are referring domains from the **already-saved** competitor [link gap](seo.md#gaps): sites that link to tracked competitors and not to this brand (`GET /v1/brands/{brand}/link-orders/candidates`, MCP `list_link_order_candidates`). Each row is `not_in_catalog`, `in_catalog` or `ordered`. Traffic is not stored on the gap, so it is always empty (the page shows an em dash).

This list does **not** call Ahrefs or DataForSEO, and it does not `POST /seo-competitors/gaps/links`. That route is a paid referring-domain pull; Link orders never starts it. Listing candidates does not spend the SEO connection budget. The weekly snapshot that stores the gap is already priced like the keywords and links reports, and counts against that budget.

A connection that cannot produce a backlinks snapshot cannot fill this list. **SE Ranking** has no `backlinks` or `links` report, so it does not feed referring domains, the link gap, or Link orders candidates. Use DataForSEO or Ahrefs for that weekly read.

`POST .../candidates/{domain}/add-to-catalog` (MCP `add_link_order_candidate_to_catalog`) find-or-creates a catalog profile with `source=competitor_gap` for a domain that is already in the saved gap. A domain that is not in the gap is refused.

## Orders

An order is a request to buy a link on one catalog donor, to one target URL, with a status of its own (`requested`, `quoted`, `negotiating`, `approved`, `published`, `paid`, `verified`, `lost`). The machine is not Coverage's placement stage.

- **Approve** is reserved for the brand's designated approver (`coverage_link_order_settings.approver_user_id`, who also needs `coverage.approve`). Until an approver is set, Approve and Paid stay blocked. MCP has no tools to approve or pay.
- **Publish** records a Coverage placement with `arrangement=paid` (uniqueness is URL plus inventory) and queues a live-link check.
- **Paid** is the same approver, plus `coverage.finance.manage`, after the order is published **and** a live-link check has passed (the link is on an up page and the target matches). Spent is booked then, not when the order is approved.
- **Verified** is a later confirmation that the link is still in place. Indexed-on-a-third-party-URL is out of this version and does not block it.
- **Lost** is a person marking it lost, or two consecutive conclusive live or target failures. A network error is not conclusive, and a single failure is not enough.

A lost order can be replaced once, by opening a new order with `replaces_order_id`. There is one open replacement at a time, and replacements cannot form a cycle. `GET .../link-orders/{id}/equivalents` (MCP `get_link_order_equivalents`) suggests catalog donors in the same domain-rating band and a niche in common.

## Live checks

`coverage.check` already writes placement observations. After it finishes, matching published, paid or verified orders get `last_check_at`, `last_check_live`, `last_check_target_match`, `last_check_rel` and `last_check_anchor`. Checks also stay scheduled, about every seven days, while the order is in those statuses and the guarantee has not expired.

## Impact

`GET .../link-orders/{id}/impact` reads already-saved Search Console and ranking points around `published_at`. It does not call a connector, and it does not claim that the link caused a change.

## Budget

The budget is a Coverage budget with tag `link_orders`, one row per currency (`POST/PATCH /v1/brands/{brand}/coverage/budgets`). Committed is the donor amount of orders in `approved` or `published` that are not yet `paid`, in that currency. Spent is only `paid` (and not credited). USD and EUR stay in their own rows.

## Query

With `coverage.read`, Query exposes two tables ([query.md](query.md)):

- `coverage_donor_profiles` — the catalog, including the publisher's domain, the PBN flag, niches and stored metrics. Notes are not a column.
- `coverage_link_orders` — each order's status, donor price (minor units and currency), live-check result and Coverage placement and payment ids. Notes and `lost_reason` are not columns.

```sql
select currency, status, count(*), sum(donor_amount_minor)
from coverage_link_orders
group by 1, 2
```

## API and MCP

Under `/v1/brands/{brand}/link-orders`:

- Catalog: `GET/POST /catalog`, `GET/PATCH /catalog/items/{id}`, `POST /catalog/import`
- Candidates: `GET /candidates`, `POST /candidates/{domain}/add-to-catalog`
- Settings: `GET/PATCH /settings` (approver and equivalent DR band)
- Summary and budget: `GET /summary`, `GET /budget`
- Orders: `GET/POST /`, `GET /{id}`, then `/quote`, `/negotiate`, `/approve`, `/publish`, `/check`, `/pay`, `/verify`, `/lost`, `/replace`, `/credit`, `/equivalents`, `/impact`, `/history`

MCP tools for agents: `list_link_order_catalog`, `import_link_order_catalog`, `list_link_orders`, `get_link_order`, `create_link_order`, `list_link_order_candidates`, `add_link_order_candidate_to_catalog`, `get_link_order_equivalents`, `get_link_order_summary`. There are no tools to approve or pay.

Webhooks (category `link_orders`), each from the audit log: `link_order.approved`, `link_order.published`, `link_order.checked`, `link_order.paid`, `link_order.verified`, `link_order.lost`, `link_order.replacement_opened`.

## What this version does not do

- Import a Google Sheet.
- Call a paid Ahrefs or DataForSEO pull from the page, the candidates list, or impact.
- Auto-detect a PBN.
- Sum USD and EUR, or show a commission or client price.
- Send money when Paid or a credit is recorded.
- Use SE Ranking as a source of referring-domain candidates.
