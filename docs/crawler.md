# Site audits and OutflareBot

Outflare crawls your own websites to audit them, with its own crawler, OutflareBot. This page covers adding a site, proving you own it, crawling it and the audit each crawl ends with: its issues, grouped by URL template, and a health score, and the fixes list the audits keep for people and agents.

Nothing here needs a connector or an API key. SEO tools (DataForSEO, Ahrefs; see [seo.md](seo.md)) add data from outside your site, and are separate.

## OutflareBot

OutflareBot identifies itself on every request:

```
Mozilla/5.0 (compatible; OutflareBot/1.0; +https://outflare.ai/bot/)
```

[outflare.ai/bot](https://outflare.ai/bot/) explains it to site owners and lists the addresses Outflare Cloud crawls from. What it promises, and what `engine/internal/crawl` does:

- **Obeys robots.txt** (RFC 9309): the rules for `OutflareBot`, or for `*` when there are none for it. A robots.txt that answers 4xx allows everything; one that answers 5xx or not at all allows nothing, and the crawl ends with reason `robots_unreachable`. It is read again when a crawl has had it for a day.
- **Goes slowly:** one request at a time per site, across every workspace on the server, with the site's pause between requests, or robots.txt's `Crawl-delay` when that is longer (capped at 30 seconds).
- **Backs off:** a `429` or `503` waits for `Retry-After` (up to two minutes), then doubles the pause for the rest of the crawl; an answer slower than five seconds doubles it too. Ten errors in a row end the crawl (`site_errors`).
- **Stays on public pages:** it follows links and sitemaps on the site's own host (and its `www.` twin), never signs in or fills in forms, does not follow the links of a page marked `nofollow` (in a meta tag or `X-Robots-Tag`) or links marked `rel="nofollow"`, `ugc` or `sponsored`, and reads at most 5 MB of a page.
- **Never reaches your network:** it refuses private, loopback and link-local addresses, checked on the address it actually dials, and only uses the default ports. Set `OUTFLARE_CRAWL_ALLOW_PRIVATE=true` on a self-hosted server to audit an intranet or a site in development.
- **Competitors get a light look only.** For a competitor a workspace tracks ([SEO competitors](seo.md#competitors)), OutflareBot reads once a week its robots.txt, up to 10 of its sitemap files (5,000 URLs at most) and its home page, nothing else, obeying robots.txt for each, with at least a second (or the `Crawl-delay`) between requests. It never follows a competitor's links, and a robots.txt that disallows OutflareBot ends the look.
- **Opt-outs:** hosts in the `crawl_optouts` table (a host covers its subdomains) are never crawled from that server. Outflare Cloud adds the sites whose owners email crawl@outflare.ai. A crawl of one answers `409 crawl_opted_out`.

## Sites and ownership

A site is a website a workspace audits: `POST /v1/workspaces/$WS/seo-sites` with its `url`. It belongs to the brand whose domains include its host, unless `brand_id` says otherwise.

Until you prove you own a site, a crawl reads at most 100 pages, waits at least 2 seconds between requests and always obeys robots.txt. A site you have proved may crawl up to 10,000 pages, as fast as one request every 250 ms, and may set `obey_robots: false` to audit pages its own robots.txt hides from crawlers. That choice applies only to that site. Each site's `limits` say what it may ask for now. The first proof moves a site still on the unproved limits to the proved defaults: up to 10,000 pages, about one request a second. Limits you set yourself stay as they are.

Most sites are proved with nothing to add. `POST .../seo-sites/{site}/verify` (and **Add and crawl** in the app, before the first crawl) looks for:

| Proof | What it needs |
| --- | --- |
| Search Console | A connected Google Search Console account that is an owner or full user of a property covering the site: a domain property (`sc-domain:example.com`) covers the domain and its subdomains, a URL-prefix property its own host. |
| Tracking tag | The workspace's Outflare tracking tag (`<script src=".../site.js" data-key="ofk_...">`, from Analytics → Tracking) on the start page. |

`verify_method` says which one matched (`search_console` or `tracking`). Otherwise add any one of the proofs in its `verification` field, then verify:

| Proof | What to add |
| --- | --- |
| DNS | A TXT record on the domain (without `www.`) with the value `outflare-site-verification=<token>` |
| Meta tag | `<meta name="outflare-site-verification" content="<token>">` in the `<head>` of the start page |
| File | `/.well-known/outflare-site-verification.txt` on the site, holding the token |

With a Cloudflare connection (sign in, or an API token with Zone > DNS > Edit), **Prove you own it** adds the TXT record in one click: `POST .../seo-sites/{site}/dns` with one of the site's `dns_connections` (MCP `add_seo_site_dns`). It only adds the record, then looks for it every minute until the site is proved; `dns_install` on the site says how it went.

The check runs while you wait, and answers `409 verification_failed` with the proofs when it finds none. Outflare checks a proved site again every 30 days, before a crawl (a Search Console that can't be reached then keeps the proof until the next check); if the proof is gone, the site drops back to the lower limits and an audit event `seo_site.unverified` says so. A test workspace's sites pass without a check, and its crawls read a small simulated site.

## Crawls

`POST .../seo-sites/{site}/crawls` queues a crawl (`202`). It reads robots.txt, then the sitemaps robots.txt lists (or `/sitemap.xml`), then the start page, and follows links breadth first until nothing is left (`done`) or `max_pages` were read (`limit`). Sitemaps may be gzipped, sitemap indexes or plain text; up to ten sitemap files are read. A crawl queues at most three times its page limit of URLs.

One crawl per site runs at a time (`409 crawl_running`). Follow it with `GET .../crawls/{crawl}`, and stop it with `POST .../crawls/{crawl}/stop`. A site with `schedule: daily` or `weekly` is crawled that often by itself. A scheduled crawl that fails (the site down, robots.txt unreachable, an error of Outflare's) is tried again soon, at any hour, rather than at the next scheduled time: after 15 minutes, then 1 and 2 hours, then every 4 hours, until one works.

### Crawl settings

Every setting has a default that reads the site the way a search engine does, so most sites need none of them. Change them with `PATCH .../seo-sites/{site}` (or the site's Crawl settings in the app); a crawl keeps the settings it started with (`settings` on the crawl).

| Setting | Default | What it does |
| --- | --- | --- |
| `max_pages` | 100, or 10,000 once proved | Pages read per crawl. |
| `max_depth` | 0 (no limit) | Clicks from the start page. |
| `use_sitemaps` | on | Find pages in the sitemaps robots.txt lists, or `/sitemap.xml`. |
| `follow_nofollow` | off | Follow links marked `rel="nofollow"` too. |
| `ignore_params` | off | Crawl `/shoes?color=red` as `/shoes`. |
| `include_patterns` | none | Regular expressions on a page's path and query; when set, only matching pages are crawled. The start page always is. |
| `exclude_patterns` | none | Pages matching any of these are left out; they win over `include_patterns`. |
| `render_js` | `auto` | Run pages' scripts: `auto` only for pages that are empty app shells, `always` for every page, `off` never. See below. |
| `crawl_delay_ms` | 2 seconds, or 1 second once proved | The pause between requests. |
| `schedule`, `schedule_hour`, `schedule_day`, `schedule_tz` | off (by hand), any time, UTC | When a scheduled crawl starts: every day or week, at an hour (and, weekly, a day) in a time zone. |

A crawl's `pages_excluded` counts the links its patterns and depth left out, and `pages_rendered` the pages read with their scripts run.

Each crawl runs as a chain of jobs that each work for about 90 seconds, so a large site never holds a worker for long and a restart picks the crawl up where it was.

`GET .../crawls/{crawl}/pages` lists every URL the crawl found, in the order found:

- `status`: `fetched` (any HTTP answer, 404s included, which are broken links), `blocked` (robots.txt), `failed` (no answer or a 5xx), `skipped` (never reached, because of the limit or a stop), or `queued` while the crawl runs.
- `source` (`start`, `link`, `sitemap`, `redirect`), `found_on`, and `depth` (clicks from the start page; sitemap pages count as one).
- `http_status`, `content_type`, `bytes`, `duration_ms`, `redirect_to` (a redirect's target is a page of its own when it is on the site), `noindex`, `nofollow` `x_robots_tag`, `links_out` and `links_in` (pages of the site linking here, so a sitemap page with none is an orphan). A meta refresh counts as a redirect.
- For an HTML page, what an audit checks: `title` and `title_count`, `meta_description`, `canonical` and `canonical_count`, `h1`, `lang`, `hreflang`, the JSON-LD `schema_types`, `word_count` (words a reader sees), `images` and `images_no_alt`, `viewport`, the Open Graph `og_title` and `og_image` link previews use, `insecure_resources` (scripts, styles, images and frames a https page loads over plain http), and `content_hash`, which is the same for pages with the same words. Page text itself is never stored.

`GET .../crawls/{crawl}/pages/{page}` adds every link on the page (up to 500, with the anchor text, whether it is on the site or `nofollow`, and the status the crawl got from it, so broken links show as 404) and the pages that link to it. `?http_status=404` lists one status's pages.

## The audit

When a crawl ends, Outflare checks every page it stored against the audit's rules and keeps one issue for each rule a page breaks. The crawl then has a `health_score` (0 to 100: the share of its pages, HTML or broken, with no error) and `issues` counts by severity.

`GET .../crawls/{crawl}/issues` lists the rules broken, errors first and then by how many pages, each with why it matters, how to fix it, and the **URL templates** its pages come from. A template is a path with the parts that vary between sibling pages replaced by `*`, such as `/blog/*` or `/products/*/reviews`, so "38 pages in /blog/* have no meta description" reads as the one fix it is. `?severity=error` keeps one severity. `GET .../crawls/{crawl}/issues/{rule}` lists the pages behind one rule (`?template=/blog/*` for one template), each with a short detail such as the broken link's target or the duplicate's URL. `GET .../seo-rules` lists every rule.

| Rule | Severity | What it finds |
| --- | --- | --- |
| `page_broken` | error | Page not found |
| `page_server_error` | error | Server error |
| `link_broken` | error | Links to a broken page |
| `redirect_chain` | warning | Redirect chain |
| `link_to_redirect` | notice | Links to a redirect |
| `canonical_broken` | error | Canonical points to a broken page |
| `canonical_multiple` | warning | More than one canonical |
| `canonical_elsewhere` | notice | Canonical points to another page |
| `noindex` | notice | Not indexed (noindex) |
| `noindex_in_sitemap` | warning | Noindex page in the sitemap |
| `blocked_in_sitemap` | warning | Sitemap page blocked by robots.txt |
| `js_only_content` | warning | Content only appears with JavaScript |
| `title_missing` | error | Missing title |
| `title_multiple` | warning | More than one title |
| `title_duplicate` | warning | Duplicate title |
| `title_too_long` | notice | Title too long |
| `title_too_short` | notice | Title too short |
| `description_missing` | warning | Missing meta description |
| `description_duplicate` | notice | Duplicate meta description |
| `description_too_long` | notice | Meta description too long |
| `h1_missing` | warning | Missing h1 |
| `h1_multiple` | notice | More than one h1 |
| `content_thin` | warning | Thin content |
| `content_duplicate` | warning | Duplicate content |
| `images_no_alt` | notice | Images without alt text |
| `lang_missing` | notice | No language set |
| `viewport_missing` | warning | Not mobile friendly |
| `orphan_page` | warning | Orphan page |
| `deep_page` | notice | Deep page |
| `page_slow` | notice | Slow page |
| `page_large` | notice | Large page |
| `redirect_loop` | error | Redirect loop |
| `redirect_to_broken` | error | Redirect to a broken page |
| `redirect_temporary` | notice | Temporary redirect |
| `link_internal_nofollow` | notice | Nofollow on internal links |
| `link_no_text` | notice | Links without text |
| `link_to_http` | warning | Links to an insecure page |
| `links_out_none` | notice | No links out |
| `links_out_many` | notice | Too many links |
| `single_link_in` | notice | Only one link to the page |
| `url_too_long` | notice | Long URL |
| `url_uppercase` | notice | Capital letters in the URL |
| `url_underscores` | notice | Underscores in the URL |
| `url_parameters` | notice | Indexable URL with parameters |
| `nofollow_page` | notice | Links not followed (nofollow) |
| `canonical_missing` | notice | No canonical |
| `canonical_to_noindex` | warning | Canonical points to a noindex page |
| `sitemap_redirect` | warning | Sitemap page redirects |
| `sitemap_broken` | warning | Broken page in the sitemap |
| `sitemap_non_canonical` | warning | Sitemap page is not canonical |
| `hreflang_invalid` | warning | Invalid hreflang code |
| `hreflang_no_self` | notice | Hreflang without the page itself |
| `hreflang_broken` | warning | Hreflang points to a broken page |
| `hreflang_no_return` | warning | Hreflang not confirmed |
| `description_too_short` | notice | Meta description too short |
| `h1_empty` | warning | Empty h1 |
| `schema_missing` | notice | No structured data |
| `http_page` | warning | Page not on HTTPS |
| `mixed_content` | warning | Insecure resources on a secure page |
| `og_missing` | notice | No Open Graph tags |
| `og_image_missing` | notice | No share image |
| `ai_search_blocked` | warning | Hidden from AI search |
| `llms_txt_missing` | notice | No llms.txt |

`js_only_content` marks a page that, as served, is an empty app shell (see below): crawlers and AI assistants that do not run JavaScript see little of it, even when Geonode has read it for the audit.

Duplicates (titles, descriptions, content) count only among pages meant to be indexed: a page marked `noindex`, or whose canonical points elsewhere, is a declared copy. Rule categories are indexability, links, content, metadata, performance, security, social and ai_visibility. Rule ids never change; new rules are added, and a crawl keeps the issues it had when it ended.

### AI crawlers

AI products send their own crawlers, and robots.txt can let each in or keep it out by its token. Each crawl works out, from the robots.txt it already read (no extra requests), what every known AI crawler may read, and checks the site's `/llms.txt`. `GET .../crawls/{crawl}/ai-access` (MCP `get_crawl_ai_access`) lists, per crawler, whether it may read the start page and how many of the crawl's HTML pages it is kept from.

| Purpose | Crawlers | What blocking it does |
| --- | --- | --- |
| search | OAI-SearchBot, Claude-SearchBot, PerplexityBot, DuckAssistBot, Amazonbot | AI search can't find the page, so answers don't cite it |
| user | ChatGPT-User, Claude-User, Perplexity-User, MistralAI-User | An assistant can't open the page when someone asks about it |
| training | GPTBot, ClaudeBot, Google-Extended, Applebot-Extended, Meta-ExternalAgent, CCBot, Bytespider | Pages aren't collected to train models; answers are unaffected |

The audit flags a page that robots.txt keeps from any search or user crawler (`ai_search_blocked`, naming them), and the start page of a site without an llms.txt (`llms_txt_missing`; an HTML page answering at `/llms.txt` counts as none). Blocking only training crawlers is a choice, not an issue. Together with [AI visibility](ai-visibility.md), which asks the models themselves, this shows whether AI can read the site and whether it mentions it.

## The fixes list

Outflare keeps one fixes list for the whole workspace, shared by people and agents, so Home and agents read one list of work rather than one per feature. Each fix has a `source` (what found it), a `subject` (what it is about), a `rule` (why it matters and how to fix it), a `group` and a `count`. Site audits are the first source (`seo_audit`): one fix per rule and URL template of a site, such as "pages in /blog/* have no meta description", with `subject` the SEO site, `group` the template and `count` its pages. Link opportunities are the next (`seo_links`): lost strong links, links to broken pages, competitors' links and toxic links to review, one fix per linking domain, page or site ([SEO](seo.md#link-opportunities)).

`GET .../fixes` lists them, in progress first, then open, worst and biggest first (`?source=seo_audit` or `seo_links`, `?subject_id=` for one site, `?status=`, `?severity=`, `?brand=`). Fixes follow their site's brand.

- Every finished crawl updates the list: new problems become open fixes, and `count` is counted again.
- A **complete** crawl (one that read every page it found, `reason: done`) that no longer finds a fix marks it `done`, with `confirmed_by` saying which crawl confirmed it. A crawl cut short by its page limit or a stop confirms nothing.
- A fix that was done and comes back opens again.
- `PATCH .../fixes/{fix}` takes it (`"assignee_id": "me"`, or a member's or agent's actor id), marks it `in_progress`, `done` or `ignored` (an issue that is intended), and keeps a `note`. Done stays until the next complete crawl checks it. Ignored fixes stay ignored.
- Deleting a site deletes its fixes.

The pages behind a site audit's fix are `GET .../crawls/{last_found_by}/issues/{rule}?template={group}`. While the list holds only site audits' fixes it uses the `seo.read` and `seo.manage` scopes. 

### Handing a fix to a coding agent

A fix can go straight to the coding agent that works on the site's code (Claude Code, Cursor, Codex), and come back verified:

1. **Prompt.** `GET .../fixes/{fix}/prompt` (MCP `get_fix_prompt`, or **Copy prompt** on the fix in the app) answers a plain-text `prompt`: the issue, why it matters, how to fix it, up to 25 example pages from the latest crawl and how to report back. What it quotes from the pages is marked as data, never instructions.
2. **Report back.** When the change is live, `POST .../fixes/{fix}/verify` (MCP `verify_fix`, or **It's live, check it**), optionally with `{"note": "..."}` such as the pull request link, marks the fix done and starts a crawl of its site. It needs `seo.manage`, so give the agent a token with that scope. While a crawl of the site runs it answers `409 crawl_running`.
3. **Verified.** A complete crawl that no longer finds the issue confirms the fix; one that still finds it opens it again.

How far agents in a mission may go on their own (taking a fix with `POST .../fixes/{fix}/start` first) is [fix autonomy](fix-autonomy.md); a production deploy of a watched site also re-checks its fixes.

Every step is a webhook event in the `fixes` category, so an agent can run the whole loop without polling: `fix.found` (a new fix), `fix.updated` (someone took, finished or ignored it), `fix.started` (taken before changing the site), `fix.verify_requested` (with the checking crawl's `run_id`), `fix.confirmed` and `fix.reopened`. Each has the fix's `source`, `rule`, `subject_type` and `subject_id`.

### Fix tickets

The fixes list is fine for machines, but a person (or a coding agent) wants one piece of work per problem. So fixes are grouped into **tickets**: one per issue and site, such as "pages have no title", with every page behind it however many URL templates they come from. Tickets are a view over the same fixes, kept in step by the database: a new fix joins its ticket, a ticket's status comes from its fixes (in progress if any is, open if any is, then done, then ignored), and a ticket goes when its last fix does. Other sources group the same way, one ticket per rule and subject.

`GET .../fix-tickets` lists them (`?status=todo`, the default, is open and in progress; `open`, `in_progress`, `done`, `ignored` or `all`; `?source=`, `?subject_id=`, `?brand=`). `GET .../fix-tickets/{ticket}` adds:

- its fixes and pages (each with the template it came from),
- the search clicks and impressions of those pages over the last 28 days, when the site has Search Console,
- the other tickets on the same site with their status and review, so nobody fixes the same thing twice or undoes another fix,
- the guidelines that apply to it (below).

**The brief.** `POST .../fix-tickets/{ticket}/write` has the workspace's own AI model (the **Fix tickets** tool under Settings → AI model, a light model by default) write the ticket like a GitHub or Linear issue: a `title`, a two-line `summary` for a person, `why` it matters for search traffic, AI citations or users, the `change` in enough detail for a coding agent, a `done_when` checklist anyone can check, and what not to do (`dont`). It writes from the issue, the pages, the brand and its website, the other tickets and their reviews, the team's guidelines and any `feedback` you send, and says `kind`: `code`, `content` or `config`. It answers `202` and runs as a job; the ticket's `brief_status` goes `queued`, `writing`, then `ready` or `failed` with `brief_error`. Without an AI connection it answers `409 connector_needed`; nothing is written with a key of Outflare's own. Writing again with `feedback` ("keep titles under 55 characters") rewrites it.

**Review.** A person decides with `POST .../fix-tickets/{ticket}/review` and `decision`:

- `approve` marks the ticket approved.
- `reject` needs a `note` saying why, and ignores its fixes, so crawls stop raising them.
- `reopen` clears the review and opens the fixes again.

With `"guideline": "site"`, `"brand"` or `"workspace"`, the note is saved as a guideline as well.

**Guidelines** are how a team tunes what Outflare recommends. Each is a sentence every later brief follows, for the whole workspace, one brand, or one site (and one issue): "titles end with | Northpeak", "never change /legal/*". `GET`, `POST .../fix-guidelines` (`text`, and `brand_id` or `subject_id` with an optional `rule`) and `DELETE .../fix-guidelines/{guideline}` manage them; a workspace keeps at most 200. Rejected tickets' notes on the same site are given to the AI as well.

**Hand it on.** `GET .../fix-tickets/{ticket}/prompt` answers the ticket as one Markdown issue (with the guidelines, every page and how to report back), ready for a coding agent or an issue tracker. It works before a brief is written, too, from the rule alone. Then report each fix live with `verify_fix` as above.

Reading tickets needs `seo.read`; writing, reviewing and guidelines need `seo.manage`. The MCP tools are `list_fix_tickets`, `get_fix_ticket`, `get_fix_ticket_prompt`, `write_fix_ticket`, `review_fix_ticket`, `list_fix_guidelines`, `create_fix_guideline` and `delete_fix_guideline`. Asking for a brief is `fix_ticket.write_requested`, a written one `fix_ticket.written` (a failed one the product event `fix_ticket.write_failed`), reviews are `fix_ticket.approved`, `fix_ticket.rejected` and `fix_ticket.reopened`, and guidelines `fix_guideline.created` and `fix_guideline.deleted`.

## Pages your firewall blocks, and pages built by JavaScript

Two kinds of page can't be read as served. A firewall or bot protection (Cloudflare, Akamai, DataDome, Imperva and the like) can answer OutflareBot with a `403` or a challenge page instead of your page. And some sites send an almost empty page and build it in the browser with JavaScript (React, Vue, Angular and the like), so read as served it has no title, words or links. For a site you have proved you own, a crawl can read both through **Geonode's Scraper API** with your own Geonode key: connect Geonode (`POST /v1/workspaces/$WS/integrations` with `{"connector": "geonode", "credentials": {"api_key": "..."}}`, or Connectors in the app), and the next crawl uses it. Outflare never uses a key of its own for this.

- Only for a proved site. A blocked page is one whose answer was a `403`, a challenge page or a `Cf-Mitigated: challenge` header. A rate limit (`429`) is waited out, not got around.
- A JavaScript page is one that looks like an empty app shell: fewer than 150 words and a framework's mount point (`id="root"`, `id="__next"` and the like). Geonode loads it in a browser and the crawl audits what it built, keeping any link from the served HTML too. A page marked `noindex` is kept as served. That is `render_js: auto`, the default; `always` runs every HTML page's scripts (each one costs Geonode tokens), and `off` never does. The site's `render_connections` lists the connections that can do it for its brand.
- Never for a page robots.txt disallows: those are not requested at all. A fallback gets past a technical block, never the site's rules.
- The page is then marked `fetched_via: "geonode"`, and keeps Geonode's answer. When Geonode cannot read it either, it keeps your site's own answer. After three failed reads in a row (out of tokens, say), a crawl stops asking for a while. A crawl's `pages_fallback` counts these pages.
- Outflare never reads them with an account or key of its own, on Cloud or self-hosted: only the workspace's own Geonode connection. Without one the page stays blocked. A crawl's `pages_firewalled` counts the blocked pages, and its `fallback_hint` says what would let the next crawl read them: `verify_site` (prove you own it) or `connect_geonode`.
- Each page costs Geonode tokens on your plan; a page loaded in a browser costs more.

Without a Geonode connection, pages are read as served. Better still for blocked pages, let OutflareBot through your firewall: its user agent and, once published, its addresses are on [outflare.ai/bot](https://outflare.ai/bot/).

## Indexability

A site's indexability report says which of its pages Google has indexed, and why some aren't. No single source is right on its own: the crawl knows what the site asks for (status, `noindex`, canonical, the sitemap), Search Console's URL Inspection says what Google decided but only for the pages it inspected and can be weeks old, Search Console's impressions prove a page is on Google, and an SEO tool (Ahrefs or DataForSEO, on your own account) sees pages ranking where Search Console has nothing. The report puts every page any of them knows in one list and gives each a verdict from the strongest evidence:

- **Indexed**: Search Console showed it in results in the last 28 days, or URL Inspection says it is on Google.
- **Likely indexed**: your SEO tool sees it ranking, and Search Console says nothing against it.
- **Not indexed**: URL Inspection says Google hasn't indexed it, and nothing shows it in results.
- **Unknown**: no source has looked at it yet.

Where they disagree, results beat an old inspection. The report also finds these issues, worst first: a page that can be indexed but isn't (error), a page in the sitemap that can't be indexed (error), an indexed page that can no longer be (warning), Google choosing another canonical (warning), a page Google knows that the crawl didn't find, an orphan or old address (warning, only after a complete crawl), sources disagreeing (notice), and an indexable page missing from the sitemap (notice). Each issue says why it matters and how to fix it.

It reads only what the crawls and syncs stored, so it costs nothing. A snapshot of its counts is kept once a day, after every crawl that read pages and every Search Console sync that brought new days or inspections, so the report shows 90 days of history. Without a crawl or a Search Console connection the report says which to add; an SEO tool is optional.

```sh
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/indexability"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/indexability/pages?issue=indexable_not_indexed"
```

The MCP tools are `get_site_indexability` and `list_indexability_pages`.

## API and scopes

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"url": "https://www.example.com", "schedule": "weekly"}' "$URL/v1/workspaces/$WS/seo-sites"
curl -X POST -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/verify"
curl -X POST -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/crawls"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/crawls/$CRAWL/pages?status=blocked"
```

Reading needs `seo.read` (viewers and operators have it); adding sites, checking ownership and starting or stopping crawls needs `seo.manage` (operators, admins, owners). Brand-limited members and tokens see and change only their brands' sites and the shared ones.

The MCP tools are `list_seo_sites`, `add_seo_site`, `update_seo_site`, `delete_seo_site`, `verify_seo_site`, `start_crawl`, `get_crawl`, `list_crawls`, `stop_crawl`, `list_crawl_pages`, `get_crawl_page`, `list_crawl_issues`, `get_crawl_issue`, `list_fixes` and `update_fix`.

## Events

Adding, changing, verifying and removing a site are the audit actions `seo_site.created`, `seo_site.updated`, `seo_site.verified`, `seo_site.unverified` and `seo_site.deleted`; starting and stopping a crawl are `crawl.started` and `crawl.stopped`. Each crawl's end is the product event `crawl.finished`, with its status, reason, page counts, bytes, duration, `health_score` and issue counts (never URLs), and `outcome` `error` with `error_code` `site_errors` or `internal` when it broke, or `failure` with `crawl_opted_out` or `site_unreachable`. A scheduler failure is `crawl.schedule_failed`, and an audit that could not run is `crawl.audit_failed` (the crawl still finishes, without issues). Each page read through the fallback is `crawl.page_fallback`, with the connector, whether it was loaded in a browser (`render_js`), the status and the tokens it cost; `outcome` `error` with `fallback_failed` (or `fallback_unavailable` when the connection could not be opened).
