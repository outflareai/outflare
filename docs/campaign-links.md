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

In the app, campaigns with a traffic source or redirect links are also on
**Tracker → Campaigns** (`G` then `J`), with their visits, conversions,
revenue, profit and ROI for the last 7 days. Opening one shows its editor:
**General** (traffic source, name, redirect mode), **Destination** (the
campaign's own rule-based paths, each with its landers and offers, their
weights and share of the clicks, and the suggested weights to apply; or a
shared flow instead) and **Tracking** (the traffic source's parameters and
postback), with the campaign URL to paste into the ad network beside them.
The editor works on the campaign's first redirect link, and makes one when
it has none. **Tracker → Offers**, **Landers** and **Flows** list the
brand's [offers](#offers), [landers](#landers) and [flows](#flows) with
today's clicks and what uses each. An offer opens in a sheet with a token
picker under its URL, a warning while it can't carry the click id, and
**Preview** with test values; a lander shows each call to action's click URL
and the script for its page; a flow opens with the campaign editor's paths,
and asks before a save that changes more than one link.

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

### Own domains, redirect modes and fallback pages

Tracker features (destinations, rule-based paths and the redirect modes
below) work only on a brand's own verified tracking domain, so one
customer's traffic never shares a domain's reputation with another's. Add
one under Domains; a link's redirect link uses it once it's verified. On the
shared domain, a click on any redirect link goes to the link's own page with
a 302, so links already shared keep working; the destinations and paths
calls say `needs_domain` until the brand has a domain.

A campaign's `redirect_mode` says how its redirect links send people on:

| Mode | What happens |
| --- | --- |
| `302` | The default, and the fastest. |
| `307` | The same, for tools that expect a 307. |
| `meta` | A page that moves on at once and sends no referrer: the destination sees no referring site. |
| `double_meta` | Two such pages, the second on the tracking domain, so even browsers that ignore the referrer policy send none. |

A redirect code that doesn't exist on a tracking domain (mistyped, or from a
deleted link) shows a not found page, or goes to the domain's fallback page:
`PUT /v1/brands/{brand}/tracking-domains/{id}/fallback`.

### Direct mode

Some networks and placements take only a direct link to the page, with no
redirect. A campaign with a traffic source still gets its clicks: give the
ad the page's URL with the campaign's `ofc` code and the source's parameters
(`https://northpeak.example/winter?ofc=7k2m9qxa&clickid={clickid}&zone_id={zone}`).
When the site's script sees `ofc` and no `ofclk`, it names a click id for the
visit (kept in the tab's session storage, so a reload keeps it), and the new
visit records the tracker click with that id, on the campaign's link for the
page (or its first), with the source's click id, cost and custom fields from
the page's URL. A reload isn't a second click.

The page can pass the click id on, as a redirect click's: `outflare.clickId()`
returns it, and the script fills `{ofclk}` in the page's links, such as an
offer's sub id (`https://network.example/offer?sub1={ofclk}`). Postbacks,
landers' `/click` and the click's conversions then work as for a redirect
click. A campaign without a traffic source (the plain UTM tool) records no
direct clicks. Direct clicks count against the workspace's tracker clicks.

### Conversions by click id

A conversion on an offer the brand doesn't own (an affiliate network's, a
partner's shop) never reaches the site's script. Its network says so instead,
with a postback: a server-to-server call naming the click.

1. Add a **postback source** for the network. Outflare shows its postback URL
   once, on the brand's own tracking domain:
   `https://go.northpeak.example/t/<secret>?cid={click_id}&payout={payout}&txid={transaction_id}&currency={currency}&status={status}`.
2. Send the click to the network with its key: put `{click_id}` in the
   destination's URL where the network takes your sub id
   (`https://network.example/offer?sub1={click_id}`).
3. Give the network the postback URL with its own macros in place of the
   ones in braces (its sub id macro for `{click_id}`).

A source made from an **affiliate-network template**
(`GET /v1/brands/{brand}/affiliate-network-templates`, data in
`integrations/affiliate-networks/`) skips steps 2 and 3: its postback URL comes
written in the network's macros (`?cid={sub1}&payout={payout_amount}&txid={conversion_id}`
for Everflow), and its click id parameter (`sub1`) is added to the URL of each
of its offers. The catalogue has Everflow, TUNE, CAKE, Affise and impact.com,
the platforms most networks run on, and networks such as ClickBank, MaxBounty,
CrakRevenue and Admitad.

