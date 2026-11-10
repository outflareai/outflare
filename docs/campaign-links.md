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

`GET /v1/brands/{brand}/tracking-taxonomy` returns this table.

Sources are cleaned when you save them: lowercase, spaces become `-`, and
common spellings are merged (`fb`, `Facebook` and `facebook.com` are all
`facebook`; `Twitter` and `t.co` are `x`; `Google Ads` is `google`). Any other
source is allowed. A channel with one usual source (Reddit, Affiliates) fills
it in for you.

## Campaigns

A campaign has a name, a channel, a source, an optional brand and an optional
landing page. Outflare gives it:

- a **slug**, made from the name and used as `utm_campaign`
  (`Spring launch, US` becomes `spring-launch-us`), unique in its brand (two brands can each
  have a `spring-launch-us`);
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
  so the snippet still sees the campaign. The snippet sends `ofclk` with
  the page view, and the visit claims the click once (a reload doesn't
  count again). Each link and campaign shows `landed`: how many of its
  counted clicks reached a page running the snippet. Clicks that didn't
  left before the page loaded, or went to a page without the snippet.
- Clicks from link scanners and previews (Slack, mail security filters)
  are kept but not counted. Each link and campaign shows its `clicks`.
- No address or user agent is stored with a click: only when it happened,
  whether it looked automated, and what they say about it: the country,
  region, city and network, the device with its maker and model, the
  system and browser with their versions, and the language
  ([where visitors are](website-tracking.md#where-visitors-are)). The
  address is kept only as a keyed hash (`ip_hash`, an HMAC with the
  server's key, per workspace), so one visitor's repeat clicks can be told
  apart from many visitors' without the address being readable.
- A redirect answers without waiting on the database: the link is kept in
  memory, and its clicks are written in batches a moment later (within a
  second). A landing page that reports its click first waits for it. A
  server takes about a million clicks a day per workspace this way. If the
  database is down, the clicks wait in the server's memory; past 100,000
  waiting, new clicks are lost, but people are still sent on.
- On a server with a plan quota (Outflare Cloud), clicks count against the
  workspace's `tracker_clicks` quota, leaving out likely bots. Over it,
  people are still sent on and the clicks aren't stored ([quotas](quotas.md)).
- A redirect link keeps working when its campaign is archived, since it
  may be printed. Turning `redirect_links` off doesn't remove the ones
  already made.

### Destinations and daily caps

A redirect link can send its clicks to several pages instead of one: two
offers to compare, or a partner's page that takes only so many clicks a day.
Each destination has:

- a **weight**: its share of the clicks against the others' weights (100
  when left out; 0 sends it none);
- an optional **daily click cap**: how many clicks a day it takes, in UTC
  until brands have a reporting time zone. A cap of 0 stops it.

When every destination is capped or stopped, clicks go to the link's own
page, the fallback. Destinations get the campaign's tags and can carry
tokens, as the link's page does. Link scanners and previews go where a
person would, but don't count against a cap.

Caps are counted by every server together as it writes its clicks, so a
redirect never waits for a count: a cap can be passed by the clicks that
arrive in the moment before the servers' next write, not more. Changing a
link's destinations keeps each destination's count for the day; one taken
off the link is kept, so its clicks still say where they went. Other servers
follow a change within a few minutes.

## Traffic sources and custom fields

A traffic source is a network a brand buys clicks from, such as Taboola,
MGID or PropellerAds. It says which URL parameters of a click carry:

- the network's own click id (`click_id`), which a postback back to the
  network names;
- what the click cost (`cost`), in the source's currency;
- up to twenty values worth reporting on, each into its own custom field
  (1 to 20): a zone, a widget, a placement, a creative. A parameter can have
  aliases (`utm_source`) that fill the same field when it's missing.

Most sources start from a template: Outflare has templates for the 12
networks with website ads it syncs and 13 more (MGID, Revcontent, NewsBreak,
PropellerAds, Zeropark, ExoClick, Adsterra, TrafficJunky, RichAds,
Pushground, MediaGo, SmartNews and AppLovin). Each template is a file in
[`integrations/traffic-sources/`](../integrations/traffic-sources/README.md)
with the network's tokens and where they come from; anyone can add one.
Every field of a source made from a template can be changed.

Give a campaign its source (`traffic_source`). Each of its links with a
redirect link then has a `campaign_url`: the redirect link with the source's
parameters, each set to the network's token, which is the address to paste
into the network. Every click on it keeps the network's click id, the cost
and the custom fields. A change to a source reaches its campaigns' clicks
within a few minutes.

A campaign without a source still keeps `sub1` to `sub20` in the custom
fields of the same number, as most trackers do.

A source's postback is where the network takes a conversion, with
Outflare's tokens such as `{source_click_id}` (the network's click id) and
`{payout}`. A template marks the values only the brand has (an account id,
a key) as `REPLACE`; a source can't be saved with one left. Sending it is
coming with tracker conversions.

### Tokens in a destination

A link's page can carry tokens each redirect click fills in, such as
`https://northpeak.example/offer?sub={field1}&cid={click_id}`:

| Token | Value |
| --- | --- |
| `{click_id}` | The click's key, also added as `ofclk` |
| `{source_click_id}`, `{cost}` | The network's click id and the cost it reported |
| `{field1}` to `{field20}` | The click's custom fields |
| `{campaign}`, `{campaign_code}`, `{source}`, `{content}` | The campaign's slug and ofc code, its source and the link's content |
| `{country}`, `{region}`, `{city}`, `{isp}`, `{asn}` | Where the visitor is |
| `{device}`, `{os}`, `{browser}`, `{language}` | What they're on |

A value is escaped for a URL's query string. Functions follow a pipe:
`{field1|raw}` as it is, `{email|hash}` its SHA-256, and `{payout|80%}` a
number scaled by a percentage. A word in braces that isn't a token is
refused when the link is saved, so a typo doesn't leave a token unfilled.
Partner postbacks use the same tokens and functions.

## Campaigns modules make for you

Some modules tag their own links, so you don't make those campaigns by
hand. Each one makes one campaign per object, the first time it needs one,
and lists it here with the rest:

| Module | One campaign per | Channel | Source | Which links |
| --- | --- | --- | --- | --- |
| Broadcasts | broadcast (`email_broadcast`) | `lifecycle_email` | `outflare` | Links in the email to the brand's own website and domains (any brand's, for an email with no brand) |
| Journeys | journey (`journey`) | `lifecycle_email` | `outflare` | The same |
| Sequences | sequence (`sequence`) | `outbound_email` | `outflare` | Links in each step to the sequence's brand's website and domains (any brand's, for a sequence with no brand), tagged before click tracking wraps them, with the step as `utm_content` (`step-1`, `step-2`) |
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
click's key, after `ref`, and the same key as `ofclk`. Its clicks are kept
with redirect link clicks, under the partner instead of a link.

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
| `GET /v1/brands/{brand}/tracking-taxonomy` | `analytics.read` |
| `GET /v1/brands/{brand}/tracking-campaigns` (filter by `status`, `channel`, `brand`) | `analytics.read` |
| `POST /v1/brands/{brand}/tracking-campaigns` | `analytics.manage` |
| `GET`, `PATCH /v1/brands/{brand}/tracking-campaigns/{id}` (id, slug or code) | `analytics.read`, `analytics.manage` |
| `GET`, `POST /v1/brands/{brand}/tracking-campaigns/{id}/links` | `analytics.read`, `analytics.manage` |
| `GET`, `PUT /v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/destinations` | `analytics.read`, `analytics.manage` |
| `GET /v1/brands/{brand}/traffic-source-templates` | `analytics.read` |
| `GET`, `POST /v1/brands/{brand}/traffic-sources` | `analytics.read`, `analytics.manage` |
| `GET`, `PATCH /v1/brands/{brand}/traffic-sources/{id}` | `analytics.read`, `analytics.manage` |
| `GET /l/{code}` (a redirect link) | Public |

Owners, admins and operators have `analytics.manage`; viewers can read.

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"Spring launch, US","channel":"paid_ads","source":"Google Ads","landing_url":"https://northpeak.example/pricing"}' \
  "$URL/v1/workspaces/$WS/tracking-campaigns"
```

The MCP tools are `get_tracking_taxonomy`, `list_tracking_campaigns`,
`get_tracking_campaign`, `create_tracking_campaign`,
`update_tracking_campaign`, `list_tracking_links`, `create_tracking_link`,
`list_tracking_link_destinations`, `set_tracking_link_destinations`,
`list_traffic_source_templates`, `list_traffic_sources`,
`create_traffic_source` and `update_traffic_source`.

Each change is an audit action (and a product event): `tracking_campaign.created`,
`tracking_campaign.updated`, `tracking_campaign.archived`,
`tracking_campaign.restored`, `tracking_link.created`, `tracking_link.redirect_added`,
`tracking_link.destinations_updated`,
`traffic_source.created`, `traffic_source.updated`, `traffic_source.archived`
and `traffic_source.restored`.
