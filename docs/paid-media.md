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
| Reddit Ads (`reddit_ads`) | Sign in with Reddit (`adsread`, and `adsedit` so Outflare can pause campaigns and set budgets when a person approves it). A connection made before edit access was asked for reads as before; connect it again to manage campaigns. To send conversions back, add the pixel ID and a Conversions API token on the connection. | Nothing |
| X Ads (`x_ads`) | Your own X developer app's API key and secret, then **Sign in to X** on the connection (Paid ads, Accounts) with the account that runs the ads. Set the app's permissions to Read and write so Outflare can pause campaigns and set budgets, and add this server's callback URL (`https://<your server>/v1/integrations/oauth/callback`) under the app's User authentication settings. An access token and secret made in the developer portal still work instead of signing in. To send conversions back, add the pixel ID and the conversion event IDs (`Sign-up = o8z2x, Purchase = o8z2y`) on the connection. | Ads API access approved for that app by X |
| LinkedIn Ads (`linkedin_ads`) | Sign in with LinkedIn (`r_ads`, `r_ads_reporting`) | Advertising API access approved for the app by LinkedIn |
| TikTok Ads (`tiktok_ads`) | Sign in with TikTok For Business and choose the ad accounts to share (Ad Account Management and Reporting read access). To add accounts later, connect again. | Nothing |
| Snapchat Ads (`snapchat_ads`) | Sign in with Snapchat (`snapchat-marketing-api`). Every ad account of every organization you belong to is listed. | Nothing |
| Taboola Ads (`taboola_ads`) | Your own Backstage API client id and secret, and optionally an account id. A network account reads every advertiser account under it; without an account id, the credentials' own account is read. | Backstage API access, which your Taboola account manager grants and issues the credentials for |
| Outbrain Ads (`outbrain_ads`) | Your own Outbrain username and password. Outflare trades them for an Amplify API token (`GET /login`), keeps the token sealed with them and gets a new one every 25 days (a token lasts 30). Every marketer the login can see is listed; spend is in the currency of the marketer's budgets. | Amplify API access for that login, which you request from Outbrain |
| Apple Ads (`apple_ads`, formerly Apple Search Ads) | Your own Apple Ads API user: its client id, team id and key id from Account Settings → API, and the private key whose public half you uploaded there (see below). Every ad account the user can reach is listed, App Store and Apple Maps. | An API role for that user (API Account Read Only is enough) |
| Amazon Ads (`amazon_ads`) | Sign in with Amazon (Login with Amazon, `advertising::campaign_management`). Every advertising profile the sign-in reaches is an ad account: one advertiser in one marketplace, with its currency and time zone, in North America, Europe or the Far East. Sponsored Products campaigns are read; their daily numbers come from Amazon's reports, which Amazon builds in a few minutes, so a sync can take longer than for other platforms. Purchases and sales are Amazon's seven-day attribution. | Nothing |
| Pinterest Ads (`pinterest_ads`) | Sign in with Pinterest (`ads:read`, read-only) | Nothing |
| ChatGPT Ads (`chatgpt_ads`) | Your own Advertiser API key from OpenAI Ads Manager (Settings). A key reaches one ad account, so connect each account with its own key. To send conversions back, add the pixel ID, a conversion key (Conversions, Manage conversion keys) and your website address on the connection. With the pixel ID there, the website script can also load the pixel on your site and pass it the signed-in visitor's hashed email and id, with no pixel code on the site: [Ad pixels without code](website-tracking.md#ad-pixels-without-code-on-your-site). | Nothing |
| Zeropark (`zeropark_ads`) | Your own API access token: in Zeropark, My account, Security, Access tokens, Add new (shown once; Zeropark recommends a new one every 30 days). Outflare sends it in the `api-token` header to the Stats API. A token reads its own account. Zeropark pays per visit, so clicks are its redirects and there are no impressions; days are in the account's time zone, and money is in US dollars. Syncing only reads; a campaign is paused, resumed, re-budgeted or given a bid, and one of its targets paused, resumed or given a bid, only when asked (below). | Nothing |

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

