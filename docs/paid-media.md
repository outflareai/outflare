# Paid media

Outflare reads what your ad accounts spend and what the platforms say it
brought in, so a person, a script or an agent can ask "what did ads cost this
month, per brand, and what did they return?" without opening six dashboards.
It can also send conversions back: when someone the ads brought signs up or
pays, the platform hears about it, so its bidding learns from real results.
Outflare never changes campaigns, budgets or ads.

Every connection is the workspace's own: it signs in to its own ad accounts and,
where the platform needs one, adds its own developer token. Outflare never
provides an ad account, an API token or a developer token, and an ad platform
stays off until the workspace connects it.

## Platforms

| Platform | Connect with | Also needs |
| --- | --- | --- |
| Google Ads (`google_ads`) | Sign in with Google (read-only Google Ads access). Manager accounts list the accounts under them. | The workspace's own developer token, from its Google Ads manager account (API Center). |
| Meta Ads (`meta_ads`) | A system user access token from your own Business Manager with `ads_read`, or sign in with Facebook | Nothing |
| Microsoft Advertising (`microsoft_ads`) | Sign in with Microsoft | The workspace's own developer token, from the Microsoft Advertising Developer Portal. |
| Reddit Ads (`reddit_ads`) | Sign in with Reddit (read-only ads access) | Nothing |
| X Ads (`x_ads`) | Your own X developer app's API key and secret, plus an access token and secret for the account that runs the ads | Ads API access approved for that app by X |
| LinkedIn Ads (`linkedin_ads`) | Sign in with LinkedIn (`r_ads`, `r_ads_reporting`) | Advertising API access approved for the app by LinkedIn |
| TikTok Ads (`tiktok_ads`) | Sign in with TikTok For Business and choose the ad accounts to share (Ad Account Management and Reporting read access). To add accounts later, connect again. | Nothing |
| Snapchat Ads (`snapchat_ads`) | Sign in with Snapchat (`snapchat-marketing-api`). Every ad account of every organization you belong to is listed. | Nothing |
| Taboola Ads (`taboola_ads`) | Your own Backstage API client id and secret, and optionally an account id. A network account reads every advertiser account under it; without an account id, the credentials' own account is read. | Backstage API access, which your Taboola account manager grants and issues the credentials for |
| Outbrain Ads (`outbrain_ads`) | Your own Outbrain username and password. Outflare trades them for an Amplify API token (`GET /login`), keeps the token sealed with them and gets a new one every 25 days (a token lasts 30). Every marketer the login can see is listed; spend is in the currency of the marketer's budgets. | Amplify API access for that login, which you request from Outbrain |
| Apple Ads (`apple_ads`, formerly Apple Search Ads) | Your own Apple Ads API user: its client id, team id and key id from Account Settings → API, and the private key whose public half you uploaded there (see below). Every ad account the user can reach is listed, App Store and Apple Maps. | An API role for that user (API Account Read Only is enough) |
| Amazon Ads (`amazon_ads`) | Sign in with Amazon (Login with Amazon, `advertising::campaign_management`). Every advertising profile the sign-in reaches is an ad account: one advertiser in one marketplace, with its currency and time zone, in North America, Europe or the Far East. Sponsored Products campaigns are read; their daily numbers come from Amazon's reports, which Amazon builds in a few minutes, so a sync can take longer than for other platforms. Purchases and sales are Amazon's seven-day attribution. | Nothing |
| Pinterest Ads (`pinterest_ads`) | Sign in with Pinterest (`ads:read`, read-only) | Nothing |

**Apple Ads** has no sign-in: it uses OAuth client credentials that belong to
your own Apple Ads organization. An account administrator invites a user with
an API role; that user makes a P-256 key pair
(`openssl ecparam -genkey -name prime256v1 -noout -out private-key.pem`, then
`openssl ec -in private-key.pem -pubout -out public-key.pem`), pastes the public
key into Apple Ads under Account Settings → API, and gives Outflare the client
id, team id and key id shown there with the private key. Outflare keeps them
sealed, signs a short-lived client secret with the key when it syncs, and trades
it with Apple for a one-hour access token, held only in memory. It reads through
the Apple Ads Platform API, which replaces the Campaign Management API (v5) that
Apple turns off on January 26, 2027. Taps are the clicks; for App Store
campaigns, total installs (tap and view-through) are the conversions. Apple
Maps campaigns report spend, impressions and taps only.

