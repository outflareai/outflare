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

## Companies and people

The page lists companies (people grouped by their company in the CRM, or by their email's domain when that matches a company record) or people, for the last 1 to 365 days, with pages viewed, key pages viewed, visits, the last page and a score. Totals show how many browsers came, how many were known, and how many people, companies and hot visitors there were. Open a person to see their recent pages.

## Scores and hot visitors

Each person and company gets a score from 0 to 100: more key pages, more return visits, more pages and (for a company) more people from it score higher, and the score fades after 7, 14 and 30 days without a visit. Key pages are paths that show buying intent; the defaults are pricing, plans, demo, contact, book, compare, vs, alternatives, enterprise, sign-up and trial pages. Set your own as paths, where a trailing `*` matches everything under it (`/pricing*`).

When a person or company reaches the hot score (60 by default), Outflare records `visitor.became_hot`, at most once a week each. It is a [webhook](api-conventions.md) event and in the audit log, and hot visitors are in the [digest](digests.md).

## Leave people out

Set **Leave out these domains** to your own company's domains so your team doesn't show up, and **Leave out customers** to hide companies and people who are already customers.

## API and MCP

- `GET /v1/workspaces/{ws}/visitors` (`customers.read`): `view` (`companies` or `people`), `days`, `hot`, `limit`, `brand`.
- `GET` and `PUT /v1/workspaces/{ws}/visitors/settings` (`customers.read`, `customers.manage`). A change is in the audit log as `visitor_settings.updated`.
- MCP: `list_website_visitors`, `get_visitor_settings`, `update_visitor_settings`.
