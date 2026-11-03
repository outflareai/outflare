# Content: posts and pages on the brand's own website

Outflare writes and publishes content for a brand's website through the workspace's own CMS connection: WordPress (an application password) or Ghost (an Admin API key). A person or an agent writes a post or page in Outflare, a person approves it, and a publication puts it on the site as a draft there or live. This is the "Content and CMS" channel of the brand agent plan: the open tools here, with the agent that decides what to write and when in Outflare Cloud.

## What an item is

An item (`content_items`, routes under `/v1/brands/{brand}/content`) belongs to one brand and has a `title`, a `slug` (empty until the CMS chooses one, or set to lowercase letters, digits and hyphens), a `target` (`posts` or `pages`), a Markdown `body`, an `excerpt`, `tags`, a `canonical_url` for content that first appeared elsewhere, and `notes` for the brief, the sources and what to check. Its `status` is:

- `draft`: being written. A draft can go to the CMS as a draft there, for review in the CMS's own editor, but never live.
- `approved`: a person (or a caller with `content.manage`) said it may go live.
- `published`: live on the site. Set by a publication, never by hand. Editing a live item's text puts it back to `approved` until it is published again, so what is live is always what was approved.
- `archived`: kept, not for publishing.

Facts in a body come from the brand's context and the knowledge base ([knowledge.md](knowledge.md)); an agent is told to check them there before writing. The body is Markdown; when it is published, raw HTML in it is left out and unsafe links dropped, so a page never carries script.

## Publishing

`POST /v1/brands/{brand}/content/{id}/publish` with `live: true` or not, and `integration_id` when the brand has more than one CMS (otherwise the one the item is already on, or the brand's one CMS connection). It queues a publication (`content_publishes`) in the same transaction as its audit event (`content.publish_requested`), and the `content.publish` job (`engine/internal/cmspublish`) makes the call: it renders the body, creates the post on the CMS or updates the one the item already is (`kind` create or update), reads it back, and records what the CMS shows on the item (`external_id`, `url`, `cms_status`) and on the publication (`status` applied or failed, `verified`, `error`). An item that went live is `published` with `published_at`. One publication of an item is queued at a time. A create whose outcome is unknown is never retried, so a post is never made twice; an update is retried while the CMS is unreachable.

For an agent, the action layer ([actions.md](actions.md)) holds a publication like any other change, so without a grant a person approves each one on the Agents page; a grant on `content/**` lets drafts or live publications through within its limits. Writing an item is a change too.

## Connectors

| Connector | Connects with | Publishes |
| --- | --- | --- |
| `wordpress` | The site address, a username and an application password (Users, Profile, Application Passwords). WordPress 5.6 and later; WordPress.com needs a Business plan or higher. The user needs to publish posts and pages (Editor, or Author for posts). | Posts and pages: title, slug, body, excerpt, status. Tags and categories stay as the site has them for now. |
| `ghost` | The site address and a custom integration's Admin API key (Settings, Integrations), as `id:secret`. Each call is signed with a five-minute token from the key. | Posts and pages: title, slug, body, excerpt, tags, canonical address, status. Updates carry the post's `updated_at`, which Ghost checks so two editors never overwrite each other. |

Both implement `connectors.CMSPublisher` (`engine/internal/connectors/cms.go`): `Targets`, `Publish` (create or update) and `ReadPost`. A new CMS adds a connector there; headless CMSs, Webflow and publishing as a pull request to a site's repository are next.

## Scopes, events, monitoring

- `content.read` lists and reads items and their publications; `content.manage` writes, approves, archives and publishes. The operator role has both.
- Audit actions: `content.created`, `content.updated`, `content.approved`, `content.publish_requested`. The job records `content.published` (with `kind`, `live`, `connector`, and `unverified` when the CMS showed something else when read back) and `content.publish_failed` with the error code (`connector_auth`, `connector_invalid`, `connector_unavailable`, `connector_gone`, `internal`).
- `outflare_content_publishes_total{connector,kind,outcome}` counts publications; the **Content (publishing to a CMS)** dashboard row shows them, and `OutflareContentPublishesFailing` fires when more than three fail in an hour ([monitoring.md](monitoring.md)).
- MCP tools: `list_content`, `get_content`, `create_content`, `update_content`, `publish_content`.

## Measuring

`GET /v1/brands/{brand}/content?status=published&published_since=<time>` counts what went live, which a content objective reads; what each page earns comes from Search Console clicks per page ([search-intelligence.md](search-intelligence.md)), AI visibility citations ([ai-visibility.md](ai-visibility.md)) and sign-ups attributed to it ([attribution.md](attribution.md)).
