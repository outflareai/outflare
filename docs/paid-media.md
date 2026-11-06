# Paid media

Outflare reads what your ad accounts spend and what the platforms say it
brought in, so a person, a script or an agent can ask "what did ads cost this
month, per brand, and what did they return?" without opening six dashboards.
It can also send conversions back: when someone the ads brought signs up or
pays, the platform hears about it, so its bidding learns from real results.
And it manages what runs: a person, or an agent within the grants a person
set, pauses and resumes a campaign and sets its daily budget ("Managing
campaigns" below). That is all it changes: it never creates campaigns, ads,
audiences or creative, and never changes bids or targeting.

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

A sync runs every six hours, and a failed one is tried again sooner: after 15 minutes, then 1 and 2 hours, then every 4 hours until one works. The first one reads 90 days; later runs read the
last 7 days, and the last 30 once a day, because platforms keep revising recent
numbers. Each run stores:

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

In the app, open **Paid ads** in the sidebar. A trail at the top picks the level: **All networks**, one network (Google Ads, Meta Ads, X Ads and so on) or one ad account of it, and every tab narrows to it; click a platform or an account in the overview to open its level. Its tabs are **Overview** (totals, by day, by platform, by account and conversion actions), **Campaigns**, **Changes** (every pause, resume and budget change at that level, and whether the platform shows it), **Tracking** (fix tracking links, tracking health and the templates), **Send conversions** and **Accounts** (ad accounts and connections), each with its own address. It shows spend, clicks, CTR, cost per click and platform conversions for 7, 28 or 90 days, by day, platform and campaign, with each account's brand and sync switch. Each campaign shows the visits, conversions and revenue Outflare's own attribution credits to it beside what the platform reports. Click a campaign and it opens as a page of its own, like a level in the ad platform, with a trail back (All campaigns, then the campaign, then the ad group): its numbers and trend by day, its conversions by type, its ad groups or ad sets and its ads, and for Google its keywords and search terms. Click an ad group or ad set for its own page with only its ads. Each page has its own address, so the browser's back button and shared links work. Paid visits whose campaign id matches no campaign in a connected account are listed on their own, with why that happens. An ad whose links lack the tracking template is marked. A connection that still needs a developer token asks for it at the top.

| Call | What it answers |
| --- | --- |
| `GET /v1/workspaces/{ws}/ads/accounts` | Every account, its brand and sync status |
| `GET /v1/workspaces/{ws}/ads/campaigns` | Campaigns ranked by spend over `from`–`to`, with CPC, CTR, cost per conversion and platform ROAS, their type, how the platform bids, the brand's target with over or under, and the trend against the days before |
| `PUT /v1/workspaces/{ws}/ads/campaigns/{id}/target` | Set a campaign's target cost per conversion (`analytics.manage`) |
| `PUT /v1/workspaces/{ws}/ads/accounts/{id}/target` | Set the target for every campaign of an account without its own (`analytics.manage`) |
| `GET /v1/workspaces/{ws}/ads/items` | Ad sets and ad groups (`level=ad_group`) or ads (`level=ad`), ranked by spend, with their campaign; `campaign_id` or `parent_id` narrows them |
| `GET /v1/workspaces/{ws}/ads/conversion-actions` | Platform conversions by action, per connector and currency; `entity_id` gives one campaign's, ad set's or ad's |
| `GET /v1/workspaces/{ws}/ads/keywords` | Google search keywords ranked by spend; `campaign_id` or `ad_group_id` narrows them |
| `GET /v1/workspaces/{ws}/ads/search-terms` | What people searched that showed a Google ad, with the keyword it matched, ranked by spend |
| `GET /v1/workspaces/{ws}/ads/summary` | Totals grouped by `connector`, `brand`, `account` or `day`, per currency; `entity_id` gives one campaign's, ad set's or ad's numbers alone |
| `POST /v1/workspaces/{ws}/ads/sync` | Sync now instead of within six hours |

All of them take `brand`. Money is in micros of the account's currency
(1,000,000 = one unit); totals are given per currency, never converted. The MCP
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
  so only its type shows.
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
| Google Ads | A conversion action that takes uploaded clicks (Upload clicks) | `gclid`, `gbraid` or `wbraid`; hashed email and phone (enhanced conversions for leads) | The sign-in needs Google Ads access that can upload |
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
  Refunded event), Outflare changes that conversion on Google Ads with a
  conversion adjustment, found by the order id it was sent with: a partial
  refund restates its value to what is left (in proportion to the value sent),
  and a full refund retracts it, so bidding learns from net revenue. Google
  may not know a conversion for a few hours after taking it, so a refused
  adjustment is tried again, up to 8 times over about a day. Platforms that
  can't change a conversion after taking it skip the adjustment and say so.
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
  could not be reached after five tries, or `queued`.