A sync runs every hour, and a failed one is tried again sooner: after 15 minutes, then 1 and 2 hours, then every 4 hours until one works. A server can read more or less often: `OUTFLARE_ADS_SYNC_INTERVAL` (a duration from `15m` to `24h`, such as `30m`) sets it for every workspace, and an extension can set it per workspace (`Host.SetAdSyncInterval`). No platform is read more often than its floor, half an hour for most and a quarter for Zeropark, because each read counts against the platform's own quotas on your account; a shorter setting reads at the floor. The first one reads 90 days; later runs read today and yesterday each time, the last 7 days once a day, the last 35 once a week and the last 95 once a month, because platforms keep revising recent numbers for up to 90 days (late conversions, invalid clicks). `POST /v1/workspaces/{ws}/ads/sync` still reads now. On Google Ads a run also harvests yesterday's **clicks** once a day (`click_view`: each click's id with its campaign, ad group, ad, keyword, match type and device, into `ad_clicks`), and the first run harvests the last 90 days one day at a time; Google keeps no more, so a refused read stops where it is and the next run carries on from the last day stored. With it a landing page that carried only a `gclid` is tied to its ad group, ad and keyword. Each run stores:

- **Ad accounts**, with currency and time zone. Each account can belong to a
  brand (`PATCH /v1/workspaces/{ws}/ads/accounts/{id}`) or be shared, and can be
  switched off. A new account takes the brand of the connection that sees it
  (a brand's own sign-in, or a shared one used by a single brand) until you
  set another. The page shows each account's name, its id as the platform
  writes it and, for Google Ads and Meta, a link that opens it there.
- **Campaigns**, with status, objective and daily budget. For Google Ads, an
  enabled campaign, ad group or ad also says whether it is really serving,
  in Google's words: **Eligible**, **Limited by budget**, **Limited by
  bidding**, **Learning**, **Not eligible** and the rest, with Google's
  reasons when you hover (`primary_status` and `primary_status_reasons` on a
  campaign, and in `details` on an ad group or ad). A campaign also carries
  Google's **Optimization score** (`optimization_score`, 0 to 1), a column on
  the Campaigns table, and **Room to grow**: the share of the searches it
  could have shown for over the last 7 days that it missed because its
  budget ran out (`budget_lost_share`) or because others ranked higher
  (`rank_lost_share`), both 0 to 1, read at each sync for enabled search
  campaigns. Sort the Campaigns table by Room to grow to see where a raise
  would buy the most.
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
  For video ads, Meta (campaigns, ad sets and ads) and TikTok (ad groups
  and ads) also say how far people watched: `video_views` (3 seconds or
  more on Meta, 2 on TikTok), `video_thruplays` (Meta's ThruPlays: 15
  seconds or the whole video; TikTok's plays of 6 seconds or more) and the
  plays that reached a quarter, half, three quarters and the end
  (`video_p25` to `video_p100`). From those the totals work out
  `hook_rate` (video views over impressions: how often the opening stops
  the scroll) and `hold_rate` (ThruPlays over video views: how many kept
  watching). They are columns to turn on in the campaign, ad set and ad
  tables ("Watched", "Hook rate", "Hold rate", "Watched to the end"), and
  empty for ads without a video.
- **Conversions by type** for each campaign, ad set and ad per day. Meta's
  standard kinds (purchase, lead, complete_registration, start_trial,
  subscribe) each count once, though Meta reports a purchase as `purchase`,
  `omni_purchase` and the pixel's own; custom conversions are listed as
  `custom.<id>` and aren't in the conversions total. Google's are its
  conversion actions, by name.
- **Google and Microsoft search keywords and search terms**: each keyword that showed an
  ad, with its match type and numbers per day, and what people searched, with
  the keyword it matched. Google leaves out searches too few people made.
- **Spend in the costs ledger**: one entry per campaign (never per ad set or ad) per day, channel
  `paid_ads`, in the account's currency and brand, so paid ads sit beside every
  other cost and outcome. An entry is updated when the platform revises the day.

Conversions and ROAS here are **platform-reported**: each platform's own count,
with its own attribution window. They are not Outflare's attribution, and they
are not added up across platforms as if they were.

## Reading it

In the app, open **Paid ads** in the sidebar. A trail at the top picks the level: **All networks**, one network (Google Ads, Meta Ads, X Ads and so on) or one ad account of it, and every tab narrows to it; click a platform or an account in the overview to open its level. Its tabs are **Overview** (totals, by day, by platform, by account and conversion actions), **Campaigns**, **Changes** (every pause, resume and budget change at that level, and whether the platform shows it), **Tracking** (fix tracking links, tracking health and the templates), **Send conversions** and **Accounts** (ad accounts and connections), each with its own address. It shows spend, clicks, impressions, CTR, cost per click, platform conversions and all conversions for the last 7, 28, 30 or 90 days or any custom from and to, by day, platform and campaign, with each account's brand and sync switch. Every report table has a **Download CSV** button that saves exactly the rows and columns shown, with their headers, money as a decimal with its currency's code (`12.34 USD`) and the dates in the file's name; the by-day chart and conversions by type have one too. Wherever a report lists paused or removed campaigns, ad groups, ads or keywords, a Status column shows which by default. Each campaign shows the visits, conversions and revenue Outflare's own attribution credits to it beside what the platform reports. Click a campaign and it opens as a page of its own, like a level in the ad platform, with a trail back (All campaigns, then the campaign, then the ad group): its numbers and trend by day, its conversions by type, its ad groups or ad sets and its ads, and for Google and Microsoft Advertising its keywords and search terms. Click an ad group or ad set for its own page with only its ads. Each page has its own address, so the browser's back button and shared links work. Paid visits whose campaign id matches no campaign in a connected account are listed on their own, with why that happens. An ad whose links lack the tracking template is marked. A connection that still needs a developer token asks for it at the top.

| Call | What it answers |
| --- | --- |
| `GET /v1/workspaces/{ws}/ads/accounts` | Every account, its brand and sync status, including those with no brand yet |
| `GET /v1/brands/{brand}/ads/accounts` | The brand's accounts |
| `GET /v1/brands/{brand}/ads/campaigns` | Campaigns ranked by spend over `from`–`to`, with CPC, CTR, cost per conversion and platform ROAS, their type, how the platform bids, the brand's target with over or under, and the trend against the days before |
| `PUT /v1/brands/{brand}/ads/campaigns/{id}/target` | Set a campaign's target cost per conversion (`analytics.manage`) |
| `PUT /v1/workspaces/{ws}/ads/accounts/{id}/target` | Set the target for every campaign of an account without its own (`analytics.manage`) |
| `GET /v1/brands/{brand}/ads/items` | Ad sets and ad groups (`level=ad_group`) or ads (`level=ad`), ranked by spend, with their campaign; `campaign_id` or `parent_id` narrows them |
| `GET /v1/brands/{brand}/ads/conversion-actions` | Platform conversions by action, per connector and currency; `entity_id` gives one campaign's, ad set's or ad's |
| `GET /v1/brands/{brand}/ads/keywords` | Google and Microsoft search keywords ranked by spend, with their status and, on Google, Google's first page, top of page and first position bid estimates and quality score as of the last sync; `campaign_id` or `ad_group_id` narrows them. Each also carries Outflare's own count of who its clicks brought: `signups` in the range, how many of those have paid since (`signups_paid`, `paid_share`), `new_payers` and `cost_per_new_payer_micros` |
| `GET /v1/brands/{brand}/ads/search-terms` | What people searched that showed a Google ad, with the keyword it matched and how it matched it (`match_type`: exact, near_exact, phrase, near_phrase, broad), ranked by spend. `keyword_signups`, `keyword_signups_paid` and `keyword_paid_share` are the matched keyword's own numbers: a network doesn't say which search a click came from |
| `GET /v1/brands/{brand}/ads/bidding-strategies` | Each portfolio bidding strategy of the brand's Google Ads accounts with its type, status, clicks, impressions, cost, average CPC, conversions, all conversions and cost per conversion over `from`–`to`, read live. Cost, average CPC and cost per conversion are in the brand's currency (`report_currency`, with `exchange_rate`); a strategy's targets stay in its own `currency` |
| `GET /v1/brands/{brand}/ads/summary` | Totals grouped by `connector`, `brand`, `account` or `day`, per currency; `entity_id` gives one campaign's, ad set's or ad's numbers alone |
| `POST /v1/workspaces/{ws}/ads/sync` | Sync now instead of within six hours |

All of them take `brand`. Money is in micros (1,000,000 = one unit).

**One currency per brand.** An ad account billed in another currency (an
account a network made you open in Singapore dollars, say) is converted into
the brand's currency, so its spend adds up with the rest: the performance
report, the summary and the totals of each campaign, ad group, ad, keyword
and search term are in the brand's currency,
at the latest daily reference rate Outflare keeps (the European Central
Bank's). Each row says what its account is billed in (`account_currency`, or
`currency` beside `report_currency` and `exchange_rate` on the others). A
budget and a cost target stay in the ad account's currency in the API,
because that is what the network sets; the app shows the budget converted,
with the account's amount on hover. Money in a currency with no rate stays
in its own and is never added to another.
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

Sign-ups, payments and revenue count by the brand's days, in its reporting
time zone (`?timezone=` overrides it for one report). Spend, clicks and the
platforms' own numbers stay in each ad account's days, as the platforms
report them, so near midnight the two can fall a day apart.

`GET /v1/brands/{brand}/attribution/ads` (MCP `get_attribution_ads`) gives the
same rows. It takes `level` (`campaign`, `adset` or `ad`), `from`, `to`,
`model` and `brand`. Meta, Google, TikTok, Snapchat, Microsoft, Pinterest
and Reddit ad sets, ad groups and ads, and X line items (as ad groups), come with their names and spend
(Snapchat reports days by campaign and by ad, so an ad squad's days are the
sum of its ads'; on Microsoft an ad is listed once it has shown in the last
95 days). Taboola, Outbrain and LinkedIn have no ad groups, so their ads
(Taboola's items, Outbrain's promoted links, LinkedIn's creatives) sit in
the campaign: each campaign is also listed as its own ad group, with the
same name and numbers, and the ads are under it. Taboola's report of ads
has no days, so Outflare reads it a day at a time for the last 31 days at
most, and reads what each ad is doing now (running, paused, rejected) for
the 50 campaigns that spent most in the days read. Outbrain's ads are the
ones shown in the last 31 days, and the 10 campaigns that spent most get
their ads' numbers per day. A LinkedIn ad comes with its name, status and
review; what it shows is on the post it sponsors, which Outflare doesn't
read. Other
platforms sync campaigns only, so their ad sets and ads come with
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

### Qualified sign-ups with a predicted value

A sign-up carries no money, so an ad platform that is told about every
sign-up bids as hard for the people who never pay as for the ones who do. A
rule on an event such as `signed_up` has two switches for that, both off by
default:

- **Send a predicted value** (`predicted_value`): each event is sent worth
  what people like the person went on to pay, in the brand's currency, in
  place of the rule's own value.
- **Send qualified ones only** (`qualified_only`): events from people less
  likely to pay than the brand's average are skipped, and the send log says
  why. A throwaway email address is never qualified.

Both decide from the brand's own history, read with
`GET /v1/brands/{brand}/ads/signup-quality?event=signed_up`: of the people
who did the event in the 90 days that ended two days ago, how many paid
(`payment.succeeded`) within 30 days and what a payer paid on average, by the
country they came from and the kind of email address they gave (work, free,
throwaway or unknown, from Outflare's own classifier; no lookup leaves the
server). A group's share is pulled toward the brand's average where the
group is small, so twenty people from one country never swing a prediction.
The predicted value is that share of what a payer paid.

With fewer than 100 people or 10 payers in the window (`enough` is false)
nothing is predicted: every event is sent, with the rule's own value. The
history is read again every hour. The rule form shows the history under
**Who pays after this event**, with the breakdown by country and email.

| Platform | Where it goes | Matched on | Also needs |
| --- | --- | --- | --- |
| Google Ads | A conversion action that takes uploaded clicks (Upload clicks), through Google's Data Manager API (see [The conversion feed](#the-conversion-feed)) | `gclid`, `gbraid` or `wbraid`; hashed email and phone (enhanced conversions for leads) | The sign-in made with the Data Manager permission: a connection from before it is asked to sign in again (`setup_needed` names `reconnect`): **Sign in again** in the connection's menu on Connectors signs that same connection in, so it keeps its accounts and history |
| Meta Ads | A pixel (dataset), as one of Meta's standard events: Purchase, Lead, CompleteRegistration, StartTrial, Subscribe, Contact, SubmitApplication, Schedule, InitiateCheckout, AddPaymentInfo, AddToCart, ViewContent or Search | `fbc` (from `fbclid`), `fbp`, hashed email, phone and record id, and the person's IP address and browser | The system user assigned to the pixel |
| Microsoft Advertising | An offline conversion goal | `msclkid`; hashed email and phone | The developer token above |
| Reddit Ads | The pixel, through the Conversions API (v3), as SignUp, Lead, Purchase and so on | `rdt_cid`, hashed email and record id, and the person's IP address and browser | The workspace's own pixel ID and Conversions API access token, from Reddit Ads Manager → Events Manager. A connection with a token but no pixel ID still sends through Reddit's older v2.0 endpoint; add the pixel ID before Reddit retires it. |
| X Ads | A conversion event of the account's pixel, through the Conversions API, by its Event ID from Events Manager | `twclid`, hashed email and phone, and the person's IP address and browser (only when both are known) | The pixel ID and Event IDs on the connection, from X Ads Manager → Tools → Events Manager. Conversions are sent with the connection's keys, or with a Conversion API access token from Events Manager if you add one |
| TikTok Ads | A pixel, through the Events API, as CompletePayment, CompleteRegistration, SubmitForm and so on | `ttclid`, the `_ttp` cookie (`ttp`), hashed email, phone and record id, and the person's IP address and browser | The sign-in's Pixel and Events access; if TikTok refuses it, an Events API access token from Events Manager on the connection |
| Snapchat Ads | A Snap Pixel, through the Conversions API, as PURCHASE, SIGN_UP, SUBSCRIBE and so on | `ScCid`, the `_scid` cookie (`scid`), hashed email, phone and record id, and the person's IP address and browser | The sign-in's access; if Snapchat refuses it, a Conversions API token from Ads Manager → Business Details on the connection |
| Taboola Ads | An active event-based conversion (Realize → Tracking, "Create conversions using code"), by its event name, as a server-to-server action | `tblci` only | Nothing: the bulk S2S endpoint takes no token |
| Pinterest Ads | The Conversions API, as checkout, signup, lead and so on | `epik`, hashed email, phone and record id, and the person's IP address and browser | The workspace's own conversion access token, from Pinterest Ads Manager → Conversions |
| ChatGPT Ads | The pixel, through OpenAI's Conversions API, as `registration_completed`, `order_created`, `lead_created`, `subscription_created` and so on, with the value in the currency's minor unit | `oppref`, the pixel's `__obref` cookie, hashed email, phone and record id; beside them the IP address the event's own context carries (`context.ip`) and the person's browser (the event's `context.userAgent`, else the one the site script stored on their latest earlier event, up to 90 days back) and the person's country, unhashed as OpenAI asks | The pixel ID and a conversion key from OpenAI Ads Manager → Conversions, and the website address (OpenAI asks for it on web events). OpenAI takes conversions from the last 7 days only. |

The IP address and browser are the ones the event's own context carries
(`context.ip`, `context.userAgent`); the browser falls back to the one the
site script stored on the person's latest earlier event, up to 90 days back.
They go to Meta, TikTok, Snapchat, Pinterest, Reddit, X and ChatGPT Ads,
unhashed as each asks, only beside a click id or an identifier, never as the
only thing sent, and only when the conversion passes the consent check below.
The send log lists them as `ip_address` and `user_agent`. On server events,
send the user's own address and browser in `context`, not your server's.

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
  about a day. On Microsoft Advertising the same happens through its offline
  conversion adjustments (restate or retract), found by the click id
  (`msclkid`) or the hashed email or phone the conversion was sent with and
  the time it happened. Platforms that can't change a conversion after taking
  it skip the adjustment and say so: Meta's Conversions API has no call to
  take one back, so keep refunded customers out of Meta with an audience set
  to **Never show them ads** ([Audiences](#audiences-one-list-several-ad-networks)):
  make a list of the people who were refunded, and Outflare excludes it on
  every campaign.
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
  A brand whose own site already asks every visitor for consent to ad data
  and personalised ads,
  but hasn't yet passed those answers to Outflare, can choose **Everyone:
  our site already collects consent** (`collected`). Conversions are then
  sent for everyone, in every country and when the country is unknown,
  marked as consented; people who withdrew are still never sent. It is the
  brand's own statement about its site, so only a signed-in person can
  choose it (an API token or an agent gets `403`), the web app asks to
  confirm, and the change is in the audit log. It covers ad audiences too:
  people are added for everyone, as consented to ad personalisation, except
  those who withdrew.
  A person's country is their latest website visit's. For someone the site
  script never saw, such as a purchase sent only from your server, it is the
  event's own: `context.location.country` (two letters, such as `US`), else
  the country of `context.ip`. When that event says nothing either, Outflare
  looks at the person's most recent earlier event that carried a country or
  an IP address, and then at the `country` on their customer record (two
  letters). Only when none of these says is the person treated as country
  unknown. So send the user's IP address (or country) in `context` on server
  events: it is the most reliable source.
- Every matched event is a row in the send log: `sent` (with the kinds of
  identifiers it carried, such as `gclid` and `email`), `skipped` with the reason
  (no consent, nothing to match on), `failed` when the platform refused it or
  could not be reached after five tries, or `queued`. A Google send also says
  its `transport` (`data_manager`, or `ads_api` after a fallback) and the Data
  Manager `request_id` it went in, which Google reports on from about half an
  hour later. Each row also has the `country` the consent decision was made
  on and its `country_source` (`visit`, `event`, `earlier_event` or `record`),
  the conversion it was going to (`target_name`, `ad_account_id`) and the
  person's name (`record_name`). In the web app the log is its own screen,
  **Paid ads → Send log**: filter by result or rule, read what happened in
  plain words, open the person from any row, and download it as CSV.
  A sent row was only accepted: **Ask Google Ads** on the row asks the
  platform what it did afterwards (processed, still processing, or refused
  and why), which is `GET /v1/brands/{brand}/ads/conversion-sends/{id}/status`.
- **Sent against counted.** Above the log, a table compares, for each
  conversion action a rule sends to, the conversions the network accepted
  with what the network counts for that action, over 7, 14, 30 or 90 days
  ending two days ago (networks report a conversion a day or two late). It
  shows how many sends carried the network's own click id, the only ones it
  can count, the share of those it counted, and a status: Counted, Under half
  counted, None counted, Too few to judge (under 10 sends with a click id) or
  Not reported (the network doesn't report by conversion action; today only
  Google Ads does). `GET /v1/brands/{brand}/ads/conversion-check?days=`
  returns the same rows with the numbers per day.
- **Match quality.** Once a network has reported it, a second table shows
  how well the network can tie the conversions sent to one of its own
  accounts. Today that is Meta's event match quality: a score out of 10 per
  event on each pixel a rule sends to. Outflare reads it once a day with the
  workspace's own Meta connection and keeps a row per event and day, so the
  table shows the latest score, the average over the week before, the share
  of events that carried each identifier (email, phone, IP address, browser,
  Meta's click id) and, in Meta's own words, what it found wrong and how to
  fix it. The status is Good, Low (under 6), Fell (a point or more under the
  week before) or No score yet. An event a rule sends that reads Low or Fell
  raises the `ads.match_quality_low` [alert](alerts.md), at most once a week
  per event. The token needs `ads_read` with `ads_management` or
  `business_management`, and its user needs access to the pixel; without
  them the table stays empty and `ad_match_quality.read` records why.
  `GET /v1/brands/{brand}/ads/match-quality` returns the same rows with the
  score per day over 30 days.

On the **Paid ads** page, **Send conversions back** adds, edits and switches
rules, sets who to send for, and shows the latest sends. Adding a rule searches
the brand's own events by name, a rule on `payment.succeeded` takes the
payment's amount without asking, and any other event offers the number
properties it has carried, read as cents when the property's name says so
(`_minor`, `_cents`). A payment rule saved with no value says so on its row,
with one click to send the amount.

A rule on `payment.succeeded` also chooses **Payments to send** (`feed_role`):
**every payment** (`custom`, the default), **first payments only**
(`first_payment`) or **repeat payments only** (`repeat_payment`). First
payments only sends a person's first ever payment for the brand and nothing
after it, marked as a new customer, so each new paying customer counts once:
point it at a conversion action set to count **one** per click, and a renewal,
top-up or the same payment sent again adds nothing. Whether a payment is the
first is read from the brand's whole payment history, so import past payments
before you switch the rule on, oldest first. Repeat payments only sends every
later payment, marked as returning. A payment with no person is neither. The
conversion feed (below) makes the same two rules for you.

| Call | What it does |
| --- | --- |
| `GET /v1/workspaces/{ws}/ads/conversion-targets?account_id=` | Where an account's conversions can go, asked live from the platform, and any setup still needed |
| `GET`, `POST /v1/workspaces/{ws}/ads/conversion-rules` | List rules with their last 30 days, or add one |
| `PATCH`, `DELETE /v1/workspaces/{ws}/ads/conversion-rules/{id}` | Change, switch off or delete a rule |
| `GET /v1/workspaces/{ws}/ads/conversion-sends` | The send log, newest first |
| `GET /v1/brands/{brand}/ads/conversion-sends/{id}/status` | What the platform did with a sent conversion after accepting it |
| `GET /v1/brands/{brand}/ads/conversion-check?days=` | Per conversion action, conversions sent against what the network counts, with a status and the same per day |
| `GET /v1/brands/{brand}/ads/match-quality` | Per pixel and event, the network's match quality score out of 10, how it moved, the identifiers sent and what to fix |
| `GET`, `PUT /v1/brands/{brand}/ads/settings` | The brand's `consent_mode`: `opt_out`, `opt_in` or `collected` (the workspace route is deprecated and acts on the first brand) |
| `GET`, `POST`, `PUT /v1/brands/{brand}/ads/accounts/{id}/feed` | Outflare's conversion feed on a Google Ads account: read it, set it up, set the new customer acquisition goal (below) |
| `GET /v1/brands/{brand}/ads/feed-health?account_id=` | What Google counts for the feed's actions against what was sent, per day and campaign |

The MCP tools are `list_ad_conversion_targets`, `list_ad_conversion_rules`,
`create_ad_conversion_rule`, `update_ad_conversion_rule`,
`delete_ad_conversion_rule`, `list_ad_conversion_sends`, `get_ad_settings`,
`set_ad_settings`, `get_ad_conversion_check`, `get_ad_match_quality`, `get_ad_feed`, `create_ad_feed`
and `update_ad_feed`. X and
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

Outbrain Ads sends no conversions: the Amplify API has no call that takes them.
Outbrain's click id (`ob_click_id`) is still recorded on each visit, so its
clicks count in Outflare's own attribution.

## Audiences from journeys

## Audiences: one list, several ad networks

An **audience** is one of the brand's lists of people (a smart list such as
"paid in the last 90 days", a list you filled by hand, or a saved query's),
named once and sent to the ad accounts you choose, so the same people reach
several ad networks. It is under **Paid ads, Audiences**, and at
`/v1/brands/{brand}/ads/audiences` (`POST` with `name`, `list_id` and
`ad_account_ids`; `PATCH` to rename it, turn it on or off, point it at another
list or change where it goes; `POST .../{id}/sync` to sync now; `DELETE`).

- **What Outflare does on the network.** Through the workspace's own
  connection it makes a customer list named "<name> (Outflare)" on each ad
  account (a Google Ads Customer Match list, a Meta custom audience, an X
  custom audience, a Reddit customer list, a ChatGPT Ads custom audience),
  adds the
  people in the list, and once a day sends what changed: people who joined go
  in, people who left come out. Only hashed emails and phone numbers leave
  Outflare. On X the developer app needs Read and write permission, and on
  Reddit the sign-in needs edit access (`adsedit`); a connection made before
  that is connected again first. On ChatGPT Ads the list is a custom audience
  filled through the connection's Advertiser API key; which campaigns target
  or exclude it is chosen in OpenAI Ads Manager, and OpenAI doesn't offer
  custom audiences on campaigns shown in the EEA or Switzerland. OpenAI takes
  no conversion back, so a list of the people who were refunded, excluded on
  your campaigns there, is how a refund reaches it.
- **Consent.** The same rules as sending conversions, for ad data and ad
  personalisation: someone who may not be added is left out (`skipped`), and
  someone who withdraws consent is taken out at the next sync.
- **Status per network.** Each destination shows whether it synced, how many
  people were sent and left out, the last error, and what the network says it
  matched (`size`: Google's size for Search and Display and its match rate,
  Meta's estimate). Networks hide the number for small lists and take a day or
  two to count a new one.
- **Networks that take a file.** A network Outflare can't fill a list on, such
  as TikTok Ads, takes an upload instead: **Download file**
  (`GET .../ads/audiences/{id}/file`) gives a CSV with the columns
  `email_sha256` and `phone_number_sha256`, never a plain email or phone.
- **Turning it off or deleting it** stops the syncs. The lists already on the
  networks stay as they are; delete them there if they are no longer wanted.

- **Never show them ads.** An audience can be people no campaign should show
  to, such as everyone who was refunded (`exclude: true`). Once its list is on
  the network, each sync queues the list as an exclusion on every active
  campaign of the ad account that hasn't had it, so a new campaign is covered
  within a day. Each one is a change in the campaign's history
  (`exclusion_add`, kind `audience`), like any exclusion. Take it off one
  campaign (`POST .../ads/campaigns/{id}/exclusions` with `kind: audience`,
  the list's id on the network as `value` and `action: remove`) and it stays
  off that campaign. Choosing the other option again takes it out of the
  campaigns Outflare put it on. Google Ads (a negative customer list on the
  campaign; Performance Max takes none, and its change says so) and Meta Ads
  (excluded on each ad set) take this from here (`can_exclude`). On the other
  networks the list is still kept up to date, and you exclude it on your
  campaigns there.

Use any other list on the network as usual: add it to a campaign (Targeting,
on a campaign's page) or exclude it.

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
needs the Data Manager permission (a connection from before is signed in
again with **Sign in again** on Connectors, which keeps the connection). See
[Lifecycle email](lifecycle-email.md#journeys) for the step itself.

## Findings and decisions

After each hourly sync Outflare reads every campaign it can change (Google Ads, Meta, Microsoft Advertising, LinkedIn, TikTok, Reddit, X and ChatGPT) and writes what it finds into one queue, the brand's **findings** (`GET /v1/brands/{brand}/ads/findings`; the same rows, and the same calls, as `GET /v1/brands/{brand}/ads/suggestions`): a change it proposes, a change it made on its own, or an alert. Each is a yes/no question a person answers (`reason`), with the numbers behind it (`evidence`: the window, the spend, the payers, the target, the search terms or segments it rests on), the lever (`kind`), the rule that made it and its version (`rule`, `rule_version`), its autonomy class, and its expected change in cost per new paying customer (`impact_micros`, negative is better: a cut's from the campaign's marginal cost per payer, a negative keyword's from the wasted spend spread over the brand's payers).

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

**One action, and what you think of it.** Each row under Actions opens its own page (`GET /v1/brands/{brand}/ads/findings/{id}`): what it would change, every number its rule looked at, the expected effect, and how it turned out. There a person says **Good advice** or **Bad advice**, with a reason (`POST /v1/brands/{brand}/ads/findings/{id}/feedback` with `rating` and `note`, `ads.manage`). It decides nothing and changes nothing on the network. The rating and reason stay on the finding (`feedback`, `feedback_note`) and are kept as one of the brand's memories (kind `feedback`), which agents read with the brand's context, so the next advice follows it; rating again replaces both. It is an `ad_suggestion.rated` event with `rating` and `has_note`, never the reason. Where the server runs a brand agent (Outflare Cloud), the page also has **Ask the agent why**, which opens the chat on that action. MCP: `get_ad_finding`, `rate_ad_finding`.

The MCP tools are `list_ad_findings`, `approve_ad_finding`, `dismiss_ad_finding`, `acknowledge_ad_finding` and `revert_ad_finding`. The workspace status carries `ads_decisions_waiting` (area `ads`, info) while findings wait for a person. Every finding is an `ad_suggestion.created` event with its `autonomy`, `rule` and `unit`; a saving applied on its own (only where the extension allows it) is `ad_suggestion.applied` beside the change's own `ad_campaign.*` event with `auto: true`; a revert is `ad_suggestion.reverted`; a verdict is `ad_suggestion.judged` and a `finding.recorded`. Metrics: `outflare_ads_findings_total`, `outflare_ads_verdicts_total` and `outflare_ads_findings_open` on the Grafana row "Paid media (findings and verdicts)".

## Alerts

After each hourly sync Outflare reads every campaign and writes what needs a person's eye into the same list as a finding with `autonomy` `alert` (`GET .../ads/suggestions?autonomy=alert`), one open row per campaign (or per account, pinned to its biggest campaign) and kind, with a sentence (`reason`) and the numbers behind it (`evidence`: the baseline, the window). It stays open and is updated while it holds, and expires when it no longer does; a person **acknowledges** one (`POST .../ads/suggestions/{id}/acknowledge`, `ads.manage`) and it isn't raised again for a day while it holds. The kinds:

- `alert.spend_anomaly`: today's spend so far is outside half to one and a half times the same weekday's over the last 4 weeks scaled to the hour (the platforms' numbers lag about 3 hours, so the hour is taken 3 back), by more than three standard deviations and at least 20 of the currency, once the day is 4 hours in.
- `alert.pacing`: from the 8th of the month, the account's month-to-date spend is outside 90 to 110% of what its active campaigns' daily budgets add up to (30.4 a month, scaled to the day).
- `alert.flatline`: an active campaign that served on 6 of the last 7 days has shown nothing today by noon in its time zone.
- `alert.zero_conversions`: 300 clicks in 30 days (100 for a campaign named after the brand) with no sign-up: from Outflare's own acquisitions where the ledger runs (`unit` `signups`), else no platform conversion.
- `alert.disapproved`: live ads the platform disapproved (Google's `policy_summary`, stored as the ad's `policy_status` detail), with the network's reasons in words (`policy_reasons`, such as "Destination not working"). The ad's row in a campaign's Ads list shows the same reasons on a red **Disapproved** badge.
- `alert.auto_apply_on`: the account lets Google apply its own recommendations to bidding, keywords or match types (`recommendation_subscription`), so two sets of rules would be changing it.
- `alert.sends_failing`: the platform refused a tenth or more of the conversions sent to it in 24 hours (at least 10).
- `alert.sends_not_counted`: the platform accepted 10 or more conversions that carried its own click id over a week (ending two days ago, because it reports a conversion a day or two late) and counts none for that conversion action in the last 35 days. A platform only counts a conversion it can tie to one of its ad clicks, so it always counts fewer than were sent, but it should never count none of the ones that came with its click id: the conversion action was removed, isn't a primary conversion, or belongs to another account. Checked on every sync, for accounts whose platform reports conversions by action (Google Ads) and rules that name their action. Its button opens the send log.
- `alert.tracking_broken`: under half of the brand's paid visits in 24 hours carried a click id (at least 20), or paid visits with campaign ids brought no sign-up while the last week's days brought three or more.
- `overspend_pause`: a campaign spent more than twice its daily budget today. In Outflare it is an alert: pause the campaign yourself if the budget is right. Only where the server's extension allows it (Outflare Cloud, `Host.SetAdAutoApprove`) and the brand's rules are out of shadow mode does Outflare **pause it** on its own (an ordinary change on the **Changes** tab, actor system, `ad_campaign.paused` with `auto: true`), with the finding `applied_auto` and `measuring` so a person can revert it; in shadow mode the finding says what it would have done (`shadow`). A campaign on an account every brand shares is never paused on its own.

The workspace status (`GET /v1/workspaces/{ws}/status`) carries an `ads_alert` blocker (area `ads`) while any alert is open: critical when one says money or data is going wrong now (tracking broken, conversions refused, a campaign paused for overspending, spend far off its usual), a warning otherwise. Every alert is an `ad_alert.raised` event with its kind.

Two things also reach people outside the Paid ads page, as [alerts](alerts.md) that go by email, Slack or Telegram as each brand chose under Settings → Alerts: `ads.sends_refused`, when an `alert.sends_failing` finding opens, and `ads.sync_failed`, when a connected ad platform refuses the sign-in or its sync fails three times in a row (once each time it starts failing), because the numbers above then stop being current.

## In the weekly digest

A brand with ad spend gets a **Paid ads** section in its
[digest](digests.md) (weekly on Mondays unless changed, in the app, as the
`digest.ready` webhook and by email when the brand chose that): ad spend,
clicks, the sign-ups and new paying customers the ads brought and the cost
per new paying customer, each beside the period before, in the brand's
currency. Under them are the three campaigns that spent the most with what
each brought (marked good or bad against the brand's target cost per new
paying customer), how many alerts are open, how many recommendations wait
for a decision, and how many of the changes measured that period worked.
Sign-ups and paying customers are Outflare's own count from the ledger,
not the platform's. Spend in a currency with no exchange rate is left out
rather than added as if it were the brand's. Section id `paid_ads`.

In the app, **Paid ads → Set up** has a **Weekly report** card that says
when the digest is made, who gets it by email, and opens the Digests page
to change either.

## Google's own changes

Every 15 minutes Outflare reads each Google Ads account's change history (`change_event`, which Google keeps for 30 days) into its own store, so what changed on the platform sits beside Outflare's own changes for as long as you like: `GET /v1/brands/{brand}/ads/platform-changes` (`analytics.read`; `connector`, `account_id`, `campaign_id`, `actor`, `source`; newest first, paged). Each says when, what changed and how (`resource_type`, `operation`, `summary`, and in `detail` the changed fields with the resource before and after), where it was made (`source`: `web`, `api`, `automated_rule`, `recommendation_auto_apply`, `script`, `editor`, `bulk`) and who (`actor`): `outflare` for Outflare's own changes (a change it applied on that campaign around then, or the connection's own sign-in through the API), `google` for Google's own automation (a rule or recommendation it applied), else the SHA-256 of the user's email; the address itself is never stored. Google Ads Editor changes aren't in the history.

## Tests

A before-and-after comparison can be thrown off by anything else that changed that week. A test avoids that: the network itself splits one campaign between how it is and a **trial** that tries one thing differently, on the same days, so the two compare fairly. Start one on the **Experiments** tab with **New experiment** (it lists the campaigns that can be tested), or press **Test it first** on a budget suggestion instead of approving it.

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

**Proposed experiments.** A recommendation that can spend more is offered
as an experiment instead of a plain approval when the network can test
that kind of change (a bid target on Google Ads, a budget on Meta) and the
campaign converts often enough for an even split to reach an answer within
six weeks. Its row under Actions has the type **Experiment** and its main
button is **Test it first**; **Make the change now** is under **More**. In
the API the finding carries `test_first` (`kind`, `days`, `note`); approve
it with `{"test": true}` to start the test. Changes that only save money
are made, not tested.

X Ads has no tests here: X splits an audience only between campaigns that already exist, and offers no way to copy a campaign with its ads, so change an X budget directly and compare the days before and after.

## Whole business: all ad spend against all revenue

**Paid ads → Whole business** leaves attribution out of its headline. It sets
every payment the brand received (less refunds, from ads or not) against what
every connected network charged in the same days: "Every $1 of ads brought
back $3.90 in revenue." No network's own counting can move that number.

- **The five numbers**: total revenue, ad spend on all networks, revenue per
  unit of ad spend (judged against the brand's return on ad spend target),
  new customers from all sources (people whose first ever payment fell in the
  range, and how many of them an ad click brought), and the blended cost per
  new customer (ad spend divided by every new customer). Each is compared with
  the same number of days just before.
- **Ad spend against total revenue, by week**: a bar per week with a colour
  per network, and all revenue as a line over it.
- **Where the money went**: per network, its spend and share, the conversions
  the network says it won, and the new paying customers Outflare traced to
  its clicks. Two networks can claim the same customer, so their total is
  often higher than the real one.
- **Do new customers pay back what they cost?** One row per month for the
  last five: the people who first paid that month, the month's ad spend
  divided by them, and what a customer had paid 30, 60, 90 and 120 days after
  their first payment. A column fills in once at least half of the month's
  customers have had that long. **Paid back in** is the day that revenue
  reaches the cost; "about" means the month isn't there yet and the number
  carries its pace so far forward.

Everything is in the brand's currency. Money in a currency with no exchange
rate is left out, and **How this is counted** names it. **Download CSV**
saves every number on the page. In the API this is
`GET /v1/brands/{brand}/ads/business` (`getAdsBusiness`), and the MCP tool is
`get_ads_business`.

## Creatives: which ads to scale, fix or stop

**Paid ads → Creatives** shows every ad that ran in the period as a large
preview with its verdict, most spend first. Switch between **Gallery** and
**List** at the top right; Outflare remembers the one you chose for the brand.

Each ad is scored step by step, so you can see where it loses people:

| Step | What it measures | Shown for |
| --- | --- | --- |
| Hook | Plays past the opening, out of everyone who saw it | Video |
| Hold | Plays to the end, out of plays past the opening | Video |
| Click | Clicks, out of everyone who saw it | All ads |
| Sign up | Sign-ups, out of clicks | All ads |
| Pay | First payments, out of sign-ups | All ads |

A step gets a grade against the usual rate of the brand's own ads on the same
network over the same days: **A** at 1.3 times usual or more, **B** from 0.9,
**C** from 0.6 and **D** below. A step has no grade until it has enough data
(1,000 impressions, 100 plays, 50 clicks or 10 sign-ups). Sign-ups and
payments come from Outflare's own count, not the network's.

The verdict follows from the grades and the brand's target cost per new
paying customer (the brand's economics, under Settings):

- **Scale**: at or under the target with at least two new paying customers and no D.
- **Keep**: doing its job; nothing to change.
- **Fix**: one step is graded C or D. The card names the weakest one.
- **Stop**: it spent twice the target without one paying customer, or costs over 1.5 times the target.
- **Too early**: not enough data yet.

Without a target no ad is marked Scale or Stop. **Pause** pauses the ad in
the network after you confirm; **Open campaign** takes you to its campaign to
change the budget. In the API this is `GET /v1/brands/{brand}/ads/creatives/results`
(`listAdCreativeResults`), and the MCP tool is `list_ad_creative_results`.

## Holdout tests

Attribution says which ad a person clicked before they signed up. It can't
say whether they would have signed up anyway. A holdout test answers that
by going without: keep your ads out of a few countries for some weeks, and
Outflare compares sign-ups from those countries with sign-ups from
everywhere else, before and during. If the other countries held steady and
the held-out ones dropped, the drop is what the ads were adding there.

1. On **Paid ads → Experiments → Holdout tests**, press **New holdout**.
   Pick the countries, the first and last day (7 to 90 days), and whether
   every network's ads are off there or only one's. Pick countries that
   together bring a steady share of your sign-ups, at least 30 in as many
   days as the holdout runs, and not your biggest one.
2. Exclude those countries on every campaign for the same days: on a
   Google Ads campaign's page under Places (or
   `POST .../ads/campaigns/{id}/locations`), or in the network itself.
   **Outflare changes no campaign for a holdout**; it only counts.
3. Read the result as it fills in. Take the exclusions off when it ends,
   or press **End today** to end it early.

Each holdout shows, for the held-out countries and for the rest, the
sign-ups from every source (ads or not), the ones an ad was credited with,
and the visits that arrived from a paid click, over the same number of
days before the start and since. From those:

- `expected_signups`: what the held-out countries would have had with the
  ads on. Their sign-ups before, moved by as much as the rest moved.
- `lift_signups`: expected minus what they had. What the ads were adding.
- `incrementality`: the lift over the sign-ups attribution credited to ads
  there before. 0.8 means four in five credited sign-ups were really won
  by the ads; the fifth would have come anyway.

| Result | Meaning |
| --- | --- |
| The ads add sign-ups (`ads_add`) | The drop is more than chance explains. |
| The ads add little (`ads_add_little`) | A real drop, but under a third of what the ads are credited with. |
| No difference found (`no_difference`) | The held-out countries did about as well without ads. |
| Ads still showing there (`leaked`) | Paid clicks kept arriving from the held-out countries at more than a tenth of their rate before: check every campaign excludes them. |
| Too few sign-ups to say (`not_enough_data`) | Under 30 sign-ups before the start, there or elsewhere. |

A sign-up is your sign-up event (`signed_up`, and any other event the
brand counts as a conversion that isn't a payment); its country is the
country of the person's latest visit. Days are UTC.

| Call | What it does |
| --- | --- |
| `GET /v1/brands/{brand}/ads/holdouts` | The brand's holdouts with their results (`analytics.read`, MCP `list_ad_holdouts`) |
| `POST /v1/brands/{brand}/ads/holdouts` | Note a holdout: `name`, `countries`, `starts_on`, `ends_on` and an optional `connector` (`analytics.manage`, `create_ad_holdout`) |
| `POST /v1/brands/{brand}/ads/holdouts/{id}/end` | End a running holdout today (`analytics.manage`, `end_ad_holdout`) |
| `DELETE /v1/brands/{brand}/ads/holdouts/{id}` | Delete the note (`analytics.manage`, `delete_ad_holdout`) |

They are in the audit log as `ad_holdout.created`, `ad_holdout.ended` and
`ad_holdout.deleted`.

## Managing campaigns

Three changes, on Google Ads, Meta, Microsoft Advertising, LinkedIn,
TikTok, Reddit, X, ChatGPT, Zeropark, Taboola and Outbrain (the other
platforms are read but not changed yet, and say so with `422
connector_invalid`). On Taboola the daily budget is the campaign's daily
cap. On Outbrain it is the campaign's own budget when that is a daily one
(or its daily target, when it is paced by one); a budget shared with other
campaigns, or a monthly or whole-campaign one, is refused with a reason,
because changing it would not mean what was asked. On Reddit, a campaign with campaign
budget optimization holds its daily budget; otherwise the campaign's budget
is the sum of its ad groups' daily budgets, and a new one is shared between
them in the same proportions. A campaign with only lifetime budgets is
refused with a reason. On X, the app must have Read and write permission
before you sign in (sign in again after changing it). On ChatGPT, the
campaign's daily spend limit is set (at least 1 of the account's currency);
a campaign with only a lifetime limit is refused with a reason, and
resuming starts delivery once its ad groups and ads are active and approved:

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

### Ad groups and ads

Below the campaign, one ad group (Meta's ad set, Google's, TikTok's or
Reddit's ad group) or one ad is paused and resumed on its own, on Meta,
Google Ads, TikTok and Reddit; on Taboola and Outbrain, which have no ad
groups, one ad is (a Taboola item, an Outbrain promoted link). On Microsoft
Advertising and X the ad group is (X's line item); their ads are read but
not changed yet. An ad group that holds its own daily budget has it set on
Meta and TikTok. Snapchat's and Pinterest's ad groups and ads, Microsoft's
ads, and LinkedIn's ads, are read but not changed yet
(`422 connector_invalid`):

| Call | What it does |
| --- | --- |
| `POST /v1/brands/{brand}/ads/items/{id}/pause` | Pause an ad group or an ad; the id is from `GET .../ads/items` (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/items/{id}/resume` | Run it again (`ads.manage`) |
| `PATCH /v1/brands/{brand}/ads/items/{id}` | Set an ad group's daily budget, `daily_budget_micros` in the account's currency (`ads.manage`) |

Each answers `202` with a change on the item's **campaign** (`ad_group_pause`,
`ad_group_resume`, `ad_group_budget`, `ad_pause` or `ad_resume`; migration
0589 widens `ad_changes.kind`), whose `after` names the ad group and the ad
by the platform's ids and by name, so the **Changes** tab says which. A job
makes the one call on the workspace's own account and the item shows the
change at once; the next sync reads it back. One change at a time per item:
a second while one is queued is a `409`.

A budget is only for an ad group that holds one: a Meta ad set or a TikTok
ad group whose campaign holds the budget answers `409` and says to change
the campaign's,
an ad has none (`400`), and Google's ad groups have no budget to set
(`422`). On the Paid ads page, a campaign's page has **Manage** on each ad
set and ad.

For an agent, a pause and a lower budget can only spend less, so they run
under its grant; a resume and a raise always wait for a person. The audit
events are `ad_campaign.ad_group_paused`, `ad_campaign.ad_group_resumed`,
`ad_campaign.ad_group_budget_changed`, `ad_campaign.ad_paused` and
`ad_campaign.ad_resumed`, with `level` and never the item's name.

### Bids by hand

On a Google Ads campaign that bids by hand (`bidding_strategy`
`manual_cpc`), an ad group's default bid for a click and a keyword's own
bid are set the same way a campaign's bid is:

| Call | What it does |
| --- | --- |
| `POST /v1/brands/{brand}/ads/items/{id}/bid` | Set an ad group's default bid: `bid_micros`, and `only_lower` (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/keywords/{id}/bid` | Set one keyword's own bid (`ads.manage`) |

Each answers `202` with a `bid` change on the campaign, naming the ad
group and the keyword. The job reads the bid in effect now (a keyword
without a bid of its own uses its ad group's), keeps it as the change's
`before`, and with `only_lower` refuses anything but a lower bid, so the
change can only spend less. A campaign that bids automatically ignores
bids made by hand, so the call answers `409` there and says to change how
the campaign bids, or its target, instead. Bids are rounded to a
hundredth of the account's currency, from 0.01 to 1000. On the Paid ads
page, **Set bid** is in the Manage menu of an ad group and of a keyword
on such a campaign.

For an agent, a change with `only_lower` runs under its grant; without it,
it always waits for a person. Bid adjustments (by device, place or hour)
are not offered: under automatic bidding the platforms ignore them, and a
schedule or a device is excluded instead ([Targeting](#targeting)).

### Removing a campaign, an ad or a keyword

On Google Ads, a campaign, an ad or a keyword can also be removed for
good. Google Ads can't undo a removal, so:

| Call | What it does |
| --- | --- |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/remove` | Remove a campaign (`campaign_remove`) |
| `POST /v1/brands/{brand}/ads/items/{id}/remove` | Remove an ad (`ad_remove`; the id is the ad's, from `GET .../ads/items?level=ad`) |
| `POST /v1/brands/{brand}/ads/keywords/{id}/remove` | Remove a keyword (`keyword_remove`) |

- removing is its own permission, and the refusal names it
  (`403 permission_required` with `permission`):
  - a person needs **Remove ad campaigns, ads and keywords** (`ads.remove`)
    in their role. Owner, Admin and Member have it; a role the workspace
    changed or made has it only when ticked under Settings, Roles;
  - an API token needs the scope `ads.remove`;
  - an agent token needs a grant that names the removal: the permissions
    `ads.remove_campaign`, `ads.remove_ad` and `ads.remove_keyword`
    (grant paths `brands/*/ads/campaigns/*/remove`, `.../items/*/remove`,
    `.../keywords/*/remove`). Off (`refuse`) is the default and refuses it,
    Ask (`hold`) waits for a person's approval, On (`run`) removes at once. A
    broad grant such as `**` or `ads/**` never covers a removal. A grant's
    `conditions` keep it to some removals (`paused_campaign_only`,
    `disapproved_only`, `max_spend_micros` over the last 30 days) and its
    `daily_limit` to a number a day; a removal outside them is refused;
  - a staff view is read-only;
- the body must say `{"confirm": true}` (`400` otherwise); the app asks first,
  naming what can't be undone, from each row's **Manage** menu;
- it is a change like a pause (migration 0505 widens `ad_changes.kind`): the
  job removes it with the remove operation of Google's campaign, ad group ad
  or ad group criterion service, reads it back, and only then marks it
  removed in Outflare. Its numbers stay. The audit events are
  `ad_campaign.removed`, `ad_ad.removed` and `ad_keyword.removed`, each
  with a `snapshot` of what was removed (name, status, campaign, ad group,
  the ad's text and links, the last 30 days' spend), so a person can make
  it again by hand, and the agent's `action_id` (which says who approved
  it) when an agent asked.

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
| `GET /v1/brands/{brand}/ads/campaigns/{id}/targeting` | Its bidding method with its full settings, audiences, and every place it shows in (`included_locations`) and is kept out of (`excluded_locations`) by name, with the platform's own ids, read live, plus the bidding methods the platform lets Outflare set (`analytics.read`) |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/bidding` | Bid for as many conversions as it can (`maximize_conversions`, with an optional `target_cpa_micros`: a standard Target CPA) or as much conversion value (`maximize_conversion_value`, with an optional `target_roas`: a standard Target ROAS), for a share of searches (`target_impression_share` with `impression_share_location`, `impression_share` and `max_cpc_micros`), for as many clicks as it can (`target_spend`, with an optional `max_cpc_micros`), or with a portfolio strategy of the account (`portfolio` with `bidding_strategy_id`); Google Ads only (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/audiences` | Add or remove (`add: false`) one of the account's audiences, such as one made from a smart list (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/countries` | Add or remove a country by its two-letter code (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/locations/preview` | What setting the campaign's places would add and take out, without changing anything (`analytics.read`) |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/locations` | Set the places the campaign shows in and is kept out of, in one change (`ads.manage`, Google Ads) |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/rename` | Rename the campaign (`ads.manage`, Google Ads) |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/bid` | Set the campaign's bid, or one placement's with `placement` (Zeropark: a target): `bid_micros`, and `only_lower` to apply it only if it lowers the bid the network shows (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/exclusions` | Add or take out an exclusion: `kind`, `value` and `action` (`add` or `remove`). On Zeropark the kind is `placement` and the value one of the campaign's targets (its address or hash): the target is paused and the campaign keeps running on the others. On Taboola the kind is `placement` and the value a publisher's site id as Taboola's reports give it (such as `examplenews-home`): that site is blocked on the campaign. On Outbrain the kind is `placement` and the value a publisher's id, or a section's with `detail` `section`, as Outbrain's publisher and section reports give them (at most 30 publishers and 100 sections a campaign); Outbrain takes the whole blocked list on every change, so Outflare reads the current list first and changes nothing if it can't. On Google Ads: a device, location, placement, network, schedule or brand (`ads.manage`) |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/negative-keywords` | Exclude a search: `keyword`, `match_type` (`exact`, the default, for only this search, or `phrase` for every search that contains it) and, to exclude it in one ad group only, `ad_group_id`. Queued as a `negative_add` change on networks whose keywords Outflare can change (Google Ads and Microsoft Advertising; Microsoft has no broad negatives, so `broad` is sent as phrase). A negative can only spend less. |
| `POST /v1/brands/{brand}/ads/campaigns/{id}/keywords` | Add a search as a keyword of one ad group: `keyword`, `match_type` (`exact` or `phrase`) and `ad_group_id`. A new keyword can spend more, so a person's session only (403 `person_only` for a token or an agent) |

**Setting the places together.** Send `included` (every place to show in), `excluded` (every place to keep out of), or both, as two-letter country codes or Google's ids (`geoTargetConstants/2036`). A side you leave out stays exactly as it is, so taking out some exclusions means listing the ones to keep. `target_all_locations: true` says outright that the campaign shows everywhere it isn't excluded from. The preview answers the places `before` and `after`, `add_included`, `remove_included`, `add_excluded` and `remove_excluded` by name and id, `reaches_more`, and `location_fingerprint`. Send that as `expected_fingerprint` with the change: it is refused with `409` if the places moved since, and checked again when the job runs. The removals and additions go to Google in one request, which it takes whole or not at all, and the places are read back afterwards. An agent's preview is answered straight away; its change always waits for a person.

**What the read answers.** `targeting.included_locations` and `excluded_locations` list every place criterion: its `name` (a country's name, or a region's or city's full name), `country_code`, `target_type` (Country, Region, City, or Proximity for a radius), and Google's `criterion_id`, `resource_name` and `geo_target_constant`. `target_all_locations` is true when the campaign names no place to show in, so it runs everywhere it isn't excluded from; `countries` is empty for that reason only then. `positive_geo_target_type` and `negative_geo_target_type` are Google's location options (presence, or presence or interest). `observed_at` is when it was read, and `provider_campaign_resource_name` is the campaign as Google names it. `targeting.bidding` carries the whole strategy: its target, and for a target impression share where on the page (`anywhere_on_page`, `top_of_page`, `absolute_top_of_page`), the share from 0 to 1 and the most paid for a click; `bidding_strategy_id` is there only when the campaign bids with a portfolio strategy.

An excluded place is taken out, or another added, one at a time with `POST .../ads/campaigns/{id}/exclusions` (`kind` `location`, `value` the place's `geo_target_constant`), so the other exclusions stay as they are.

**A bidding change is read back.** After the job makes it, Outflare reads the campaign's bidding from the platform again. The change's `verified_at` is set when the platform shows what was asked; otherwise `verify_note` says what it shows.

### Reading anything else live

For whatever the calls above don't answer, one call reads any Google Ads resource live, in Google's own terms, and a second names what can be asked for. They only read.

| Call | What it does |
| --- | --- |
| `GET /v1/brands/{brand}/ads/accounts/{id}/query` | `resource` (such as `campaign`, `ad_group_criterion`, `ad_group_ad`, `campaign_criterion`, `shared_set`), `select` (the fields, comma-separated), `where` (one comparison each, repeated, joined with AND), `order_by` and `limit` (100 unless given, at most 1000). Answers the rows in Google's shape with `row_count`, `complete`, `observed_at` and the query as it was sent (`analytics.read`) |
| `GET /v1/brands/{brand}/ads/accounts/{id}/query-fields` | The resources that can be read, or with `resource` its fields: type, whether each can be selected, filtered and sorted, and an enum's values (`analytics.read`) |

A read without a metric or a date condition lists everything the resource holds, so paused campaigns and keywords that never had an impression are there too. That makes it the way to take stock of a campaign before changing or pausing it: its settings (networks, schedule, ad rotation, tracking template), ad groups, keywords and bids, negatives and shared lists, every ad's text, pins, paths and policy status, and asset links. It also reads reporting the stored numbers don't keep, such as `metrics.search_impression_share`, `metrics.search_absolute_top_impression_share`, `metrics.search_budget_lost_impression_share`, `metrics.search_rank_lost_impression_share` and `metrics.search_click_share` by campaign, keyword or `segments.date`. A metric Google withholds is absent from the row, never zero. `complete` is false when there were more rows than `limit`: narrow the conditions, or order by an id and ask for the ids after the last one. MCP tools: `query_ad_account`, `list_ad_query_fields`.

A **portfolio bidding strategy** is one target several campaigns share. The account's own are managed on **Paid ads → Accounts**, and a campaign's Targeting card puts it on one (**Change bidding → Portfolio strategy**); switching it back to a standard strategy takes it off.

| Call | What it does |
| --- | --- |
| `GET /v1/brands/{brand}/ads/accounts/{id}/bidding-strategies` | The account's portfolio strategies, read live: type, status, target and how many campaigns use each (`analytics.read`) |
| `POST /v1/brands/{brand}/ads/accounts/{id}/bidding-strategies` | Create a Target CPA (`target_cpa` with `target_cpa_micros`), Target ROAS (`target_roas` with `target_roas`), Maximise conversions, Maximise conversion value, Target impression share (`target_impression_share` with `impression_share_location`, `impression_share` and `max_cpc_micros`) or Maximise clicks (`target_spend`) strategy (`ads.manage`) |
| `PATCH /v1/brands/{brand}/ads/accounts/{id}/bidding-strategies/{strategy}` | Move its target or rename it; every campaign on it bids to the new target (`ads.manage`) |

These, a website conversion action and the account's callouts ([Account settings](#account-settings)) are **account changes** (`ad_account_changes`, migration 0507): each answers `202` with the change, the same `ads.change` job makes it and reads it back, and `GET /v1/brands/{brand}/ads/accounts/{id}/changes` (or `/changes/{change}`) shows how it ended, with what Google made in `result`. An agent's always waits for a person: raising a target cost per conversion or lowering a target return spends more.

Each answers `202` with the change, which runs as a job like the other campaign changes and shows on the **Changes** tab. A bid is `bid` there (audit event `ad_campaign.bid_changed`; migration 0581 widens `ad_changes.kind`): the job reads the bid the network shows, keeps it as the change's before, and with `only_lower` fails the change unless the new bid is lower, so it can only spend less. An agent's bid change with `only_lower` follows its grant; without it, it always waits for a person. An exclusion is `exclusion_add` or `exclusion_remove` there (audit events `ad_campaign.exclusion_added` and `ad_campaign.exclusion_removed`); adding one can only spend less, so an agent's follows its grant, while taking one out always waits for a person. Zeropark campaigns can be paused, resumed and given a daily budget like the other platforms', but the rules make no findings for them: they judge by customers acquired, which Zeropark's traffic has no click id to tie to. On Meta an audience or a country is added to or removed from every ad set of the campaign, keeping the rest of each ad set's targeting as it was. A campaign never loses its last country: removing it is refused, so it can't start running everywhere. A change made by an agent always waits for a person, whatever its grant. Each is an audit event (`ad_campaign.bidding_changed`, `ad_campaign.audience_added`, `ad_campaign.audience_removed`, `ad_campaign.country_added`, `ad_campaign.country_removed`).

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
| Date ranges | At least the last 30 days; a custom range recommended | One date menu: today, yesterday, the last 7, 28, 30 or 90 days, this month, last month, or any from and to |
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
a person on the **Actions** tab under **New ads**. A search ad shows as it
reads in Google's results: up to three headlines, the site with its path,
and up to two descriptions, with how many more there are. Under it, the
whole ad is listed: every headline and description with its length against
Google's limit (on a campaign's ads table, behind **All headlines and
descriptions**), **Another combination** shows a different pick (Google
chooses which to show each time), **Show on a phone** narrows it, and
**Edit** opens the text beside its preview; a line that is too long can't
be saved. A campaign's own ads show the same way once they have synced
(`details.headlines`, `details.descriptions`, `details.path1` and `path2`
on an ad, one line each). Approving one makes it
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

**The Overview's numbers.** The Overview opens on six numbers: cost per
new customer, cost per sign-up, new customers, spend, revenue and return
on ad spend. Each shows its change against the period it is compared
with, the old number, a small chart of the range, and a coloured word
against the brand's target for it. "Compared with" switches between the
days straight before the range and the same days a year earlier. "Choose
numbers" opens a panel to pick up to eight numbers (sign-ups, clicks,
cost per click, click to sign-up rate and the networks' own conversions
are the others), put them in order and set the targets, saved with Save.
Beside the two cost targets, a brand can set `target_new_payers_monthly`,
`monthly_budget_micros`, `target_revenue_monthly_micros` and
`target_roas`; `overview_numbers` holds the choice. A monthly target is
judged by the share of a month (30 days) the range covers, so 50 new
customers a month is 47 for a range of 28 days. A cost within a tenth of
its target is on target, under it has room to spend more, over it costs
too much. A count, revenue or return within a tenth of its target or
above it is on target. Spend is inside its budget or over it. The
monthly budget is only compared against: nothing stops spending, which
is what the daily budget ceiling is for. `daily` in
`GET /ads/performance` carries each day's `clicks`, `impressions`,
`platform_conversions` and `revenue_micros` beside spend, sign-ups and
new customers, which is what the small charts draw.

**The daily budget ceiling.** `daily_budget_ceiling_micros` is the most the
daily budgets of the brand's active campaigns may add up to; set it under
**Settings → Limits**. A raise that would pass it (a budget change, or
approving a budget raise) answers 409 `ad_budget_ceiling` and queues
nothing. A signed-in person is told so and can go ahead by confirming a
second time (`over_ceiling: true`); an API token or an agent can't. A cut
always passes. Resuming a paused campaign is checked the same way, since
its whole daily budget comes back: over the ceiling it answers 409
`ad_budget_ceiling`, and a signed-in person can confirm
(`over_ceiling: true` on the resume, or on approving a suggested resume). Anyone with `analytics.manage` may set or lower the ceiling;
only a signed-in person raises or removes it (403 `person_only` otherwise).

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
harvested it, else from the person's earlier visit with the very same click
id that carried the template's ids or `gad_campaignid` (a sign-up page on
another domain often keeps the `gclid` but not the campaign, and Google's
click report leaves some clicks out), else the campaign from the UTM tags:
`utm_campaign` as the campaign's id when it is all digits (a link with
`utm_campaign={campaignid}`), else as its name. A visit with a different
click id is a different click and lends it nothing.

Google reports a day's clicks the next day, so a sign-up or payment from a
click that carried only a `gclid` is usually read before its click is
known, and is written with the campaign unknown. Every run, once the click
harvest or the brand's campaigns have changed, the ledger looks again at
the brand's acquisitions and payments still without a campaign whose click
is under 90 days old (Google keeps 90 days), with the same rules, and fills
in the campaign, ad group, ad and keyword it now finds (`matched_by`
`click_id`) and the campaign a payment's revenue counts for, and gives a
refund its payment's. So "Campaign unknown" for a Google click usually
fills in the next day.

- A sign-up with a paid click makes an **acquisition**; a sign-up by
  someone already acquired adds its time to theirs.
- A person's **first ever payment** makes them a new customer: it is set
  on their acquisition, or makes one when the payment itself had a paid
  click in the window. It is revenue in the **new** bucket.
- A later payment is **returning** revenue: lifetime revenue of the
  campaign that acquired them, and revenue by click for the campaign
  whose paid click was last before the payment within the window, if any.
  Only people an ad brought in count in the reports: when a customer who
  came another way clicks an ad before a later payment (someone who
  searches for your name to log in), the row is kept but adds no revenue
  or ROAS to the campaign.
- A refund is a negative row with the same acquisition, campaign and
  bucket as the payment it refunds, found by its `payment_id`.
- `ad_consent` records whether the person had granted ad data consent, as
  conversions sent back decide it.

Events are read in the order they arrived, so a payment a billing
connector synced late still counts. Merging two records moves their
acquisitions and revenue; erasing a person removes theirs. The job records
`ad_acquisition.recorded` and `ad_revenue.recorded`, `paidads.campaigns_filled`
when it fills in unknown campaigns, and `paidads.run_failed` when a run
fails; its metrics (`outflare_paidads_ledger_events_total`,
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

### Counting by another attribution model

The ledger ties each sign-up to the last ad click before it. To see the
same campaigns under one of the brand's attribution models, pass
`model=` (`first_touch`, `linear`, `last_non_direct`, or the id of a
model the brand has credited): each row then also carries
`model_signups`, `model_revenue`, `model_cost_per_signup_micros` and
`model_roas`, the model's share of the conversions in the range for the
visits that came from that campaign, ad group or ad. A model splits a
conversion between visits, so `model_signups` can be a fraction. The
usual columns stay as they are, and the response's `model` says which
one was used (`last_paid_click` when none was asked for). An unknown
model, or one with no credits yet, is a 400.

In the app, the Campaigns table has "Also count by" for this, which it
remembers per brand, and shows the network's own count as "Network
says", so three answers sit side by side: the last ad click, the model,
and the network.

### Reading it

| Call | What it answers |
| --- | --- |
| `GET`, `PUT /v1/brands/{brand}/ads/economics` | The brand's targets (the two costs, new customers, budget and revenue a month, return on ad spend), the numbers its Overview shows, window, payback, margin and the two switches (`analytics.read`; `PUT` is `analytics.manage`, the switches a person's) |
| `GET /v1/brands/{brand}/ads/campaigns/{id}/signals` | Read live from the network: the share of eligible impressions the campaign lost to its budget and to its rank, and its numbers by device, network, country, placement and hour over `from`–`to` (`analytics.read`) |
| `GET /v1/brands/{brand}/ads/performance` | Per campaign, ad group or ad (`level`) over `from`–`to`: spend, clicks, impressions and the network's own conversions beside `signups`, `new_payers`, `cost_per_signup_micros`, `cost_per_new_payer_micros`, `target_status` (`on_target` within a tenth, `under`, `over`), `new_revenue`, `returning_revenue`, `lifetime_revenue`, `roas` (return on ad spend: new and returning revenue in the account's currency over spend; null without spend), `payback_months` and `trend`; `model` adds the four `model_*` columns counted by that attribution model; `connector`, `account_id`, `campaign_id`, `window_days` and `customers` (`all`, `new`, `returning`) narrow it. `daily` is the same campaigns per day and currency (`spend_micros`, `signups`, `new_payers`), which add up to the rows (`analytics.read`) |
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

### Would matching by network and device help?

Outflare ties a visit to a person only by what the browser itself says: a
sign-in, a click in an email it sent, or a visitor link. So someone who
clicks an ad on a phone and signs up on a laptop is not credited to the
ad. Some tools close that gap by guessing: two browsers on the same
network, in the same place, on the same kind of device are called one
person. Outflare doesn't do that, and this check tells you what you would
gain and risk if it did, before anything changes.

**Paid ads → Settings → Tracking → Run the check** (or
`GET /v1/brands/{brand}/ads/match-check?days=30`, MCP
`get_ads_match_check`) reads the brand's stored visits. It collects
nothing new, joins nothing and changes no credit. A visit's signature is
what a visit already keeps: its network (ASN), country, region, city, time
zone, device type, browser, system, device brand and model. It holds no
IP address and no user agent, so it is coarser than what such tools use;
read the result as a lower bound on accuracy.

- `would_credit`: known people no ad is credited with today, for whom
  exactly one unknown browser with the same signature clicked an ad within
  the acquisition window before one of their visits.
- `ambiguous`: people for whom more than one such browser fits, or whose
  signature more than 50 browsers share (an office, a mobile carrier).
- `accuracy`: the test against what is known. Of the pairs of browsers
  with the same signature that each belong to a known person
  (`known_pairs`), the share that are the same person.
- `verdict`: `worth_testing` when the guess was right for at least 90% of
  known pairs and would credit ads with at least 5% more people,
  `not_worth_it` otherwise, and `not_enough_data` under 20 people or 20
  known pairs.

### The set-up path

`GET /v1/brands/{brand}/ads/setup` lists the steps to measuring ads this
way, each with `done`, a line on where it stands and the page and MCP tool
that fix it: the website script on the site, sign-ups and payments sent as
events, a billing connection (or payments sent from your server), an ad
account on the brand, conversions sent back to the network, the tracking
template on every live Google ad, the economics set, the knowledge sheet
reviewed, and the consent setting chosen. The Paid ads page shows them
under **Set up**.

## The Paid ads page

The page has six tabs, one job each, and one date menu in the header
(today, yesterday, the last 7, 28, 30 or 90 days, this month, last month or
a custom range). With one ad account, the account is named under the title
with when it last synced; a picker shows only when there is more than one.

| Tab | What it is for |
| --- | --- |
| **Overview** | One sentence that says where you stand against your target, then cost per new customer, cost per sign-up, new customers, spend, revenue and return. A chart draws cost per new customer against your target day by day, each point over the seven days up to it. Campaigns named for the brand are judged apart from everything else. **Do this next** lists the three open actions with the biggest effect. **From ad click to first payment** is the funnel in three steps. Every number on it reads the same attributed rows, so they can't disagree. |
| **Actions** | Every recommendation, alert and new ad waiting for your OK, counted on the tab. |
| **Campaigns** | Each campaign with its sign-ups, customers and cost per customer, the campaigns as each network reports them, bidding strategies, and the per-day, per-platform and per-account tables. Tables of campaigns, ad groups, ads and keywords start on **All enabled**, as Google Ads does; the **Status** filter (All enabled, All but removed, All) is remembered in the browser. Under each sit two total rows, one for the rows shown and one for all of them. Statuses read Enabled, Paused and Removed, and every amount shows its cents. |
| **Customers** | Each person a paid click brought and the ad that brought them. A click from the last few hours says it arrives in a few hours (the network reports a click a few hours after it happens, and Outflare reads its click report on every sync), and a click whose campaign is not in a connected account says so, never a bare id. |
| **Experiments** | One table of tests (status, what is tested, day X of Y, the trial's cost per conversion beside the campaign's, and the result, "Too early to call" until there is enough data, with **Keep the trial** and **Stop**), and a table of the results of past changes. |
| **Settings** | A row of its pages sits under the tabs (General, Tracking, Send conversions, Send log, Ad accounts, Change history), so each is one click from the others. General is one table with a status on each line: ad accounts, recommendations, conversions sent to the networks, the send log, tracking, the counting window, which customers' revenue counts, and the change history. Each line opens its own page or holds the choice itself. Each line says Working, Set, Off or Fix with what is wrong, and problems are listed first: the target cost per new customer, whether conversions are set up and for whom, how many conversions the networks refused in the last week, and whether tracking is complete. |

Links to the earlier tabs keep working: the inbox and suggestions open
Actions, results open Campaigns, verdicts and tests open Experiments.

### Campaigns

The Campaigns table shows, per campaign, its status, daily budget, spend,
sign-ups, new customers, cost per customer and how that compares with your
target (**Over**, **On target**, **Under**, or **Too few to judge** under
five customers), and ends with a total. Revenue, ROAS, clicks and the
network's own conversions are under **Columns**. **Pause** or **Resume**
is on the row; **More** has **Change budget**, **Set a target**, **Open in
Google Ads** and **Remove**. With **Status** set to **All**, the table
lists every campaign, also a paused one that never spent.

Tick campaigns (or tick the header to select every one shown) to show one
bar that pauses, resumes or removes them, or raises or lowers their budgets
by a percentage. One confirmation lists each with its status and what it
spent in the last 30 days, and for a budget change every budget before and
after and, for a raise, the most it can add a month. Removing needs the
permission to remove (see "Removing a campaign, an ad or a keyword"); a
campaign it can't change is named with the reason.

Inside a campaign, ad groups and ads have **Pause** or **Resume** on the
row, and the same ticks: ad groups and ads pause or resume together, ads on
Google Ads can be removed together, and keywords can be removed together. Under **Search terms**, **Exclude** adds the search as a negative
keyword: only this exact search or every search that contains it, for the
campaign or for its ad group only. It asks first and warns when the search,
or a keyword it would block, brought conversions. **Add as keyword** on a search that
isn't a keyword yet adds it to its ad group, exact or phrase, after a
confirmation that it can spend more; only a signed-in person can.

When a campaign's ads send people to more than one page, **Landing
pages** adds the ads up by page: ads, spend, clicks, the network's
conversions, the sign-ups and payments Outflare matched, and cost per
conversion.

A Google Ads campaign also shows **Room to grow, and where the money
goes**: how many of the searches it could have shown for it missed because
the budget ran out or because others ranked higher, and its spend, clicks
and conversions by device, country, network and hour. It is read from
Google Ads when you open the campaign.

### Actions

Actions is one table. Each row has a type (**Fix** for an alert, **Cut
waste** for a saving, **Grow** for anything that can spend more,
**Experiment** for a test), what to do, why (the numbers behind it), the
expected effect on cost per new customer, and one main button:

- A recommendation has **Approve**. A saving is sent straight away. A
  change that can spend more asks first and shows the daily budget before
  and after and the most it can add in a month. **Test it first**, **Open
  the campaign** and **Dismiss** are under **More**.
- An alert's button opens the page where it is fixed (tracking, the send
  log, ad accounts, Settings or the campaign). **Mark as handled** is under
  **More**.

The rows sit under **To do**, **In progress** (approved and being
measured), **Done** (applied, with **Undo**, or judged) and **Dismissed**,
each with its count. Above the table, **Recommendations** has an On/Off
switch. While they are off, Outflare still reviews the account every day
and lists what it would have recommended under **Preview**; nothing is
offered or applied. Only a signed-in person can switch them.

## Coming next

Syncing whole segments to audiences on a schedule, new Meta ad sets and
campaigns, and judging new ads against the ones beside them are planned.
