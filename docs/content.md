# Content: posts and pages on the brand's own website

Outflare writes content for a brand's website, and a person approves it. Then it reaches the site one of two ways. A site on WordPress or Ghost gets it through the workspace's own CMS connection, as a draft there or live. Any other site gets it in the `content.approved` webhook, and puts it in place itself, under its own review ("Delivering to your own site" below). This is the "Content and CMS" channel of the brand agent plan: the open tools here, with the agent that decides what to write and when in Outflare Cloud.

## What an item is

An item (`content_items`, routes under `/v1/brands/{brand}/content`) belongs to one brand and has a `title`, a `slug` (empty until the CMS chooses one, or set to lowercase letters, digits and hyphens), a `target` (`posts` or `pages`), a Markdown `body`, an `excerpt`, `tags`, a `canonical_url` for content that first appeared elsewhere, and `notes` for the brief, the sources and what to check. Its `status` is:

- `draft`: being written. A draft can go to the CMS as a draft there, for review in the CMS's own editor, but never live.
- `approved`: a person (or a caller with `content.manage`) said it may go live.
- `published`: live on the site. Set by a publication, never by hand. Editing a live item's text puts it back to `approved` until it is published again, so what is live is always what was approved.
- `archived`: kept, not for publishing.

Facts in a body come from the brand's context and the knowledge base ([knowledge.md](knowledge.md)); an agent is told to check them there before writing. The body is Markdown; when it is published, raw HTML in it is left out and unsafe links dropped, so a page never carries script.

## Delivering to your own site

Outflare doesn't need access to a site's code or CMS. A brand adds a webhook endpoint for `content.*` events (Settings, Webhooks, or `POST /v1/workspaces/{ws}/webhooks`). When a person approves an item, the `content.approved` webhook sends that endpoint the whole item. The receiver can be the site's own code, an automation tool (Zapier, Make, n8n), or a coding agent, and it puts the page in place under the site's own review. Then it reports the live address back with `POST /v1/brands/{brand}/content/{id}/delivered` (MCP: `deliver_content`, scope `content.manage`). The item becomes `published` at that address, and Outflare starts measuring it.

The webhook's `data` holds:

| Field | What it is |
| --- | --- |
| `content_id`, `revision` | The item, and how many times it has been approved. Approving it again sends a higher revision, so the receiver replaces the version it has. Pass `revision` to `.../delivered`, and an out-of-date one is refused. |
| `target`, `slug`, `brand` | Post or page, its slug, and the brand's id, name, handle and website. |
| `language` | A language tag such as `en-AU`. The item is written in it, with matching spelling, currency and units. |
| `placement` | Where the page goes: `type` (post, page, landing, comparison, docs, glossary, faq, other), `collection`, `path`, `parent`, `template_hint` (an existing page whose layout to follow) and `nav_label`. |
| `content` | `title`, `excerpt`, the body as `markdown` and as `html` (rendered the same way as for a CMS: raw HTML left out, unsafe links dropped), `tags`, `word_count` and `reading_minutes`. |
| `seo` | `meta_title`, `meta_description`, `primary_keyword`, `secondary_keywords`, `intent`, `robots`, `canonical`, Open Graph fields and `schema` (JSON-LD objects). |
| `links` | `internal` links in the page to pages on the site, `external` links (sources), and `add_links_from`: existing pages that should link to this one, with the text the link goes after. |
| `media` | Images, each with a `url`, `role` (featured, inline, social), `alt`, `caption`, size, `filename` and `credit`. Copy them onto the site's own storage. |
| `brief` | `goal`, `opportunity` (why the page exists), `measure`, `check_before_live` (facts a person should confirm), `overlaps` (pages competing for the same search, and what to do) and `redirects`. |
| `review`, `callback` | Who approved it and when, and the path and scope to report the live address to. |

A site whose pages live in a GitHub repository can use the Outflare content GitHub Action instead ([`integrations/github-action/content`](../integrations/github-action/content/README.md)). It runs in the site owner's own repository with their own Outflare token (`content.read` and `content.manage`), so Outflare never holds access to the repository. Each run lists the brand's approved items, writes each as a Markdown file with front matter (`outflare_id` and `outflare_revision` included) on its own branch, `outflare/content-<id>`, and opens a pull request with the brief, the links to add from other pages and the images. Approving the item again updates that pull request; a closed one isn't reopened for the same revision. Once the file is on the base branch at the approved revision, the next run reports the item live at its address on the site (`site-url` and `url`).