Each postback (GET, or POST as a form or JSON) writes a conversion to the
click ledger with the click it credits, even when no person is known; when
the click's visit knew the person, it also goes on their timeline as
`tracker.converted`. It takes `cid` (or `clickid`, `click_id`, `subid`),
`payout` (or `sum`, `revenue`, `amount`), `currency`, `txid` (or
`transaction_id`, `orderid`), `type` (such as `lead` or `sale`), `status`
(`approved`, `pending` or `declined`) and `p1` to `p20`.

A source has:

- an **IP allowlist**, when the network publishes the addresses it calls from;
- a **click expiry**: how long after its click a conversion still counts
  (30 days unless set);
- a **duplicate policy** for a transaction id seen before: ignore it, record
  a new conversion, update the one recorded (a status change), or add its
  payout (an upsell). Without a transaction id, the click and type stand in
  for it.

Postbacks answer only on the brand's own verified tracking domain, at up to
6,000 a minute per source. Rotating a source's secret retires its old URL at
once.

When a network can't send a server postback, a **pixel** on the thank-you page
does the same from the browser: `<img src="https://go.northpeak.example/t/<secret>/pixel?cid={sub1}&payout={payout}">`,
or a script, `<script src="https://go.northpeak.example/t/<secret>/pixel.js?cid={sub1}"></script>`,
with the network's own macros in braces. It always answers with an image, and
its `X-Outflare-Outcome` header says what happened. A pixel's URL is in the
page, so anyone can read it: prefer a postback for payouts that matter. A
source with an IP allowlist takes no pixels.

The **conversion log** (`GET /v1/brands/{brand}/tracker-conversion-log`) keeps
every postback and pixel call of the last 30 days: what it sent, what Outflare
answered and why (an unknown or expired click, a bad parameter, the wrong
domain, an address off the allowlist). A call whose secret names no source
has no brand to show it to and isn't kept. A recorded conversion is the
`tracker_conversion.created` webhook, and a change to one
`tracker_conversion.updated`, each with the conversion's click, payout,
status and person.

**Uploads** record conversions in bulk: up to 1,000 as JSON
(`POST /v1/brands/{brand}/tracker-conversions`) or 10,000 as a CSV file whose
first row names the columns (`.../tracker-conversions/csv?source=<id>`). A row
with a click id records a conversion; a row with only a transaction id updates
the status or payout of the conversion with that id, as networks report
approvals and reversals days later. The answer says what happened to each row.

### Offers

An **offer** is a page the brand sends clicks to, usually on an affiliate
network (`POST /v1/brands/{brand}/offers`). It has:

- a **URL**, which can carry tokens. Its network (a postback source) adds its
  click id parameter unless the URL carries `{click_id}`; `missing_click_id`
  says when neither does, so postbacks couldn't name the click;
- a **payout** and currency, with **payouts by country** before it. With
  `payout_mode` `postback`, a conversion takes the payout its postback sends,
  and the offer's when it sends none; with `fixed`, always the offer's;
- a **conversion type** for postbacks that name none, such as `lead`;
- an optional **daily cap** on clicks or conversions (declined ones don't
  count), and a **fallback offer** that takes its clicks while it is capped.
  With no fallback, or one that is capped too, clicks go to the link's other
  destinations, then its own page.

Give a link's destination an `offer_id` instead of a `url` to send it to an
offer. The destination keeps the offer's URL, and a new URL on the offer goes
to every destination at once. An offer that is a destination can't be
archived. Each conversion says the offer its click went to (`offer_id`, and
`?offer=` on the ledger). A conversion cap counts every server's conversions
within a minute, so a few clicks past it can still go to the offer.

### Landers

