# Campaign links

Campaign links give every link you share one set of tags, so each visit, lead
and sale can be credited to the channel, source and campaign that brought it.
They live under **Analytics → Campaign links**, and in the API, MCP and SDKs.
Nothing here calls a third-party service: tagging runs inside Outflare.

## The taxonomy

Every campaign belongs to one of nine channels. The channel fixes
`utm_medium`, so the same kind of traffic is never split across spellings.

| Channel | `channel` | `utm_medium` | Usual sources |
| --- | --- | --- | --- |
| Paid ads | `paid_ads` | `cpc` | google, meta, linkedin, microsoft, reddit, x, tiktok |
| Organic social | `organic_social` | `social` | linkedin, x, facebook, instagram, youtube, tiktok, threads, bluesky |
| Reddit | `reddit` | `social` | reddit |
| Outbound email | `outbound_email` | `email` | outflare, emailbison, instantly, smartlead |
| Lifecycle email | `lifecycle_email` | `email` | outflare, newsletter |
| Affiliates | `affiliates` | `affiliate` | partner |
| Referral | `referral` | `referral` | newsletter, podcast, producthunt, event, qr |
| SEO | `seo` | `organic` | google, bing |
| AI answers | `ai_answers` | `ai` | chatgpt, perplexity, gemini, claude, copilot |

`GET /v1/workspaces/{ws}/tracking-taxonomy` returns this table.

Sources are cleaned when you save them: lowercase, spaces become `-`, and
common spellings are merged (`fb`, `Facebook` and `facebook.com` are all
`facebook`; `Twitter` and `t.co` are `x`; `Google Ads` is `google`). Any other
source is allowed. A channel with one usual source (Reddit, Affiliates) fills
it in for you.

## Campaigns

A campaign has a name, a channel, a source, an optional brand and an optional
landing page. Outflare gives it:

- a **slug**, made from the name and used as `utm_campaign`
  (`Spring launch, US` becomes `spring-launch-us`), unique in the workspace;
- a **code**, a random 10-character id sent as `ofc`. The code still
  identifies the campaign when a platform or a person strips the other tags.

The slug, channel and source never change once the campaign exists, so old
links keep reporting to the same place. You can rename a campaign, change its
brand or landing page, and archive it. An archived campaign takes no new
links; links already out keep working.

## Links

A link is a page plus optional `utm_content` (which ad, button or post) and
`utm_term`. Outflare removes any old `utm_*` and `ofc` tags from the page,
keeps its other parameters and `#fragment`, and adds the tags in a fixed
order:

```
https://northpeak.example/pricing?plan=pro&utm_source=linkedin&utm_medium=cpc&utm_campaign=spring-launch-us&utm_content=carousel-1&ofc=ab12cd34ef#faq
```

Making the same link twice returns the one you already have. Pages must be
`http` or `https` web addresses.

## Redirect links

By default a link goes straight to the page, and the website tracking
snippet reads the tags there. Where the snippet can't run, or a long tagged
address doesn't fit (a bio link, a partner's page, a QR code, a podcast
note), give the link a **redirect link** as well: a short address that
counts each click, then sends the person on to the tagged page.

```
https://links.northpeak.example/l/k7m2pq9x
```

- Ask for one per link with `"redirect": true`, or turn on
  `redirect_links` on the campaign so every new link gets one. Asking again
  for a link made without one adds it.
- The redirect link uses the workspace's tracking domain when it has one,
  and this server's address otherwise.
- The person lands on the tagged page with `ofclk`, the click's key, added,
  so the snippet still sees the campaign.
- Clicks from link scanners and previews (Slack, mail security filters)
  are kept but not counted. Each link and campaign shows its `clicks`.
- No address or browser details are stored with a click: only when it
  happened, and whether it looked automated.
- A redirect link keeps working when its campaign is archived, since it
  may be printed. Turning `redirect_links` off doesn't remove the ones
  already made.

## Campaigns modules make for you

Some modules tag their own links, so you don't make those campaigns by
hand. Each one makes one campaign per object, the first time it needs one,
and lists it here with the rest:

| Module | One campaign per | Channel | Source | Which links |
| --- | --- | --- | --- | --- |
| Broadcasts | broadcast (`email_broadcast`) | `lifecycle_email` | `outflare` | Links in the email to the brand's own website and domains (any brand's, for an email with no brand) |
| Journeys | journey (`journey`) | `lifecycle_email` | `outflare` | The same |
| Sequences | sequence (`sequence`) | `outbound_email` | `outflare` | Links in each step to the sequence's brand's website and domains (any brand's, for a sequence with no brand), tagged before click tracking wraps them |
| Social replies | watch (`social_watch`) | `reddit`, or `organic_social` for X and LinkedIn | the network | The watch's landing page, with each reply as `utm_content` |

Links to other companies' sites are never tagged, and a link you tagged
yourself (it already has `utm_campaign`) keeps your tags. Test emails and test
sends are never tagged.

## Partner links and `ref`

Partner links also carry the partner's referral code as `ref`, written last
(after `ofc`). [Partners](partners.md) makes one campaign per partner program
membership (`object_kind` `partner_program`), not per partner, because one
partner can promote several brands and a campaign has one brand. Its name is
the partner's ref code, so a partner's real name never appears in
`utm_campaign`. A partner's short link (`/r/<code>`) adds `ref_click`, the
click's id, after `ref`.

Two readers use these tags differently, on purpose:

- **Channel and campaign reporting** (the CMO view) credits by `ofc` first,
  then the UTM tags.
- **Commissions** credit the partner by `ref` first, then `ofc`, because
  links imported from other affiliate tools have a `ref` but no `ofc`. Each
  program also keeps its own window and first or last rule, which never
  follow the workspace's attribution settings, and a conversion from the
  workspace's backend binds the partner: a visit alone only proposes one.

Keep the two rules separate: merging them would either pay partners for
traffic their code didn't bring, or lose imported partner links.

## API

| Call | Scope |
| --- | --- |
| `GET /v1/workspaces/{ws}/tracking-taxonomy` | `analytics.read` |
| `GET /v1/workspaces/{ws}/tracking-campaigns` (filter by `status`, `channel`, `brand`) | `analytics.read` |
| `POST /v1/workspaces/{ws}/tracking-campaigns` | `analytics.manage` |
| `GET`, `PATCH /v1/workspaces/{ws}/tracking-campaigns/{id}` (id, slug or code) | `analytics.read`, `analytics.manage` |
| `GET`, `POST /v1/workspaces/{ws}/tracking-campaigns/{id}/links` | `analytics.read`, `analytics.manage` |
| `GET /l/{code}` (a redirect link) | Public |

Owners, admins and operators have `analytics.manage`; viewers can read.

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"Spring launch, US","channel":"paid_ads","source":"Google Ads","landing_url":"https://northpeak.example/pricing"}' \
  "$URL/v1/workspaces/$WS/tracking-campaigns"
```

The MCP tools are `get_tracking_taxonomy`, `list_tracking_campaigns`,
`get_tracking_campaign`, `create_tracking_campaign`,
`update_tracking_campaign`, `list_tracking_links` and `create_tracking_link`.

Each change is an audit action (and a product event): `tracking_campaign.created`,
`tracking_campaign.updated`, `tracking_campaign.archived`,
`tracking_campaign.restored`, `tracking_link.created` and `tracking_link.redirect_added`.