In the web app, the **Content** page (under SEO) lists a brand's posts and pages. A person can approve one there, copy it as Markdown (with front matter) or HTML, download it as a `.md` file to put in place by hand, and mark it live at its address.

Fields the item doesn't have are left out. The writer (a person, or an agent from the site's crawl and search data) sets them on the item with `language`, `placement`, `seo`, `links`, `media` and `brief` (`POST` or `PATCH .../content/{id}`). Paths are paths on the site (`/blog/a-post`) or full `https` addresses. The audit log keeps only the item's id and revision for `content.approved`. The body goes only to the webhook.

## Publishing to a CMS

`POST /v1/brands/{brand}/content/{id}/publish` with `live: true` or not, and `integration_id` when the brand has more than one CMS (otherwise the one the item is already on, or the brand's one CMS connection). It queues a publication (`content_publishes`) in the same transaction as its audit event (`content.publish_requested`), and the `content.publish` job (`engine/internal/cmspublish`) makes the call: it renders the body, creates the post on the CMS or updates the one the item already is (`kind` create or update), reads it back, and records what the CMS shows on the item (`external_id`, `url`, `cms_status`) and on the publication (`status` applied or failed, `verified`, `error`). An item that went live is `published` with `published_at`. One publication of an item is queued at a time. A create whose outcome is unknown is never retried, so a post is never made twice; an update is retried while the CMS is unreachable.

For an agent, the action layer ([actions.md](actions.md)) holds a publication like any other change, so without a grant a person approves each one on the Agents page; a grant on `content/**` lets drafts or live publications through within its limits. Writing an item is a change too.

## Connectors

| Connector | Connects with | Publishes |
| --- | --- | --- |
| `wordpress` | The site address, a username and an application password (Users, Profile, Application Passwords). WordPress 5.6 and later; WordPress.com needs a Business plan or higher. The user needs to publish posts and pages (Editor, or Author for posts). | Posts and pages: title, slug, body, excerpt, status. Tags and categories stay as the site has them for now. |
| `ghost` | The site address and a custom integration's Admin API key (Settings, Integrations), as `id:secret`. Each call is signed with a five-minute token from the key. | Posts and pages: title, slug, body, excerpt, tags, canonical address, status. Updates carry the post's `updated_at`, which Ghost checks so two editors never overwrite each other. |

Both implement `connectors.CMSPublisher` (`engine/internal/connectors/cms.go`): `Targets`, `Publish` (create or update) and `ReadPost`. Outflare adds no more CMS connectors. Every other site uses the `content.approved` webhook above.

## Scopes, events, monitoring

- `content.read` lists and reads items and their publications; `content.manage` writes, approves, archives, publishes and reports deliveries. The operator role has both.
- Audit actions: `content.created`, `content.updated`, `content.approved` (with `revision`), `content.publish_requested`, `content.delivered` (with `url` and `revision`). `content.approved` and `content.delivered` are webhook events. The job records `content.published` (with `kind`, `live`, `connector`, and `unverified` when the CMS showed something else when read back) and `content.publish_failed` with the error code (`connector_auth`, `connector_invalid`, `connector_unavailable`, `connector_gone`, `internal`).
- `outflare_content_publishes_total{connector,kind,outcome}` counts publications; the **Content (publishing to a CMS)** dashboard row shows them, and `OutflareContentPublishesFailing` fires when more than three fail in an hour ([monitoring.md](monitoring.md)).
- MCP tools: `list_content`, `get_content`, `create_content`, `update_content`, `publish_content`, `deliver_content`.

## Measuring

`GET /v1/brands/{brand}/content?status=published&published_since=<time>` counts what went live, which a content objective reads; what each page earns comes from Search Console clicks per page ([search-intelligence.md](search-intelligence.md)), AI visibility citations ([ai-visibility.md](ai-visibility.md)) and sign-ups attributed to it ([attribution.md](attribution.md)).