A **lander** is a page clicks go to before an offer: a review, a quiz, an
advertorial (`POST /v1/brands/{brand}/landers`). Give a link's destination a
`lander_id` beside the offers it leads to. A list of destinations (a path's,
or the link's own) with landers sends each click to one of its landers, by
weight, and the lander's calls to action to the list's other destinations:

- `https://go.northpeak.example/click` goes to one of them by weight;
- `https://go.northpeak.example/click/2` goes to the second of them while it
  is open, else by weight. A lander has 1 to 100 calls to action (`ctas`).

The lander gets the click id as `ofclk` (and in `{click_id}`), and its page
carries `<script src="https://go.northpeak.example/click.js" async></script>`,
which adds it to the page's links to `/click`. Nothing is stored in the
browser. A call to action goes on only for a click Outflare sent to a lander
in the last day, on the brand's own tracking domain; anything else (a
bookmarked button, a scraper) gets the domain's fallback page. Each lander
click is kept (`GET /v1/brands/{brand}/lander-clicks`), with its call to
action and where it went, and gives the click the offer it went to, so its
conversions get that offer's payout. Lander clicks count against the
destinations' and offers' daily caps.

### Suspect clicks and conversions

Outflare flags tracker clicks and conversions that look automated, from what
it already keeps (the click's network, the keyed hash of its address and its
times), and never refuses them:

- `datacenter_network`: the click came from a cloud or hosting provider's
  network (a short list checked by hand, in `engine/internal/fraud/data`),
  where people rarely browse from. Networks that carry people through
  privacy relays (Cloudflare, Akamai, Fastly) are never on it. Direct clicks
  (from the site script) get this check too;
- `repeat_click`: the same address and browser clicked the same link more
  than ten times in an hour (the browser too, since many people can share an
  address; counted by each server apart, so a few more can slip through);
- `fast_conversion`: a conversion less than 10 seconds after its click.

A flagged click still goes where a person's would, but counts against no
destination or offer cap, as a likely bot doesn't. Links show their
`suspect_clicks`; a conversion inherits its click's reasons and shows them in
`fraud_reasons`, and `?suspect=true` (or `false`) filters the ledger.

### Destinations and daily caps

A redirect link can send its clicks to several pages instead of one: two
offers to compare, or a partner's page that takes only so many clicks a day.
Each destination has:

- a **weight**: its share of the clicks against the others' weights (100
  when left out; 0 sends it none);
- an optional **daily click cap**: how many clicks a day it takes, counted
  in the brand's reporting time zone, so it resets at the brand's midnight.
  A cap of 0 stops it.

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

### Rule-based paths

A path sends the clicks that match its conditions to destinations of its
own: mobile visitors in the US to one offer, a lab's IP range to a test page,
returning visitors to a thank-you page. A click takes the first active path,
in order, whose conditions all match and that has an open destination; a
path whose destinations are all capped is passed over. Then come the link's
default destinations, then its own page.

Conditions combine with AND, and the values in one condition with OR:
"country is US or CA, and device is mobile". Each is `is` or `is_not` on:

| Field | Values |
| --- | --- |
| `country`, `region`, `city` | Where the visitor is; countries as two-letter codes |
| `isp`, `asn` | The visitor's network, by name or number |
| `device`, `device_brand`, `os`, `os_version`, `browser`, `browser_version`, `language` | What they're on |
| `referrer` | The referring site's host, such as `news.example` |
| `ip` | Addresses or ranges, such as `203.0.113.0/24` |
| `weekday`, `hour` | `mon` to `sun` and `0` to `23`, in the brand's reporting time zone |
| `visitor` | `unique`, or `returning` within a day. Each server remembers the visitors it saw, by the keyed address hash, so one whose clicks reach different servers can count as new |
| `field1` to `field20` | The click's custom fields |

Text matches whole values, without regard to case. A link has up to 10
paths, each with up to 20 conditions and 20 destinations.

### Flows

