# Website visitors

**Visitors** (in the sidebar after CRM) shows which companies and people came to your website, what they looked at and who is ready to talk. It is built on [website tracking](website-tracking.md): add a site key and the snippet first.

## How visitors are named

Outflare only names a visitor it can tie to someone already in your workspace. It never buys or guesses identities.

- **Your product.** When your product sends `identify` for a visitor (see [website tracking](website-tracking.md)), their earlier and later visits join their record.
- **Email clicks.** When someone clicks a tracked link in a sequence or lifecycle email that points at your own site, the link carries a short signed token (`ofvisit`). The snippet sends it with the first page view and removes it from the address bar, and that browser's visits join the person the email went to.

The email-click token is safe to sit in a URL:

- The server signs it, so a browser can't make one up, and a site key still can't name or merge anyone (the token only works because the server made it for one click).
- It names one browser: the first browser to use it. The same link forwarded to a colleague, or opened on another device, names nobody.
- It is weaker than a sign-in: if someone the email was forwarded to clicks it and later signs in to your product as themselves, their browser and its visits move to their own record.
- Until the snippet removes it, the token is in the landing page's address, so your own server logs or other analytics on the page may record it. It only works with your workspace's site key, for seven days, and only until the first browser claims it.
- It lasts seven days, mail scanners and link checkers never get one, and it is never stored in your events.
- It is only added to links to your own site: a brand's domain or website (or a subdomain), or an origin on one of your site keys, and only while the workspace has an active site key.

Turn it off with **Know people from email clicks** in the page's settings (`email_clicks: false`).

## Company lookups

Visitors nobody knows yet can be matched to a company from their IP address, with the workspace's own data provider: People Data Labs (IP Enrichment) or Snitcher (IP to Company). It is off until you turn on **Look up companies from visitors' addresses** in the page's settings (`reveal_companies`), and it needs one of those connections. Outflare never looks anything up on its own account.

- Only visitors who accepted your site's cookies are looked up, each browser at most once a day and each address at most once a week, up to **Lookups a day at most** (`reveal_daily_limit`, 500 by default). Lookups spend your provider's credits.
- The address goes to the provider in a background job and is never stored. Lookups are kept by a keyed hash of the address, with the company's domain and name.
- A home, phone, VPN or data-centre connection names nobody. Those visitors are counted as on such a network (`no_company` in the totals).
- Lookups are often wrong, so they never add anything to your CRM. A company the CRM already has at that domain is linked straight away and can turn hot. Any other company is listed as **Not in CRM** until someone clicks **Add to CRM** (`POST /v1/workspaces/{ws}/visitors/companies`, MCP `add_visitor_company`). It is then added as a prospect from the provider (`acquired_via` `snitcher` or `peopledatalabs`), with its domain marked as from a lookup (`source` `visitor_reveal`, unverified), so it is never treated as opted in.
- **Not a company (ISP)** (`not_companies`) is for an internet provider or carrier mistaken for its customers. Its visitors lose it, it is never named again, and a company record that only lookups put in the CRM is archived.
- A company found for visitors who don't come back within 90 days, and that nobody added, is dropped.
- When a site sends its events through its own domain, Outflare sees that server's address rather than the visitor's, so lookups name nobody for it unless the proxy passes the visitor's address on with the key's proxy secret ([website tracking](website-tracking.md)).

## People by name

RB2B, Vector and Warmly name the people on a website. With your own account at one of them, Outflare can take in the people it names. Outflare never identifies anyone itself and keeps no identity graph: the tool decides, on your account, and pushes each person to a webhook.

1. Connect the tool under Connectors (it asks for nothing).
2. In the Visitors settings, turn on **Take in the people your tool names** (`reveal_people`) and accept the notice. These tools name people in the United States only; you say so in your site's privacy policy, honour opt-outs (Global Privacy Control included) and check the rules where you sell. Never use it for visitors in the EU, UK, Canada or Australia.
3. Copy the connection's webhook address from the settings (`people_connections[].webhook_url`) into the tool's webhook settings. Keep it private: anyone with it can add people to your review list. Removing the connection turns the address off.

What happens to each person the tool sends:

- Someone whose email address the CRM already has is left as they were. The sighting is in their history (`visitor.person_revealed`).
- Anyone else becomes a person record: a prospect tagged with the tool (`acquired_via` `rb2b`, `vector` or `warmly`), with their name, job title and LinkedIn profile. People without an email address, and people at a domain you leave out, are skipped.
- New people are **held for review** (`needs_review`): they are left out of the people list (`GET .../records` lists them only with `review=needs_review`), and the send policy blocks every email to them (reason `needs_review`) until someone approves them.
- The Visitors page lists them under **People to review**. **Approve** (`POST /v1/workspaces/{ws}/records/{record}/review` with `decision` `approve`, MCP `review_record`) lists them like anyone else. They stay third-party prospects: cold email only, never marketing email without their own consent ([opt-outs](opt-outs.md)). **Reject** archives the record.
- With the setting off, the webhook answers `ignored: off` and keeps nothing.

## Companies and people

The page lists companies (people grouped by their company in the CRM, or by their email's domain when that matches a company record) or people, for the last 1 to 365 days, with pages viewed, key pages viewed, visits, the last page and a score. Totals show how many browsers came, how many were known, and how many people, companies and hot visitors there were. Open a person to see their recent pages.

## Scores and hot visitors

Each person and company gets a score from 0 to 100: more key pages, more return visits, more pages and (for a company) more people from it score higher, and the score fades after 7, 14 and 30 days without a visit. Key pages are paths that show buying intent; the defaults are pricing, plans, demo, contact, book, compare, vs, alternatives, enterprise, sign-up and trial pages. Set your own as paths, where a trailing `*` matches everything under it (`/pricing*`).

When a person or company reaches the hot score (60 by default), Outflare records `visitor.became_hot`, at most once a week each. It is a [webhook](api-conventions.md) event and in the audit log, raises a `visitor.hot` [alert](alerts.md), and hot visitors are in the [digest](digests.md).

## Leave people out

Set **Leave out these domains** to your own company's domains so your team doesn't show up, and **Leave out customers** to hide companies and people who are already customers.

## API and MCP

- `GET /v1/workspaces/{ws}/visitors` (`customers.read`): `view` (`companies` or `people`), `days`, `hot`, `limit`, `brand`.
- `GET` and `PUT /v1/workspaces/{ws}/visitors/settings` (`customers.read`, `customers.manage`). A change is in the audit log as `visitor_settings.updated`.
- `POST /v1/workspaces/{ws}/visitors/companies` (`customers.manage`): add a looked-up company to the CRM.
- `POST /v1/visitor-hooks/{id}/{token}` (public, the address is the authority): where RB2B, Vector or Warmly send people. One person or up to 100 per call; field names as the tool sends them.
- `POST /v1/workspaces/{ws}/records/{record}/review` (`customers.manage`): approve or reject a record held for review.
- MCP: `list_website_visitors`, `get_visitor_settings`, `update_visitor_settings`, `add_visitor_company`, `review_record`.
