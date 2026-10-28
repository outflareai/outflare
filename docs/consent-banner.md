# Consent banner

The website script (`/site.js`) can show your cookie consent banner itself, so it replaces a separate banner tool such as Cookiebot, OneTrust or CookieYes. Tracking, ad conversions sent back and the banner then share one consent state, and each visitor's choice is kept as a consent on their customer record.

Every law and framework is on by default, and you can turn each one off per site:

- **Ask first** in the EEA, the UK and Switzerland (GDPR, UK GDPR, the Swiss FADP).
- **Opt-out** in the US, with a "Do not sell or share my personal information" button (the CCPA and the other state laws).
- **Global Privacy Control**: browsers that send it get no marketing.
- **Google Consent Mode v2**, plus the **Meta**, **TikTok** and **Microsoft** consent signals.

## Turn it on

In the web app, open **Analytics → Website tracking**, choose the site and open **Consent banner**. You can also use the API (`PUT /v1/workspaces/{ws}/site-keys/{id}/consent-banner`) or MCP (`update_consent_banner`):

```json
{ "enabled": true, "policy_url": "/cookies" }
```

The site needs the snippet only. Visitors see the banner on their next page, or within five minutes if they already have the site open.

## Who sees what

Each region has a rule:

| Rule | What the visitor gets | Default for |
|---|---|---|
| `opt_in` | Analytics, marketing and preferences stay off until they accept. Accept all and Reject all look the same, as European regulators require. | `eea` (the EEA, the UK and Switzerland) |
| `opt_out` | Everything is on. The banner says so once, with a "Do not sell or share my personal information" button that turns marketing off. | `us` |
| `notice` | Everything is on. The banner says so once. | |
| `none` | Everything is on, with no banner. | `other` |

The region comes from the visitor's country when your own-domain proxy passes it in `X-Outflare-Country`, together with the key's proxy secret (see [website tracking](website-tracking.md)), for example from Cloudflare's `CF-IPCountry` or Vercel's `x-vercel-ip-country`. Otherwise it comes from the browser's time zone. US states can't be told apart reliably from a browser, so `us` covers the whole country.

With Global Privacy Control on (`frameworks.gpc`, the default), a browser that sends it starts with marketing off in every region. The visitor can still turn it on themselves.

## Categories

**Necessary** is always on: it covers remembering the choice itself (the `of_consent` cookie). The banner asks about **analytics**, **marketing** and **preferences**; turn off any category your site doesn't use (`categories`). The analytics choice also decides whether the visitor's id is kept in the `of_aid` cookie.

Keep other tags back until their category is allowed: give them `type="text/plain"` and `data-category`, and give an iframe `data-src` instead of `src`:

```html
<script type="text/plain" data-category="marketing" src="https://ads.example/pixel.js"></script>
<script type="text/plain" data-category="analytics">startStats();</script>
<iframe data-category="marketing" data-src="https://video.example/embed/123"></iframe>
```

They run as soon as their categories are allowed: straight away for a visitor who allowed them before, or when the visitor clicks Accept. Tags added later, by your own code or a tag manager, are checked too. A tag that lists several categories (`data-category="analytics marketing"`) needs all of them.

## Ad platforms

When the banner is on, the script sets these for you (each one can be turned off under `frameworks`):

- **Google Consent Mode v2:** `gtag('consent', 'default', ...)` on the first page, then `update` on each choice. `ad_storage`, `ad_user_data` and `ad_personalization` follow marketing, `analytics_storage` follows analytics, `functionality_storage` and `personalization_storage` follow preferences, and `security_storage` is always granted. For the default to land before Google's tags, put the snippet above your Google tag or Tag Manager container.
- **Meta:** `fbq('consent', 'grant' | 'revoke')`.
- **TikTok:** `ttq.holdConsent()` while waiting, then `grantConsent()` or `revokeConsent()`.
- **Microsoft UET:** `uetq.push('consent', 'default' | 'update', {ad_storage})`.

Outflare's own conversion send-back ([paid media](paid-media.md)) reads the same choice: marketing sets the `ads` consents (`ad_user_data` and the rest) that it checks before sending.

## Looks

- **Layout:** `bar` (across the page, at the `top` or `bottom`), `box` (a card at `bottom_right`, `bottom_left`, `bottom`, `top` or `center`), or `modal` (in the middle, over the page).
- **Theme:** `light`, `dark` or `brand`. Brand uses the colours you set under `colors`, otherwise the colours in the Company hub (**Company → About your company → Logo and colours**), or a brand's own when the site key belongs to a brand that has them (`logo_url` and `colors` on the brand, through the API or MCP `update_brand`). `colors` overrides any theme. The logo comes from the Company hub too, unless you set `logo_url` or turn off `show_logo`.
- **Reopen button:** after a choice, a small button in the corner lets the visitor change their mind (`reopen_button`). Any element with `data-outflare-consent-open`, such as a "Cookie settings" link in your footer, opens the banner too.
- **Words:** Outflare's own come in English, German, French, Spanish, Italian, Dutch and Portuguese, in the visitor's language. Override any of them per language under `texts`, for example `{"en": {"title": "Cookies at Northpeak"}}`.
- **Your own CSS and HTML:** `custom_css` is added after the preset's styles. `custom_html` replaces the banner's markup: buttons with `data-of-consent="accept"`, `"reject"`, `"customize"` or `"save"` and checkboxes with `data-of-category="analytics"` work as in the presets, and any element can call `outflare.consent(...)`. The banner sits in a shadow root, so your site's styles don't leak into it. Both run on your website, so changing them needs the `workspace.manage` scope.

## From your own code

```js
outflare.consent();                       // {necessary, analytics, marketing, preferences, decided, region, rule, gpc}
outflare.consent(true);                   // accept everything
outflare.consent(false);                  // refuse or withdraw everything
outflare.consent({ marketing: false });   // change some
outflare.showConsent();                   // open the banner on its categories
outflare.onConsent(function (c) { ... }); // now, and on every change
```

These calls work without Outflare's banner too: a site with its own banner can keep it and still have each choice kept in Outflare. Add `data-banner="off"` to the snippet if Outflare's banner should never show on a page.

## Proof of consent

Each choice that changes something is kept as consents (source `consent_banner`), one per category the banner asks about (channel `web`), plus Google's three ad purposes (channel `ads`). Each one records:

- what the visitor chose and how (`accept_all`, `reject_all`, `custom`, `opt_out`, `gpc` or `api`)
- the banner's `version` and `consent_version`
- the region, the country when known, Global Privacy Control, the language and the page's path

Before the visitor is known, the consents are kept on their anonymous id. When your product sends `identify` with that `anonymousId`, they join the person's record with their earlier visits and events, and merges move them like everything else on a record. Read them with `GET /v1/workspaces/{ws}/records/{record}/consents`.

## Asking again

- A visitor's choice is asked for again after `reask_after_days` (365 by default; 0 never asks again) where the rule is `opt_in`.
- Starting to ask about a new category asks every visitor again.
- **Ask again** (`POST .../consent-banner/reask`, MCP `reask_consent_banner`) raises `consent_version`, for example after your site starts using a new kind of cookie.

## IAB TCF

The banner doesn't speak IAB's Transparency and Consent Framework (`__tcfapi`), which mostly matters to publishers selling ads through programmatic exchanges. Only a consent platform registered with IAB Europe may run it, so it can be added later if a publisher needs it.