Signing in uses the operator's OAuth app (`CONNECTOR_<ID>_CLIENT_ID` and
`_CLIENT_SECRET`, see [connectors.md](connectors.md)); the tokens it produces
belong to the workspace's own ad account. Amazon Ads also sends that app's
client id (not its secret) with every call, as Amazon requires, so the
connection keeps it beside the workspace's tokens. When a connection still needs a
developer token, `GET /v1/workspaces/{ws}/ads/connections` lists it under
`setup_needed`, and a person adds it in Integrations
(`PUT /v1/workspaces/{ws}/ads/connections/{id}/setup`). Outflare checks the
token against the platform before storing it, sealed like every other
credential.

## What is stored

A sync runs every six hours, and a failed one is tried again sooner: after 15 minutes, then 1 and 2 hours, then every 4 hours until one works. The first one reads 90 days; later runs read the
last 7 days, and the last 30 once a day, because platforms keep revising recent
numbers. Each run stores:

- **Ad accounts**, with currency and time zone. Each account can belong to a
  brand (`PATCH /v1/workspaces/{ws}/ads/accounts/{id}`) or be shared, and can be
  switched off.
- **Campaigns**, with status, objective and daily budget.
- **Daily campaign metrics**: spend, impressions, clicks, and the platform's own
  conversions and conversion value where it reports them. Days are the account's
  own days, in its time zone.
- **Spend in the costs ledger**: one entry per campaign per day, channel
  `paid_ads`, in the account's currency and brand, so paid ads sit beside every
  other cost and outcome. An entry is updated when the platform revises the day.

Conversions and ROAS here are **platform-reported**: each platform's own count,
with its own attribution window. They are not Outflare's attribution, and they
are not added up across platforms as if they were.

## Reading it

In the app, open **Paid ads** in the sidebar: spend, clicks, CTR, cost per click and platform conversions for 7, 28 or 90 days, by platform and campaign, with each account's brand and sync switch. A connection that still needs a developer token asks for it at the top.

| Call | What it answers |
| --- | --- |
| `GET /v1/workspaces/{ws}/ads/accounts` | Every account, its brand and sync status |
| `GET /v1/workspaces/{ws}/ads/campaigns` | Campaigns ranked by spend over `from`–`to`, with CPC, CTR, cost per conversion and platform ROAS |
| `GET /v1/workspaces/{ws}/ads/summary` | Totals grouped by `connector`, `brand`, `account` or `day`, per currency |
| `POST /v1/workspaces/{ws}/ads/sync` | Sync now instead of within six hours |

All of them take `brand`. Money is in micros of the account's currency
(1,000,000 = one unit); totals are given per currency, never converted. The MCP
server has a tool for each (`list_ad_accounts`, `list_ad_campaigns`,
`get_ads_summary`, `sync_ads`, `list_ad_connections`, `update_ad_account`).