A **flow** is a set of rule-based paths and default destinations (pages,
offers and landers) that many links share (`POST /v1/brands/{brand}/flows`).
Give a link the flow (`PUT .../links/{link}/flow` with its `flow_id`) and the
link routes by the flow's paths and destinations instead of its own, which
are kept for when the flow is taken off (`flow_id` null). An edit to the
flow's paths (`PUT .../flows/{id}/paths`) or destinations
(`PUT .../flows/{id}/destinations`) changes every link that uses it; the flow
says how many do (`links`). A cap on a flow's destination counts the clicks
of all of them. A flow that links use can't be archived.

### Suggested weights

Outflare suggests weights from how each lander, offer and page did
(`GET .../links/{link}/weight-suggestions`, or `.../flows/{id}/weight-suggestions`;
a link that routes by a flow gets the flow's). Each list of a path (its
landers, and the offers and pages after the click or after a lander) shows
each entry's clicks, conversions, revenue and its chance to be the best on
the metric: `revenue` per click (the default) or the conversion rate
(`metric=conversions`), over the last 14 days (`days`, up to 90). The
suggested weights give each entry clicks in proportion to that chance,
keeping at least 5% for each so the others go on being tested.

- Likely bots, suspect clicks and conversions, and declined conversions are
  left out, so the suggestion never learns from bots.
- A list is ready once each entry that takes clicks has 50 clicks and the
  list has 5 conversions; until then it says what's missing and suggests the
  weights it has. An entry at weight 0 stays stopped.
- Revenue in other currencies counts at the latest exchange rate, in the
  brand's currency.
- Nothing changes on its own. Apply a suggestion with
  `PATCH .../links/{link}/weights` (or `.../flows/{id}/weights`), which sets
  only the weights it names.

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

### Conversions back to the traffic source

A traffic source with a postback URL is told of each tracker conversion on
its campaigns' clicks, for networks with no conversion API (push, pop,
native). Its `postback_rules` say which ones and with what payout:

- `share`: the percentage of the payout sent (100 by default), and
  `decimals` (2) it is rounded to;
- `min_payout`: smaller payouts, after the share, aren't sent;
- `types` (every type when empty) and `statuses` (`approved` by default)
  sent;
- `resend`: a conversion is sent again when its status or payout changes,
  such as an approval days later or a reversal.

Each state of a conversion is sent once, from a job, with the tokens of the
postback URL filled in (`{source_click_id}`, `{payout}`, `{status}` and the
rest). A send is tried up to five times when the network can't be reached or
answers 429 or 5xx, never follows a redirect and only reaches public
addresses. `GET .../traffic-sources/{id}/postbacks` lists the sends with the
URL called and what the network answered.

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
| `GET`, `PUT /v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/paths` | `analytics.read`, `analytics.manage` |
| `PUT /v1/brands/{brand}/tracking-domains/{id}/fallback` | `mailboxes.manage` |
| `GET`, `POST /v1/brands/{brand}/postback-sources`; `GET`, `PATCH .../{id}`; `POST .../{id}/rotate` | `analytics.read`, `analytics.manage` |
| `GET /v1/brands/{brand}/tracker-conversions` (filter by `source`, `campaign`, `status`, `offer`) | `analytics.read` |
| `GET /v1/brands/{brand}/affiliate-network-templates` | `analytics.read` |
| `GET`, `POST /v1/brands/{brand}/offers`; `GET`, `PATCH .../{id}` | `analytics.read`, `analytics.manage` |
| `GET`, `POST /v1/brands/{brand}/landers`; `GET`, `PATCH .../{id}` | `analytics.read`, `analytics.manage` |
| `GET`, `POST /v1/brands/{brand}/flows`; `GET`, `PATCH .../{id}`; `PUT .../{id}/destinations`, `PUT .../{id}/paths` | `analytics.read`, `analytics.manage` |
| `GET`, `PUT /v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/flow` | `analytics.read`, `analytics.manage` |
| `GET .../links/{link}/weight-suggestions`, `PATCH .../links/{link}/weights`; `GET /v1/brands/{brand}/flows/{id}/weight-suggestions`, `PATCH .../flows/{id}/weights` | `analytics.read`, `analytics.manage` |
| `GET /v1/brands/{brand}/lander-clicks` (filter by `lander`, `click_id`) | `analytics.read` |
| `GET /click`, `GET /click/{n}`, `GET /click.js` (a lander's calls to action, on the brand's tracking domain) | Public |
| `POST /v1/brands/{brand}/tracker-conversions`, `POST .../tracker-conversions/csv` | `analytics.manage` |
| `GET /v1/brands/{brand}/tracker-conversion-log` (filter by `source`, `via`, `outcome`, `click_id`) | `analytics.read` |
| `GET`, `POST /t/{secret}` (a postback, on the brand's tracking domain) | Public |
| `GET /t/{secret}/pixel`, `GET /t/{secret}/pixel.js` | Public |
| `GET /v1/brands/{brand}/traffic-source-templates` | `analytics.read` |
| `GET`, `POST /v1/brands/{brand}/traffic-sources` | `analytics.read`, `analytics.manage` |
| `GET`, `PATCH /v1/brands/{brand}/traffic-sources/{id}` | `analytics.read`, `analytics.manage` |
| `GET /v1/brands/{brand}/traffic-sources/{id}/postbacks` (filter by `status`) | `analytics.read` |
| `GET /l/{code}` (a redirect link) | Public |

Owners, admins and operators have `analytics.manage`; viewers can read.

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"Spring launch, US","channel":"paid_ads","source":"Google Ads","landing_url":"https://northpeak.example/pricing"}' \
  "$URL/v1/brands/$BRAND/tracking-campaigns"