On the **Paid ads** page, **Send conversions back** adds and switches rules,
sets who to send for, and shows the latest sends.

| Call | What it does |
| --- | --- |
| `GET /v1/workspaces/{ws}/ads/conversion-targets?account_id=` | Where an account's conversions can go, asked live from the platform, and any setup still needed |
| `GET`, `POST /v1/workspaces/{ws}/ads/conversion-rules` | List rules with their last 30 days, or add one |
| `PATCH`, `DELETE /v1/workspaces/{ws}/ads/conversion-rules/{id}` | Change, switch off or delete a rule |
| `GET /v1/workspaces/{ws}/ads/conversion-sends` | The send log, newest first |
| `GET`, `PUT /v1/brands/{brand}/ads/settings` | The brand's `consent_mode`: `opt_out` or `opt_in` (the workspace route is deprecated and acts on the first brand) |

The MCP tools are `list_ad_conversion_targets`, `list_ad_conversion_rules`,
`create_ad_conversion_rule`, `update_ad_conversion_rule`,
`delete_ad_conversion_rule`, `list_ad_conversion_sends`, `get_ad_settings` and
`set_ad_settings`. X and LinkedIn follow once their API access is approved.

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
in the ad account. See
[Lifecycle email](lifecycle-email.md#journeys) for the step itself.

## Suggestions

After each sync Outflare reads every campaign it can change (Google Ads, Meta, Microsoft Advertising, LinkedIn and TikTok) and suggests at most one change for each, with why and the numbers behind it, on the **Suggestions** tab at whatever level the page shows:

- **Raise the budget** by a fifth when a conversion cost at most nine tenths of the target over the last 14 days and the budget ran out on at least 4 of the last 7.
- **Lower the budget** by a fifth when a conversion cost more than 1.3 times the target (with at least 5 conversions).
- **Pause** a campaign that spent three targets' worth in 14 days with no conversion (without a target, three times what a conversion costs across its account).
- **Resume** a paused campaign whose conversions cost at most eight tenths of the target while it ran in the last 90 days.

On Google search campaigns, from the last 30 days, it also suggests:

- **Add a keyword**: a search people made that converted at least twice at or under the target, and isn't a keyword in its ad group yet, as an exact keyword there.
- **Add a negative keyword**: a search with at least 10 clicks and two targets' worth of spend but no conversion, as an exact negative for the campaign.
- **Give a keyword its own ad group**: a keyword with at least 10 conversions and two fifths of its ad group's spend, shared with at least two other keywords. Outflare makes an ad group named after it with the keyword (exact) and copies of the group's active responsive search ads, and adds it as an exact negative to the old group, so its searches go to the new one.

On Google campaigns that bid for as many conversions as they can with no target, it also suggests:

- **Bid to a target**: a campaign with at least 30 conversions in the last 28 days gets a target cost per conversion at what a conversion cost it over those days, so Google keeps that cost while it learns. It is measured after 14 days to settle, and works when a conversion costs at most 1.05 times what it did with at least 0.85 times the conversions; it didn't work when conversions fell below 0.7 times or a conversion cost more than 1.2 times. It can be tried as a test first (see Tests).

At most one of each per campaign at a time. They are measured on the whole campaign after a week to settle (a day for a negative). With an account picked, the tab also shows Google Ads' own recommendations for it, read live.

Budget suggestions need a target cost per conversion (the campaign's, its account's or the platform's own). A campaign changed in the last week, or still being measured, gets none. Nothing changes until a person approves; an agent changes campaigns with the campaign calls below, which follow its grant. Approving makes the change exactly as those calls do (it shows on the **Changes** tab) and starts its measuring window:

- the platform's settling time (3 days for a budget, 7 for a resumed campaign), then
- at least a week, and long enough for about 30 conversions at the campaign's rate, plus 3 days for conversions reported late,
- at most six weeks; a change still unclear then is **Not sure**.

When the window ends the change is judged against as many days just before it: **Worked**, **Didn't work** or **Not sure**, with a sentence on why. A raised budget works when the extra conversions cost at most 1.2 times the target; a lowered one when a conversion got cheaper; a pause when the account converted at least nine tenths as well without the campaign; a resume when its conversions cost at most 1.1 times the target. A changed campaign whose conversions cost more than twice the target three days running is judged early. Conversions here are the platform's own.

| Call | What it does |
| --- | --- |
| `GET /v1/workspaces/{ws}/ads/suggestions` | Suggestions by network (`connector`), account, campaign or status, open first (`analytics.read`) |
| `POST /v1/workspaces/{ws}/ads/suggestions/{id}/approve` | Make the change and start measuring it; a person's session only (`ads.manage`), except savings applying on their own (below) |
| `POST /v1/workspaces/{ws}/ads/suggestions/{id}/dismiss` | Leave the campaign as it is (`ads.manage`) |
| `GET /v1/workspaces/{ws}/ads/recommendations` | The platform's own recommendations for an account, read live (Google Ads; `analytics.read`) |
| `POST /v1/workspaces/{ws}/ads/suggestions/refresh` | Read the campaigns again now, at most once an hour (`ads.manage`) |

**Savings that apply on their own.** An extension can let an agent approve suggestions that only spend less (a lower budget, a pause, a negative keyword) for a brand, with `Host.SetAdAutoApprove`; `/v1/meta` then lists `ad_suggestions_auto`. Anything that can spend more always waits for a person, a suggestion on an account every brand shares never applies on its own, the agent's grant still applies, and the change is measured and judged like any other. `ad_suggestion.approved` carries `auto: true` for these.

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
| `GET /v1/workspaces/{ws}/ads/tests` | Tests by network, account, campaign or status, with both sides' numbers and the verdict (`analytics.read`) |
| `POST /v1/workspaces/{ws}/ads/tests` | Start a test; a person's session only (`ads.manage`) |
| `POST /v1/workspaces/{ws}/ads/tests/{id}/keep` | Apply the trial's change to the campaign and end the test (`ads.manage`) |
| `POST /v1/workspaces/{ws}/ads/tests/{id}/roll-back` | End the test and leave the campaign as it was (`ads.manage`) |
| `POST /v1/workspaces/{ws}/ads/suggestions/{id}/approve` with `{"test": true}` | Test a budget suggestion instead of making it (`ads.manage`) |

X Ads tests are planned once X's developer access is set up.

## Managing campaigns

Three changes, on Google Ads, Meta, Microsoft Advertising, LinkedIn and
TikTok (the other platforms are read but not changed yet, and say so with
`422 connector_invalid`):

| Call | What it does |
| --- | --- |
| `POST /v1/workspaces/{ws}/ads/campaigns/{id}/pause` | Pause a campaign (`ads.manage`) |
| `POST /v1/workspaces/{ws}/ads/campaigns/{id}/resume` | Run a paused campaign again (`ads.manage`) |
| `PATCH /v1/workspaces/{ws}/ads/campaigns/{id}` | Set its daily budget, `daily_budget_micros` in the account's currency (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/pause-all` | The kill switch: pause every active campaign of the brand at once, on every platform that can be changed, as one batch (`ads.manage`) |
| `GET /v1/workspaces/{ws}/ads/changes` | Every change asked for, by network (`connector`), account (`account_id`), campaign, status or brand (`analytics.read`) |

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
| `GET /v1/workspaces/{ws}/ads/campaigns/{id}/targeting` | Its bidding method and target, audiences, countries, and notes on anything else (regions, cities, exclusions, ad sets that differ), read live, plus the bidding methods the platform lets Outflare set (`analytics.read`) |
| `POST /v1/workspaces/{ws}/ads/campaigns/{id}/bidding` | Bid for as many conversions as it can (`maximize_conversions`, with an optional `target_cpa_micros`) or as much conversion value (`maximize_conversion_value`, with an optional `target_roas`); Google Ads only (`ads.manage`) |
| `POST /v1/workspaces/{ws}/ads/campaigns/{id}/audiences` | Add or remove (`add: false`) one of the account's audiences, such as one made from a smart list (`ads.manage`) |
| `POST /v1/workspaces/{ws}/ads/campaigns/{id}/countries` | Add or remove a country by its two-letter code (`ads.manage`) |

Each answers `202` with the change, which runs as a job like the other campaign changes and shows on the **Changes** tab. On Meta an audience or a country is added to or removed from every ad set of the campaign, keeping the rest of each ad set's targeting as it was. A campaign never loses its last country: removing it is refused, so it can't start running everywhere. A change made by an agent always waits for a person, whatever its grant. Each is an audit event (`ad_campaign.bidding_changed`, `ad_campaign.audience_added`, `ad_campaign.audience_removed`, `ad_campaign.country_added`, `ad_campaign.country_removed`).

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

## Coming next

Syncing whole segments to audiences on a schedule, new Meta ad sets and
campaigns, and judging new ads against the ones beside them are planned.