To credit sales to each ad by its id, paste the network's tracking template
into it ([website tracking](website-tracking.md#ad-tracking-templates)): every
paid click then lands with the network's own campaign, ad set and ad ids.

## Sending conversions back

A conversion rule sends one customer event (such as `signed_up` or
`order_completed`, from the website snippet, the API or a connected tool) to one
conversion on one ad account:

| Platform | Where it goes | Matched on | Also needs |
| --- | --- | --- | --- |
| Google Ads | A conversion action that takes uploaded clicks (Upload clicks) | `gclid`, `gbraid` or `wbraid`; hashed email and phone (enhanced conversions for leads) | The developer token above; the sign-in needs Google Ads access that can upload |
| Meta Ads | A pixel (dataset), as a standard event such as Purchase or Lead | `fbc` (from `fbclid`), `fbp`, hashed email, phone and record id | The system user assigned to the pixel |
| Microsoft Advertising | An offline conversion goal | `msclkid`; hashed email and phone | The developer token above |
| Reddit Ads | The Conversions API, as SignUp, Lead, Purchase and so on | `rdt_cid`, hashed email and record id | The workspace's own Conversions API access token, from Reddit Ads Manager → Events Manager |
| TikTok Ads | A pixel, through the Events API, as CompletePayment, CompleteRegistration, SubmitForm and so on | `ttclid`, the `_ttp` cookie (`ttp`), hashed email, phone and record id | The sign-in's Pixel and Events access; if TikTok refuses it, an Events API access token from Events Manager on the connection |
| Snapchat Ads | A Snap Pixel, through the Conversions API, as PURCHASE, SIGN_UP, SUBSCRIBE and so on | `ScCid`, the `_scid` cookie (`scid`), hashed email, phone and record id | The sign-in's access; if Snapchat refuses it, a Conversions API token from Ads Manager → Business Details on the connection |
| Taboola Ads | An active event-based conversion (Realize → Tracking, "Create conversions using code"), by its event name, as a server-to-server action | `tblci` only | Nothing: the bulk S2S endpoint takes no token |
| Pinterest Ads | The Conversions API, as checkout, signup, lead and so on | `epik`, hashed email, phone and record id | The workspace's own conversion access token, from Pinterest Ads Manager → Conversions |

Taboola matches only on its click id: a conversion with no `tblci` from the
click is skipped. Taboola accepts each batch at once and processes it later
without reporting back, so `sent` there means Taboola received it; check the
conversion's numbers in Realize. Revenue goes only in the currencies Taboola
lists for S2S (USD, EUR, GBP and 16 others); in any other currency the
conversion is sent without it.

Apple Ads takes no conversions: its ads open the App Store, not your website,
and Apple measures installs in your app through its AdServices framework, so
there is nothing for Outflare to send back.

Amazon Ads doesn't take conversions back yet: its conversion events match on
hashed identifiers per advertiser account rather than on an ad click, and are
still in beta.

How it works:

- Every two minutes Outflare matches new events to the enabled rules and sends
  them, up to 500 per call. Only events that arrive after the rule was made are
  sent, never older ones, and only within a week of arriving.
- The ad click comes from the event itself (`click_ids`) or from the person's
  latest visit with that platform's click id in the 90 days before it. Email and
  phone come from the person's record, hashed with SHA-256 before they leave;
  nothing is sent in the clear and nothing is copied into the send log.
- The event's message id is the conversion's id (Google's order id, Meta's,
  Reddit's, TikTok's, Snapchat's and Pinterest's event id, Taboola's order id). On the website, `outflare.track()` returns it: pass it to
  the browser pixel as its event id (`fbq('track', 'Purchase', {}, {eventID:
  id})`) and the platform counts a conversion once when both send it.
- The value comes from an event property (in major units, 12.50, or minor units,
  1250) or a fixed value; the currency from the rule, the event's `currency`
  property or the account's.
- **Consent.** In the EEA, the UK and Switzerland, and whenever a person's
  country is unknown, a conversion is sent only when they said yes to ad data
  (a consent with channel `ads` and purpose `ad_user_data`, recorded with
  `POST /v1/workspaces/{ws}/records/{id}/consents` when your consent banner
  says yes). Elsewhere the workspace's setting decides: **Everyone,
  unless they opted out** (`opt_out`, the default) or **Only people who said
  yes** (`opt_in`). Anyone who withdrew consent is never sent.
- Every matched event is a row in the send log: `sent` (with the kinds of
  identifiers it carried, such as `gclid` and `email`), `skipped` with the reason
  (no consent, nothing to match on), `failed` when the platform refused it or
  could not be reached after five tries, or `queued`.

On the **Paid ads** page, **Send conversions back** adds and switches rules,
sets who to send for, and shows the latest sends.

| Call | What it does |
| --- | --- |
| `GET /v1/workspaces/{ws}/ads/conversion-targets?account_id=` | Where an account's conversions can go, asked live from the platform, and any setup still needed |
| `GET`, `POST /v1/workspaces/{ws}/ads/conversion-rules` | List rules with their last 30 days, or add one |
| `PATCH`, `DELETE /v1/workspaces/{ws}/ads/conversion-rules/{id}` | Change, switch off or delete a rule |
| `GET /v1/workspaces/{ws}/ads/conversion-sends` | The send log, newest first |
| `GET`, `PUT /v1/workspaces/{ws}/ads/settings` | `consent_mode`: `opt_out` or `opt_in` |

The MCP tools are `list_ad_conversion_targets`, `list_ad_conversion_rules`,
`create_ad_conversion_rule`, `update_ad_conversion_rule`,
`delete_ad_conversion_rule`, `list_ad_conversion_sends`, `get_ad_settings` and
`set_ad_settings`. X and LinkedIn follow once their API access is approved.

Outbrain Ads only reads: the Amplify API has no call that takes conversions.
Outbrain's click id (`ob_click_id`) is still recorded on each visit, so its
clicks count in Outflare's own attribution.

## Coming next

Syncing audiences from segments, and letting an agent change budgets behind
mission approval, are planned.
