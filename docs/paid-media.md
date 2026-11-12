# Paid media

Outflare reads what your ad accounts spend and what the platforms say it
brought in, so a person, a script or an agent can ask "what did ads cost this
month, per brand, and what did they return?" without opening six dashboards.
It can also send conversions back: when someone the ads brought signs up or
pays, the platform hears about it, so its bidding learns from real results.
And it manages what runs, scored in one unit of Outflare's own, **cost per
new paying customer** ("Cost per new paying customer" below): a person, or
an agent within the grants a person set, pauses and resumes a campaign, sets
its daily budget, bidding, audiences and countries, adds keywords and
negatives, and approves new ads, ad groups and search campaigns the agent
drafts ("Managing campaigns", "Targeting", "New ads" and "Findings" below).
Anything that can spend more waits for a person; savings can apply on their
own when the brand turns that on.

Every connection is the workspace's own: it signs in to its own ad accounts and,
where the platform needs one, adds its own developer token. Outflare never
provides an ad account, an API token or a developer token, and an ad platform
stays off until the workspace connects it.

Google Ads no longer takes a developer token (Google retired them on
9 September 2026, and ignores one sent anyway). Its API access level now
belongs to the Google Cloud project of the server's Google sign-in app
(`GOOGLE_OAUTH_CLIENT_ID`, or `CONNECTOR_GOOGLE_ADS_CLIENT_ID`), so whoever
runs the server applies for it once, for every workspace: in the Google Cloud
console, open that project, then the Google Ads API, and apply for Basic
access (it needs a project whose branding Google verified). The data stays the
workspace's own: each one signs in to its own Google Ads accounts. A token a
workspace added before is still sent and does no harm.

## Platforms

| Platform | Connect with | Also needs |
| --- | --- | --- |
| Google Ads (`google_ads`) | Sign in with Google (read-only Google Ads access). Manager accounts list the accounts under them. | Nothing: Google retired developer tokens in September 2026 (see below). |
| Meta Ads (`meta_ads`) | A system user access token from your own Business Manager with `ads_read` (and `ads_management` for journeys to fill audiences), or sign in with Facebook | Nothing |
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

A sync runs every hour, and a failed one is tried again sooner: after 15 minutes, then 1 and 2 hours, then every 4 hours until one works. The first one reads 90 days; later runs read today and yesterday every hour, the last 7 days once a day, the last 35 once a week and the last 95 once a month, because platforms keep revising recent numbers for up to 90 days (late conversions, invalid clicks). `POST /v1/workspaces/{ws}/ads/sync` still reads now. On Google Ads a run also harvests yesterday's **clicks** once a day (`click_view`: each click's id with its campaign, ad group, ad, keyword, match type and device, into `ad_clicks`), and the first run harvests the last 90 days one day at a time; Google keeps no more, so a refused read stops where it is and the next run carries on from the last day stored. With it a landing page that carried only a `gclid` is tied to its ad group, ad and keyword. Each run stores:

- **Ad accounts**, with currency and time zone. Each account can belong to a
  brand (`PATCH /v1/workspaces/{ws}/ads/accounts/{id}`) or be shared, and can be
  switched off. A new account takes the brand of the connection that sees it
  (a brand's own sign-in, or a shared one used by a single brand) until you
  set another. The page shows each account's name, its id as the platform
  writes it and, for Google Ads and Meta, a link that opens it there.
- **Campaigns**, with status, objective and daily budget.
- **Ad sets and ads** (Meta Ads and Google Ads, where Google calls ad sets ad
  groups): each one under its campaign, with its status, a Meta ad set's own
  budget, and for each ad what the platform says about it: its headline, text,
  landing page, URL parameters (Meta) or final URL suffix and tracking template
  (Google, the ad's own or the ones it inherits from its ad group, campaign or
  account), its type (Google) and a preview image (Meta, a link that expires
  and is refreshed by each sync), and whether those links carry Outflare's
  tracking template.
  Removed ad groups and ads aren't listed, but their numbers still sync while a
  report has them.
- **Daily metrics** for each campaign, ad set and ad: spend, impressions,
  clicks, and the platform's own conversions and conversion value where it
  reports them. Days are the account's own days, in its time zone. An ad set's
  and an ad's numbers are already in their campaign's, so totals count
  campaigns only. From Meta also link clicks, landing page views, 3-second
  video views and reach. Reach is each day's people: added over days, someone
  reached on two days counts twice, so frequency is the average day's.
- **Conversions by type** for each campaign, ad set and ad per day. Meta's
  standard kinds (purchase, lead, complete_registration, start_trial,
  subscribe) each count once, though Meta reports a purchase as `purchase`,
  `omni_purchase` and the pixel's own; custom conversions are listed as
  `custom.<id>` and aren't in the conversions total. Google's are its
  conversion actions, by name.
- **Google search keywords and search terms**: each keyword that showed an
  ad, with its match type and numbers per day, and what people searched, with
  the keyword it matched. Google leaves out searches too few people made.
- **Spend in the costs ledger**: one entry per campaign (never per ad set or ad) per day, channel
  `paid_ads`, in the account's currency and brand, so paid ads sit beside every
  other cost and outcome. An entry is updated when the platform revises the day.

Conversions and ROAS here are **platform-reported**: each platform's own count,
with its own attribution window. They are not Outflare's attribution, and they
are not added up across platforms as if they were.

## Reading it

In the app, open **Paid ads** in the sidebar. A trail at the top picks the level: **All networks**, one network (Google Ads, Meta Ads, X Ads and so on) or one ad account of it, and every tab narrows to it; click a platform or an account in the overview to open its level. Its tabs are **Overview** (totals, by day, by platform, by account and conversion actions), **Campaigns**, **Changes** (every pause, resume and budget change at that level, and whether the platform shows it), **Tracking** (fix tracking links, tracking health and the templates), **Send conversions** and **Accounts** (ad accounts and connections), each with its own address. It shows spend, clicks, impressions, CTR, cost per click, platform conversions and all conversions for the last 7, 28, 30 or 90 days or any custom from and to, by day, platform and campaign, with each account's brand and sync switch. Every report table has a **Download CSV** button that saves exactly the rows and columns shown, with their headers, money as a decimal with its currency's code (`12.34 USD`) and the dates in the file's name; the by-day chart and conversions by type have one too. Wherever a report lists paused or removed campaigns, ad groups, ads or keywords, a Status column shows which by default. Each campaign shows the visits, conversions and revenue Outflare's own attribution credits to it beside what the platform reports. Click a campaign and it opens as a page of its own, like a level in the ad platform, with a trail back (All campaigns, then the campaign, then the ad group): its numbers and trend by day, its conversions by type, its ad groups or ad sets and its ads, and for Google its keywords and search terms. Click an ad group or ad set for its own page with only its ads. Each page has its own address, so the browser's back button and shared links work. Paid visits whose campaign id matches no campaign in a connected account are listed on their own, with why that happens. An ad whose links lack the tracking template is marked. A connection that still needs a developer token asks for it at the top.

| Call | What it answers |
| --- | --- |
| `GET /v1/workspaces/{ws}/ads/accounts` | Every account, its brand and sync status, including those with no brand yet |
| `GET /v1/brands/{brand}/ads/accounts` | The brand's accounts |
| `GET /v1/brands/{brand}/ads/campaigns` | Campaigns ranked by spend over `from`–`to`, with CPC, CTR, cost per conversion and platform ROAS, their type, how the platform bids, the brand's target with over or under, and the trend against the days before |
| `PUT /v1/brands/{brand}/ads/campaigns/{id}/target` | Set a campaign's target cost per conversion (`analytics.manage`) |
| `PUT /v1/workspaces/{ws}/ads/accounts/{id}/target` | Set the target for every campaign of an account without its own (`analytics.manage`) |
| `GET /v1/brands/{brand}/ads/items` | Ad sets and ad groups (`level=ad_group`) or ads (`level=ad`), ranked by spend, with their campaign; `campaign_id` or `parent_id` narrows them |
| `GET /v1/brands/{brand}/ads/conversion-actions` | Platform conversions by action, per connector and currency; `entity_id` gives one campaign's, ad set's or ad's |
| `GET /v1/brands/{brand}/ads/keywords` | Google search keywords ranked by spend, with their status, Google's first page, top of page and first position bid estimates and quality score as of the last sync; `campaign_id` or `ad_group_id` narrows them |
| `GET /v1/brands/{brand}/ads/search-terms` | What people searched that showed a Google ad, with the keyword it matched and how it matched it (`match_type`: exact, near_exact, phrase, near_phrase, broad), ranked by spend |
| `GET /v1/brands/{brand}/ads/bidding-strategies` | Each portfolio bidding strategy of the brand's Google Ads accounts with its type, status, clicks, impressions, cost, average CPC, conversions, all conversions and cost per conversion over `from`–`to`, read live |
| `GET /v1/brands/{brand}/ads/summary` | Totals grouped by `connector`, `brand`, `account` or `day`, per currency; `entity_id` gives one campaign's, ad set's or ad's numbers alone |
| `POST /v1/workspaces/{ws}/ads/sync` | Sync now instead of within six hours |

All of them take `brand`. Money is in micros of the account's currency
(1,000,000 = one unit); totals are given per currency, never converted.
Totals carry `all_conversions` beside `conversions`: every conversion the
platform counted, its secondary conversion actions included (Google's all
conversions), where conversions are the primary ones its bidding uses. The MCP
server has a tool for each (`list_ad_accounts`, `list_ad_campaigns`, `list_ad_items`,
`list_ad_conversion_actions`, `list_ad_keywords`, `list_ad_search_terms`,
`get_ads_summary`, `sync_ads`, `list_ad_connections`, `update_ad_account`,
`set_ad_target`).

### Targets and trends

Each campaign comes with what an agent or a person needs to judge it, not only
its numbers:

- **Type** (`objective`): the platform's own word, such as Google's `search`,
  `performance_max`, `display`, `video` or `demand_gen`.
- **How the platform bids** (`bidding_strategy`) and the cost per conversion or
  return it bids to (`platform_target_cpa_micros`, `platform_target_roas`), as
  set in Google Ads. A portfolio bid strategy keeps its target on the strategy,
  so only its type shows here; the account's portfolio strategies list it
  ([Targeting](#targeting)).
- **The brand's own target cost per conversion**, set on the campaign or, for
  every campaign without one, on its ad account. It is Outflare's alone and is
  never sent to the platform. `target_status` compares the range's cost per
  conversion with it: `on_target` within a tenth either way, `under` below that,
  `over` above it, and `over` too when the campaign spent with no conversions.
- **Trend**: `previous_totals` holds the same numbers for the same number of
  days just before the range, and `trend` says which way spend moved:
  `scaling_up` or `scaling_down` past 15% either way, `steady` inside it, `new`
  with no spend before and `stopped` with none now. `spend_change` is the move
  as a fraction.

The Paid ads page shows them in the Campaigns table, with a Target button on
each campaign and on each ad account. Day-by-day charts are on the page and on
each campaign.

To credit sales to each ad by its id, paste the network's tracking template
into it ([website tracking](website-tracking.md#ad-tracking-templates)): every
paid click then lands with the network's own campaign, ad set and ad ids.
Google Ads clicks know their campaign even without it: with auto-tagging on,
Google adds `gad_campaignid` to the landing page, and the website script keeps
it as the visit's campaign. Ad group and ad ids still need the template.
On Google Ads, Outflare can add it for you: **Paid ads → Tracking → Fix tracking
links** shows every URL suffix it would change, then adds the template when
you confirm ([how it works](website-tracking.md#adding-the-template-to-google-ads-for-you)).

### What each campaign brought

**Paid ads → What each campaign brought** puts Outflare's own count beside
each campaign's spend. It shows the visits that came through the campaign
(matched by the ids its tracking template put on the landing page), and the
conversions and revenue [attribution](attribution.md) credits to those visits
under the workspace's model, with the return on spend. The platform's own
conversion count is shown next to it for comparison, never added in.

A campaign with spend and no tracked visits is listed too: it usually has no
tracking template yet.

`GET /v1/brands/{brand}/attribution/ads` (MCP `get_attribution_ads`) gives the
same rows. It takes `level` (`campaign`, `adset` or `ad`), `from`, `to`,
`model` and `brand`. Meta and Google ad sets and ads come with their names and
spend; other platforms sync campaigns only, so their ad sets and ads come with
first-party numbers but no name or spend.

## Sending conversions back

A conversion rule sends one customer event (such as `signed_up` or
`payment.succeeded`, from the website snippet, the API or a connected tool) to one
conversion on one ad account.

For purchases, send **`payment.succeeded`**, valued at `amount_minor` (minor
units). Every payment becomes one, whether it arrives as a
[transaction](transactions.md), an `Order Completed` event or from a billing
connector, and a payment sent more than one way is counted once
([One payment, counted once](transactions.md#one-payment-counted-once)). A rule
on `order_completed` sends only payments that arrive as orders.

A rule can also name one of the workspace's
[conversion definitions](conversions.md) (`"conversion": "qualified_call"`)
instead of a bare event. It then sends only the events that meet the
definition's conditions, such as a call over a minute, and each one is worth
what the definition says unless the rule sets its own value. The event type
defaults to the definition's name for that network (`network_events`) when the
platform offers it.

Each event is checked against the conditions once, a minute after it arrives.
One that doesn't qualify is never sent and never appears in the send log.
Archiving the definition stops the rule. In the app, **Add a rule** offers the
definitions first.

| Platform | Where it goes | Matched on | Also needs |
| --- | --- | --- | --- |
| Google Ads | A conversion action that takes uploaded clicks (Upload clicks), through Google's Data Manager API (see [The conversion feed](#the-conversion-feed)) | `gclid`, `gbraid` or `wbraid`; hashed email and phone (enhanced conversions for leads) | The sign-in made with the Data Manager permission: a connection from before it is asked to connect again (`setup_needed` names `reconnect`) |
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
  latest visit with that platform's click id in the 90 days before it. For
  Google, when no visit kept a `gclid`, `gbraid` or `wbraid`, the `gclid` in
  the Google tag's `_gcl_aw` cookie (read with marketing consent) is used.
  Gmail addresses are sent to Google without the dots before the `@`, as
  Google asks, so they match the Google account. Email and
  phone come from the person's record, hashed with SHA-256 before they leave;
  nothing is sent in the clear and nothing is copied into the send log.
- The event's message id is the conversion's id (Google's order id, Meta's,
  Reddit's, TikTok's, Snapchat's and Pinterest's event id, Taboola's order id). On the website, `outflare.track()` returns it: pass it to
  the browser pixel as its event id (`fbq('track', 'Purchase', {}, {eventID:
  id})`) and the platform counts a conversion once when both send it.
- **Sent from both the browser and your server.** Give the event the same
  message id on both sides: `outflare.track("Signed Up", {}, {messageId:
  "signup-" + userId})` in the browser, and `messageId: "signup-<userId>"` on
  your server's event. Outflare sends it to each rule once, under that id. The
  server's copy is preferred, since it names the person: a browser copy waits
  15 minutes for it, and is sent only if it doesn't come.
- **If the ids don't match** (the browser call left out `messageId`, so the
  script made its own), the two copies are still matched: the same event name
  for the same person, or the same browser, within 15 minutes of each other is
  one event, and the server's copy is the one sent. Tracking health raises
  `events_without_shared_id` so the developer can add the id. An order's
  copies with the same `order_id` in their properties are one event however
  far apart they arrive, with no warning.
- **Refunds.** <a id="refunds"></a>When a payment whose conversion was sent
  is refunded (a `payment.refunded`, from a refund transaction or an Order
  Refunded event), Outflare changes that conversion on Google Ads: a partial
  refund restates its value to what is left (in proportion to the value sent),
  and a full refund retracts it, so bidding learns from net revenue. A
  conversion that went through the Data Manager API is restated the way that
  API takes it: the same event is sent again, with the same `transactionId`
  and the new value, which Google treats as an adjustment of the first. The
  Data Manager API documents no way to take a conversion away, so a
  retraction, and every change to a conversion sent the older way, goes
  through the Ads API's conversion adjustments, found by the order id the
  conversion was sent with. Google may not know a conversion for a few hours
  after taking it, so a refused adjustment is tried again, up to 8 times over
  about a day. Platforms that can't change a conversion after taking it skip
  the adjustment and say so.
- The value comes from an event property (in major units, 12.50, or minor units,
  1250) or a fixed value; the currency from the rule, the event's `currency`
  property or the account's.
- **Consent.** In the EEA, the UK and Switzerland, and whenever a person's
  country is unknown, a conversion is sent only when they said yes to ad data
  (a consent with channel `ads` and purpose `ad_user_data`, recorded with
  `POST /v1/brands/{brand}/records/{record}/consents` when your consent banner
  says yes). Elsewhere the workspace's setting decides: **Everyone,
  unless they opted out** (`opt_out`, the default) or **Only people who said
  yes** (`opt_in`). Anyone who withdrew consent is never sent.
  A person's country is their latest website visit's. For someone the site
  script never saw, such as a purchase sent only from your server, it is the
  event's own: `context.location.country` (two letters, such as `US`), else
  the country of `context.ip`. So send the user's IP address (or country)
  in `context` on server events, or they are treated as country unknown.
- Every matched event is a row in the send log: `sent` (with the kinds of
  identifiers it carried, such as `gclid` and `email`), `skipped` with the reason
  (no consent, nothing to match on), `failed` when the platform refused it or
  could not be reached after five tries, or `queued`. A Google send also says
  its `transport` (`data_manager`, or `ads_api` after a fallback) and the Data
  Manager `request_id` it went in, which Google reports on from about half an
  hour later.

On the **Paid ads** page, **Send conversions back** adds and switches rules,
sets who to send for, and shows the latest sends.

| Call | What it does |
| --- | --- |
| `GET /v1/workspaces/{ws}/ads/conversion-targets?account_id=` | Where an account's conversions can go, asked live from the platform, and any setup still needed |
| `GET`, `POST /v1/workspaces/{ws}/ads/conversion-rules` | List rules with their last 30 days, or add one |
| `PATCH`, `DELETE /v1/workspaces/{ws}/ads/conversion-rules/{id}` | Change, switch off or delete a rule |
| `GET /v1/workspaces/{ws}/ads/conversion-sends` | The send log, newest first |
| `GET`, `PUT /v1/brands/{brand}/ads/settings` | The brand's `consent_mode`: `opt_out` or `opt_in` (the workspace route is deprecated and acts on the first brand) |
| `GET`, `POST`, `PUT /v1/brands/{brand}/ads/accounts/{id}/feed` | Outflare's conversion feed on a Google Ads account: read it, set it up, set the new customer acquisition goal (below) |
| `GET /v1/brands/{brand}/ads/feed-health?account_id=` | What Google counts for the feed's actions against what was sent, per day and campaign |

The MCP tools are `list_ad_conversion_targets`, `list_ad_conversion_rules`,
`create_ad_conversion_rule`, `update_ad_conversion_rule`,
`delete_ad_conversion_rule`, `list_ad_conversion_sends`, `get_ad_settings`,
`set_ad_settings`, `get_ad_feed`, `create_ad_feed` and `update_ad_feed`. X and
LinkedIn follow once their API access is approved.

### The conversion feed

For Google Ads, one call sets up everything Smart Bidding needs to learn from
real customers, without choosing conversion actions by hand: `POST
/v1/brands/{brand}/ads/accounts/{id}/feed` (MCP `create_ad_feed`, scope
`ads.manage`; in the app, **Send conversions back → Set up the feed**).

- **Three conversion actions**, made on the account as actions that take
  uploaded clicks: **Outflare sign-up** (category sign-up, secondary, 30-day
  click window), **Outflare first payment** (purchase, or *paid subscription*
  when the brand's payments are subscriptions; the primary action bidding
  learns from, counted once per click, 90-day click window) and **Outflare
  repeat payment** (purchase, secondary). An action that already exists by
  name is used, never made twice.
- **Three rules** with a `feed_role`: sign-up sends `signed_up` (or the brand's
  `signup` conversion definition when it has one); first payment sends a
  person's first `payment.succeeded` in the brand, valued at its
  `amount_minor`; repeat payment sends their later ones. A payment is "first"
  when the person has no earlier `payment.succeeded` in the brand (the paid
  ads ledger's own judgement is used where it has one). A payment with no
  person on it goes to neither.
- **Customer type.** Each conversion tells Google whether the person is a
  **new** customer (their first payment) or a **returning** one
  (`userProperties.customerType`), which Google uses instead of its own
  guess for the new customer acquisition goal and its new-versus-returning
  reports. A rule made by hand can set `customer_type` to `new`, `returning`
  or `auto` (worked out per event) the same way; it sends nothing by default.
- **The Data Manager API.** Conversions go through Google's Data Manager
  API (`events:ingest`, at most 2,000 a request) with the workspace's own
  Google sign-in, which now asks for the Data Manager permission beside
  Google Ads. Google closed the Ads API's own upload call
  (`UploadClickConversions`) to new integrations in June 2026, and the Data
  Manager takes a request whole or not at all, so Outflare narrows a refused
  request down to the bad rows, fails those with Google's reason and sends
  the rest. The event's message id is the `transactionId`, so a retry never
  counts a conversion twice. When the Data Manager refuses the destination
  itself (the account is not allowlisted, the sign-in lacks the permission,
  the API is not enabled on the server's Google Cloud project), the account
  falls back to the Ads API upload for as long as Google still takes it; the
  feed shows `transport: ads_api` and why, and `PUT` with `transport:
  data_manager` puts it back once fixed. Everything is SHA-256 hashed before
  it leaves, and consent is as for every other conversion.
- **The payers list.** A Customer Match list **Outflare paying customers** is
  made on the account, marked as *purchasers*, and refreshed once a day from
  everyone with a `payment.succeeded` in the brand (hashed emails and phones,
  through `audienceMembers:ingest`, at most 10,000 a request; the Customer
  Match terms are stated as accepted, so accept them in Google Ads first).
  Adding follows the consent rules for audiences; people who withdrew consent
  or whose record was merged away or archived are taken out. An erased
  person's identifiers are gone with them, so they leave the list when their
  membership expires: Google keeps a member at most 540 days, and the list
  asks for that. Google reads the list as the account's existing customers
  for the goal below, and campaigns can exclude it.
- **New customer acquisition.** `PUT .../feed` with `nca` sets Google's goal
  on every active and paused campaign of the account: `bid_higher` bids more
  for new customers and still serves to everyone (Google's *New Customer
  Value*, `TARGET_ALL`); `new_only` serves to new customers only (*New
  Customer Only*, `TARGET_SPECIFIC`), and is allowed once the payers list
  has 1,000 people (`payers_list_too_small` before: Google says a smaller
  list weakens the goal); `off` removes it. `additional_value_micros` is
  added to a new customer's first conversion for bidding, in micros of the
  account's currency. An agent's `PUT` always waits for a person, like a
  bidding change.
- **Reconciliation.** `GET .../ads/feed-health?account_id=` compares, per
  day, the conversions Google reports for the three actions with the sends
  it accepted, and, once the paid ads ledger is on, the first-party new
  payers, per campaign too. The ratio of accepted sends to Google's
  conversions over the last week is compared with the four weeks before; a
  move of more than 30% is `drifted`, and the daily refresh raises
  `ad_feed.ratio_drifted`.
- **Developer tokens.** Google retired them on 9 September 2026 and ignores
  one sent anyway, so Outflare no longer asks for one or sends one; a token a
  workspace added before stays sealed in its credentials, unused. Google's
  API access level now belongs to the Google Cloud project of the server's
  Google sign-in app (see the top of this page).

Outbrain Ads only reads: the Amplify API has no call that takes conversions.
Outbrain's click id (`ob_click_id`) is still recorded on each visit, so its
clicks count in Outflare's own attribution.

## Audiences from journeys

A journey's `sync_audience` step adds a person to a customer list audience on
one of your ad accounts, or takes them out: a Meta custom audience made from a
customer list, or a Google Ads Customer Match list that is open. `GET
.../ads/audiences?account_id=` (MCP `list_ad_audiences`) lists them, live from
the platform. Make the audience on the platform first; Outflare only fills it.

Only the person's SHA-256 hashed emails and phone numbers are sent, and the
platform matches them on its side, so it never says who matched. Adding
follows the same consent rules as conversions (above), for both ad data and ad
personalisation; removing never needs consent. Each change records
`ads.audience_synced` on the person. Meta needs the token to have
`ads_management` as well as `ads_read`, and the Custom Audience terms accepted
in the ad account. Google Ads lists are filled through Google's Data Manager
API (`audienceMembers:ingest` and `:remove`), which replaced the Ads API's
offline user data jobs for new integrations in April 2026, so the sign-in
needs the Data Manager permission (a connection from before is asked to
connect again). See
[Lifecycle email](lifecycle-email.md#journeys) for the step itself.

## Findings and decisions

After each hourly sync Outflare reads every campaign it can change (Google Ads, Meta, Microsoft Advertising, LinkedIn and TikTok) and writes what it finds into one queue, the brand's **findings** (`GET /v1/brands/{brand}/ads/findings`; the same rows, and the same calls, as `GET /v1/brands/{brand}/ads/suggestions`): a change it proposes, a change it made on its own, or an alert. Each is a yes/no question a person answers (`reason`), with the numbers behind it (`evidence`: the window, the spend, the payers, the target, the search terms or segments it rests on), the lever (`kind`), the rule that made it and its version (`rule`, `rule_version`), its autonomy class, and its expected change in cost per new paying customer (`impact_micros`, negative is better: a cut's from the campaign's marginal cost per payer, a negative keyword's from the wasted spend spread over the brand's payers).

**One unit.** A brand that has set a target cost per new paying customer (its economics, below) is judged in **payers**: the sign-ups and first-time payers Outflare's own ledger ties to each campaign per day, beside the network's spend, clicks and impressions (`unit` `payers`). A brand without a target is judged on the network's own conversions against the campaign's, account's or network's target cost per conversion, and every finding says so (`unit` `platform_conversions`). The network's target (a tCPA or tROAS) is compared with the network's own numbers, since that is what it bids to; the payers are the floor.

**Every change waits for a person.** In Outflare, self-hosted and open source, nothing is changed on an ad account until a person approves it: every finding the rules make is `approve` (status `open`) and waits in the brand's queue, savings included. **Savings** (a budget cut, a pause, a negative keyword, a keyword or ad paused, a target tightened, an exclusion) are the one class that can apply on their own, and only where the server's extension allows it: Outflare Cloud can, through the extension API (`Host.SetAdAutoApprove`; `/v1/meta` lists `ad_suggestions_auto` when it is set). There, an allowed saving is `auto`: it applies at once, as an ordinary change on the **Changes** tab (actor system, `ad_campaign.*` with `auto: true`), and the finding is `measuring` with `applied_auto` so a person can **revert** it; an agent may also approve such a saving. Anything that can spend more or restructure a campaign (a budget raise, a resume, a new keyword, a looser target, a bidding target, a merge of ad groups) always waits for a person, and the extension is never asked about it. An `alert` has nothing to apply; a person acknowledges it. While the brand's `shadow_mode` is on (it starts on), every change-class finding is only recorded (`shadow`, status `shadow`): what the rules would have done, so you can watch them before turning them live. A campaign on an account every brand shares has no economics, so nothing on it applies on its own.

**Discipline, for every rule.** A change is measured over the brand's **judgement window**: two conversion cycles (the brand's median click-to-first-payment lag) and at least 14 days, with the trailing lag days left out so late payments don't read as a drop. One lever per campaign per cycle: a campaign with a change still measuring gets nothing else. No change-class finding within 7 days of any change to the campaign (14 for a target move). The data floor is 5 payers each side (30 for a tROAS verdict). The brand's top 30% of campaigns by new payers over 30 days are **winners**: never paused or cut, and their keywords are left alone. While an `alert.tracking_broken` or `alert.sends_failing` alert is open on the brand, no change-class finding is made at all. Performance Max is judged on days 15 to 42 after a change and never gets keyword or search-term findings; which way a target move is expected to push its volume follows the network's capabilities (on PMax a tROAS cut can lower it).

**The rules**, with their numbers (`engine/internal/adsuggest/rules.go`, each a named constant; the research behind them is in `plans/paid-ads/research/`):

- **Budget raise** (`budget_up`, approve): the network says a tenth or more of impressions were lost to the budget over 7 days (without that word, the budget ran out on 4 of the last 7 days) and a payer costs at most the target over the window; a fifth up.
- **Budget cut** (`budget_down`, auto): a payer costs more than 1.3 times the target over the window with the floor met, or three targets' worth was spent in 14 days with no payer; a fifth down.
- **Pause** (`pause`, auto): three targets' worth spent with no payer over max(14 days, 3 cycles).
- **Resume** (`resume`, approve): paused at least 7 days, and while it ran in the last 90 days a payer cost at most eight tenths of the target, with at least 10 payers.
- **Target tighten** (`target_tighten`, auto): a tCPA or tROAS set and the campaign beats it by 15% over 30 days with 30 payers; the target moves 10 to 20% toward actual, never past it, one move per cycle.
- **Target loosen** (`target_loosen`, approve): the target holds the campaign back (actual over the target and under a tenth of impressions lost to the budget), or Performance Max spends under seven tenths of its budget: 10 to 20% looser (20 to 30% on PMax).
- **Bidding ladder** (`bidding_target`, approve): Maximise conversions with no target, 30 payers in 30 days at a cost within 15% of the target: a target at what a conversion actually cost, to the cent, never above it. Google's bidding settles for 14 days before measuring; it can be tried as a test first (see Tests).
- **Negative keyword** (`negative_add`, auto; Google Search): a search with 10 clicks, no payer and none of the network's conversions, costing at least 50 of the currency, 2% of the campaign's spend or two targets; a word or phrase across the campaign's searches with 150 clicks and no payer (a phrase negative); a search whose sign-ups never pay (5 sign-ups, no payer in 60 days; payers only). The ledger knows the keyword a click matched, not the search typed, so a search converting through a broad keyword is never negated on that alone.
- **Keyword promote** (`keyword_add`, approve): a search with 50 clicks (20 with payer evidence) and 2 payers at or under the target that isn't a keyword in its ad group: an exact keyword there.
- **Keyword pause** (`keyword_pause`, auto): a keyword that spent three targets with no payer and clicked three payers' worth of clicks, over 30, then 60, then 90 days; never on a winner.
- **Ad pause** (`ad_pause`, auto): two or more enabled ads in a group and one brings under half the other's payers per thousand impressions, over 100 payers between them.
- **Consolidate** (`consolidate`, approve): ad groups each under 30 payers in 30 days that bid on the same keyword texts (the same intent split by match type) merge into the one with the most payers. It replaces the retired keyword split (single-keyword ad groups are obsolete); the merge is done in the network's own tools, and dismissing the finding closes it.
- **Exclusions** (`exclusion_add`, auto; Google): a device, place, placement or hour with 300 clicks (100 on a brand campaign) and no payer over 60 days (an hour becomes a schedule window); search partners or Display expansion on with a payer costing one and a half times the rest of the campaign's. The segments come from the network's own report, read once a day per campaign, with the ledger's payers by device, country and hour from the acquiring visit.
- **Brand leak** (auto on Search as a phrase negative; approve on Performance Max as a brand exclusion): a non-brand campaign where a fifth of the search clicks name the brand.
- **Stale target** (`alert.target_stale`): a target untouched 90 days that actual misses by more than a quarter.

Up to one finding per lever kind per campaign at a time; a finding the next run no longer finds true expires. With an account picked, the page also shows Google Ads' own recommendations for it, read live.

**Verdicts.** When a change's window has run (or early, when a payer has cost more than twice the target three days running), it is judged against as many days just before it, in the finding's unit, on the whole campaign (a pause on the whole account): **Worked** at or under 1.1 times the target (a raise when its extra payers cost at most 1.2 times the target; a saving when a payer got cheaper), **Didn't work** past 1.5 times (or when a saving lost volume without getting cheaper), **Not sure** under the floor, with a sentence on why. Each verdict writes a **brand finding** (`GET /v1/brands/{brand}/findings`, scope channel `paid_ads`, reviewed in 90 days) so the agent remembers what each lever did on this brand and network.

**Revert.** An applied finding (measuring or judged, with a change) is undone with `POST .../findings/{id}/revert`: the inverse change as a new row on the **Changes** tab, carried out by the same job (the budget restored, a paused campaign resumed, the negative or exclusion taken out, a paused keyword or ad enabled again, a keyword that was added paused, the target put back); the finding shows `reverted_at` and `revert_change_id`, and its verdict stands.

| Call | What it does |
| --- | --- |
| `GET /v1/brands/{brand}/ads/findings` | The brand's queue, ranked: open decisions by impact (the biggest saving first), then alerts, then savings applied this week, then measuring and judged; `status`, `autonomy`, `kind`, `campaign_id`, `connector`, `account_id` narrow it (`analytics.read`) |
| `POST /v1/brands/{brand}/ads/findings/{id}/approve` | Make the change and start measuring it; a person's session only (`ads.manage`), except a saving where the server's extension allows it (`ad_suggestions_auto`); `{"test": true}` tests a budget or bidding finding inside the network first |
| `POST .../findings/{id}/dismiss`, `.../acknowledge` | Leave the campaign as it is; close an alert (`ads.manage`) |
| `POST .../findings/{id}/revert` | The inverse change of an applied finding (`ads.manage`) |
| `POST .../findings/refresh` | Run the rules now; at most once an hour per workspace, so a refresh within the hour queues nothing and answers `{"queued": false, "next_at": ...}` (`ads.manage`) |
| `GET /v1/brands/{brand}/ads/suggestions` | The same rows, served by the same calls, by network (`connector`), account, campaign, status, autonomy or kind, open first (`analytics.read`); `POST /v1/brands/{brand}/ads/suggestions/{id}/approve`, `.../dismiss`, `.../acknowledge` and `.../suggestions/refresh` as above |
| `GET /v1/brands/{brand}/ads/recommendations` | The platform's own recommendations for an account, read live (Google Ads; `analytics.read`) |

The MCP tools are `list_ad_findings`, `approve_ad_finding`, `dismiss_ad_finding`, `acknowledge_ad_finding` and `revert_ad_finding`. The workspace status carries `ads_decisions_waiting` (area `ads`, info) while findings wait for a person. Every finding is an `ad_suggestion.created` event with its `autonomy`, `rule` and `unit`; a saving applied on its own (only where the extension allows it) is `ad_suggestion.applied` beside the change's own `ad_campaign.*` event with `auto: true`; a revert is `ad_suggestion.reverted`; a verdict is `ad_suggestion.judged` and a `finding.recorded`. Metrics: `outflare_ads_findings_total`, `outflare_ads_verdicts_total` and `outflare_ads_findings_open` on the Grafana row "Paid media (findings and verdicts)".

## Alerts

After each hourly sync Outflare reads every campaign and writes what needs a person's eye into the same list as a finding with `autonomy` `alert` (`GET .../ads/suggestions?autonomy=alert`), one open row per campaign (or per account, pinned to its biggest campaign) and kind, with a sentence (`reason`) and the numbers behind it (`evidence`: the baseline, the window). It stays open and is updated while it holds, and expires when it no longer does; a person **acknowledges** one (`POST .../ads/suggestions/{id}/acknowledge`, `ads.manage`) and it isn't raised again for a day while it holds. The kinds:

- `alert.spend_anomaly`: today's spend so far is outside half to one and a half times the same weekday's over the last 4 weeks scaled to the hour (the platforms' numbers lag about 3 hours, so the hour is taken 3 back), by more than three standard deviations and at least 20 of the currency, once the day is 4 hours in.
- `alert.pacing`: from the 8th of the month, the account's month-to-date spend is outside 90 to 110% of what its active campaigns' daily budgets add up to (30.4 a month, scaled to the day).
- `alert.flatline`: an active campaign that served on 6 of the last 7 days has shown nothing today by noon in its time zone.
- `alert.zero_conversions`: 300 clicks in 30 days (100 for a campaign named after the brand) with no sign-up: from Outflare's own acquisitions where the ledger runs (`unit` `signups`), else no platform conversion.
- `alert.disapproved`: live ads the platform disapproved (Google's `policy_summary`, stored as the ad's `policy_status` detail).
- `alert.auto_apply_on`: the account lets Google apply its own recommendations to bidding, keywords or match types (`recommendation_subscription`), so two sets of rules would be changing it.
- `alert.sends_failing`: the platform refused a tenth or more of the conversions sent to it in 24 hours (at least 10).
- `alert.tracking_broken`: under half of the brand's paid visits in 24 hours carried a click id (at least 20), or paid visits with campaign ids brought no sign-up while the last week's days brought three or more.
- `overspend_pause`: a campaign spent more than twice its daily budget today. In Outflare it is an alert: pause the campaign yourself if the budget is right. Only where the server's extension allows it (Outflare Cloud, `Host.SetAdAutoApprove`) and the brand's rules are out of shadow mode does Outflare **pause it** on its own (an ordinary change on the **Changes** tab, actor system, `ad_campaign.paused` with `auto: true`), with the finding `applied_auto` and `measuring` so a person can revert it; in shadow mode the finding says what it would have done (`shadow`). A campaign on an account every brand shares is never paused on its own.

The workspace status (`GET /v1/workspaces/{ws}/status`) carries an `ads_alert` blocker (area `ads`) while any alert is open: critical when one says money or data is going wrong now (tracking broken, conversions refused, a campaign paused for overspending, spend far off its usual), a warning otherwise. Every alert is an `ad_alert.raised` event with its kind.

## Google's own changes

Every 15 minutes Outflare reads each Google Ads account's change history (`change_event`, which Google keeps for 30 days) into its own store, so what changed on the platform sits beside Outflare's own changes for as long as you like: `GET /v1/brands/{brand}/ads/platform-changes` (`analytics.read`; `connector`, `account_id`, `campaign_id`, `actor`, `source`; newest first, paged). Each says when, what changed and how (`resource_type`, `operation`, `summary`, and in `detail` the changed fields with the resource before and after), where it was made (`source`: `web`, `api`, `automated_rule`, `recommendation_auto_apply`, `script`, `editor`, `bulk`) and who (`actor`): `outflare` for Outflare's own changes (a change it applied on that campaign around then, or the connection's own sign-in through the API), `google` for Google's own automation (a rule or recommendation it applied), else the SHA-256 of the user's email; the address itself is never stored. Google Ads Editor changes aren't in the history.

## Tests

A before-and-after comparison can be thrown off by anything else that changed that week. A test avoids that: the network itself splits one campaign between how it is and a **trial** that tries one thing differently, on the same days, so the two compare fairly. Start one on the **Tests** tab, or press **Test it first** on a budget suggestion instead of approving it.

- **Google Ads** runs it as a custom experiment on a search or display campaign: the trial gets a share of the campaign's traffic (half, unless the call says otherwise) and bids to a different target cost per conversion.
- **Meta** runs it as a split test: Outflare copies the campaign with its ad sets and ads, gives the copy a different daily budget, and Meta splits the audience so no one sees both. The ad account must belong to a business, and the token needs `ads_management` and `business_management`.

A test starts the next day and runs until the smaller side has about 30 conversions at the campaign's recent rate: at least two weeks, at most six. The first days don't count while the trial settles (a week for a new bid target, three days for a budget). Outflare reads both sides after each sync and shows them as they go. Three days after the test ends (for conversions reported late) it says which did better:

- a **bid target** wins when the trial gets clearly more conversions for its money, at 95% confidence;
- a **budget** wins when the trial's conversions cost at most 1.1 times the target and no clearly more than the campaign's.

A trial whose conversions cost more than twice the target three days running is judged early. Then a person presses **Keep it** (Google promotes the trial's bid target to the campaign; on Meta the campaign gets the trial's budget and the copy is paused) or **Roll back** (the campaign stays as it was and the trial stops). Either can be pressed while a test runs, too. A suggestion that was tested is judged with its test. A campaign being tested gets no suggestions, and has one test at a time.

| Call | What it does |
| --- | --- |
| `GET /v1/brands/{brand}/ads/tests` | Tests by network, account, campaign or status, with both sides' numbers and the verdict (`analytics.read`) |
| `POST /v1/brands/{brand}/ads/tests` | Start a test; a person's session only (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/tests/{id}/keep` | Apply the trial's change to the campaign and end the test (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/tests/{id}/roll-back` | End the test and leave the campaign as it was (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/suggestions/{id}/approve` with `{"test": true}` | Test a budget suggestion instead of making it (`ads.manage`) |

X Ads tests are planned once X's developer access is set up.

## Managing campaigns

Three changes, on Google Ads, Meta, Microsoft Advertising, LinkedIn and
TikTok (the other platforms are read but not changed yet, and say so with
`422 connector_invalid`):

| Call | What it does |
| --- | --- |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/pause` | Pause a campaign (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/resume` | Run a paused campaign again (`ads.manage`) |
| `PATCH /v1/brands/{brand}/ads/campaigns/{id}` | Set its daily budget, `daily_budget_micros` in the account's currency (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/pause-all` | The kill switch: pause every active campaign of the brand at once, on every platform that can be changed, as one batch (`ads.manage`) |
| `GET /v1/brands/{brand}/ads/changes` | Every change asked for, by network (`connector`), account (`account_id`), campaign, status or brand (`analytics.read`) |

Each call checks the campaign (yours, on an active account, in a state the
change makes sense for) and answers `202` with the **change** (`ad_changes`,
migration 0395): what the campaign was before, what was asked, who asked and,
for an agent, its action. A job (`ads.change`) then makes the one call on the
workspace's own account, reads the campaign back from the platform, and
records how it ended: `applied` with `verified_at` when the platform shows
the change, `applied` with a `verify_note` when it shows something else or
could not be read back (the next sync corrects the mirror), or `failed` with
the platform's reason. An unreachable platform is retried for a while first.
The campaigns list follows the platform's answer at once. On the Paid ads
page each campaign has Pause, Resume and a budget field, and a brand has
**Pause all ads**.

`ads.manage` is its own scope, because it spends money. Operators have it;
give it to a token on purpose. For an agent on the action layer
([actions.md](actions.md)):

- a pause spends nothing, so it runs under the agent's grant;
- raising a daily budget by more than a fifth, or setting one on a campaign
  that had none, always waits for a person, whatever the grant;
- a raise or a resume that would put the brand's active campaigns over the
  grant's daily spend cap, or the month (what it has spent plus the daily
  total for the rest of it) over its monthly cap, waits for a person. A run
  grant on ads paths gets $50 a day and $500 a month unless a person sets
  its own caps.

### Removing a campaign, an ad or a keyword

On Google Ads, a person can also remove a campaign, an ad or a keyword for
good. Google Ads can't undo a removal, so:

| Call | What it does |
| --- | --- |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/remove` | Remove a campaign (`campaign_remove`) |
| `POST /v1/brands/{brand}/ads/items/{id}/remove` | Remove an ad (`ad_remove`; the id is the ad's, from `GET .../ads/items?level=ad`) |
| `POST /v1/brands/{brand}/ads/keywords/{id}/remove` | Remove a keyword (`keyword_remove`) |

- only a person on their own signed-in session can: any API token, agent or
  staff view gets `403 person_only`, and the action layer refuses an agent's
  outright, with no grant or approval that lets it through;
- the body must say `{"confirm": true}` (`400` otherwise); the app asks first,
  naming what can't be undone, from each row's **Manage** menu;
- it is a change like a pause (migration 0505 widens `ad_changes.kind`): the
  job removes it with the remove operation of Google's campaign, ad group ad
  or ad group criterion service, reads it back, and only then marks it
  removed in Outflare. Its numbers stay. The audit events are
  `ad_campaign.removed`, `ad_ad.removed` and `ad_keyword.removed`.

Every change is an audit event and a webhook (`ad_campaign.paused`,
`ad_campaign.resumed`, `ad_campaign.budget_changed`, `ads.paused_all`), and
how it ended is a product event (`ad_change.applied`, `ad_change.failed`) and
the metric `outflare_ads_changes_total` (the Grafana row "Paid media
(managing campaigns)"). Meta needs the `ads_management` permission on the
token or sign-in, LinkedIn `rw_ads`, and TikTok write access to Ad Account
Management on the app; a connection made before that was asked for is
reconnected to manage campaigns. Microsoft's sign-in already covers it.

## Targeting

How a campaign bids, which audiences it reaches and in which countries it runs, on Google Ads and Meta. The campaign's page on Paid ads shows them, with buttons to change each one.

| Call | What it does |
| --- | --- |
| `GET /v1/brands/{brand}/ads/campaigns/{id}/targeting` | Its bidding method and target, audiences, countries, and notes on anything else (regions, cities, exclusions, ad sets that differ), read live, plus the bidding methods the platform lets Outflare set (`analytics.read`) |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/bidding` | Bid for as many conversions as it can (`maximize_conversions`, with an optional `target_cpa_micros`: a standard Target CPA) or as much conversion value (`maximize_conversion_value`, with an optional `target_roas`: a standard Target ROAS), or with a portfolio strategy of the account (`portfolio` with `bidding_strategy_id`); Google Ads only (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/audiences` | Add or remove (`add: false`) one of the account's audiences, such as one made from a smart list (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/countries` | Add or remove a country by its two-letter code (`ads.manage`) |

A **portfolio bidding strategy** is one target several campaigns share. The account's own are managed on **Paid ads → Accounts**, and a campaign's Targeting card puts it on one (**Change bidding → Portfolio strategy**); switching it back to a standard strategy takes it off.

| Call | What it does |
| --- | --- |
| `GET /v1/brands/{brand}/ads/accounts/{id}/bidding-strategies` | The account's portfolio strategies, read live: type, status, target and how many campaigns use each (`analytics.read`) |
| `POST /v1/brands/{brand}/ads/accounts/{id}/bidding-strategies` | Create a Target CPA (`target_cpa` with `target_cpa_micros`) or Target ROAS (`target_roas` with `target_roas`) strategy (`ads.manage`) |
| `PATCH /v1/brands/{brand}/ads/accounts/{id}/bidding-strategies/{strategy}` | Move its target or rename it; every campaign on it bids to the new target (`ads.manage`) |

These, a website conversion action and the account's callouts ([Account settings](#account-settings)) are **account changes** (`ad_account_changes`, migration 0507): each answers `202` with the change, the same `ads.change` job makes it and reads it back, and `GET /v1/brands/{brand}/ads/accounts/{id}/changes` (or `/changes/{change}`) shows how it ended, with what Google made in `result`. An agent's always waits for a person: raising a target cost per conversion or lowering a target return spends more.

Each answers `202` with the change, which runs as a job like the other campaign changes and shows on the **Changes** tab. On Meta an audience or a country is added to or removed from every ad set of the campaign, keeping the rest of each ad set's targeting as it was. A campaign never loses its last country: removing it is refused, so it can't start running everywhere. A change made by an agent always waits for a person, whatever its grant. Each is an audit event (`ad_campaign.bidding_changed`, `ad_campaign.audience_added`, `ad_campaign.audience_removed`, `ad_campaign.country_added`, `ad_campaign.country_removed`).

## Account settings

Two things on a Google Ads account itself, beside its campaigns:

- **Conversion actions.** **Paid ads → Send conversions** lists each Google
  account's conversion actions, read live: type, category, status, whether
  bidding uses it (primary), how it counts and its click window. **New
  website conversion** creates one (`WEBPAGE`): a name, what it counts
  (purchase, sign-up, lead, page view, contact or other), one or every
  conversion per click, a click window of 1 to 90 days, and an optional
  value. Once Google has made it, the screen shows its code with plain
  instructions: the global site tag for every page and the event snippet for
  the page the conversion happens on, each with a copy button. This is
  separate from sending conversions back, which needs no tag.
- **Callouts.** Short lines (up to 25 characters) Google can show under every
  ad of the account. **Paid ads → Accounts** lists them, adds one and removes
  one (the asset stays in Google's library).

| Call | What it does |
| --- | --- |
| `GET /v1/brands/{brand}/ads/accounts/{id}/conversion-actions` | The account's conversion actions with the website code of website ones (`analytics.read`) |
| `POST /v1/brands/{brand}/ads/accounts/{id}/conversion-actions` | Create a website conversion action (`ads.manage`) |
| `GET /v1/brands/{brand}/ads/accounts/{id}/callouts` | The account's own callouts (`analytics.read`) |
| `POST /v1/brands/{brand}/ads/accounts/{id}/callouts` | Add one (`ads.manage`) |
| `DELETE /v1/brands/{brand}/ads/accounts/{id}/callouts/{asset}` | Remove one (`ads.manage`) |

Each change is an account change, as above; an agent's always waits for a
person. Audit events: `ad_conversion_action.created`, `ad_callout.added`,
`ad_callout.removed`, `ad_bidding_strategy.created`,
`ad_bidding_strategy.updated`. Other platforms answer `422
connector_invalid`. MCP: `list_ad_account_conversion_actions`,
`create_ad_website_conversion`, `list_ad_callouts`, `add_ad_callout`,
`remove_ad_callout`, `list_ad_bidding_strategies`,
`create_ad_bidding_strategy`, `update_ad_bidding_strategy`,
`get_ad_bidding_strategy_report`, `get_ad_account_change`.

## Required Minimum Functionality

Google asks a tool that applies for Standard access to the Google Ads API as
a full-service tool to offer a list of features: its [Required Minimum
Functionality](https://developers.google.com/google-ads/api/docs/api-policy/rmf).
This is where each item is in Outflare.

| Item | What Google asks | In Outflare |
| --- | --- | --- |
| C.10 | Create a campaign | New ads, a new search campaign ([New ads](#new-ads)) |
| C.20 | Geo targeting | Countries on a campaign ([Targeting](#targeting)) |
| C.30 | Language targeting | Not available: Search campaigns lost language targeting in Google's September 2026 change, so there is nothing to set here |
| C.65 | Conversion tracking with its code snippet | New website conversion and its global site tag and event snippet ([Account settings](#account-settings)) |
| C.75 | Callouts, account level | Account callouts ([Account settings](#account-settings)) |
| C.96, C.97 | Target CPA and Target ROAS, standard and portfolio | Standard: `maximize_conversions` with a target, `maximize_conversion_value` with a target. Portfolio: an account's strategies ([Targeting](#targeting)) |
| C.98 | Maximise conversions | `maximize_conversions` ([Targeting](#targeting)) |
| C.120 | Set a budget | A campaign's daily budget ([Managing campaigns](#managing-campaigns)) |
| C.190 | Create an ad group | New ads, a new ad group ([New ads](#new-ads)) |
| C.260, C.270, C.300 | Add a keyword, campaign negative keywords, match types | Keywords and negatives with exact, phrase or broad ([Findings and decisions](#findings-and-decisions), [New ads](#new-ads)) |
| M.10 | Edit campaign settings | Budget, bidding, countries and audiences |
| M.96, M.97, M.98 | Edit Target CPA, Target ROAS and Maximise conversions | Changing a campaign's bidding, or a portfolio strategy's target |
| M.110, M.130, M.140 | Pause, enable and remove a campaign, an ad, a keyword | Pause and resume ([Managing campaigns](#managing-campaigns), findings), and [removing](#removing-a-campaign-an-ad-or-a-keyword), a person only |
| R.10 | Account report: clicks, cost, impressions, conversions, all conversions | **By account** on Paid ads, all on by default |
| R.20 | Campaign report, the same with status | **Campaigns as each network reports them** on Results, status on by default |
| R.40 | Ad report with status | A campaign's ads, with Status |
| R.50 | Keyword report with first page and first position bid estimates | A campaign's keywords: First page bid, Top of page bid and First position bid on by default, with Status |
| R.70 | Search term report with its match type | A campaign's search terms, with Match type |
| R.130 | Bidding strategy report | **Bidding strategies** on Results |
| Date ranges | At least the last 30 days; a custom range recommended | The last 7, 28, 30 or 90 days, or any from and to |
| Downloads | Labelled clearly, with every required field | **Download CSV** on every report table |

Dynamic Search Ads (R.100) aren't made in Outflare, so their search term
report isn't required.

## New ads

The brand agent writes new ads where a campaign needs them, from the
brand's own knowledge and site, with why each should do better (the
playbook **Write new ads where the old ones are tired**). A person can
write one through the API too. Each is a **draft**:

- **A new ad** in an ad group (an ad set on Meta). On Google, a responsive
  search ad: 3 to 15 headlines of at most 30 characters, 2 to 4
  descriptions of at most 90, and an optional display path. On Meta, a link
  ad with one headline, its main text, an image, a button (Learn more by
  default) and the Facebook page it runs as (the account's first unless
  named).
- **A new ad group** in a Google campaign, with its keywords (phrase match
  unless named) and an ad.
- **A new search campaign** on Google, with a daily budget, at least one
  country, bidding (maximise conversions unless named) and an ad group.

Drafts are checked against the platform's rules when written, and wait for
a person on the **Suggestions** tab under **New ads**. Approving one makes it
on the workspace's own account, everything paused: on Google in one batch,
so a refused draft makes nothing. Check it on the platform, then **Turn on**:
the ad, and the ad group and campaign when the draft made them, are turned
on. Nothing spends until then.

| Call | What it does |
| --- | --- |
| `GET /v1/brands/{brand}/ads/drafts` | The brand's drafts, those waiting for a person first, by `status`, account or campaign (`analytics.read`) |
| `POST /v1/brands/{brand}/ads/drafts` | Write a draft: `kind` (`ad`, `ad_group`, `campaign`), where it goes (`ad_group_id`, `campaign_id` or `account_id`), what it makes and `reason` (`ads.manage`) |
| `PATCH /v1/brands/{brand}/ads/drafts/{id}` | Rewrite one before it is approved (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/drafts/{id}/approve` | Make it on the platform, paused; a person's session only; again on a failed one to retry (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/drafts/{id}/go-live` | Turn on what it made; a person's session only (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/drafts/{id}/dismiss` | Leave it unmade (`ads.manage`) |

Writing a draft spends nothing, so an agent writes it under its grant;
approving and turning on are always a person's. The job (`ads.draft`) makes
the platform calls, so a draft moves from `draft` to `creating`, `paused`,
`going_live` and `live`, or to `failed` with the platform's reason (or back
to `paused` when turning on failed). Each step is an audit event
(`ad_draft.written`, `ad_draft.edited`, `ad_draft.approved`,
`ad_draft.launched`, `ad_draft.dismissed`), the job's results are product
events (`ad_draft.made`, `ad_draft.went_live`, `ad_draft.failed`), and the
metric `outflare_ads_draft_steps_total` is on the Grafana row "Paid media
(suggestions)". Meta makes new ads only; new ad sets and campaigns are made
in Ads Manager.

### The knowledge sheet gate

Ads are written only from the brand's **knowledge sheet**: what its website
says it offers ([agent-memory.md](agent-memory.md), brand knowledge). Before
the first draft, a person checks the sheet and marks it reviewed
(`POST /v1/brands/{brand}/knowledge/review`, from their own session; the
Paid ads page's **Set up** card, "Looks right"). Until then, and whenever
the sheet can't be trusted, writing or rewriting a draft is refused with
422 `knowledge_unreviewed`: nobody has reviewed it, its last read failed
(`read_error` on `GET /v1/brands/{brand}/knowledge`: `connector_needed`
without a page reader, `ai_model_needed` without an AI model), or the
website changed what it sells since the review. A person may send
`override_knowledge: true` to write one draft anyway; an agent can't. The
status shows `ads_knowledge_unreviewed` (area `ads`) for every brand with an
ad account whose sheet isn't trusted. Connecting an ad account to a brand
turns on a daily read of its website, so the sheet is at most a day old;
a read that changes the brand's products, services, prices or summary
clears the review so the owner looks again.

### Checks on a draft

Beside Google's own limits (the only refusals), a draft's answer carries
`checks`: warnings for the person who approves it, recorded with the draft
and refreshed when it is rewritten.

| Check | When |
| --- | --- |
| `price_not_in_sheet` | A price or percentage in the copy that the sheet doesn't have |
| `superlative` | best, #1, cheapest, guaranteed, leading and the like |
| `competitor_named` | A tracked competitor's name (Competitors) in the copy |
| `few_headlines` | Fewer than 11 distinct headlines |
| `pins_thin` | More than one pinned headline position with fewer than 3 variants |
| `title_case` | Headlines with most words capitalised |
| `long_headline` (a note) | Headlines over 20 characters |
| `rsa_crowded` | The ad group already runs more than one enabled ad |

The draft writer (the playbook and `createAdDraft`'s guidance) is asked for
one evergreen unpinned responsive search ad per ad group, 11 or more
distinct headlines across keywords, benefits, features, calls to action and
authority, sentence case, short headlines, claims only from the sheet, and
at most one challenger with partial pins (`headline_pins`, one entry per
headline, 0 unpinned or the position 1 to 3, three variants a position).
## Cost per new paying customer

Everything above reports what the networks claim. This part scores a
brand's ads in one unit of Outflare's own: **cost per new paying
customer**, a person's first ever payment tied to the last paid click
within the brand's acquisition window, with **cost per sign-up** beside it.
The network's conversion count stays on every row as a diagnostic, never
the judge.

### The brand's economics

Each brand sets what a new paying customer and a sign-up may cost (micros
of its currency), the **acquisition window** (days after an ad click a
sign-up or first payment still counts for it, 30 by default, 1 to 365),
the months a new customer's revenue is expected to take to pay back its
cost (12 by default) and the gross margin on that revenue. From the margin
and payback Outflare works out a break-even cost per payer (monthly
revenue per payer, times the margin, times the months), which is what the
target defaults to once there are payers. `shadow_mode` keeps every rule
to recording what it would have done (on by default); turned off, the
rules' findings go live and wait for a person in the queue. Only a person,
signed in, turns shadow mode off; an API token or an agent that tries gets
`403 person_only`. There is no setting that lets changes apply without a
person: in Outflare every change waits for one, and only a server whose
extension allows it (Outflare Cloud, `ad_suggestions_auto` in `/v1/meta`)
lets savings apply on their own. Each change records
`ad_economics.updated`.

### The ledger

Every minute, for each workspace with an ad account, a job reads the
customer events that arrived since its last run (five minutes after they
arrive, so the visit and the identify call that tie them to a person have
landed; 90 days back on its first run): `signed_up` (and the brand's own
conversion events from its attribution settings), `payment.succeeded` and
`payment.refunded`, each with a record. For each it finds the person's
last visit within the window that carried a paid click (a network click id
such as `gclid`, the tracking template's ad ids, or a visit classified as
paid ads), including visits before they were identified, and resolves the
campaign, ad group, ad and keyword: from the template's ids, else from
Google's `gad_campaignid`, else from the click id when the sync has
harvested it, else the campaign by its name from the UTM tags.

- A sign-up with a paid click makes an **acquisition**; a sign-up by
  someone already acquired adds its time to theirs.
- A person's **first ever payment** makes them a new customer: it is set
  on their acquisition, or makes one when the payment itself had a paid
  click in the window. It is revenue in the **new** bucket.
- A later payment is **returning** revenue: lifetime revenue of the
  campaign that acquired them, and revenue by click for the campaign
  whose paid click was last before the payment within the window, if any.
- A refund is a negative row with the same acquisition, campaign and
  bucket as the payment it refunds, found by its `payment_id`.
- `ad_consent` records whether the person had granted ad data consent, as
  conversions sent back decide it.

Events are read in the order they arrived, so a payment a billing
connector synced late still counts. Merging two records moves their
acquisitions and revenue; erasing a person removes theirs. The job records
`ad_acquisition.recorded` and `ad_revenue.recorded`, and `paidads.run_failed`
when a run fails; its metrics (`outflare_paidads_ledger_events_total`,
`outflare_paidads_ledger_lag_seconds`, `outflare_paidads_acquisitions_total`)
are on the Grafana row "Paid media (cost per new customer)".

### The two windows

The **acquisition window** says how long after a click a sign-up or first
payment still counts for it. The ledger stores acquisitions with the
brand's window; `GET /performance?window_days=` recomputes them from
visits with another window (up to 90 days) for one read, so a brand can
see how much a shorter or longer window would change the picture before
changing it. The **judgement window** is how long a change is measured
before a rule judges it: two conversion cycles, where a cycle is the
brand's median click-to-first-payment lag from the ledger, and never less
than 14 days.

### Reading it

| Call | What it answers |
| --- | --- |
| `GET`, `PUT /v1/brands/{brand}/ads/economics` | The brand's targets, window, payback, margin and the two switches (`analytics.read`; `PUT` is `analytics.manage`, the switches a person's) |
| `GET /v1/brands/{brand}/ads/performance` | Per campaign, ad group or ad (`level`) over `from`–`to`: spend, clicks, impressions and the network's own conversions beside `signups`, `new_payers`, `cost_per_signup_micros`, `cost_per_new_payer_micros`, `target_status` (`on_target` within a tenth, `under`, `over`), `new_revenue`, `returning_revenue`, `lifetime_revenue`, `payback_months` and `trend`; `connector`, `account_id`, `campaign_id`, `window_days` and `customers` (`all`, `new`, `returning`) narrow it (`analytics.read`) |
| `GET /v1/brands/{brand}/ads/acquisitions` | The people the ads acquired, newest click first, with their campaign, ad group, ad and keyword, when they clicked, signed up and first paid, their payments and revenue to date; `from`, `to`, `campaign_id`, `customers`, paged with `cursor` (`customers.read`) |
| `GET /v1/brands/{brand}/ads/setup` | The guided path below |

Spend and costs are in micros of each account's currency; revenue is in
minor units per currency; nothing is converted or added across currencies.
The MCP tools are `get_ad_economics`, `set_ad_economics`,
`get_ads_performance`, `list_ad_acquisitions` and `get_ads_setup`. The
workspace status (`GET /v1/workspaces/{ws}/status`) gains the area `ads`:
`ads_economics_missing` when a brand has an ad account and no target, and
`ads_over_target` when a new customer cost more than the target over the
last 7 days with at least 5 new payers or three targets' worth of spend.

### The set-up path

`GET /v1/brands/{brand}/ads/setup` lists the steps to measuring ads this
way, each with `done`, a line on where it stands and the page and MCP tool
that fix it: the website script on the site, sign-ups and payments sent as
events, a billing connection (or payments sent from your server), an ad
account on the brand, conversions sent back to the network, the tracking
template on every live Google ad, the economics set, the knowledge sheet
reviewed, and the consent setting chosen. The Paid ads page shows them
under **Set up**.

## Coming next

Syncing whole segments to audiences on a schedule, new Meta ad sets and
campaigns, and judging new ads against the ones beside them are planned.