```

The MCP tools are `get_tracking_taxonomy`, `list_tracking_campaigns`,
`get_tracking_campaign`, `create_tracking_campaign`,
`update_tracking_campaign`, `list_tracking_links`, `create_tracking_link`,
`list_tracking_link_destinations`, `set_tracking_link_destinations`,
`list_tracking_link_paths`, `set_tracking_link_paths`, `set_tracking_domain_fallback`,
`list_postback_sources`, `create_postback_source`, `update_postback_source`,
`rotate_postback_source`, `list_tracker_conversions`, `upload_tracker_conversions`, `list_tracker_conversion_log`,
`list_affiliate_network_templates`, `list_offers`, `get_offer`, `create_offer`, `update_offer`,
`list_landers`, `get_lander`, `create_lander`, `update_lander`, `list_lander_clicks`,
`list_flows`, `get_flow`, `create_flow`, `update_flow`, `set_flow_destinations`, `set_flow_paths`, `set_tracking_link_flow`,
`get_tracking_link_weight_suggestions`, `set_tracking_link_weights`, `get_flow_weight_suggestions`, `set_flow_weights`,
`list_traffic_source_templates`, `list_traffic_sources`, `list_traffic_source_postbacks`,
`create_traffic_source` and `update_traffic_source`.

Each change is an audit action (and a product event): `tracking_campaign.created`,
`tracking_campaign.updated`, `tracking_campaign.archived`,
`tracking_campaign.restored`, `tracking_link.created`, `tracking_link.redirect_added`,
`tracking_link.destinations_updated`, `tracking_link.paths_updated`, `tracking_domain.fallback_set`,
`postback_source.created`, `postback_source.updated`, `postback_source.archived`, `postback_source.restored`,
`postback_source.rotated`, `tracker_conversion.created`, `tracker_conversion.updated`, `tracker_conversion.uploaded`,
`tracker_offer.created`, `tracker_offer.updated`, `tracker_offer.archived`, `tracker_offer.restored`,
`tracker_lander.created`, `tracker_lander.updated`, `tracker_lander.archived`, `tracker_lander.restored`,
`tracker_flow.created`, `tracker_flow.updated`, `tracker_flow.archived`, `tracker_flow.restored`, `tracker_flow.paths_updated`,
`tracker_flow.destinations_updated`, `tracking_link.flow_set`, `tracking_link.weights_updated`, `tracker_flow.weights_updated`,
`traffic_source.created`, `traffic_source.updated`, `traffic_source.archived`
and `traffic_source.restored`.
