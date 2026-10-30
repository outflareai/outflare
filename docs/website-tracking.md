# Website tracking

Website tracking records visits to your own sites as product events, so sign-ups and revenue can be credited to the channel, campaign or partner that brought each person. It captures what the visitor arrived with, exactly as it came. Channels and attribution are worked out on the server from these raw events, never in the browser.

## Set it up

The quickest way is **Analytics → Website tracking** (the same step as **Company → Brand setup**, or `GET .../brands/{brand}/setup`): three steps, get the script, add it to the site, then see the first visit. It makes the key and offers one click through your own Google Tag Manager, a message to send a developer, a prompt for an AI coding tool, or the snippet to paste. The step ticks off when the first visit arrives. By hand:

1. Add a site key: **Analytics → Website tracking** (also under **Settings → General**), `POST /v1/workspaces/{ws}/site-keys` (scope `customers.manage`) or MCP `create_site_key`. Give it the site's name. You can also set `origins`, the sites it takes events from. A plain domain, `example.com`, covers the site and every subdomain (www, docs, app) over https, so one key serves them all; it is stored as `https://*.example.com`. List any other origin exactly, port included (`https://shop.example.com:8443`, or `http://localhost:3000` while you try it locally; http is taken only from localhost), and a `brand_id` if the site belongs to one brand.
2. Paste the key's `snippet` into each page's `<head>`:

   ```html
   <script async src="https://app.outflare.ai/site.js" data-key="ofk_test_not_a_real_key"></script>
   ```

   Self-hosted installs serve `/site.js` from their own address. To keep ad blockers from dropping events and to keep the visitor's cookie for its full year, serve it from your own domain instead (see [Serve it from your own domain](#serve-it-from-your-own-domain)).
3. When someone signs up or signs in, have your product's server send `identify` with their `userId` and the visitor's `anonymousId` to `POST .../customer-events`. In the browser, `outflare.anonymousId()` returns it, and it is also in the first-party `of_aid` cookie. Their earlier visits then join their record.

## Feature flags

With `data-flags` on the tag, the script also asks for the visitor's [feature flags](feature-flags.md): `outflare.onFlags(fn)`, `outflare.flag("key")` and a class on `<html>` per version, for switches and A/B tests.

## Keeping it current

`site.js` sends its version with every batch, and each site key keeps the last one it saw (`script_version`). Install health (`GET .../install-health`, MCP `get_install_health`) flags a key that never sent a visit a day after it was made, one that went quiet for two days, and one whose pages run a copy older than the one this server serves. Each shows on the brand's setup page and on Home until it's fixed.

## What it records

On every page, including in-app navigation, the snippet sends a `page` event on the visitor's random `anonymousId` with:

- `context.page`: the URL, host, path, query string, referrer and title. The URL and query string keep every parameter, so reports can filter on any of them, except the ones the site key's `excluded_params` lists (names such as `plan`, or prefixes such as `fb_*`) and the ones always left out: sign-in tokens, passwords, codes, session ids, signatures, and any value that is an email address or a signed token. So a sign-in link or an `?email=` in an address never reaches your data. The referrer, another site's address, keeps only its origin and path.
- `context.campaign`: the `utm_source`, `utm_medium`, `utm_campaign` (as `name`), `utm_term`, `utm_content` and `utm_id` tags.
- `properties.click_ids`: whichever ad click ids the page had (`gclid`, `gbraid`, `wbraid`, `dclid`, `fbclid`, `msclkid`, `li_fat_id`, `rdt_cid`, `twclid`, `ttclid`, `ScCid`, `epik`, `tblci`, `ob_click_id`, `qclid`, `irclickid`) and, once the visitor allows marketing, the cookies ad pixels set: `fbp` and `fbc` from Meta's `_fbp` and `_fbc`, `gcl_aw` from Google's `_gcl_aw`, `ttp` from TikTok's `_ttp` and `scid` from Snapchat's `_scid`. Track events carry the cookies too, so a conversion sent back to a network can be matched to the click.
- `properties.ad`: the network's own ids from a [tracking template](#ad-tracking-templates) (`of_campaign`, `of_adset`, `of_ad`, `of_placement`), as `campaign`, `adset`, `ad` and `placement`. A macro the network left unfilled, such as `{{ad.id}}`, is dropped.
- `properties.ofc` (an Outflare campaign link) and `properties.ref` (a referral or partner code, from `ref` or `via`).
- `properties.ofclk` on page views: the click key a [redirect link](campaign-links.md#redirect-links) adds to the page it sends people to. The visit claims that click, once, so the link can show how many of its clicks reached the site.

`outflare.track("Signup Started", {plan: "pro"})` sends a track event and returns its message id, which you can give an ad pixel as its event id so a conversion sent back from [Paid ads](paid-media.md#sending-conversions-back) counts once. Its name must be a plain one: dotted names such as `payment.succeeded` belong to Outflare and its connectors, and a browser can't send them. A name the workspace hasn't defined arrives quarantined in the catalog, like any other. Once 100 names are waiting for approval, the browser can only send names that are already there.

## Visits, touches and devices

Each event joins a visit: a session that ends after 30 minutes without an event, or when the visitor comes back through a different campaign (new tags, a new ad click id or ad, a `ref` or an `ofc`; an ad pixel's cookie never starts one). The visit's id is in `context.sessionId`. A visit keeps its landing page, referrer, tags, click ids, ad ids, `ref` and `ofc`, plus a rough country (from the browser's time zone, not its address), device type (desktop, mobile, tablet), browser and system. A visit from another page of your own site, or from another of the key's origins, is not counted as a referral.

When your product identifies a visitor, their visits join their record, and Outflare works out these fields on the person (computed, so nobody writes them):

- `first_touch_at`, `first_touch_source`, `first_touch_medium`, `first_touch_campaign`, `first_touch_referrer`, `first_touch_landing_page`: their first visit.
- The same with `last_touch_`: their latest visit that came from somewhere, or their latest visit if all were direct.
- `referral_code`: the partner code (`ref`) on their latest visit that had one.

The source is the visit's `utm_source`, else the ad network behind its click id (`gclid` is google, `ttclid` tiktok, `fbclid` facebook), else the site that linked to it, else `direct`. The medium is `utm_medium`, else `cpc`, `referral` or `none` the same way. Channels and attribution models are worked out from the visits on the server. When two records merge, their visits move together and the touches are worked out again.

Requests from bots, crawlers, link previews, monitors and scripts are counted on the key (`bots_today`) and not stored. A browser driven by automation (`navigator.webdriver`) sends nothing. When a request fails on the network or with a server error, the snippet tries again after 1, 5 and 25 seconds. Each event has its own `messageId`, so a retry is never stored twice.

## Ad tracking templates

A tracking template is one string you paste into an ad network once, so every paid click lands with UTM tags and the network's own campaign, ad set and ad ids. That is how media buyers set up Hyros, RedTrack and Voluum, and it lets a sale be credited to an ad by its id, even after the ad is renamed. **Paid ads → Tracking templates**, `GET /v1/workspaces/{ws}/ads/tracking-templates` (scope `analytics.read`) or MCP `list_ad_tracking_templates` give each network's template, the field it goes in, the click id the network adds by itself and the cookies of its pixel.

For Meta, paste this into the ad's **URL parameters**:

```text
utm_source=meta&utm_medium=cpc&utm_campaign={{campaign.name}}&utm_content={{ad.name}}&of_campaign={{campaign.id}}&of_adset={{adset.id}}&of_ad={{ad.id}}&of_placement={{placement}}
```

There are templates for Google Ads and Microsoft Advertising (the final URL suffix), Meta, LinkedIn, Reddit, TikTok, Snapchat, Pinterest, Taboola and Outbrain. X and Amazon Ads have no macros for their ids, so their template is the tags alone. LinkedIn sets its values on the campaign: add each parameter there and choose the dynamic value named after the `=`. Apple Ads (formerly Apple Search Ads) send people to your app, not your site, so they have none. A template works without connecting the network: connecting it in [Paid ads](paid-media.md) adds its spend, and sends conversions back.

The script reads click ids and `of_` parameters on any page, and the pixel cookies only after consent (see [Consent](#consent)).

### Tracking health

Every landing page from a paid ad (a paid `utm_medium` such as `cpc` or `paid_social`) is counted by how well it was tracked: with the network's own click id, with only the template's ad ids, or with neither. Neither means the ad has no tracking template, so the visit can be credited only by its UTM tags. A template value that arrives as the macro itself (`{{ad.id}}`, `__CID__`) is dropped and counted too, because it means the network didn't fill it in. Operators see both on the **Ad tracking health** row of the Grafana dashboard, with an alert for each ([monitoring.md](monitoring.md)). The counts carry the network only, never the workspace or the visit.

Each workspace sees its own under **Paid ads → Tracking health** (and `GET /v1/workspaces/{ws}/ads/tracking-health`, MCP `get_ad_tracking_health`). It covers the last `days` (7 by default) and shows:

- **Paid visits per network:** the share with the click id, with only the ad ids, or untracked.
- **Synced campaigns that spent with no tracked visit:** these usually lack the template.
- **Conversions sent back per platform:** with the commonest reason one was refused.

A list of issues comes first, worst first, each with what to do. An issue is raised when:

- a network has at least 20 paid visits and a fifth or more of them are untracked;
- any campaign spent without a tracked visit;
- a platform refused a tenth or more of the conversions sent to it.

## Serve it from your own domain

Ad blockers drop scripts and requests to analytics hosts, and Safari keeps a cookie a script writes for only seven days. Forward a path on your own site to Outflare, and load the snippet from it:

```html
<script async src="/ofx/site.js" data-key="ofk_test_not_a_real_key"></script>
```

The snippet sends its events to the same path (`/ofx/v1/site/events`), so they go to your own domain. When the visitor has consented, the answer sets `of_aid` from the server on your domain, which browsers keep for its full year, and your own server can read the cookie at sign-up to send `identify`.

Forward `/ofx/site.js` and `/ofx/v1/site/events` to `https://app.outflare.ai/site.js` and `https://app.outflare.ai/v1/site/events` (or your own install). For example, with a Next.js rewrite:

```js
rewrites: async () => [{ source: "/ofx/:path*", destination: "https://app.outflare.ai/:path*" }]
```

or with nginx:

```nginx
location /ofx/ { proxy_pass https://app.outflare.ai/; proxy_set_header Host app.outflare.ai; proxy_ssl_server_name on; }
```

Cloudflare Workers, Vercel and Netlify rewrites work the same way. `data-api` sets where events go when it isn't next to the script.

Every event then comes from your server's address, so Outflare can't see the visitor's own address (which per-visitor limits use, and features that work out a visitor's company may too). To pass it on, make the key a **proxy secret** (**Proxy secret** on the key, `POST .../site-keys/{id}/proxy-secret` or MCP `create_site_proxy_secret`). It starts `ofp_` and is shown once. Keep it in your proxy's configuration, never in a page. Have the proxy send it in `X-Outflare-Proxy` and the visitor's address in `X-Outflare-Client-IP`, replacing any value the browser sent. For example, with nginx:

```nginx
location /ofx/ {
  proxy_pass https://app.outflare.ai/; proxy_set_header Host app.outflare.ai; proxy_ssl_server_name on;
  proxy_set_header X-Outflare-Proxy "ofp_test_not_a_real_secret";
  proxy_set_header X-Outflare-Client-IP $remote_addr;
}
```

A Cloudflare Worker sets the same two headers, taking the address from `CF-Connecting-IP`. A plain Next.js rewrite can't add headers, so use middleware there. If your site sits behind a CDN, take the address from the CDN's own header (such as `CF-Connecting-IP`), never from the first `X-Forwarded-For` entry, which the browser can set. Outflare takes the address only when the secret matches and it is a public address, and otherwise uses the connecting address. With a proxy secret, each visitor's address can send up to 600 requests a minute. A new secret replaces the old one at once, and `DELETE .../proxy-secret` removes it.

With the secret, the proxy can also pass the visitor's country in `X-Outflare-Country` (two letters, such as `DE`), from its CDN's geolocation (`CF-IPCountry` on Cloudflare, `x-vercel-ip-country` on Vercel). The [consent banner](consent-banner.md) then picks the region's rule by country instead of the browser's time zone.

## Sites on more than one domain

A visitor who goes from `northpeak.example` to `northpeak-app.example` would get a new id on the second. List the domains in `data-domains` and links between them carry the visitor's id (`_ofa`, valid for two minutes so a shared link doesn't join its readers), which the snippet on the other side takes and removes from the address. It takes the id only when the visitor came from another listed domain (the browser's referrer), so a copied link posted somewhere else starts a new visitor; keep the default referrer policy, or at least `strict-origin-when-cross-origin`, on those pages:

```html
<script async src="https://app.outflare.ai/site.js" data-key="ofk_test_not_a_real_key" data-domains="northpeak.example,northpeak-app.example"></script>
```

The key's snippet already lists them when its origins are on more than one domain. Subdomains of one domain need nothing: the cookie is on the whole domain.

## What a site key can't do

A site key sits in your page's HTML, so anyone can copy it. It can only add browser events:

- It sends `page` and `track` only. A `userId`, traits, and identify, group or alias calls are refused or dropped, so a browser can never name, create or merge a person. The time is the server's, not the browser's.
- It reads nothing. The answer only counts what was taken, and the key works on no other endpoint.
- Browser events never start a journey or send email, never meet a journey's event condition, and never count as revenue. Their `source` is `browser`, and the server API refuses that source for anything else.
- `origins` filters out stray traffic, such as a copy of your page on another site. It is not a lock, since anyone can send any `Origin`.
- Limits: 20 events per request, 6,000 requests a minute per key, 250,000 events a day per key and 1,000,000 a day per workspace. An address that keeps sending keys that don't exist is turned away for the minute.

**Rotate** gives a key a new value, and the old one keeps working for seven days so you can update the site. **Revoke** stops it at once. Both are in the audit log (`site_key.rotated`, `site_key.revoked`).

## Consent

With consent, the snippet keeps the visitor's id in a first-party cookie, `of_aid`, for a year. Where consent is needed, it sets no cookie until you call `outflare.consent(true)`. Until then the id lasts for the visit: it is kept in the tab's session storage (`of_vid`), so every page of the visit is the same visitor and sees the same [feature flag](feature-flags.md) versions, and it is gone when the tab closes. Visits before consent aren't linked to each other. Consenting keeps the same id in the cookie. `outflare.consent(false)` removes the cookie.

- With `data-consent="auto"` (the default), visitors whose time zone is in the EEA, Switzerland or the UK, and anyone with Global Privacy Control on, start without a cookie.
- `data-consent="required"` does the same for everyone.
- `data-consent="granted"` is for sites whose own banner asks first and only loads the snippet afterwards.

Ad pixels' cookies (`_fbp`, `_fbc`, `_gcl_aw`, `_ttp`, `_scid`) are read only once the visitor allows the marketing category, whatever they chose for analytics. Without a consent banner, marketing follows the analytics answer. Click ids and `of_` ad ids come from the page's address, so they are read either way, and are kept only on that page's visit until consent.

Without consent, the campaign tags a visitor landed with are on that first page's visit only. If they sign up later, on another visit or another domain, Outflare can't join the two. On a site without a consent banner, keep the landing page's `utm_*`, click ids, `ref` and `ofc` (for example in the sign-up form or its URL) and send them with the sign-up `identify`: the tags in `context.campaign` and the click ids in `properties.click_ids`, as the snippet does. The sign-up is then credited even when the visits can't be joined.

To have Outflare show the banner itself, with region rules, Google Consent Mode and the ad platforms' consent signals, turn on the [consent banner](consent-banner.md) for the site key. `outflare.consent()` then also takes categories (`{analytics: true, marketing: false}`), and every choice is kept as the visitor's consent.

`outflare.reset()` gives the visitor a new id, for example on sign-out on a shared computer. Nothing is sent to Outflare's own analytics. The events stay in your workspace with the rest of your customer data.
