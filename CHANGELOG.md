# Changelog

What changed in each release of Outflare, for people who self-host. The newest release is first. Versions follow [Semantic Versioning](https://semver.org/); before 1.0, a minor version (0.x.0) may change the API, and its notes say how.

Each pull request that changes what Outflare does adds a change note in [changes/](changes/README.md) (or a line under **Unreleased**, the older way). Write it for someone running Outflare, not for the reviewer: what they can now do or what behaves differently, and anything they must do when they upgrade. The notes become the [daily changelog](https://outflare.ai/changelog/), and a release is cut and published automatically soon after each change: the notes added since the last release become its section here ([docs/releasing.md](docs/releasing.md)).

## [Unreleased]

## [0.110.0] - 2026-11-21

### Added

- Say what you want, on Start here in Paid ads, Creatives: describe an ad in your own words and your own AI model drafts a skill (picture prompt and words) to read, change and save. Nothing is made until you do.
- Funnels now say whether a drop-off can be trusted. They warn when a step's data stopped, never arrived or isn't linked to the earlier steps, and Check this step shows each step on its own and opens the funnel from that step in a tab. Long funnels open as a list that names the biggest drop.

### Changed

- Funnels can now have up to 20 steps instead of 10, so a long journey such as ad visit, download, install, sign-up and first use fits in one funnel. A long funnel scrolls sideways on the Funnels page instead of squeezing its bars.
- Mentions now find podcast episodes that name your brand or a competitor, on your own free Podcast Index key, and leave out what brands post on their own channels. You choose per brand or competitor what Mentions listens for.
- The menu's rail has more areas, so each one lists fewer pages: CRM, Partnerships, Automations, Email, Search, Social and Ads replace Customers and Marketing. Visitors moved to Analytics, Competitors moved to Company, and the Email group is now called Customer email. On a short screen the rail scrolls.
- The menu now shows Pinned and Recents for the area you are in. "Pin to menu" in the top bar pins the page or the report, funnel or other thing you have open, and the things you opened last are listed under your pins. Both are saved to your account, so they follow you to any computer.
- SEO targets: the page and its target keyword are now separate columns, each sortable, with a filter row under the headers for page, keyword, status, intent, page type and band.

### Fixed

- The changes from v0.109.0, which didn't pass the release checks, are now published.
- Object storage's start-up check no longer fails on its own file name, storage calls retry a failure that passes, AI visibility says once when a chosen connection is gone instead of failing every prompt, reading a long Slack sales post no longer cuts the AI's answer short, and crashes from browser extensions are no longer counted as the app's.
- Website tracking no longer fails a visitor's events when a page title or URL holds a hidden null character, cookie reports skip it too, EmailBison now gets your opt-out list as the CSV upload its API expects, and an expired desktop sign-in or a search provider out of credits counts as yours to fix rather than an error.

## [0.109.0] - 2026-11-21

### Added

- Start here in Paid ads, Creatives: four steps to a brand's first ad with what is next, and starter skills (Problem first, One number, Before and after) added with one click, each with two prompts to split test.

### Changed

- Alerts: several alerts of the same kind at once now come as one email that lists them, instead of one email each. A lander or offer that stops answering is emailed to the person who added it, not every owner and admin.
- API tokens: the tokens page has a "Created by" column and shows who created each token when you open it. The API returns the same as `created_by`; a token made by another token or an agent shows as Unknown.
- Mentions on YouTube: with YouTube on under Social → Mentions → Sources, each scan searches YouTube for videos naming your brand or a tracked competitor through your own web search connection, then reads each new video's captions, so a mention shows who made the video, its views, what was said around the name and the second it was said.
- The Partnerships tab that holds every offer, link order and agreement is now called Offers, so it isn't confused with Deals in CRM. Old links to the tab still open it.
- Publishing a query endpoint now writes its description for you, in plain English, from the query's filters, parameters, order and row limit, and keeps it up to date as you name the parameters. Change it as you like, or press Write it again. Endpoints show their description in the list, and the API's new describe call writes one without publishing.

### Fixed

- The changes from v0.108.0, which didn't pass the release checks, are now published.

## [0.108.0] - 2026-11-21

### Added

- SEO overview has a Country pages view: pages made for a country, city or network, each with its target country, Search Console numbers in that country against the 28 days before, the tracked rank there and whether its keyword is tracked, grouped by template with median positions.
- SEO overview has a Links view: referring domains week by week against your biggest tracked competitors, linking domains won and lost a week, links lost in the last 30 days with bought ones first, bought links whose live check failed or found another anchor or page, and the anchor profile of live links.
- SEO overview has a Technical view: issues from the latest site crawl counted in unique pages and templates (languages folded together), new and fixed since the crawl before, sorted by the search clicks of the pages they touch, with robots.txt changes, indexing, sitemaps and Core Web Vitals from real visits.

### Changed

- The product account lookup and outreach blocklist connectors now copy their whole lists into the opt-out list on a schedule (new accounts every 5 minutes, the blocklist exports every 30), so companies are checked without a call and people who become customers are stopped. See how fresh each copy is with `GET .../guard-list-syncs`.
- Every person now has a Lead source category, Inbound or Outbound, worked out from their Source, so you can filter People and the Sales queue by it. People who arrive through a Sales channel get Source Inbound and the channel as their Sub-source.
- The SEO overview map can shade countries by revenue, payments, clicks that brought no payment, revenue per click, organic sessions and time on site as well as clicks and impressions, shows every figure when you hover a country, and lists all countries in a sortable table. Organic sessions, time on site, sign-ups and payments by country are now synced from PostHog.

### Fixed

- The changes from v0.107.0, which didn't pass the release checks, are now published.

### Upgrading

- runs migration 0680. A connected account lookup reads its whole account feed on the first sync, in runs of a few minutes each.

## [0.107.0] - 2026-11-21

### Added

- Paid ads has a new Whole business tab: all your ad spend against all your revenue, whichever ad got the credit. It shows revenue per unit of ad spend, the blended cost per new customer, what each network claims next to what Outflare counted, and whether each month's new customers have paid back what they cost.
- SEO overview has a Cannibalization view across a brand's sites: competing searches marked new, ongoing or gained a page against the 28 days before, resolved ones listed, same-language and cross-language searches apart, the page to keep (which you can now choose), split clicks, an estimated gain and the fix on the fixes list.
- SEO overview has a Declining view: pages that lost search clicks over the last 7 or 28 days, as new, still declining or recovering, with what moved (impressions, position or CTR), the country, device and query that lost most, sign-ups, a modeled loss in dollars, and flags for a broken page or another page taking the query.
- The SEO dashboard shows money left on the table: queries in positions 4 to 20 worth moving to position 3, queries clicked less than their position earns, and pages that lost clicks, each in extra clicks and dollars at the brand's revenue per organic click (or a rate you set), with the link orders already pointing at each page and the formula on the page.
- Keyword targets can carry the page's search intent, page type and a Markdown summary of what the page holds, and a whole keyword map imports from a CSV file. SEO overview, Targets gives every page a status (on target, cannibalized, wrong intent, missing content) with the page that ranks instead and suggested fixes, and lists up to 10,000 pages per site.

### Fixed

- The changes from v0.106.0, which didn't pass the release checks, are now published.

## [0.106.0] - 2026-11-21

### Added

- Paid ads has a new Creatives tab. Every ad is shown as a large preview and scored step by step (hook, hold, click, sign-up, pay) against your own usual rates, with a verdict: scale, keep, fix, stop or too early. Switch between gallery and list, filter by verdict, and pause an ad from its card.

### Fixed

- The changes from v0.105.0, which didn't pass the release checks, are now published.

## [0.105.0] - 2026-11-21

### Added

- Paid ads has holdout tests, on the Experiments tab: keep your ads out of a few countries for some weeks, and Outflare compares sign-ups there with everywhere else to say how many sign-ups the ads really add. Outflare only counts; you exclude the countries on your campaigns.
- Mentions: see who names your brand or your tracked competitors in videos, podcasts, Reddit posts, forums and news, with the quote, who said it, their audience and a score, under Social → Mentions. It reads what your Reddit watches, news searches and competitor scans already found, and is off until you turn it on.
- Site owners can suggest changes to a partnership offer up to three times: the price, their page, the link text and the publish date. Where the link points, that it's a normal link, that it stays up and that nothing is paid before it's live stay fixed. Accept the latest round to make it the agreement.
- Partnership offers open as a full page and can start from a saved template. Each brand sets, on Partnerships → Payments, what happens when a paid link is removed after payment (restore or refund, a shrinking refund, or a warning), and every offer carries the rule it was sent with.

### Changed

- Paid ads syncs how far people watch your Meta and TikTok video ads: plays that count as watched, the quarters reached, and from them a hook rate and a hold rate per campaign, ad set and ad. Turn the columns on in the tables under Columns.
- Paid ads, Settings, Tracking has a new dry run: would matching visits by network and device find more ad customers? It reads the visits you already have and says how many more people an ad would be credited with, and how often that guess is right. Nothing is collected, joined or changed.
- Every error is now an event you can keep on your own server. Once someone is signed in, the web app sends its own events (an error shown, a crash, a page opened) to the server, so they reach your events workspace with or without PostHog. Errors and warnings the server only wrote to its log become events too, with where in the code they were logged.
- The Campaigns table in Paid ads can count the same campaigns under any of your attribution models, beside the last ad click: pick one under "Also count by" for its sign-ups, cost per sign-up, revenue and ROAS. The ad network's own count now shows by default as "Network says".
- The Paid ads Overview shows every number against the period before, with the old number and a small chart, and colours each against your target. You can compare with the days before or the same days a year earlier, choose which numbers show and in what order, and set targets for new customers, budget, revenue and return on ad spend beside the two cost targets.

### Fixed

- The changes from v0.104.0, which didn't pass the release checks, are now published.
- A request the caller closed before the answer (a closed tab, a page left mid-load) is now logged as `client_closed` instead of a 500 server error, so the request log and error counts show only real failures.
- Data, Catalog opens at once: the list of tables shows straight away and the row counts and dates fill in after, instead of the page waiting while every table is counted. Agents and the API can ask for the same with `numbers=false`.
- The sidebar's badge check is no longer refused once a minute for people and tokens kept to some brands. They get no badges, which are about the whole workspace.
- Importing a Sales conversation for a person who has since been merged into another now continues on the merged person's conversation, instead of being refused with a conflict on every later send.
- Searching Slack from a Sales conversation now says what to change when Slack refuses the search token (it must be a person's token with the search:read scope), instead of reporting Slack as unavailable.

## [0.104.0] - 2026-11-21

### Added

- Brand files: each brand can keep its logos, fonts, product shots, screenshots, example ads, voices and pictures of people in one place, over the API and MCP, for ads and other content to be built from. A voice or a person's picture can't be used until someone confirms that person agreed.
- Pictures, video and voice on your own AI accounts: over the API and MCP, a brand can ask its own OpenAI, fal.ai or ElevenLabs account for a picture, a video or a voice recording, and the file is kept with the brand's files. fal.ai and ElevenLabs are new connections under Connectors. You choose the model, each request is sent once so nothing is paid twice, and you pay the provider directly.
- Brand profile: a new first page under Company that keeps what your agents know about the brand (company, offer, customers, personas, positioning, proof, competitors, objections, voice, goals, channels and rules) as separate facts. What an agent finds waits for a person's yes before it is ever said to a customer, and every change is kept in a history.
- Skills and creatives: over the API and MCP, a brand can save a recipe for one kind of ad and make ads from it on its own AI account. Recipes are versioned, two to four prompts can run side by side as a split test, and each ad waits for a person's yes or no. Outflare lays the words and logo over the picture, so one picture gives every size.
- Creatives tab in Paid ads: a brand's creative library, talking videos, skills and prompts, and brand kit now have pages in the app, each one click from the others.
- Data catalog: Data → Catalog, and the MCP tool describe_brand_data, show every table of a brand's data that Queries and agents read with SQL: what it holds, where it comes from, how many rows and how fresh, and what each column means, with its unit, what it joins to, how often it is filled and its common values. Agents now get run_query and the catalog in their default tools.
- Discovery lanes now have a Lane leaderboard in Sales reporting: each lane of a profile with what it found, how much was new, what passed, its cost and its lesson, the shared record agents read, and a page per lane to approve, reject, pause, resume or reprioritise it. Agents get MCP tools to read the board, claim lanes, upload with per-company answers and score what they learned.
- The send log in Paid ads shows Meta's match quality: a score out of 10 for each event you send, how it moved against the week before, which identifiers went with it and what Meta says to fix. A low or falling score raises an alert.
- The website script can load your Meta and TikTok pixels: two new switches on Website tracking, off by default. It sends each conversion a page causes to the pixel with the same ID Outflare's server sends, so the platform counts it once and matches more conversions to an ad. No pixel code on your site.
- A new Partnerships menu puts working with other sites in one place, in the order the work happens: Find, Deals (every offer, link order and agreement on one board), Live, Affiliates, Payments (everything you owe in one list) and Results. Old Presence, Partners and Link order links still work.
- SEO overview has a Targets view: each page's target keyword with its Search Console position and change, a 12-week trend, tracked rank by country, other pages competing for it, and whether it is in the title and H1. Tiles count targets in the top 3, top 10 and 11–20 and filter the table.
- Talking videos: over the API and MCP, a brand can make a video of a person saying a script on its own ElevenLabs and fal.ai accounts, with an upscale, the look of a phone camera and captions, then fix or remove a word without making the whole video again.

### Changed

- Removing an ad campaign, ad or keyword is now a permission: a role setting for people, the `ads.remove` scope for API tokens, and Off, Ask or On for each agent, optionally only for paused campaigns, disapproved ads or items that spent nothing. Refusals name the missing permission, and each removal is audited with a snapshot.
- Paid ads tables have a tick on every row and a "select all" in the header. Tick campaigns to pause, resume, change the budget of or remove them together; ad groups and ads to pause or resume them (and remove ads on Google Ads); keywords to remove them. One confirmation lists each one with its status and what it spent in the last 30 days, and removing follows the removal permission.
- Alerts: mute or unmute many kinds of alert for yourself in one click. Narrow the list with Show or search, then Mute all for me, or tick only the ones you want; Undo puts them back.
- Tracker campaigns: the campaign URL now sits at the top of a campaign's page with a Copy URL button, on every tab. The page has a clear Back to campaigns button, its tabs are named Settings, Landers and offers, and Tracking parameters, and Settings uses the full width.
- "Everyone: our site already collects consent" now covers ad audiences as well as conversions. A brand that chose it has people added to its ad audiences in every country as consented to personalised ads, except anyone who withdrew. The confirmation in the app now says so.
- A rate limit answer (HTTP 429) from web data, Parallel, EmailBison, Blitz or an email verifier is now waited out, honouring its Retry-After, instead of being read as an empty account; only out of credits (HTTP 402) still stops work for a person. Long provider calls (Treg, web data, Parallel) are no longer cut off after 30 seconds, and a finder call that timed out is never remembered as "no email".
- Each rule under Send conversions now shows the ID of the pixel, dataset or conversion it sends to, with a copy button, so you can check it against your site or the ad platform.
- Conversions sent to Meta, TikTok, Snapchat, Pinterest, Reddit and X now carry the person's IP address and browser beside the click id and hashed identifiers, so the networks match more of them to an ad. The send log shows them as `ip_address` and `user_agent`.
- Agents can read the creative studio through SQL: the brand's files, provider requests, talking videos with their stages and versions, creatives, skills, lessons and each creative's history are in Query and the data catalog, behind `content.read`.
- Talking videos and creatives keep everything you say about them: a fix waits for your keep or go back, every version can be gone back to, you can like one or say "not this" with a reason, make variations of one that works, and teach lessons (how to say a word, rules for prompts) that every later creative follows. One history per creative shows all of it.
- Preview where a tracker click would go. The offer and lander forms, the API (`GET /v1/brands/{brand}/destination-preview`) and the MCP tool `preview_destination` show the final address for a URL as you wrote it: the click id and campaign tags Outflare adds, anything it replaces, and what it leaves alone. A preview counts no click.
- Pictures, video and voice keep their recipe and take references: every request stores what was sent to the provider and what came back, can be made again with one thing changed, and can name brand files (a frame of a real video, a person, an ad you liked) to make the new file from, so a person or a scene stays the same.
- The account lookup and outreach blocklist connections now pass on the wait a service names with HTTP 503 and Retry-After, so callers can treat it as busy rather than down.
- Agents can export a query's results as CSV over MCP (export_query_csv), up to 10,000 rows, for lists longer than run_query returns.
- The new menu (a rail of areas with a menu for the area you are in) is now the menu everyone starts with. The old single sidebar is one click away: press "Old menu" at the bottom of the rail, and your choice is remembered in this browser. Data's menu also links straight to Events and their sources, and Transactions.
- Meta Ads: a conversion rule can now send InitiateCheckout, AddPaymentInfo, AddToCart, ViewContent and Search, so the steps before a purchase reach your pixel too.
- Tracker offers and landers: delete one or several at once, even while links use them (they come off those links, and reports keep their numbers). A row now opens its details in the page with a Back button and its own address instead of a side panel, with Edit, Duplicate and Delete on every row and a search box. The API and MCP get delete for both.
- Treg can now find a person's LinkedIn profile from their name and company's domain, and Blitz can read a company's size (its employee range and how many people work there on LinkedIn) from its LinkedIn page. Both run on your own connection and are billed to your own account.
- Paid ads in SQL: Queries and agents can now read ad accounts, campaigns, ad groups and ads with daily spend, clicks and conversions, keywords and search terms, the ad click that brought each customer in and its revenue, attribution credits, ad changes, and the click tracker's sources, offers, landers and costs. Each table is explained in the data catalog.
- Agent work, tests, journeys, social and content in SQL: Queries and agents can now read agent runs and missions with their updates and reports, the action log, what agents learned, A/B tests, insights and alerts, journey steps, social accounts and posts, content, email templates and suppressions. Each table is explained in the data catalog.
- Customers, sales, support and partners in SQL: Queries and agents can now read every record's field history, merges, lists and members, scores and signals, sales leads and commissions, CRM tasks, meetings and emails (without content), MRR by day, support conversations (without bodies), and partner programs, referrals, rewards and payouts. Each table is explained in the data catalog.
- Owners and admins can now delete a published query endpoint, from the Endpoints list or its own page, after a confirmation. Its address stops answering and can be used again, its call log goes with it, and the saved query and the audit record stay. Turning an endpoint off still pauses it instead.
- Search, SEO and AI visibility in SQL: Queries and agents can now read Search Console clicks, URL inspections, Core Web Vitals, crawls, rank checks, competitors, AI visibility prompts, answers, mentions and citations, AI crawler visits, web analytics, page speed, heatmaps, site visitors, experiments and session recording details. Each table is explained in the data catalog.
- Smart list rules: event filters now offer the properties your events actually carry, money filters take whole amounts (100 for $100), time windows and counts take any number, and the "holds nearly everyone" warning also covers lists that match any rule.
- The smart list builder is simpler: say who to include and who to leave out, pick each condition's kind from one menu, switch "and" and "or" with a click, and see the live count and the list in plain words beside it.
- The box that fills in a smart list from a description is now a clear text field with its own heading, so it is obvious where to type.
- Tracker: traffic sources, affiliate networks and log rows now open in the page with a Back button and Cancel, instead of a side panel, and a log row shows names and dates in place of ids. The report's custom columns are edited in a card above the table, so each new column shows as you add it. Flows get the same Back button.
- Tracker routing takes up to 100 landers and 100 offers or pages in each list, up from 20 in all. Landers and offers are split on their own, so 25 landers and 31 offers at the same weight get 1/25 and 1/31 of the clicks each. The routing editor adds several at once, shows shares to one decimal and marks one that is listed twice.
- Tracker conversions now say whether the traffic source was told, and if not, why (suspect, filtered by the postback rules, no traffic source and so on). A postback that carries both the network's own click id as cid and Outflare's ofclk credits the click, and a send that went out without the network's click id is marked.
- Tracker → Setup now shows the brand's tracking domain right on the page, so you can add and verify it without leaving Ads. It is the same domain your emails use, one per brand, and both Domains and Tracker → Setup now say so.
- Treg's email finder lookups and address checks are now booked in the spend ledger at the cost Treg reports for each call, to the millionth of a dollar, instead of as credits with no price, so Outcomes shows what they really cost.

### Fixed

- The changes from v0.103.0, which didn't pass the release checks, are now published.
- Paid ads tables set to "All" now list every campaign, ad group and ad, including paused ones that never spent. Before, a campaign with no spend in the dates shown was left out whatever the status filter said. The API takes `idle=true` on the campaign, ad and performance lists for the same.
- An extension's AI calls now report what they cost on a router such as OpenRouter (not 0), with cached input at its cheaper rate. Through OpenRouter, Anthropic's models now cache the stable start of a request, as a direct Claude connection already did. Extensions can list the models a workspace's AI connection offers.
- Tracker destinations now get the click id (ofclk) exactly once, even when a lander or offer URL says ofclk={click_id}, and a URL with any other ofclk is refused with the reason. Offers have a "Click id delivery" setting, so a page of your own no longer shows "Missing click id".
- The connect dialog keeps a long key and a long error inside the box. When Meta has switched off API access for the app a token was made for, Meta Ads now says so in plain words, with what to do next.
- Conversions sent back to ad networks now carry the ad click id from the person's own earlier events, such as the click id your server sent with their sign-up, so a payment sent from your server can be tied to the ad that brought them even when no website visit was linked to them.
- Long forms in pop-up windows, such as connecting Google Workspace, now scroll, so the buttons at the bottom can always be reached.
- Country and language names show in English, as the rest of the app, instead of following the browser's language.
- A payment counted from an Order Completed event now takes the product the order names, so a company's first order moves it to active on that product. A payment sent first without a product picks it up from its order.
- Paid ads: a sign-up whose last visit kept the ad's click id but lost its campaign (a sign-up page on another domain, or a click Google's click report leaves out) is now credited to the campaign of the earlier landing with the same click id. Sign-ups already recorded as "Campaign unknown" this way are filled in on the next run.
- A query for one brand now shows only that brand in every table: visits, spend, backlinks, reviews, social posts, link prospects, link orders and organic conversions used to show the other brands you can see too.
- Sales feed: a Slack post that arrives in the same moment as a channel's first read now shows up on the next read, a couple of minutes later, instead of waiting for the hourly check.
- Treg's profile lookup by name now takes its "no such person" answer as nobody found, instead of a failed request.

### Upgrading

- files need object storage (`OUTFLARE_S3_*`). Without it the feature stays off.
- the finished files need object storage (`OUTFLARE_S3_*`). Without it the feature stays off.
- closing pauses, the phone look, captions and edits need `ffmpeg` and `ffprobe` (with libx264 and AAC) on the engine's PATH. Without them videos are made as the model made them.

## [0.103.0] - 2026-11-19

### Added

- Audiences can now be people who should never see your ads, such as everyone who was refunded. Choose "Never show them ads" on an audience and Outflare excludes its list on every active Google Ads and Meta Ads campaign, and on new campaigns within a day. This is how refunds reach Meta, which can't take a conversion back.
- Audiences now sync to ChatGPT Ads: Outflare makes a custom audience from one of your lists and keeps it up to date with hashed emails and phone numbers, like on Google, Meta, X and Reddit. Use it to exclude people who were refunded, since ChatGPT Ads can't take a conversion back.
- The website script can load your ChatGPT Ads pixel for you: turn on "Load the ChatGPT Ads pixel" on a site's script and it loads the pixel of the brand's ChatGPT Ads connection, counts page views, and passes the signed-in visitor's hashed email and ID, with no pixel code on your site.
- Two new connections say who must not be contacted, on your own accounts: a product account lookup (is this person or their company already a customer) and an outreach blocklist service. Each answers clear, blocked or unknown for every address, and anything but a clean answer is unknown, never clear.
- A new menu to try: a short rail of areas (Home, Inbox, Agents, Customers, Marketing, Analytics, Data, Company) with a menu beside it that shows only the pages of the area you are in. Press "Try the new menu" at the bottom of the sidebar, and "Old menu" on the rail to go back; your choice is remembered in this browser.
- The weekly digest now has a Paid ads section: spend, the sign-ups and new paying customers your ads brought, cost per new paying customer against the week before, your top campaigns, and what is waiting for a decision.
- Qualified sign-ups: a conversion rule on a sign-up can now send each one worth what people from the same country with the same kind of email went on to pay, and hold back the ones less likely to pay than your average. The rule form shows who pays after signing up, from your own history.
- Queries in plain English: describe the list you want ("customers who spent over $500, with their email, user id and sign-up date, biggest spenders first") and get it as a visual builder you can change without SQL, then save it, share it or make it a list. Queries, Endpoints and Customer data now sit under a new Data menu.
- Sales can now read the Slack channels your other tools post into, such as a website form. Each post becomes a person and company, a Sales conversation or a signal, and the queue shows which tool each one came from, filters by it in one click and marks the new ones; a Slack search on every conversation shows what your team already said; Outflare only reads Slack, using your own Slack app.
- Sales reporting's Discovery view shows the discovery lanes of the brand's own lead finder on Outflare Cloud, live: what each lane sent, what was already known, and where every company is, from being checked to a person in the campaign, with why companies stopped. A lane opens as its own page with its companies and people. Discovery agents get MCP tools to send companies and read their lanes.
- SEO overview now opens on a dashboard: tiles for today, yesterday, the month so far, its forecast and last month, each showing organic revenue, payments, sign-ups and search clicks against the period before. Pick a tile to see the pages behind it, sortable and downloadable as CSV, or a map of clicks by country.
- SEO overview now opens on Search Console's Performance report, read live from your own Search Console so the numbers match, with comparisons and every Search Console filter. It adds branded or non-branded, questions, query length, position, minimum impressions, won or lost rows, weekly or monthly charts and CSV export.

### Changed

- Conversion rules on payments can now send first payments only, so an ad platform counts each new paying customer once and renewals or top-ups add nothing. Choose it under Paid ads → Send conversions back → Payments to send; repeat payments only is there too.
- Paid ads' Send log now opens with a "Sent against counted" table: for each conversion action, how many conversions Outflare sent, how many carried the network's click id, and how many the network counts, with a status when it counts under half or none.
- Adding people to an EmailBison campaign now checks the campaign first (active, with sequence steps, sending days and connected senders) and holds people while it isn't ready or has used its senders' daily capacity. Someone EmailBison already holds is left out instead of added, and new contacts go in together, with a note saying where they came from.
- Errors are easier to fix and far less noisy: one bug is one error in every browser and build, browser-extension and scanner noise is filtered (plus your own ignore rules), and each error shows the share of visitors it hits, its site, release and what the visitor did before. Emails about new errors are now off until you switch them on.
- Keywords in Paid ads now show who they brought, on Outflare's own count: sign-ups, the share of them that paid, new paying customers and what each cost. Search terms show the same share for the keyword they matched, so searches that bring people who never pay stand out.
- The Product account lookup and Outreach blocklist connectors can now check a company by its domain alone, before anyone there is known: the lookup asks for the company's domain, and the blocklist checks it against the service's daily domains export. The Lead finder shows companies' A to D grades beside their fit.
- Recommended donors in Link orders can now show 25, 50, 100 or 200 picks, so you can tick them all and find emails and send them to a sequence in one go.
- Recommended donors now hide sites already in one of your sequences, add checkboxes with Select all, and show each site's light and top-page traffic share. Orders need a green light before you approve or publish, and a site Ahrefs checked in the last 30 days is never pulled again.
- A list's page has Edit name and description, so any list can be renamed and described where you read it.
- Connect your AI app with tool groups to list a whole area's tools up front, such as `https://app.outflare.ai/mcp?tools=core,analytics,seo`; every other tool stays one search away. Tools marked read-only can now never make a change: Outflare refuses it, so apps that run reads without asking stay safe.
- Products add themselves from your events: an event that names a product and its category (`product` and `product_category`) adds the product with that category the first time it is seen, and keeps the category in step with later events. Events without a category are held for review as before.
- Paid ads actions that can spend more are now proposed as experiments where the network can test them and the campaign converts often enough: the row says Experiment and its button is Test it first, so the change proves itself on half the campaign before you keep it.
- The Campaigns table has a Room to grow column: the share of searches each Google search campaign missed last week to its budget and to rank. Resuming a paused campaign is now checked against your daily budget ceiling, like a raise.
- Sales channels now show what matters for each source: pick the fields a Slack channel's posts lead with, and the queue shows those instead of a sentence about the form. Reps can mark a conversation Good, Poor fit or Junk, filter the queue by mark, and see the marks counted per source. New conversations can also be filtered to any teammate, with how many each one holds.
- Assigning a Sales conversation now makes that teammate the person's Owner, replacing any Owner set earlier, and unassigning it clears the Owner when it was still that teammate; each change is in the person's history. A conversation that has been taken can also be passed to another SDR or account manager by its owner or a manager, once no email for it is still being sent.
- Sales: a conversation now shows every email you exchange with its person from your connected Gmail, in any thread. Before, only replies inside the original thread appeared, so a new email you wrote them was left out. Mail that includes anyone outside the conversation still stays out.
- The Performance report on SEO overview now looks like Search Console's, with date buttons, Add filter, check-box totals and an axis on each side of the chart. It also gains "24 hours", hour by hour, compared with the 24 hours before.
- Smart lists can group rules with all, any or none, and test page visits, other lists, email consent, event counts and properties. The new builder has searchable pickers, reads the rules back as a sentence, and can write them from a description on your workspace's own AI model.
- The columns you save as your default on a list or a Paid ads table now stay with your account, so the table looks the same in every browser and on every device you sign in on.

### Fixed

- The changes from v0.102.0, which didn't pass the release checks, are now published.
- Email finding through Treg: a provider that is busy because everyone's calls fill it no longer slows your connection down, so the next providers in the chain are asked straight away. Only a rate limit on your own account slows the pace.
- Email finding and verification through Treg: a provider that is busy for a moment (HTTP 429) is asked again in place, then later, instead of being read as an empty balance. A busy finder no longer stops a run, and a busy verifier no longer stores a good address as risky from the built-in checks.
- Paid ads recommendations, verdicts and the Bidding strategies table now show money in your brand's currency, like the rest of Paid ads, when an ad account is billed in another one. A brand's target is also compared with spend in the same currency.
- A connected sequencer's draft campaign can now be linked to a sequence before anyone starts it there: name it in `link_campaigns` on `POST /v1/workspaces/{ws}/platforms/{id}/sync` (or the `sync_platform` MCP tool) and it comes in as a draft sequence, so it can be chosen as a campaign. Syncs still link live campaigns by themselves and leave other drafts out.
- AI apps that ask before changes, such as Claude, can now read with any Outflare tool without an approval prompt: the new `call_read_tool` runs only tools that read, and `call_tool` stays the way to change things. Reading dashboards and their tiles is now in the core tool set.
- Sales: taking a conversation now sends your introduction even when your own address is on the workspace blocklist. Before, a rep a sequencer's blocklist listed by address could never be copied on their own conversation, so the introduction failed on every retry.
- The Sales queue row is easier to read on a laptop: a form's fields show their label above the value instead of squeezed beside it, the row's buttons stay on one line, and a form no longer repeats its own fields as text.
- An analytics or Search Console site nobody placed now belongs to the brand whose website it is, instead of the connection's brand or the workspace's first brand. When one Google sign-in reads the Search Console properties of several brands, a brand's search reports and dashboards no longer list another brand's pages and clicks.
- Treg: when Treg has no room for a call it is asked again after 2, 4 and 8 seconds, then the same call goes to your other Treg connection, for finding and for verifying an email. Verification now waits for Treg's slower answers, and a busy Treg no longer slows your connection down.
- Background work no longer waits behind a bulk email verification. Verification now runs on its own few workers, and addresses a verifier could not answer are asked again after five minutes instead of at once, so a slow or busy verifier can no longer hold up syncs, sends and alerts for half an hour at a time.

### Upgrading

- stored errors are regrouped once in the background after the upgrade; old links keep working.

## [0.102.0] - 2026-11-18

### Changed

- A list's page now links each person to their record and shows their email, lifetime value and sign-up date. A Columns menu picks which columns show, and Save as my default keeps your pick for every list.
- Microsoft Advertising campaigns now show keywords and search terms, take negative keywords and ad group pauses, and have refunds taken back from sent conversions. X campaigns show their line items, which can be paused. Audiences now sync to X and Reddit as well as Google and Meta. Fixed pausing and budget changes on Microsoft campaigns.

### Fixed

- The changes from v0.101.0, which didn't pass the release checks, are now published.

## [0.101.0] - 2026-11-18

### Added

- New API call and MCP tool to exclude a search from a Google Ads campaign or ad group: `POST /v1/brands/{brand}/ads/campaigns/{id}/negative-keywords` (`add_ad_negative_keyword`), with exact or phrase match. It is queued and listed like any campaign change.
- Connect Blitz with your own API key to find decision makers at a company and their work emails from LinkedIn profiles. When a person's LinkedIn profile is known, the email step now asks by profile first (Blitz, then Treg), and asks again about people it could not find before the profile was known. It also asks about more people at once.
- Optional funnel steps: mark a middle step Optional and people who skip it still count at the next step, so a sign-up step no longer hides payers who already had an account. The step shows how many did it and how many skipped it.
- Internal and test people: list your team's email domains and your test accounts under Analytics, switch it on, and funnels, trends, retention, stickiness, lifecycle and paths leave them out. Nothing is deleted, and turning it off counts them again.
- Your connections can do more: find a company's LinkedIn page and read a person's profile with Blitz, find who works at a company with Parallel, check whether EmailBison already emailed someone, ask your email finders one provider at a time, and fall back to your second verification service when the first can't answer.
- Catalog sites now say whether to skip them: too much search traffic on the home page or on one to three pages, or more links out than a real publication gives. Check in Ahrefs reads only the five numbers that decide it, and recommended donors leave such sites out.
- Subscriptions from your own billing system: send each customer's subscription to `POST /v1/brands/{brand}/billing-subscriptions` when it starts and whenever it changes. Monthly revenue, billing status, plan and the Revenue page follow, the same as with a billing connector.
- Every recommended action under Paid ads now opens its own page with the numbers behind it and what it would change. Say Good advice or Bad advice and why: Outflare saves it to the brand's memory, so agents follow it next time.
- Audiences under Paid ads: pick one of your lists of people once and send it to several ad networks. Outflare makes the list on Google Ads and Meta, adds people as hashed emails and phones, updates it daily, and shows each network's status and match numbers. Networks that take an upload, such as ChatGPT Ads, get a ready file.
- Paid ads has a daily budget ceiling: set the most your running campaigns may spend in a day under Setup, Limits. A raise past it is refused for agents and API tokens, and asks you to confirm a second time.
- Agents and the API can now read anything in a Google Ads account live: every campaign setting, ad group, keyword, negative, ad and asset, whether or not it ever had an impression, plus impression share reporting. A second call lists what can be asked for. Both only read.
- Paid ads warns you when an ad network accepts your conversions but counts none of them: a new alert under Actions when a week of conversions sent with the network's own click id shows as zero for that conversion action, with a button to the send log.
- Failed payments and disputes are events: a transaction sent with the status failed writes `payment.failed`, and a chargeback writes `payment.disputed`, on the person's timeline. Neither counts as revenue.

### Changed

- The send log can ask Google Ads what it did with a conversion after accepting it: processed, still processing, or refused and why. Press Ask Google Ads on a sent row.
- An agent can now take back its own change while it waits for your approval, and point to the newer request that replaces it, so an out-of-date request can no longer be approved by mistake.
- The agent's chat panel can be made wider: drag its left edge (or use the arrow keys) to give the conversation more room. The width is remembered in your browser and stays the same on every page; double-click the edge to put it back.
- Connecting ChatGPT Ads now asks for the Pixel ID, conversion key and website address on the same form as the Advertiser API key, so conversions can be set up in one go.
- Conversions sent to ChatGPT Ads now carry the person's IP address (when your server passes it on as `context.ip`), their browser and their country, so OpenAI can match more of them to an ad click. The browser comes from the event, or from the person's latest visit recorded by the Outflare site script, so your server doesn't need to send it.
- Dashboard reports are easier to change: click a report's title or Edit to open it full screen with Save changes, Cancel and Save as a copy, and use its menu to rename, duplicate or open it in Trends. A report can now keep its own dates instead of following the dashboard's.
- Error capture is now one switch at the top of the Errors page, with a plain line that says whether errors are being captured on your site, or what is stopping them. On Website tracking, what the script does on its own (errors, clicks, form submits, A/B tests) has its own switches that save at once, without Edit.
- The Errors page is easier to act on: tabs for Open, Fixed, Ignored and Archived, search and filters, and ticking several errors to mark them fixed, ignore, archive or delete them at once, with Undo. Each error has a "Copy fix prompt for AI" button that hands your coding assistant the error, its stack, pages, browsers and release, plus "Copy error" for tickets.
- Funnels, trends, retention and paths each have their own link, and edits stay a draft until you press Save changes, with Cancel and Save as a copy. Funnel steps can be dragged into a new order, and ad clicks show as a labelled reference bar with its own switch.
- Funnels, Trends, Retention and Paths now open on a list of what you have saved, with a button to start a new one; click a row to open it. A funnel step's time limit no longer spills out of its box.
- Donors now have to meet a base minimum: DR 30, 5,000 organic visits a month and no more than 45% of the traffic on one page. Recommended donors also work without choosing a page: they cover the top five suggested pages, three donors each.
- A search that brings customers can be added as a keyword from a campaign's Search terms: Add as keyword, exact or phrase, with a confirmation that it can spend more.
- Paid ads, Campaigns: each campaign shows its daily budget, sign-ups, new customers and cost per customer against your target, with Pause on the row and a link to it in Google Ads. Tick several campaigns to pause them or change their budgets with one confirmation. Inside a campaign, Pause and Resume are buttons, and a search you're paying for has Exclude.
- The chart on Paid ads now has four number tiles, like Google Ads: click one to draw or hide its line, and pick another metric (clicks, cost, average CPC, click rate, conversions and more) from its menu. Your choice is remembered in your browser.
- Customers from ads now show their campaign, ad group and keyword within a few hours of the click instead of the next morning: Outflare reads Google Ads' click report on every sync, not once a day.
- Paid ads Overview has a chart of cost per new customer against your target, day by day, so you can see when it is safe to raise budgets. `GET /v1/brands/{brand}/ads/performance` returns the same numbers per day as `daily`.
- Paid ads Customers no longer says "Campaign unknown". A click from today says its campaign arrives tomorrow, and a click from a campaign that is not in a connected ad account says so and how to fix it.
- A disapproved Google ad now says why: the network's own reasons, such as "Destination not working", show on the ad's row and in the alert under Actions.
- Paid ads Experiments is now one table: each test with its status, what it tests, how far along it is, the trial's cost beside the campaign's and the result, with Keep the trial and Stop on the row. Results of past changes are a table too.
- Change bidding on a Google Ads campaign now offers Target impression share: pick where on the page the ad should show, the share of searches, and the most to pay for a click.
- A campaign whose ads send people to more than one page shows Landing pages: its ads added up by page, with spend, clicks, conversions and cost per conversion.
- Paid ads shows every ad account in your brand's currency: an account billed in another currency is converted at the latest daily rate, so its spend, costs and revenue add up with the rest. The number at the top of Paid ads now follows the ad network or account you pick.
- A campaign's Targeting now names every place it shows in and every place it is kept out of, with Google's ids for agents and the API, and says plainly when it runs everywhere. Campaigns and portfolio strategies can bid for a share of searches (Target impression share) or for clicks, and each bidding change is checked against Google afterwards.
- Opening a Google Ads campaign shows its room to grow (the searches it missed because the budget ran out, or because others ranked higher) and where its money goes by device, country, network and hour. Also on the API and MCP as campaign signals.
- Search ads now look like they do in Google's results: three headlines, the site and two descriptions, with every line and its length one click away. This shows on a campaign's ads and on new ads waiting for your approval, which you can now edit before approving. Existing ads fill in on the next sync.
- Paid ads tables now say whether an enabled Google Ads campaign, ad group or ad is really serving, in Google's words (Eligible, Limited by budget, Learning, Not eligible), with the reasons on hover. Campaigns also show Google's Optimization score.
- An agent or the API can now set every place a Google Ads campaign shows in and is kept out of in one change, with a preview of what would be added and taken out. Exclusions you don't mention stay as they are, the change is refused if the places moved since the preview, and it is read back afterwards. A campaign can also be renamed.
- Paid ads' Setup tab is now Settings, with its pages (General, Tracking, Send conversions, Send log, Ad accounts, Change history) in a row under the tabs, so each is one click from any other and there is nothing to go back out of.
- Paid ads Setup shows a real status on every line, problems first: the target cost per new customer, conversions, refused sends in the last week and tracking each say Working or Fix with what is wrong.
- Paid ads tables now work more like Google Ads: campaigns, ad groups, ads and keywords show only enabled ones to start with (a Status filter brings back paused and removed, and is remembered), statuses read Enabled, Paused and Removed, two total rows sit under each table, numbers line up under their headings, and every amount shows its cents.
- Products can belong to a category, such as proxies or web data, and a metric can count one category's products only, such as GB used across every proxy product in 30 days. Trends can now be split and filtered by a product's category and by a field of the person or their company, such as their plan or industry.
- A Treg connection can now read a person's LinkedIn profile (through Fetchin) and say where they work now, on your own Treg balance, so a tool that finds people can check someone still works at the company before contacting them.
- Sales conversations now open by themselves when someone you added to an EmailBison campaign through Outflare replies interested, books a meeting or asks a question. The conversation arrives in New conversations with the whole thread, and a later reply from the same person joins it.
- Sales managers and workspace admins can now see each SDR and account executive's activity by day, plus a log filtered by teammate, action, person or company. Seen actions and successful AE transfers now retain history for reporting.
- The SEO links pipeline in Presence has a Researching stage before Prospect, for sites you are looking at but haven't decided to pitch. AI apps connected to Outflare now see the tools to add a site to a pipeline and move it between stages straight away. If you set the pipeline up earlier, apply the SEO links kit again to add the stage.
- Treg finds more work emails: it now asks Blitz, Filtrat, Kitt and Anymailfinder in turn instead of Treg's own router, which often ran out of time. A Treg connection can also be your verification service (Filtrat, with BounceBan as a second opinion). One person can now use up to four credits on a Treg connection.

### Fixed

- The changes from v0.100.0, which didn't pass the release checks, are now published.
- A signed-in connection such as Google Search Console no longer breaks when the provider refuses a token early: Outflare renews the sign-in and checks again first. When it really has expired, the connection says so in plain words, with a Sign in again button that now works on an expired connection and also mends the other connections to the same Google account.
- **Connect Gmail** in Sales (My email) and in CRM email no longer fails with Google's "redirect_uri_mismatch" error. Both now return to the same Google address as "Continue with Google".
- A campaign's page now opens from its link even when the campaign is paused and spent nothing in the dates you're looking at. Before, it said "Campaign not found".
- Listing the fields of a Google Ads resource for a live read (`query-fields` with a resource) was refused by Google. It now answers.
- Paid ads no longer counts revenue from customers ads didn't bring in. Before, a customer who came another way and clicked an ad before a later payment (for example searching your name to log in) added returning revenue and ROAS to that campaign. Now only people an ad acquired count.

### Upgrading

- the Google OAuth client needs only the `/v1/auth/google/callback` redirect URI for these; `/v1/sales/google/callback` and `/v1/crm-mail/google/callback` are no longer used for new connections.

## [0.100.0] - 2026-11-17

### Added

- Recommended donors can now be sent straight to an Outreach sequence: Find emails and send to a sequence has OutflareBot find each site's advertising or editorial address (never data-protection, legal or no-reply mailboxes) and adds the best one to a sequence: the one used last time, or a new draft made right there.

### Changed

- Paid ads, Actions is now one table: every recommendation and alert with its type, why, the expected effect and one main button. Alerts open the page that fixes them, raises show the budget before and after and the most they can add a month, and recommendations have an obvious On/Off switch with a preview of what you'd get.
- Paid ads has a new layout: six tabs (Overview, Actions, Campaigns, Customers, Experiments, Setup), one date menu with Today and a custom range, and an Overview where every number reads the same source. Your own brand's campaigns are judged apart from the rest, the top actions are listed first, and a three-step funnel shows clicks, sign-ups and first payments.

### Fixed

- The changes from v0.99.0, which didn't pass the release checks, are now published.
- A slow email lookup no longer stalls the email step. When a connected finder such as Treg gives no answer in time for a person, that counts as no address from it and the step moves on, where before it asked again up to five times and could switch the connection off for a while.

## [0.99.0] - 2026-11-17

### Added

- Outflare for Chrome: a side panel that shows what you know about the person an email is from in Gmail, or the company whose website you are on, and adds the ones you don't know with a note or a to-do. The toolbar icon turns green on a company you know and red on a do-not-contact site, checked inside the browser. For now it is loaded from the `extension/` folder.
- Website tracking can capture clicks and form submits without any code: turn on "Capture clicks on buttons and links" or "Capture form submits" for a site and they arrive as the events Element Clicked and Form Submitted, ready for funnels and trends. What visitors type is never read. Add `data-outflare-event="Plan Chosen"` to any element to give its click your own name.
- Dashboards: pin a saved report as a tile. A report saved on Tracker → Report (tracker clicks or site visits) can sit on a dashboard beside your charts, showing its top rows and totals. It is for your signed-in team: on a shared link the tile shows only the report's name.
- Trends and dashboards can now show a brand's search and link-building numbers: Search Console, Bing and Google Analytics clicks, impressions, CTR, position and visitors; live, new and lost referring domains; and link-order spend, commitments, publications and payments. They read data Outflare already stores, so a dashboard can put organic search next to sign-ups.
- Trends and dashboards can show Search Console the way its own report does: clicks, impressions, CTR and position filtered and split by query, page, country and device. Any dashboard can now carry tiles such as top queries, clicks by country, or one page's positions on mobile.
- Error tracking: turn on "Capture errors" for a site and the new Errors page under Analytics shows what breaks on your website or app, grouped into issues with how often each happens, on which pages and to whom. Resolve or ignore an issue; a resolved one opens again if it comes back. Email addresses and keys are taken out before anything is sent.
- Libraries for sending product events from your servers: Node, Python, Go, Ruby and PHP. Each has identify, track, page, screen, group and alias, sends in batches in the background and retries when the server is busy.
- People from events: Source "Event" is now "Events and conferences", and a new Sub-source names the event or conference each person was met at. Pick one of the brand's events or type a new one, add several people from one event at once or from a CSV, and filter People by event.
- Link orders: label the donors you most want links from as Best link. The catalog shows them first, can show only them, and the label can be set from a cell's right-click menu, the donor card or a best_link CSV column.
- Link orders catalog: import donors from an Excel (.xlsx) file or a Google Sheet link, not only CSV. A sheet shared as "Anyone with the link" or published to the web is read without connecting Google, then imported in batches with the same column matching and intermediary for every row.
- Link orders: Recommended donors. Pick a page to link to and the catalog ranks donors for it, with the reasons: topic, language, DR, organic traffic, price per 1,000 visits and links to the competitors that rank for the page, minus link farms and donors already used. Add the best to a quote request in one click; nothing is ordered on its own.
- Link orders catalog: sites whose metrics look like a PBN (high DR with almost no traffic or keywords, few referring subnets, thousands of outgoing links) get a "Likely PBN?" badge with the reasons, a filter to review them, and Flag as PBN in the right-click menu and on the donor card. It only suggests: the PBN flag stays yours to set.
- Link orders: record a link you already bought in one form (article, page, anchors, price and currency), or import past orders from a CSV. Outflare adds the site to the catalog, opens the order, saves the price, approves it if you are the brand's approver and records the publication, which starts the live-link check; Paid stays a separate step.
- Link orders: an order now shows the conversation with its donor: what Outreach sent to the site or its intermediary, their replies with the Inbox label, and the prices written in those replies, so the agreed price is one glance away.
- Libraries for sending screens and events from mobile apps: iOS, Android, React Native and Flutter. Apps send with a public site key that has no origins, keep events on the phone while it is offline, and say which app version and phone each event came from.
- Named clicks: give any button or link on your site an event name, such as Signup Clicked, without changing the site. Open Analytics, then Named clicks, pick a click from the list of what people clicked, and name it. Matching clicks are stored under that name from then on, ready for funnels, trends and A/B test goals. Needs "Capture clicks" on for the site.
- Outbrain and LinkedIn campaigns now show their ads, with spend, clicks and conversions per day: Outbrain's promoted links with their title, landing page and whether Outbrain approved them, and LinkedIn's creatives with their name, status and review. Neither platform has ad groups, so the ads sit directly in the campaign. They are read only for now.
- Pinterest Ads and Reddit Ads now show what is below each campaign: ad groups and their ads, with spend, clicks and conversions per day, each ad's landing page, and whether its link carries your tracking template. They are read only for now; pause, resume and budgets stay at the campaign level on these two.
- Session recordings and heatmaps: watch a visit to your website back as the visitor saw it, and see where each page is clicked and how far down it is scrolled. Both come from the snippet you already have, are off until you turn them on under Analytics → Recordings, and never record what a visitor types. Recordings need file storage on the server.
- The website script can tell your marketing site that a visitor is signed in to your app. Call `outflare.signedIn(true)` after sign-in and every page on your domain can ask `outflare.isSignedIn()`, show or hide elements with `data-outflare-signed-in`, and use a "Signed in" condition on flags and A/B tests. The cookie only says yes: it never says who the visitor is.
- Snapchat Ads and Microsoft Advertising now show what is below each campaign: ad squads or ad groups and their ads, with spend, clicks and conversions per day, each ad's headline and landing page, and whether its link carries your tracking template. They are read only for now; pause, resume and budgets stay at the campaign level on these two.
- Upload your build's source maps and the Errors page shows every stack in your own files, lines and function names instead of minified ones. One command in your build script uploads them; your source code is never kept, only names and positions. Errors has a new Source maps page that lists what you uploaded.
- Taboola campaigns now show their ads: each item with its headline, picture, landing page, whether Taboola approved it, and its spend, clicks and conversions per day. Taboola has no ad groups, so the ads sit directly in the campaign. They are read only for now.
- Tracker: enter what a campaign cost by hand. For a network whose cost Outflare can't read, Tracker → Logs → Cost updates takes a campaign, the days and the amount (or a CSV of them) and shares it out evenly over the campaign's clicks, so every report has cost and the total is exactly what you entered.
- Tracking plans: in the event dictionary, mark the properties every event must carry, and see which events arrive without them or with properties you never listed. Nothing is dropped.
- Treg joins the email finders: connect your own Treg token under Connectors and add it to a lead list's email waterfall to find work emails from a name and company domain. Lookups are billed to your own Treg balance (at most $0.10 each) and each billed lookup counts as one credit against the list's cap and in the spend ledger.
- Your pages can now tell Outflare who a signed-in visitor is, safely. Turn on browser identify for a site in Website tracking, have your server sign each user id with the secret you get, and call `outflare.identify(userId, signature)` in the page. The visitor's earlier visits join their record. A page can't name a person without your server's signature.
- Destinations: write your product events to a table in your own BigQuery or Snowflake as they arrive, and press Send a test event on any destination to see it work before real events come in.

### Changed

- The Agents page keeps itself current: Waiting for you, All changes and Rules load again every few seconds while the page is open, and at once when you come back to the tab, so new changes from your agents show up without a reload. A selection you are making in Waiting for you stays ticked.
- AI crawler visits without Cloudflare: your server can now report them. Call `crawlerVisit` (`crawler_visit` in Python) in the server library for every request, and AI visibility shows how often GPTBot, ClaudeBot, PerplexityBot and the others read each site, per day. Only the counts are kept.
- Under Send conversions back, "Who to send for" has a third choice: "Everyone: our site already collects consent". Pick it when your own site asks every visitor for consent to ad data, and conversions are sent for everyone in every country. People who opted out are still never sent.
- Adding a rule under Send conversions back now lets you search your own events by name instead of typing one from memory, picks the amount for you (a payment sends its own amount), and works out whether the number is in dollars or cents. Rules can now be edited, and a payment rule with no value is fixed in one click.
- Paid ads has a Send log screen: every conversion sent to an ad platform, with what happened in plain words, what it was matched on, its value, and a link to the person. When an event doesn't say which country someone is in, Outflare now uses their earlier events and then their record before holding the conversion back.
- Sales reporting now shows discovery searches, tool calls, retries and recorded costs from a connected Lead Portal. Compare lane yield and inspect screening and grading evidence, with missing prices clearly marked.
- The email step of a pipeline now asks your own connected email finder (Treg, Prospeo, Findymail and others) when Outflare's free checks leave only a low-confidence guess, spreading calls across your connections, before your verifier checks the address.
- Outflare tells you when a new error appears. The first error of a kind nobody has seen before raises an alert, and so does an error you resolved when it comes back; an issue you ignored stays quiet. Choose who gets it by email, Slack or Telegram under Alerts, like every other alert.
- The event dictionary is now the **Event list**, the first view of Customer data → Events. Its counts cover a range you pick (last 7, 30 or 90 days, or all time) and show how many people sent each event and where it came from. `order_completed` and `payment.succeeded` say that one is made from the other, so they no longer look like duplicates. Adding a Send conversions rule links to the Event list.
- Feature flags and A/B tests can be limited to people: everyone in a People list (such as staff or pilot users), a field of the person's record (such as their plan), or named user ids. Outflare recognises the visitor from the id your product identified them with.
- Funnels can be split by more than the first step. Under "Break down by a field of", pick any later step (for example the plan people chose at step 2) or the person, to compare how each plan, lifecycle stage or field of your own converts. In the API, `breakdown` takes `step:<n>:<field>` and `person:<key>`.
- Heatmaps tell the steps of a page apart when its content changes at one address (a sign-up, a wizard): each step has its own heatmap and picture. Tick pages in the Heatmaps or Recordings list to exclude them from tracking, and Recordings has a "Delete all" button.
- CRM → Lead finder has an Instructions box when you start a run: write your own brief on which companies fit and how to judge them, next to the accept and reject lists. It is saved for that ideal customer profile, and every AI step of a run reads it.
- The iOS library now installs in Xcode from the repository's address (File, Add Package Dependencies), and every library has the same version as the Outflare release it comes with, so you can tell at a glance which release a library belongs to.
- Link orders: the live-link check now compares the link text on the donor page with the ordered primary and second anchors, and the Checks tab marks it as matching, partly matching or not the ordered anchor. It is a warning only and never marks a link lost.
- Link orders catalog: filter by category, subcategory, audience country, article type, DR, price and traffic ranges, referring domains, keywords, outgoing domains, date added, and price per 1,000 visits. A "Sort by" list adds best value, price, DR, traffic, referring domains, keywords and newest first, and Reset clears everything.
- One donor catalog for every brand: Import donors can send a broker's file to every brand at once, and the Catalog tab copies an existing catalog to the other brands with its prices, metrics and flags. Each brand's orders stay its own.
- Link order target suggestions now put first the pages SE Ranking has at positions 4 to 20, where a link is most likely to move them onto the first page or into the top 3. Positions 4 to 10 count more, and each suggestion says the keyword and the position.
- Feature flags and A/B tests can now be decided in your own server's memory: `LocalFlags` in the TypeScript and Python libraries reads a brand's flag definitions every 30 seconds and picks each visitor's version without a request to Outflare per page, with the same answers as the website script.
- Mailbox health now counts the emails, replies and bounces your connected Instantly or Smartlead account sends from each mailbox, read from your own account about hourly. A new endpoint and agent tool give one mailbox's activity by day. Warm-up mail is never counted.
- Parallel joins the connector catalog under Lead data: connect your own Parallel API key to research a company on the web, with findings that name the pages they came from. Each run is paid from your own Parallel balance and booked on the connection.
- Partner programs can now credit only conversions from a time you choose. Set the credit start time before importing payment history for a program moved from another platform, so older sales and sign-ups earn no new rewards. Existing rewards and refunds stay unchanged.
- Pause and resume a single ad on Taboola, Outbrain and Reddit (and a Reddit ad group) from the campaign's page, the API or MCP, leaving the campaign and its other ads running. An agent's pause follows its grant; a resume always waits for a person.
- Trends and funnels can filter and split payments by where the payer first came from: the channel, source and landing page of their first website visit. Revenue from organic search, or from people who first landed on the blog, is now one filter.
- The Rankings tab now says what the last import from SE Ranking did: the project it matched and how many keywords came in, that no project in the account is for this site (with the sites its projects are for), or that the import failed and why. Before, an import that brought nothing in looked the same as one that never ran.
- Delete recordings in bulk: tick several and delete them, or delete every recording older than a number of days. A page's heatmap, or every heatmap, can be cleared to start counting again, such as after a redesign.
- Recording settings have "Hide your own keys": type a made-up key that looks like the ones your product shows, and everything with that shape is hidden in recordings and heatmap pictures. Only the shape is kept.
- Recordings and heatmap pictures hide API keys, tokens and passwords shown on a page, in the visitor's browser before anything is sent: known key formats, user:password@host addresses, values after labels such as "API key" or "Password", and long runs of mixed letters and digits.
- Recordings and heatmaps have more settings: record only the pages you list or leave some out, drop recordings of very short visits, and cap how many visits are recorded a day. A heatmap whose page can't be shown yet now says why, with a button to turn recordings on.
- Recordings start playing when opened and mark every click, typing, new page and pause on the progress bar; the list names known visitors and filters by website. Heatmaps take their own picture of each page, so they work without recordings, show only the busiest areas by default and list the ten most clicked spots by name.
- Tracker logs: a postback to a traffic source can be sent again. **Send again** on a row of Postbacks out (or `POST .../traffic-sources/{id}/postbacks/{send}/resend`, MCP `resend_traffic_source_postback`) queues it with the status and payout it had, to the source's postback URL as it is now, so a send that failed on a wrong URL goes through once you fix the URL.
- Sales shows assignment, seen status, feedback and contact location on each conversation. Assigned leads appear in My conversations before work starts and teammates can reassign them. SDRs and AEs can apply favorite templates in one click, browse previews, and personalize replies with AI before reviewing and sending.
- Sales conversations now stay in step with the Inbox and the CRM. Taking a conversation makes you the person's Owner, confirming a customer wins their deal, hand-offs from support show with New conversations, and the Inbox links your Sales queues. Sales screens say conversation, person or company in place of "lead".
- Archive a Sales conversation directly from the queue, with Undo for accidental clicks. Archived conversations keep their messages and notes, and every archive and reopen remains recorded with who made the change.
- The TypeScript and Python libraries got shortcuts for flags and errors on a server. `LocalFlags` answers for one flag with `variant`, `isEnabled` and `payload`; the event tracker's `exposed` counts a visitor in a flag's A/B test when your server shows them a version; and `captureException` sends an error to the Errors page with email addresses and keys taken out.
- On Website analytics, Search, each page and search query now opens a page of its own: a page shows the searches that showed it, and a search shows the pages it showed, each with clicks, impressions, click-through and average position over the period you picked. Works with Google Search Console and Bing Webmaster Tools, and through the API and MCP with `page` and `keyword` on the report.
- Website analytics, Search loads faster: Search Console is asked for a report's total and its rows at the same time, and the Pages and Search queries tables load together, so switching between them shows the answer straight away.
- Tracker report: three new columns for landers. Lander views (clicks sent to a lander), lander clicks (those that went on to an offer) and lander CTR show for every grouping, sort, work in custom columns and are in the CSV, so you can compare landers by how many visitors they pass on.
- Tracker → Report opens on tracker clicks, shows campaigns, offers, landers and paths by name (in the table, filters and CSV), and labels custom fields with what your traffic sources call them. Filters on a campaign, source, offer or lander are picked from a list. A mistyped custom column is refused where you type it, and deleting a saved view asks first.
- A Google Workspace connection can now create new users in your own tenant, when you turn on "Allow creating users" and authorize the Directory user scope. Each new user takes one of your own licences.

### Fixed

- The changes from v0.98.0, which didn't pass the release checks, are now published.
- Developer setup no longer treats older payments from a history import as possible retries under new IDs. The retry check now compares only payments received within 24 hours of when they happened.
- Link orders catalog: a broker export's price column is now imported when its header names the currency twice ("Price USD, $"), and "Ref. IPs / Subnets" fills referring subnets. The catalog table shows every intermediary metric as a sortable column you can hide, draws large catalogs a page at a time, and an import can set the intermediary for every row.
- Link orders catalog: the Catalog page now loads the whole catalog instead of stopping at 1,600 sites, so filters, sorting, recommended donors and PBN hints cover a full intermediary database. The catalog API returns up to 1,000 rows a page.
- SE Ranking project imports read ids, positions and flags that SE Ranking sends as text as well as numbers, and a failed import now says on the Rankings tab which step failed (the projects, the keywords or the positions) and why, instead of only "unavailable".
- Importing people now adds a missing user ID when an email matches an existing person, so later events and transactions find the same record. Rows whose email belongs to a person with a different user ID are skipped with a clear reason.
- The drill-down report's totals no longer count some conversions, revenue and cost twice when you group by something not every click has (offer, lander, path, a custom field), and those clicks now show as their own row. At conversion time, days are the conversion's. Money in a currency with no exchange rate is named in a warning.
- Sales conversation lists show when the displayed reply or message was sent, including the time in your local timezone. Blank lines no longer hide the reply behind its greeting in compact previews.
- A Telegram chat, and picking its customer, open as full pages instead of a side panel. Every Settings tab has an icon, and the AI model settings and approval messages no longer mention retired missions.
- Tracker → Campaigns now shows each campaign's real clicks, conversions, revenue, cost, profit and ROI (it showed zeros), for the dates you pick, sorted by any column, with a Report button per campaign. A new campaign asks for its fallback page, one saved without it can be fixed in its editor, and campaigns can be archived and restored there.
- A repeat postback that only changes a conversion's status now keeps the payout the network first reported (it was replaced by the offer's own payout, or 0). Paging the conversions and cost update lists reaches rows that share a timestamp, and a later cost update for the same days always wins over an earlier one.
- Tracker → Report: a custom column whose formula doesn't read (such as "revenue /") is now refused where you type it, and a report that fails because of a custom column offers to remove it instead of leaving the page stuck. Archived affiliate networks are no longer offered in the offer form or in Setup.
- Tracker: a postback with an unfilled placeholder (status={status}) is recorded instead of refused, and Setup builds postback URLs only from placeholders a network has. Logs tables have column headers and say why a conversion is suspect. Archived sources and networks leave their lists and can be restored; currencies come from a list; number fields no longer jump as you type.
- Tracker: link-preview fetchers such as Meta's facebookexternalhit and WhatsApp no longer count as visits (or as paid clicks). A conversion can't be dated before its click. A report asked for "from today" works in time zones ahead of UTC. The Offers and Landers lists say what their daily counts include.
- The Treg connector now reads an answer without an address as "no address found". Before, some of those answers were taken for a broken service, retried, and could switch the connection off for a while.

### Security

- Security improvements to tracker click recording.

## [0.98.0] - 2026-11-16

### Added

- You can now pause and resume a single ad set or ad, without pausing the whole campaign, on Meta and Google Ads, and set the daily budget of a Meta ad set that holds its own. Open a campaign on the Paid ads page and use Manage on an ad set or ad; the API and MCP have the same changes.
- Alerts can now post to Telegram: connect your own Telegram bot under Connectors, then pick a chat, group or channel for each kind of alert under Settings → Alerts, beside its Slack channel and email.
- Ask a question: type what you want to know at the top of Dashboards and get a chart you can open in the builder or save. It runs on your own AI model and sees only event names and descriptions, never people or their data.
- Customer accounts: if your product has workspaces or teams, they are now their own records (Accounts), with members and their roles, separate from People and Companies. Each brand starts with smart defaults for its business type (who it sells to, whether it has accounts) and can change them. A person can also have a preferred email that sends go to.
- Destinations: send your product events on to a webhook, Slack, Google Analytics 4 or HubSpot as they arrive, each on your own connection, with a log of what was sent, skipped or refused. Find it under Events.
- Event dictionary: a new Dictionary view on Product events lists every event with a plain name, what it means, how much of it arrives, the properties it carries and where it is used. Edit names and notes in place; the names then show wherever you pick an event.
- On Google Ads campaigns that bid by hand (manual CPC), you can now set an ad group's bid and a keyword's bid from Outflare. Use Set bid in the Manage menu on a campaign's page; the Changes tab shows the bid it replaced. The API and MCP can also make a change that may only lower a bid.
- Sales opportunity on a support conversation hands it to your sales team in one step: it moves there with a note, you keep it under Watching, and the customer's owner gets it in Needs attention. When your AI spots an opportunity in a conversation, the Inbox says so and offers the hand-over.
- Online and Away in the Inbox: say when you are taking chats, and a new website chat goes to someone online, moves to the next person after 30 seconds with no answer, and carries on by email when nobody is free or the visitor has left.
- Intercom: keep answering in Intercom while Outflare keeps a copy of every conversation in Support, with its messages, notes, tags, ratings and what Fin did (how it ended, its rating and the articles it answered from). Copying starts when you connect Intercom for a brand, then stays in step every few minutes, and each customer joins your people. Copied conversations are read-only in Outflare.
- Tracker → Traffic sources now shows each Zeropark target's clicks, cost, conversions, revenue and ROI, with Pause and Resume, so you can stop the placements that lose money without leaving Outflare.
- Saved replies: write an answer once and drop it into any reply from the Saved replies menu, in support conversations and in replies to outreach, with the person's first name and your own filled in. Replies to outreach also get a Draft with AI button that writes an answer from your knowledge for you to edit.
- Stickiness: see how many days, weeks or months people come back in, on the Retention page and on dashboards. Funnels now draw conversion over time as a line, and a new Time to convert chart shows how long people take between steps.
- Taboola and Outbrain campaigns can now be changed from Outflare: pause and resume, the daily budget, and blocking a publisher site (or an Outbrain section) on a campaign, each as a queued change on your own account. Until now both were read-only.
- TikTok Ads now shows what is below each campaign: its ad groups and ads, with spend, clicks and conversions per day, each ad's text and landing page, and whether its link carries your tracking template. You can pause and resume an ad group or an ad from a campaign's page, and set the daily budget of an ad group that has its own.
- Trends do more: medians and percentiles, times per person, weekly and monthly active people, and a breakdown by two fields at once with each group as its own line. Show a trend as stacked areas or bars, bars across, a ring of shares or a world map, with a running total, shares of the whole, a moving average or a second axis.
- A Zeropark campaign's bid, or the bid of one of its targets, can now be set through the API and MCP as a queued change that records the bid before; agents may only lower a bid without a person's approval.
- Zeropark campaigns can now be paused, resumed and given a daily budget from Paid ads, and a single target paused or resumed through the API and MCP, each as a queued change on your own account.

### Changed

- Two new alerts under Settings → Alerts: one when a connected ad platform stops syncing (a refused sign-in, or three failed syncs in a row), and one when an ad platform starts refusing the conversions sent to it. Like every alert, each can go by email, Slack or Telegram.
- Self-hosted servers can read connected ad platforms more often than once an hour: set `OUTFLARE_ADS_SYNC_INTERVAL` to a duration from `15m` to `24h` (such as `30m`). No platform is read more often than its floor (half an hour for most, a quarter of an hour for Zeropark), so a shorter setting reads at the floor.
- Website analytics is now one tab per question: Overview, Pages, Sources, Websites, Countries, Devices, Search and Connected tools. Each tab shows a single table or chart, names where its numbers come from (Outflare Analytics, your search tool, or the connected tool you opened) and has its own address.
- Charts look and read better: smooth lines with a soft fill, sharp text at any size, and a card beside the cursor with the exact numbers for each day, week or month, the period before and the change. Line and bar charts show a forecast in a lighter dashed line with its likely range, and the day or week still running is marked "so far". Retention, lifecycle and funnel charts got the same treatment.
- Dashboards are editable in place: press Edit on any tile to change its event, filters, breakdown, chart type or name, build new charts straight from Add tile, and change the dates, comparison, name and share link without leaving the page.
- Dashboard filters: add a filter such as plan is pro at the top of a dashboard and every chart on it shows only those events, on share links too. Tiles also move and resize by dragging.
- Fixes: a ticket now opens as its own page inside the app, says what is wrong with examples from your pages, and explains its labels. AI write-ups are shorter, more specific and shown formatted. Each ticket has a number and a link, plus Copy (for a developer) and Give to agent (instructions for a coding agent).
- The revenue forecast now shows how it moved week by week: what it said each week of the quarter, how much was already won, and how far it rose or fell from the week before. A reading is saved once a day for every brand with deals or a target.
- A teammate you loop in on a conversation is now told. Their Watching view shows how many are new, the Inbox shows a message with an Open button, and if they haven't opened it after five minutes they get one email with a link.
- Right-click a conversation in the Inbox (or use its More button) to assign it to anyone, move it to a team or loop a teammate in. The same three are on the bar that shows when you tick several.
- Inbox rows have quick actions: right-click a conversation (or use its More button) to open it in a new tab, mark it unread, assign it to yourself, snooze it, close it or copy its link. Tick several rows to act on them together, and undo a close, snooze or assignment from the message that confirms it.
- The Inbox now tells you when something new arrives. New chats and messages show up on their own with an Open button, and a "Sound and notifications" switch plays a chime and shows a browser notification when the Inbox is in the background.
- Work a conversation as a team in the Inbox. Move a reply to a team, loop a teammate in so it shows under their new Watching view, and add private notes that only your team sees, on replies to outreach as well as support conversations.
- The Inbox warns you when a teammate has the same conversation open, or is writing a reply in it, so two people don't answer the same customer.
- Journeys now ignore events that arrive more than a day after they happened, such as imported history: they are kept for reports, but start, move and end no journey, so a backfill never emails people about the past. Automations, signals and lifecycle stages already worked this way.
- The Inbox now shows support conversations and replies to your outreach in one place: pick a support folder or a sales view in the views column, and the list and the conversation open beside it. Saved views of replies sit in the same column.
- A playbook of your own can now also hold scores, routing rules, emails and metrics, and its page has "Did it work?": each of its journeys that holds some people back is compared with the ones it held back.
- More full pages have an address of their own: a journey step being edited, a commission plan, and the signal types, credit and conversations pages. Back closes them, a refresh keeps them open, and you can send someone the link.
- The tracker now checks that the landers and offers your links send clicks to still answer: landers about once an hour, offers every six hours. The Landers and Offers tabs show what it found, and you get an alert when one fails two checks in a row, so clicks aren't wasted on a dead page. An offer's check can be switched off on its form.
- Paid ads: customers from a Google click that showed as "Campaign unknown" now get their campaign once Google reports the click, usually the next day, and a campaign id in utm_campaign is recognised. Results and the headline show revenue and return on ad spend (ROAS) beside spend.
- One People list: everyone, cold contacts included, with a Source (Cold, Inbound, Event, Sign-up, Invited, Customer) set once when they are added and a Marketing email yes/no column. Cold contacts get no marketing email until they opt in. Outreach's Leads is now the Cold filter, and you can add people to a sequence straight from the list.
- Routing now knows when your team works and how much each person can take. Under the routing rules, set a teammate's working days and hours, the most people and companies they own at once, or mark them away. New records go to someone at work first, and never to someone who is away or full.
- Sales reps can now use the company's Google Workspace connection for their Sales email instead of signing in to Google themselves. In Sales, My email shows **Use Google Workspace** to reps the CRM email settings allow, and taking someone off those settings disconnects their Sales email too.
- Sales reporting has a Discovery view: when your connected Lead Portal reports its company discovery, see how many companies were found, passed screening, were graded and sent to research, which search lanes produced them, and what each tool cost against the spending cap.
- Sales reporting's Discovery view now has the full lane board from your connected Lead Portal: filter the search lanes by state, page through them, and open one to see how it searches, why it was proposed, what it learned and what it found. Agents can read it with the get_sales_discovery_lanes tool.
- The side menu fits better: open several groups at once without the others closing, fold away the headings you don't use, close every group with one button beside Search, and the menu remembers how you left it. Rows are a little tighter.
- Tracking domains now show when their registration runs out, and you get an alert 30 days before, 7 days before and when it has, so the links on a domain don't stop working because it lapsed. The date comes from the domain's public registration record.
- Website tracking moved from Analytics to Settings, and now says plainly whether a brand's script is working: Live when a visit arrived in the last 24 hours, with the last visit and today's events, or Quiet when visits have stopped. The setup steps only show until the first visit arrives.
- The Tracker's Targets table now shows each Zeropark target's bid and state as Zeropark holds them, so a target paused in Zeropark's own panel shows as paused, and a bid can be changed by clicking it.

### Fixed

- AI visibility: when a run's answers fail, the page now says how many failed and why (a connection that is gone, a refused key, an account out of credit), instead of looking as if nothing ran. A run in progress has a Stop button, and one that stands still for ten minutes says so.
- AI visibility: answers read from ChatGPT's and Gemini's own screens through DataForSEO are no longer cut off after 45 seconds. They now get the three minutes an assistant can need to search the web and write, so runs stop losing most of their screen answers, and one that still runs out of time is recorded as a timeout.
- Website analytics on a brand now reads only that brand's own sites: the live reports never fall back to another brand's Search Console or Google Analytics site, and offer to pick the brand's site when it has none. A site read by two connections counts once, and signing in to Google again renews the connection instead of adding a copy.
- Site audits no longer report email links hidden by Cloudflare's email obfuscation (`/cdn-cgi/l/email-protection`) as links to a broken page, and reading a page now finds those addresses.
- CRM's Telegram page opens again, ⌘K finds People, Companies, Deals and the rest of the menu's pages, a link to a page that doesn't exist says so, old People links open the CRM lists, and page names match the menu (Library, Customer data, sales conversations).
- Sales introductions and handoffs can CC connected colleagues when their company domain is on an outreach blocklist. Prospect blocks, address-level blocks and opt-outs remain enforced.
- Search Console syncs now save each property's new days, sitemaps and status before inspecting its pages, and sync the properties that waited longest first. A sync that is interrupted part-way no longer leaves a property showing an old error and days-old data.
- Sign in again on a connection now signs that same connection in, instead of adding a second one. It keeps its accounts and history, so a Google Ads connection from before Outflare asked for Data Manager can grant it without counting your ad spend twice.
- After signing in again, the dialog for choosing which sites to import shows Done when nothing needs to change, instead of a greyed-out Saved button.
- Sitemaps from Search Console no longer go stale when a sync can't read some of the search data: a sitemap you submit shows after the next daily read, or straight away when you sync the connection yourself.

### Upgrading

- nothing to do; without the variable, syncs stay hourly. Two new metrics, `outflare_ads_sync_overdue` and `outflare_ads_sync_oldest_seconds`, a dashboard row and the alert `OutflareAdSyncOverdue` say when a platform's connections fall behind.

## [0.97.0] - 2026-11-15

### Added

- Account page: a person's or company's page now shows where they stand at a glance: what needs attention, stage, owner, last contact, revenue and its trend, an account plan, products and usage, people, deals, support conversations and tasks. Deals get a Lost reason, and the timeline shows support conversations, tasks and notes.
- Agreements and renewals: a setup kit adds an Agreement record (subscription, contract, spend commitment or retainer) with its value, term and renewal date. A company's page lists its agreements and flags a renewal 60 days ahead.
- New predictions to start from: churn risk, expansion potential and each deal's win likelihood, which the forecast uses in place of fixed stage weights. Each is checked against what really closed or churned.
- Company groups and share of wallet: companies have a Parent company field, and a parent's page shows the whole group with each company's revenue, health and open signals. The new Share of wallet setup kit records what a company spends a year on what you sell, so its page shows how much of that is yours and how much room is left.
- Journeys for companies: a journey can now be about each company instead of each person. It starts when the company, or anyone there, does an event, runs once per company, and its steps act on the company: tasks, fields, tags, owners, alerts, Slack, webhooks and the new "Raise a signal" step, which also works in journeys about people.
- Signals from conversations: a brand can let its own AI read what customers say in support, company Telegram chats, shared email and call transcripts, and raise a signal with the customer's own words. It is off until you switch it on, runs on your own AI account, and never reads private email. A new Credit view shows whose signals turned into revenue.
- New playbook: Hand every won deal over to the people who look after it. A won deal lands in Needs attention as a new customer to onboard, with a kick-off task for the owner, and a closed support conversation that comes back is flagged to the owner too.
- New playbook "See customer health, churn risk and who is ready to grow": sets up the health sub-scores, sentiment, both predictions and the rules and journeys that flag them, in one go.
- Journeys can be about deals. Pick "Each deal" when you make one and it starts on the deal's own events, such as a stage change, reads the deal's fields, ends when that deal is won or lost, and acts on the deal: tasks, fields, owners, alerts and signals.
- Journeys can now do anything in Outflare: the new "Do an Outflare action" step calls any part of the product in the journey's brand, such as adding a note, pausing something or starting work in another module. It runs as one of your agents, so what that agent may do on its own runs straight away and anything else waits for your approval in Agents.
- New playbook: Score account health and catch usage drops. Name your usage event and it adds a health score for companies, the metrics behind it, and a rule and journey that flag a company whose usage falls. Setup kits can now bring metrics and scores.
- Build your own playbooks. New playbook on the Playbooks page groups the rules, journeys, smart lists and signal types a brand already has under one name, and gives them one page that shows what is on, what each part has done and how its signals ended. Removing a playbook never removes its parts.
- Playbooks can show whether they work: hold back a small share of people when you set one up, and its page compares those who went through its journeys with those held back, with conversions, revenue and lift.
- Assign a site on Presence: pick who on the team is managing a site you want a link or placement from (a dropdown of teammates with their photos), so nobody else reaches out. Opportunities show each site's assignee and filter to yours or unassigned ones, the CRM's relationship check names the teammate, and pitching a teammate's site asks you to confirm first.
- Rules for companies: an event or inactivity rule can now be about a company, where anything anyone there does counts ("no one at the company has used the product for 14 days"). Deals, support conversations and CRM tasks also arrive as events (deal.won, support.conversation_reopened, task.completed), so rules, journeys and metrics can act on them.
- Renewal deals open by themselves: a journey step opens one deal per renewal coming up, with its value and date, and the forecast counts it once, as the renewal. The renewals playbook now does this 60 days ahead.
- Forecast on the Revenue page: what you can expect to book this quarter against your target. It adds the deals already won, every renewal due weighted by the company's health, and every open deal weighted by its stage. Each line shows what it adds, and you can change the target and the weights.
- Revenue: a new page under Measure shows each brand's monthly recurring revenue and how it moved, net and gross retention, what was collected, the renewals coming up with each account's health, and the revenue sitting with customers whose health is low. It fills in from your billing connection, renewal dates and health score.
- Routing: say which teammates get which companies and people. Each brand keeps a short list of rules tried from the top, and each new person or company with no owner goes to the teammate in the matching pool who owns the fewest. Anyone who already has an owner keeps them.
- Sales reporting preview sits beside Sales workspace: explore seven milestones, hourly or daily volume, each tool’s cost per lead, Sales mailbox capacity and closed deals. All figures are clearly marked as example data until reporting is connected.
- Sales team and commissions, under Revenue: quotas per person and quarter rolled up by manager, and commission plans made of rules (what it pays on, inbound or outbound, new or returning customers, days since the first payment, rates by quota, caps and refunds), with every line showing the rule that paid it.
- Scores: keep one number from 0 to 100 on every company or person, such as a health score or a lead score. You choose what adds points and what takes them, and each record shows why it scores what it does. Rules, journeys and smart lists read the score like any field.
- Conversation reading now also says how a customer sounds (positive, mixed or negative) with their own words, on your own AI. Scores, rules and lists read it as sentiment; unknown never counts as unhappy.
- Signals and a Needs attention queue: rules, people and agents can now flag a person or company for someone to act on (churn risk, an upsell, a renewal coming up). Each signal has an owner, a due date and an outcome, a record holds at most five open ones, and one nobody picks up goes to the owners and admins.
- Connect Zeropark with your own API token to bring in each campaign's daily spend, visits, conversions and payout, beside your other ad platforms.
- Tracker cost from Zeropark is now exact: each day, Zeropark's spend per campaign and target is shared out over the matching clicks by their own visit cost, so every report shows cost and ROI that add up to what Zeropark charged.

### Changed

- A new playbook, "Flag renewals and quiet accounts for their owners", sets up Needs attention in a few clicks: it adds a rule for companies that stop using the product and one for renewals within 60 days, and journeys that raise a signal and a task for the account's owner. Setup kits can now carry signal types and journeys about companies, and exporting a journey as a kit takes them along.
- Every analytics site now belongs to a brand: "Shared by all brands" is gone. A site takes the brand someone picked for it, else its connection's brand, else the workspace's first brand until someone picks one; a site's domain still only suggests a brand. Sites that were shared or had no brand moved the same way, with their stored history.
- Needs attention has an Assign menu to give a signal to a teammate, a company or person with no owner has a Route button that applies your routing rules, and the people and company lists show their scores as columns.
- Giving a lead, company or deal to someone is one step: an Owner control on every record's page, and "Assign to" for the rows you tick in the CRM list. Settings, Team gains an org chart.
- Every mailbox, sending domain and ad account now belongs to a brand. New ones join their connection's or domain's brand, or else the workspace's first brand. Ones that had no brand moved to the first brand, and the "Not in a brand" card is gone. Move any of them to another brand on the Brands page.
- Two people can share a deal: set a Second owner and their share, and the deal is split between them for quotas, the forecast and commission. A month's commission downloads as a CSV file, one row for every line, for payroll.
- CRM email settings can now exclude people by email address, including people who don't have an Outflare account yet. Whoever joins with one of those addresses is never read.
- The CRM menu links straight to People, Companies and Deals. The Sales workspace page is now Sales conversations, and the page of events, products, metrics, scores and imports is Customer data.
- A brand's Events page now shows every event it received, from your website, your product's servers and any connector, newest first, with counts by source and filters by source and name. Before, it showed only events sent to the events API, so a brand tracking only its website saw an empty page. The events API's receipts, rejections and replays are on the page's second view.
- Forms and details that used to slide in from the right now open as a full page with a Back button, across the app: new tokens, lists, flags, link orders, imports and more. The step editor in the journey builder stays beside the flow.
- Google Workspace connections that allow profile changes can now set a user's recovery email and forward their new mail to another address, keeping a copy in the inbox.
- The "Do an Outflare action" journey step has a pick-list of common actions (add a note, route to an owner, pause an ad campaign, pause a journey or rule and more), so you choose by name instead of typing an API path.
- Metrics can keep the latest value of a number a product reports, such as a prepaid balance, a plan limit or a seat count, so rules and the account page can use it.
- Tracker → Traffic sources shows where each source's cost comes from, and a Zeropark source shows how each day's spend was shared out over its clicks.
- One list of people and one of companies. CRM → People and Companies now also show scores, records held for review and archived ones (with Restore), and the bulk edit you can undo. Customer data keeps Events, Duplicates, Products, Transactions, Metrics, Scores, Routing, Imports and Recently deleted, and no longer has a second list.
- Each playbook now shows what it set up in your brand and what it has done: every rule, journey and list with whether it is on, and the signals it raised with how many were won. You can also set a playbook up straight from its page, without starting an agent run.
- Taking records out of a brand (`remove_from_brand`) is refused, because brands share nothing; move a record to another brand with `add_to_brand` instead. Ad campaigns again follow their ad account's brand when the account moves.
- The Revenue page splits what was collected by product, and the forecast shows who it sits with: what each teammate has won and what is expected from the deals and renewals they own.
- Routing rules can give records to a team, so a new teammate starts getting leads by joining the team, and can keep to a territory: a list of countries, such as Germany, Austria and Switzerland.
- Rules can react to dates and trends: compare a date field with today, +60d or -14d ("renews within 60 days", "not contacted for 30 days"), checked every day, and test how far a metric moved against the period before ("usage down more than 30%").
- Compare Sales milestones as lines or bars and open an hour or day to inspect every selected stage. Connect your Lead Portal for pipeline progress, costs per verified lead and mailbox capacity, alongside Outflare SDR pickup and confirmed customers.
- Open Reporting from Sales to follow the pipeline and confirmed customers. Explore the clearly labelled UI demo even before the leads source is connected. Live reporting keeps Outflare results available, marks missing source data clearly and links customers to their Sales conversations.
- Sales Results compares replies, SDR pickups, confirmed customers and their recorded expected monthly revenue with the previous equal period, without repeating the flow chart. Tools & spend links to each paying provider so you can manage a plan or add funds.
- Sales queues, lead details and reporting lists show the original uploading agent or named API key. Results compares each uploader’s pipeline progress and discovery submission quality, keeping unknown origins and revoked identities visible without exposing credentials.
- Scores now say what kind of number they are (measured, AI-read or predicted), keep their history, and can be made of other scores: a health score with usage, billing and engagement visible under it. Deals can be scored too.
- Tidier screens: a menu entry with one page opens it in one click, routing rules and the People tabs fit on a phone, routing conditions read in plain words, and the Outflare action step shows plain labels.
- The sidebar is arranged around how the work flows: Needs attention is first under Customers, a new Automations group holds Playbooks and Rules and journeys, Lifecycle is now called Email, and the lead finders sit under Outreach. Every link works as before.
- The Sales team page shows how each person handles their signals: how many they closed this quarter, how many were won, how long it usually takes, and how many are waiting or late.
- Picking a teammate looks the same everywhere: the Owner control on a record, "Assign to…" in CRM lists and Coverage now share one searchable list with photos. People in the team directory who have no sign-in can own records too, and their deals count towards their quota and commission.

### Fixed

- The Google Workspace tenant checks connection is back: the new Google Workspace email and calendar connection had taken its place in the list of connectors. Both are now listed and work side by side.
- "When this fires, do…" on a rule no longer fails with an error: the step you pick opens in the new journey, ready to fill in. Choosing a step is a bigger screen with groups down the side and a search box. On the automation map, many sources that send the same events fold into one card.
- Notifications now show at the top right, under the header, so they never cover the agent chat's message box. Each one has a close button, can be swiped away, disappears on its own after a few seconds, and the same message never stacks twice. At most three show at once.

### Security

- Security improvements to the web app.

## [0.96.0] - 2026-11-14

### Added

- ChatGPT Ads joins Paid ads: connect your ad account with your own OpenAI Advertiser API key to see spend, clicks and conversions per campaign, pause, resume and set budgets with your approval, get budget suggestions, track ChatGPT ad clicks (oppref), and send sign-ups and purchases back through OpenAI's Conversions API.
- Connect Google Workspace with your own service account, without downloading a key, to check a tenant and its domains, and, if you allow it, change users' names, photos and Gmail signatures.
- Connect an InboxKit account and workspace to discover and import selected mailboxes, or automatically synchronize its inventory through the supplier mailbox interface. InboxKit continues to bill your connected account directly.
- Partner conversions are now checked for fraud (self-referrals, payout details matching the customer, cookie stuffing, instant conversions, high refund rates, suspect tracker clicks), and a flagged conversion's rewards wait until you clear or confirm the flag.
- Sales teams can work a shared new-lead queue, open full conversations, save personal email templates, keep notes through handoffs and confirm customers without recording a payment. Personal email sending requires each person's own consent.
- People can send email from their own Gmail address through Outflare. It checks every recipient against your opt-outs first, keeps the sent email on the contact, and never sends twice. It uses your Google Workspace connection, once it is allowed to send.

### Changed

- Brand data always belongs to a brand: records, sequences, reports and the rest can no longer be saved without one.
- Meetings read from your team's calendars now follow the same privacy as their email: private to the calendar's owner unless they share the contact, or team sharing shows those the AI check finds to be about work. Events marked private in Google are never saved.
- The Partners page has a Review tab: see each flagged conversion's signal, partner and held reward, then clear the flag or confirm fraud, optionally suspending the partner.

### Fixed

- Sales queues show the prospect’s reply, source links and arrival time before you open a conversation. Managers start on All leads with status and teammate filters; Sales keeps the API panel closed until opened and gives browser tabs readable names.
- When a connected sequencer refuses to turn a mailbox's warm-up off, the Mailboxes page now says it is still turning off there, and Outflare keeps retrying, instead of showing it as off.

## [0.95.0] - 2026-11-13

### Added

- Tracker clicks can now be looked up on your own IPQualityScore account (Tracker → Setup): a high risk score or a data-centre network flags the click and its conversions, and their postbacks to traffic sources wait for the lookup.
- The Tracker has a Logs tab: clicks, lander clicks, conversions, and postbacks in and out, newest first with Live refresh, plus the postbacks that weren't recorded, grouped by why with the fix. The API and MCP list clicks and lander clicks too.
- The Tracker has a Setup tab: a checklist that ticks itself as you set up conversion tracking, a postback URL builder in each affiliate network's own tokens with its pixel, script and server call, and a test conversion that says what happened and why.
- The Tracker has Traffic sources and Affiliate networks tabs: start from a gallery of network templates, preview the campaign URL as you edit its parameters, and send conversions back with a postback that can't be saved half filled in; each affiliate network shows its postback URL to paste into the network.

### Changed

- A sent broadcast now shows its recipients: one row per person with what happened to their email (delivered, bounced and why, marked as spam, skipped and why) and when it was sent, delivered, opened and clicked. Filter by status with one click, search for a person, or download a group as a CSV. The API and MCP list the same with `show` and `q`.
- Direct-mode tracker clicks, recorded by the site script, are now checked against the brand's user agent click filters too, so they are flagged as suspect like redirect clicks.
- Brands can choose which tracker clicks get an IP reputation lookup on their own IPQualityScore account: flagged clicks, converting clicks and a sample of the rest, up to a monthly cap. The lookups themselves arrive next.
- IPQualityScore joins the connector catalog under Fraud checks: connect your own account to score the addresses of tracker clicks for proxies, VPNs, Tor, data centres and crawlers.
- Link orders: right-click any catalog cell for site actions. Find email shows the addresses already stored for the site (people, link-building contacts, Outreach leads, notes). Send to a sequence adds a stored or typed address to one of the brand's Outreach sequences with the usual suppression and exclusion rules.
- Link orders: import an intermediary's full database (up to 32 MB, sent in batches with progress). Headers like "Traffic / Organic" now map, and the donor card shows the full metrics: referring domains, backlinks, keywords, outgoing links, geo and more. Donors get their own Intermediary (searchable), Theme and Site age fields.
- Link orders: the catalog marks sites worth writing to directly, not only through the intermediary (not a PBN, DR or traffic stored, an email at the site already stored), and you can filter to them and Find email or Send to a sequence from the row. The mark itself sends nothing.
- Link orders: enter a quote or payment in the invoice's own currency — GBP, PLN, CHF, JPY and 20 other common ones, not only USD and EUR. Outflare keeps the amount as invoiced and shows it beside the US-dollar equivalent at the ECB reference rate for the invoice date. Recording a payment still never sends money.
- On Link orders Targets, the sources line still shows how many stored keywords, targets and competitor pages the suggestions come from, but no longer lists every tracked competitor by name.
- The sidebar groups its modules under seven headings (Customers, Email and outreach, Search, Social, Ads, Measure, Company). Home puts the outreach email numbers under their own heading. The Agent page uses plainer names: All changes, Playbook runs and Rules, where a grant is now called a rule.
- Conversions flagged as suspect (a hosting network, a repeat clicker, a filter list, the honeypot or a conversion seconds after its click) are no longer sent back to traffic sources, so the networks' bidding doesn't learn from bots. They stay in the ledger and the reports.
- Brands can keep click filter lists of addresses, ranges and user agents they know are bots or fraud: matching tracker clicks still go on but are flagged as suspect, so they count against no cap and the report can leave them out.
- Tracker campaigns take a cost model: what the traffic source reports per click, a fixed cost per click or per conversion, or a revenue share, so the Tracker's Report shows cost and profit for every campaign.
- The lander script now carries a honeypot: a link no person can see or reach, so a bot that follows every link on a lander flags its own click as suspect, keeping it out of caps and reports.
- The Tracker's Report can show tracker clicks instead of site visits: clicks, conversions from your affiliate networks, payouts and the cost traffic sources reported, by campaign, traffic source, offer, lander, path, custom field or any click detail.
- X Ads connects with a **Sign in to X** button on Paid ads, Accounts, through your own X developer app, instead of pasting an access token and secret.
- X Ads and Reddit Ads can now be managed from Outflare: pause, resume and set campaign budgets (with your approval), get the same budget and pause suggestions as Google and Meta, read each network's own conversions, and send sign-ups and purchases back through X's and Reddit's Conversions APIs (Reddit's newer v3 API once you add your pixel ID).

### Fixed

- Business details on About your company no longer show example values in empty fields, and an address in a city-state such as Singapore no longer names the country twice.

## [0.94.0] - 2026-11-12

### Added

- AI answer share: see mention rate and share of voice from already-stored AI answers, by topic, with a weekly trend and citations of compare pages. This does not start a new run.
- A drill-down report groups a brand's visits, credited conversions, revenue and ad spend by up to five levels at once (channel, country, ad campaign, day and more), with subtotals, profit, ROI and conversion rate, filters, sorting and a CSV export.
- The drill-down report takes custom columns (formulas over its metrics, such as revenue per visit), and its settings can be saved as views: private until shared with the brand's team, with dates that move with today.
- The Tracker has a Campaigns tab: each tracker campaign with its visits, conversions, revenue, profit and ROI for the last 7 days, and an editor for its traffic source, redirect mode, rule-based paths, landers and offers with weights and their share of the clicks, suggested weights you apply with one click, a shared flow instead, and the campaign URL to paste into the ad network.
- The Tracker has Offers, Landers and Flows tabs: offers with a token picker, a missing click id warning, payouts by country, caps with a fallback and a test preview; landers with each call to action's click URL and page script; and flows edited like a campaign's paths.
- A Tracker module with a Report page: visits, conversions, revenue, cost and profit grouped up to five levels deep, with rows to expand or double-click to drill into, filters, saved views, custom columns, a CSV download, and the whole view in a link you can share.

### Changed

- Each brand has a reporting time zone. Attribution and paid ads reports count days in it (with `?timezone=` for one report), and tracker caps and path rules by day or hour follow the brand's midnight. Ad platforms' own numbers stay in their accounts' time zones.
- Insights, dashboards, site analytics, email reports and the digest now count days in the brand's reporting time zone, so "today" and a chart's days are the brand's. Periods stay whole across daylight-saving changes, and `?timezone=` reads any of them in another zone.
- Link orders can now target article and product pages: a new Targets tab suggests pages from stored competitor data, you tick the ones to use, and each order gets a primary anchor from SE Ranking plus an optional second anchor (brand, URL, or another keyword).

### Upgrading

- runs migration 0538.
- runs migration 0541.
- runs migration 0539.

## [0.93.0] - 2026-11-12

### Added

- Business details on About your company: save each brand's legal name, tax or company number and postal address once. Lifecycle email adds the address to its footer (or place it with {{brand_address}}), sequences can use it too, and the brand's help centre, cookie policy and agents show it.
- Broadcasts can go to a saved list, and you can upload a CSV of people (by email or your own user id) into a list from the broadcast editor. A broadcast can also be a service message, which skips marketing choices and the unsubscribe link but never reaches suppressed addresses.
- With a Google Workspace connection, the CRM now also reads enabled members' calendars: meetings with a CRM contact or company appear on its timeline and in the relationship check. Internal and personal meetings are never copied.
- Admins can now read the team's work email into the CRM through their own Google Workspace connection, so nobody has to sign in one by one. The same filters, sharing rules and AI privacy check apply, and it can start from each mailbox's whole history.
- Delete a brand for good: on the brand's page (Company → Brand), choose Delete brand and type its name. Everything in the brand goes with it, and nothing moves to your other brands. Owners and admins only; your only brand can't be deleted.
- The app now has a Link orders page in the SEO section, next to Backlinks, for paid placements from the brand's own donor catalog. Amounts are the donor price only; Approve and Paid are limited to the brand's designated approver, and recording Paid does not send money.
- Link ROI: see the donor price of each published link against already-stored Search Console, ranking and referring-domain changes on the target page, 30, 60 and 90 days around publication. The figures do not show that the link caused a change.
- Organic conversions: see first-touch Organic Search signups and revenue by landing page and search group, from your PostHog events, without counting the same payment twice.
- Paid ads: one click sets up Outflare's conversion feed on a Google Ads account: three conversion actions (sign-up, first payment, repeat payment) sent through Google's Data Manager API with each person's new or returning customer type, a daily-refreshed Customer Match list of paying customers, and the new customer acquisition goal. Google Ads no longer asks for a developer token.
- Paid ads are now scored in cost per new paying customer: set what a new customer and a sign-up may cost each brand, and Outflare ties every sign-up, first payment and later payment to the ad click that brought it, so each campaign, ad group and ad shows its cost per new customer against your target, with new, returning and lifetime revenue beside the network's own numbers.
- Google Ads: create a website conversion and copy its tag, manage account callouts and portfolio Target CPA and Target ROAS strategies, remove campaigns, ads and keywords (you only, after a confirmation), see all conversions, keyword bid estimates and search term match types, pick 30 days or any dates, and download every report as CSV.
- Every CRM card now opens with a relationship check: whether the company is already a customer, has an open deal or sits in another pipeline, which teammate has emailed them for a warm intro, and whether the last conversation went quiet.
- Direct tracking for tracker campaigns: an ad can link straight to your page with the campaign's code, and the site script records the click itself, with the traffic source's click id, cost and fields. outflare.clickId() and {ofclk} in your links pass the click id on to offers, so postbacks still find the click.
- Suspect tracker clicks are flagged: clicks from cloud and hosting networks, an address clicking the same link over and over, and conversions within seconds of their click. Flagged clicks still go on but don't use up your caps. Links show how many there were, and conversions say why they're suspect.
- Tracker links and flows suggest weights for their landers, offers and pages: each one's chance to be the best on revenue or conversion rate, from the last days' clicks without bots or suspect ones. Review the change and apply it in one step; nothing changes on its own.

### Changed

- The API panel's examples are ready to paste: they carry the page's real brand, workspace and item ids, and **Create a token for this** puts a new read-only token for just that call and brand into the example, shown once.
- Site audits now count distinct pages with errors, warnings and notices, so a crawl card is no longer inflated by rules that fire once per language pair. Notices are hidden until you choose to show them, and URL templates that differ only by a locale (such as /de and /fr) are rolled up into one row with a locale breakdown.
- Each brand's catalog (record types, fields, option lists and events), channels, alert settings, record lookup, recipient checks and mailbox senders are now only on the brand's own routes. The old workspace routes answer 410 with the route to call instead.
- New ads are written only from a brand knowledge sheet you have reviewed: check what Outflare read from your website and mark it "Looks right". A read that can't run says which connection to add, the website is read again daily once an ad account is connected, and each draft lists warnings (a price the sheet lacks, a superlative, a competitor's name, too few headlines).
- A brand's logo and main colour now fill in from its own website: when you add a brand or change its website, and when Company → Logo and colours has no logo yet. Outflare uses the logo the site names, else its icon, and never replaces one you set. The Logo field no longer shows an example address.
- Inside a brand, Company → Brand now shows only that brand's own settings, never a list of your other brands. Switch brands, or add a new one, from the brand switcher at the top.
- "All brands" is gone: the app always shows one brand, and its stats, reports, outcomes, attribution and email pages read only that brand. Every brand, archived brands and records in no brand are under Settings → Brands.
- Making a broadcast is now a full page in four steps (kind, email, who gets it, name) with a preview of the email beside it, and it can go to a lead list as well as a saved list or segment. Uploading a CSV of people moved to Lists, New list, with an example file and a check of what was read before anything is saved.
- Consent banner: add `data-reopen="off"` to the tracking script to leave out the round button in the corner, when your site has its own "Cookie settings" link that calls `outflare.showConsent()`.
- Desktop app: the account menu at the bottom of the sidebar now shows which version of the Outflare app you have, with Check for updates. A waiting update still shows as Update available in the sidebar.
- New product event names no longer wait for approval: events are usable straight away in segments, lists, journeys and reports. Outflare checks each new name, with your own AI model when one is connected, and asks only when one looks like a typo of a name you already send, such as sign_up for signed_up.
- Importing leads into a list is simpler: **Import leads** on a list's page uploads a CSV straight into that list, and the import on Leads picks a list from a menu (or makes a new one) instead of typing its name.
- Kits with smart lists, rules and journeys can now be set up for any brand, and each brand gets its own copies. Two brands can also have smart lists with the same name.
- Paid link orders can now start from the brand's already-saved competitor link gap, stay in sync with live-link checks, and suggest equivalent catalog donors when a link is lost. A passing check is what allows pay (the donor price only; no money is sent).
- Paid link orders can now be queried as `coverage_link_orders` and `coverage_donor_profiles` (with `coverage.read`), and the product guide is in the docs. Catalog import is CSV only; a Google Sheet is not imported. SE Ranking does not feed referring-domain candidates.
- Paid ads findings are now judged in cost per new paying customer: rules for budgets, pauses, targets, negatives, keywords, ads and exclusions read Outflare's own sign-ups and payers. Every change waits for you to approve it (in Outflare Cloud, savings can apply on their own with an undo), and shadow mode shows what the rules would do first.
- Paid ads sync every hour and re-read longer windows as the platforms revise their numbers. Outflare alerts you when spend is far off its usual, the month is off pace, a campaign stops serving or brings no sign-ups, ads are disapproved, conversions are refused or tracking breaks, and it keeps Google's own change history beside its changes.
- Playbook automations belong to a brand: each brand sets up, approves and runs its own, and an event automation starts only on its own brand's events. Automations that covered every brand now belong to the first brand. Adding a website visitor's company to the CRM, and the company IP lookups find, stay within the visitor's brand.
- Presence now scans a brand's top 10 competitors every 30 days by default, and the rest every 90 days. Brands still on the previous monthly default for the rest move to 90 days; a cadence you set yourself is left as it is. Change either on the Presence competitors page.
- The workspace routes for stats, reports, outcomes, lifecycle email, email recipients, attribution and install health are retired and answer 410 `route_moved`: call the same route under `/v1/brands/{brand}/`.
- The SEO overview shows each number on its own row with a full chart: values on the y-axis, each week on hover, the period before drawn beside it, and the biggest weekly rise and drop. Pick any dates, from 4 weeks to 2 years, and the API's `period` compares them with the weeks before.
- SEO overview now leads with non-branded clicks (with CTR and position), search conversions, keywords in the top 3 and top 10, AI mention rate, and share of voice versus tracked competitors, all from stored data. Snapshot tiles stay on one SEO connector so the arrows compare like with like, and missing query history is no longer read as zero branded clicks.
- Sidebar pages are now real links: right-click, Ctrl/Cmd-click or middle-click any of them to open it in a new tab.
- In a keyword gap's "who ranks" panel, sites marked "Not a competitor" now have a Track button too, so one click makes any ranking site a competitor.
- Connections with no brand now show what tells whose they are: the account they signed in to, what the provider reports (such as Google Tag Manager's containers and the sites they tag, or Cloudflare's zones), who connected them and when, and a suggested brand you can assign in one click.
- The "Won't be emailed" badge on leads and lists now says why: unsubscribed, bounced, marked as spam, on a blocklist and so on, whether it is the address or its whole domain, and where the entry came from. The API returns the same as `suppression` on each lead.

### Fixed

- AI receipts now only counts real receipts: emails that say receipt, invoice, payment or renewal in the subject, or that show a total. Sign-in alerts and newsletters from AI companies are ignored. A colleague's receipt copied by a Google Workspace rule is no longer credited to the member whose domain let it in.
- The screen where you approve an AI app, and the API token and connected app settings, now include Lifecycle email (templates, broadcasts, journeys and sending) and Coverage. Before, an app like ChatGPT or Claude could never be given these permissions, so its email template and send tools failed. Turn Lifecycle email on for an app you already connected in Connect your AI.
- Campaign links always belong to the brand you are in, so the brand picker on a new campaign is gone, and a few leftover "All brands" and "missions" labels are corrected. The Python SDK's `is_held` now recognises a change that waits for a person on the Agents page. The demo workspace scripts work with brands again.
- Desktop app: email previews (in email templates, sent emails and the help centre) show again instead of a blank box.
- When one person makes two payments of the same amount minutes apart, a copy of the second that arrives before its twin is now kept as its own payment. Before, a guess could join it to the first payment, which already had both copies, and Developer setup reported a payment matched only by a guess.
- Every Outflare tool now works in ChatGPT and other apps that cap how many tools they show. Apps see about 50 core tools, including email templates, send email and broadcasts, plus `find_tools` to search all of Outflare's tools and `call_tool` to run any of them. Connect to `/mcp?tools=all` to list every tool instead. The MCP server now reports the Outflare version it runs.
- Sending transactions or customer events at the same time for the same people no longer fails now and then with a 500 error: a write that loses a database deadlock is retried instead.
- Failed Ahrefs (and other SEO) reads no longer count against the connection's budget, and a refused report is not retried and billed again every hour. Competitor suggestions run weekly with the snapshot, not every day.
- Competitor suggestions are fetched once per weekly cycle: the weekly snapshot still asks the provider, and a second suggest job is no longer queued in the same cycle.
- Email providers and sending domains that belong to no brand are shared, so every brand's Email providers page now lists them, marked Shared, instead of asking you to connect a provider. Disconnecting the default provider makes the next one the default, so sends always have a sender.

### Security

- Security improvements to sign-in sessions.

### Upgrading

- names that were waiting for approval are approved, and their held events are processed within a few minutes of the upgrade.
- this version adds migration 0521, which creates the two Query views.
- workspaces that connected Google Ads before this release connect it again under Connectors, so the sign-in carries the Data Manager permission; the feed says so under setup_needed until then.
- runs migration 0533. Copies of site.js you host yourself need the new version (17) for direct mode.
- runs migration 0534.

## [0.92.0] - 2026-11-11

### Added

- Brands can now manage a paid-link catalog and place link orders through the API (and the MCP tools that call it). Amounts are the donor price only, in USD or EUR, never summed together. Approving an order and marking it paid is limited to the person set as that brand's approver, and paid only after the link is published and a live-link check has passed. There is no commission or markup.
- Rules on redirect links: send the clicks that match a path's conditions (country, device, network, IP range, hour, first or returning visitor, custom fields) to its own pages, in order, with the link's other pages for everyone else.
- Redirect links can send people on with a 307 or a meta refresh that hides the referrer, and a tracking domain can send unknown links to a page of your choice. Destinations, rules and these modes now run only on your own tracking domain; on the shared domain, links go to their own page.
- A conversion log for tracker postbacks and pixels: see every call from the last 30 days with what it sent and why it was or wasn't recorded. Recorded and changed conversions are also webhooks, tracker_conversion.created and tracker_conversion.updated.
- Tracker conversions by pixel and upload: put a pixel or script on a thank-you page when a network can't send postbacks, or upload a network's report as CSV or through the API. Rows with only a transaction id update conversions already recorded, for approvals and reversals reported later.
- Conversions by click id: give each affiliate network a postback URL on your tracking domain, and every conversion it reports is recorded against the redirect click it came from, with its payout and status, even when no person is known. Duplicates, late postbacks and unknown callers are handled per source.
- Tracker flows: save a set of rule-based paths and destinations (pages, offers and landers) once and use it on as many campaign links as you like. An edit to the flow changes every link that uses it, and the flow shows how many do.
- Tracker landers: send clicks to a review or quiz page before your offers. Each lander's buttons link to /click (an offer by weight) or /click/N (a chosen offer), with no cookies. Every lander click is kept and credits its conversion to the offer it led to.
- Tracker offers: give each offer a payout (fixed, by country or from the postback) and a daily cap with a fallback offer, and send link clicks to it. Postback sources can start from 21 affiliate-network templates, such as Everflow, CAKE and ClickBank, so their postback URLs come in the network's own macros.
- Tracker conversions now go back to your traffic sources: a source with a postback URL is told of each conversion on its campaigns. Its rules set the share of the payout sent, a minimum, which types and statuses are sent, and whether changes such as approvals are sent again. Every send is logged.

### Fixed

- Running a partner import again now brings over a partner's new email from Tolt. The new address replaces the old one when that was the only, unverified email on their record, and is added beside a verified one.

### Upgrading

- runs migration 0512.
- runs migration 0513.
- runs migration 0515.
- runs migration 0514.
- runs migration 0518.
- runs migration 0517.
- runs migration 0516.
- runs migration 0519.

## [0.91.0] - 2026-11-10

### Added

- Redirect links can send their clicks to several pages: give each a weight for its share and an optional daily click cap, and once every page has had its clicks for the day, the rest go to the link's own page. Link scanners don't count against a cap.

### Upgrading

- runs migration 0511.

## [0.90.0] - 2026-11-10

### Added

- Team email sharing: an owner can let everyone on a brand read each other's work email with CRM contacts. Everyone is told 48 hours first, your own AI model keeps HR, pay and other private email back, anyone can keep a contact private, Personal or HR-labelled and payroll or banking mail is never saved, and each read of a colleague's email is recorded.
- Traffic sources for redirect links: add the network you buy clicks from (25 templates, such as Taboola, MGID and PropellerAds), and each link shows the address to paste into it. Clicks keep the network's click id, the cost and up to 20 custom fields, and a link's page can carry tokens such as `{click_id}` or `{field1}`.

### Changed

- Gmail capture can now save email with every CRM contact and company on a brand, not only the ones each person picked. An owner switches it on; an email is saved only when everyone outside the company in it belongs to one contact or company, and newsletters are skipped.
- Partner short links now keep the same details as redirect links: where the visitor is, their device, and a keyed hash of their address. Clicks from link scanners and previews no longer count toward a partner's clicks. The click id a link adds (`ref_click`) is now a 16-character key; ids from older links still work as `click_id`.
- Redirect links answer faster and take far more clicks: each redirect no longer waits on the database, and clicks are written in batches a moment later. A server takes about a million clicks a day per workspace. Each click also keeps a keyed hash of the visitor's address, never the address itself, so repeat clicks can be told apart.

### Upgrading

- runs migration 0509.
- runs migration 0508.
- runs migration 0510.

## [0.89.0] - 2026-11-09

### Added

- A team directory in Settings, Team: everyone at the company with their department, team and manager, plus the work accounts the company owns and who holds each. Company Telegram accounts sign in to Outflare as a linked device by QR code, with checks every six hours.
- Company Telegram chats sync into the CRM, read only. Each is matched to a customer (your own AI suggests, the account's holder confirms in CRM, Telegram), customers waiting four hours alert the holder, and your AI can summarise a chat or an account's chats when it changes hands.
- Website visits and redirect link clicks now keep the visitor's region, city and network (internet provider or mobile carrier), their device's maker and model, their system and browser versions, and their language. Break visits down by any of them in Insights, or read them in Query.

### Fixed

- The changes from v0.88.0, which didn't pass the release checks, are now published.
- A Google Workspace rule that copies AI receipts from everyone's inbox now works for the whole company. Receipts sent to any colleague at your company's domain count, not only those sent to Outflare members.

### Upgrading

- runs migration 0504. Each server that answers visitors downloads DB-IP's free City Lite and ASN Lite files (about 65 MB, monthly, no key) from download.db-ip.com into OUTFLARE_GEOIP_DIR; set OUTFLARE_GEOIP_DOWNLOAD=off on a server without internet access and copy the files in yourself.

## [0.88.0] - 2026-11-09

### Added

- Outflare for Mac, in testing: the Outflare you use in the browser, in a window of its own, in the dock, signed in once from your browser, with system notifications and a badge. Test builds are unsigned for now; the download page follows.
- Check any domain's SPF, DKIM, DMARC, MX and domain blocklist standing without an account, with the exact DNS record to add for each problem. It powers the free checkers on outflare.ai and is open to anyone at `GET /v1/tools/domain-check`.
- Each partner now has their own page, with their numbers, referred customers, conversions, rewards, payouts and history in one place. Searching and sorting the partners list now covers every partner in the program, not only the newest 100, so you can find an older or imported partner by code, email, name or id.
- Each partner reward now has a proof trail from the click or visit to the sale, rule, approval and payout, and you can download it for a payout or a partner. The Rewards and Conversions lists show and search the partner, customer and sale, and bulk approve leaves out rewards your old platform still pays.
- Partnership pipelines: Presence's Partnerships tab has a board each for SEO links, sponsorships, integrations, affiliates and comparison sites. Add a partner by website; when one goes live it becomes a tracked placement, and affiliates move on their own when they join your partner program and make their first sale.

### Changed

- The Agent page's top row is shorter: Chat, Waiting for you, History and Settings. History holds every agent's changes and Outflare agent runs; Settings holds what agents may do. Each picks its view from a row of buttons.
- AI accounts now shows the workspace's receipt email address at the top, with a copy button. Outflare makes it by itself the first time anyone opens the page, and admins can press Generate email address if that fails.
- The AI receipts forwarding steps now cover receipts from claude.ai and chatgpt.com, drop GitHub, and say that receipts sent from a stripe.com address need the Gmail filter.
- Paid ads now belong to one brand each, under `/v1/brands/{brand}/`, and the old workspace routes answer 410 with the route to call instead. The Ads page asks you to pick a brand; connecting ad accounts and assigning them to brands stay on the workspace.
- Approvals, agent runs and API logs each have brand routes under `/v1/brands/{brand}/` that show only that brand's items. The workspace routes still work for now; next they will keep only the team's own items, such as invites and connections.
- Knowledge, memories, help centres and the support inbox (conversations, the assistant, SLAs, chat widgets and handoff topics) now belong to one brand each, under `/v1/brands/{brand}/`. The old workspace routes answer 410 and name the route to call instead. Their pages ask you to pick a brand. Support mailboxes and the workspace's assistant and SLA defaults stay on the workspace.
- Ideal customer profiles, senders, inbox tags and saved inbox views now belong to one brand each, under `/v1/brands/{brand}/`, and the old workspace routes answer 410. A profile shared by every brand is now copied into each brand, and two brands can use the same profile name.
- Outflare for Mac updates itself: when a newer version is ready, an Update button appears at the bottom of the sidebar. One click installs it and reopens the app; nothing else to do.
- Presence's Found list marks sites whose links to you look like spam (Possible spam, Likely spam), using your SEO tool's own spam judgement such as Ahrefs' and Outflare's link checks, with the reasons on hover. Nothing is hidden until you choose Spam site.
- A partner portal's link can now be readable, such as `/p/northpeak/`, instead of the program's id. Program settings → Portal suggests an address from the program's name; links with the id keep working. The Portal tab can also put the portal on your own domain, such as partners.example.com, with one DNS record.
- Partner program settings are now a full page instead of a side panel, in tabs (General, Payouts, Tracking, Fraud, Portal, Import), with plain on/off switches and one Save button that appears when something changed. The Partners page also shows whether the portal is open.
- Program settings → General has a "Review each application" switch, so a program can approve each new partner by hand or accept everyone who applies. Every new program, referral programs included, reviews each application by default.
- New SEO partnership prospects get the site's domain rating filled in from what your SEO tool already read (backlinks and competitor scans), so you can see who's worth pitching first.
- Presence opens on Found, and the Overview tab is gone (Reports has the same numbers). Found shows every page as its own row with a link that opens it in a new tab, says what the page shows in its own column, and asks "Yes, track it" or "No, hide it" with a line explaining what each does.
- Presence's Found list can mark a site as spam (its pages leave the list, and pages found on it later stay hidden) or check a page again in a week, for a site that's offline. Follow-ups, formerly the Work queue tab, are now a view on the Found page.
- Reviews: SourceForge is now a review site. Outflare finds each brand's SourceForge listing for you to confirm, alongside Trustpilot, G2 and Capterra, then reads its rating and reviews every day.
- Approvals, agent runs and API logs now show only the picked brand's on the Agents and Logs pages. The team's own, about no brand (such as invites, members and connections), are on a new Settings page, Team activity, and on the workspace routes, which no longer show a brand's.
- Test events you send to a webhook are marked "Test" in its delivery list, with the receiver's status code, so you can show that the endpoint works. A failed test no longer counts against the endpoint. It doesn't start a failure streak, raise the error rate or turn the endpoint off on a 410.

### Fixed

- If Gmail's forwarding confirmation reaches your receipts address before you link the Gmail address in Outflare, linking it now shows the Confirm in Gmail button. You no longer have to ask Gmail to send it again.
- A web search an agent asks for now belongs to the brand it searches for: `POST /v1/brands/{brand}/search-results` (MCP `search_web` takes `brand`) replaces the workspace route, so the approval waits under that brand alone on the Agents page, and its cost and event carry the brand. A change that names no brand says "Every brand" on its card when a brand is picked.
- Outcomes counts only outreach costs. Ad spend, searches, rank checks, SEO reports and site crawls stay out of cost per meeting. Searches and SEO reports now count toward the brand they ran for. The list of charges shows only the brand you're looking at. Costs that belong to no single sequence are no longer described as shared out when no emails went out.
- After an update, an open tab or a page your browser kept from before no longer shows a blank screen. The app and the partner portal reload once and open the new version.
- Reviews: SourceForge reviews are now kept with their date, title and text, not only the listing's score and count.

## [0.87.0] - 2026-11-07

### Added

- Site audits now check whether AI answers can use your pages: quoting blocked (nosnippet, short max-snippet, Bing noarchive), questions with no direct answer, articles without an author or date or not updated in two years, and structured data that doesn't match the page. Googlebot, Bingbot and more AI crawlers join the AI crawler list.
- AI visibility has a new Your pages tab: whether AI assistants can read, quote and trust your own pages, what to fix, and the pages AI answers already cite first.
- Your pages in AI visibility can check pages with your own AI model: it reads a page the way an AI answer does and says whether it answers up front, which claims are unclear, and what to change, with a score out of 100. Check competitors' cited pages the same way to compare.
- Page speed: your Outflare tag now measures how fast every page loads, responds to clicks and settles for real visitors, and Analytics › Page speed shows it per page with the pages that got slower. Analytics › Right now shows who is on your website this minute, where they came from and what they are doing.

### Changed

- A/B tests: the visual editor has a bar with a version switcher, undo and redo, and a saved status, plus tabs for the page's parts and the version's changes. Double-click text to type on the page, drag a part to move it, and ask your AI model to change the page or the part you picked, then keep or undo what it wrote.
- A/B tests: describe a change to a no-code test's page, such as "add a line about premium plans and a Try it button under the header", and your AI model writes real changes to that page for you to keep. A run that takes too long now stops with a clear reason, and you can stop one yourself.
- With a brand agent, the Agents page opens straight to its chat. Waiting changes, history, what agents may do and API tokens are in one "Approvals and history" menu instead of a row of tabs.
- The Agent page shows its parts as a row of tabs at the top (Chat, Waiting for you, Everything changed, What agents may do, Runs) instead of hiding them in an "Approvals and history" menu.
- The sidebar shows how many agent changes wait for your yes or no next to Agents, for the brand you're in, so you see it from any page. The workspace stats (`GET /v1/workspaces/{ws}/stats`) return the same number as `actions_waiting`.
- The AI visibility Sources table now shows each page's address first and its site next, and leaves out ChatGPT's own image links. Click a page to see when answers first cited it, the site's ratings, the links to it, the questions it answers, and whether it links to or only names you and each competitor.
- Brands that AI answers name next to you are matched to their websites by themselves. Ones already on your competitor list are recognised by name, and the rest go to the Competitors page to say Yes or No, asking "Is this their website?" when the match isn't certain.
- AI visibility uses the Competitors page as its one competitor list. Brands that AI answers name next to you wait there for a Yes or No, a competitor added in the AI visibility setup goes there too, and one you said No to stops being suggested.
- AI visibility now sorts the brands AI answers name: a product of a listed competitor ("Zyte API") counts as that competitor, tools that aren't competitors go under "Named, not competitors", and every name it isn't sure of shows its best guess at the website. Each name is sorted once and remembered.
- Brands no longer share any people, companies, leads, lists, email topics, sequences, support conversations or websites: older items saved with no brand now belong to the first brand, and news topics and fix guidelines are copied into every brand.
- A sending domain, mailbox, lead list or sequence can no longer be taken out of a brand to be shared: move it by adding it to the brand it belongs to.
- SEO sites, audits, keywords, competitors, backlinks, fixes and deploys now belong to one brand each, under `/v1/brands/{brand}/`. The old workspace routes answer 410 and name the route to call instead. The SEO overview shows one brand, or each brand in turn when all brands are picked.
- Website tracking keys, visits, customer events, webhooks and support addresses saved before they had a brand now belong to one brand, so no brand sees another's.
- Social, Reddit campaigns, stories, story pitches, PR experts, journalists and coverage now belong to one brand each, under `/v1/brands/{brand}/`. The old workspace routes answer 410 and name the route to call instead. Their pages ask you to pick a brand.
- AI visibility, link building, news watch and reviews now belong to one brand each, under `/v1/brands/{brand}/`. The old workspace routes answer 410 and name the route to call instead.
- A competitor's placements and Presence's Where competitors appear now list the most valuable links first, not the biggest sites. Each shows how it's got (can't buy, earned, paid or free), a 0 to 100 value with reasons, and a filter.
- Each competitor is now read at most once a month by default: search data on an SEO connection (weekly snapshot, Refresh all competitors or one competitor's Read) and Presence scans, top competitors included. Every brand was moved to this default. Change it in the SEO tool's settings or on the Presence competitors page.
- Developer setup now shows when payments sent from your server can't be matched for certain: orders without an order_id, payments joined by a guess or with different amounts both ways, ids reused for renewals, retries under new ids, amounts sent in dollars instead of cents, and payments going quiet. Payments saved twice before can be merged in one click.
- Presence Opportunities suggests the English version of a page instead of its translations, so a site with /fr/ and /de/ copies shows up once, on its English page.
- Presence Opportunities shows how a link on each page is got (can't buy, earned, paid or free to get), and you can filter by it.
- Presence is now its own item in the menu. Found and Opportunities are tables you can sort by domain rating, traffic, AI citations or competitors, filter, and scroll through without pages. Opportunities can show one row per site, and both can hide your competitors' own sites.

### Fixed

- A/B tests: changes to a page that builds itself in the browser (a React or Next.js site) no longer vanish in the visual editor, preview links always show their version (also while the test is a draft), and when you describe a change to a version your AI model sees what that version already added and can change it too.
- Refresh all competitors now reads search numbers even right after Ahrefs refused one report your plan doesn't include, which used to mark the whole connection broken. The page also says how many competitors have their search numbers, domain age and site read so far.
- AI visibility counts every competitor on the brand's list, however long, so the setup and every run include them all; before, the list stopped at 25.
- Every tracked competitor now gets its search numbers (Domain Rating, keywords, visits, linking sites) each week, not just the first 50, within your SEO connection's budget. A competitor your provider can't read no longer stops the rest, and domain ages that didn't load are tried again within hours.
- The indexability report lists every page in a site's sitemaps, including those past the crawl's page limit, and shows each page's full address as a link that opens it.
- When your product sends a verified email that only one record holds unverified, such as an import, a lead or an unsubscribe with no user id or other email of its own, that record is now merged into the person instead of being listed as a possible duplicate. Its consent and opt-outs come with it.
- A payment or refund sent as an order event and as a transaction at the same moment is now kept once, with its person. Before, it could show twice on the Transactions page, and Developer setup could warn about payments with no person.

## [0.86.0] - 2026-11-06

### Added

- How AI describes you lists what the answers say your brand is known for and what they hold against you, for you and each competitor, with where it comes from. Mark a claim as not true and it goes on the fixes list with the pages to correct.
- Keep one AI visibility question set per market, asked and drafted in that market's language and place, and switch markets at the top of every page.
- AI visibility is its own menu: a guided three-minute setup (your brand, competitors, drafted questions, assistants and the cost per run), an Overview that says whether you're winning in AI answers and what to do next, and pages for questions, competitors, sources, how AI describes you, traffic and settings.
- Sources ranks the pages AI answers cite or read by how much being named on each is worth, with the competitors named there; each one is a Presence opportunity. Brands the answers name that you don't track are suggested on the brand's competitor list, with one click to track or dismiss.
- AI visibility questions now say how much the person asking knows (the problem, the kind of solution, a competitor or your brand) and what they ask for (how do I, how do I fix, who's best, alternatives, X vs Y, best supplier, about you), with persona, topic and monthly demand. Your own AI model can draft questions from your brand, competitors and Search Console searches for you to keep or drop.
- AI visibility has a score from 0 to 100 for your brand and each competitor, weighted by how many people ask each question, with a range that shows how much it can wobble and which weeks were a real change. A grid shows the score for each awareness level and question type.
- AI visibility can now ask ChatGPT and Gemini on their own screens, as a signed-out person in your cohort's country sees them, through your own DataForSEO account (about US$0.004 an answer). Each answer shows the searches the assistant ran, every page it read and whether it cited it, and every brand it named.
- Share a brand's AI visibility by link, with a password and expiry if you like, for anyone to see read-only; download the questions and placements as CSV; and get the score, the questions gained and lost and new objections in the weekly digest.
- AI visibility now says whether each answer recommends your brand and each competitor, only lists it, or warns against it, with the answer's words, and what kind of sites the answers cite. Brands the answers name that you don't count yet show up as suggested competitors you can add in one click.
- A brand's website is read for what it offers when you add the brand, through your own web data connection and AI model: a summary, the industry and bullets for products, services, features, integrations, capabilities, who it serves and use cases, each linked to its page, on the brand's page for you to edit. AI visibility drafts its questions from it.
- Competitors now tracks prices. Name one pricing page for your brand and each competitor, and Outflare reads it every week on your own web data account, shows the plans and prices side by side, and keeps every change: a new or dropped plan, a price up or down. Prices are read off the page itself, with no AI, and only that one page of each site is read.
- Connect SE Ranking with your own Data API key to read the keywords a domain ranks for, with position, search volume and ranking page, plus its organic overview and competitors in search. Calls count against the connection's monthly budget like Ahrefs and DataForSEO.
- Keywords you already track in an SE Ranking project now come into Rankings by themselves each day, with SE Ranking's daily positions and the page that ranked, so Outflare never pays to check them again. Connect SE Ranking and the project for your site's host is picked up; the first import brings the last 90 days.

### Changed

- Search insights: the AI referrals report now works without GA4. When no GA4 landing-page outcomes are synced, it counts visits from ChatGPT, Perplexity, Claude and other assistants in the referrers of your PostHog, Plausible or Matomo connection, for the whole site.
- Drafting AI visibility questions with a DataForSEO connection now starts from the real questions people ask ChatGPT and Google's AI Overviews about your brand and competitors, and weights each question by how many times a month people ask AI assistants about it.
- AI visibility counts, once a day on your own DataForSEO account, how many of the real AI answers DataForSEO keeps mention your brand and each competitor, with your share and the mentions gained or lost since the day before.
- The Overview's trend has a Compare by switch: your score week by week against each competitor, by assistant, by market, or by the questions' awareness level, type, topic or persona.
- AI visibility puts the questions where AI answers name your brand but recommend a competitor on the fixes list, with what to do, and confirms the fix once answers recommend you.
- Pages that AI answers to your questions cite, without naming you, now appear in Presence opportunities, scored by how often they are cited and how many competitors those answers name. Pages the assistants read but don't cite are listed too.
- Each tracked question shows whether AI answers name you, how often they recommend you, the change since the period before and the last weeks as bars, so you can see traction after working on a placement. Sort by the questions not naming you, biggest drops or biggest gains; the Overview lists what you gained and lost.
- AI visibility shows what AI answers bring your site: visits from AI assistants, the conversions and revenue credited to them, which assistants sent them and the pages they landed on, next to what you spent.
- Each brand gets its own daily or weekly digest, with its own schedule and recipients, and every number in it counts only that brand. Two brands can also use the same partner program name.
- Every mailbox and sending domain belongs to one brand and sends only for that brand's sequences. Mailboxes that were shared moved to the brand whose sequence they send for (or their domain's, or the workspace's only brand); any left with no brand are listed under Company → Brands to add to one, and send for no brand until then.
- About your company is now each brand's own: every brand has its own description, goals, voice, rules, notes, logo and colours, and agents read only the brand they work for. Each brand starts from a copy of what the company had.
- Each brand has its own knowledge and memories. Items saved with no brand became each brand's own copy, so every brand still has them, and new knowledge and memories always belong to one brand.
- Each brand has its own support assistant settings, reply time targets, hand-off topics, chat widget, help centre and alert settings. Settings saved for the whole workspace became each brand's own copy, so nothing changes until you edit one brand.
- Partner and promo codes are unique per brand, so two brands can each give a partner the same code. A program can't move into a brand that already uses its slug or one of its codes; the move says so instead.
- Brands are fully separate: the old workspace routes for a brand's data answer 410 `route_moved` with the `/v1/brands/{brand}/` route to call instead. Install health names any token or app still calling an old route, and the SDKs, MCP server and web app use the brand routes.
- Support conversations take the brand of the mailbox they came to (or the workspace's only brand), so each brand's inbox shows its own customers. A partner link on a brand's tracking domain or portal, and a conversion reported on a brand's route, only match that brand's partner and promo codes.
- Changes waiting for your OK on the Agents page lead with the yes/no question the agent asks, then say which page or site it is about and why. Without a question they say plainly what the change does ("Read who links to a page or domain"). Ids and rule names are gone from the card, and the exact request is one click away.
- Competitors → Overview has a Refresh all competitors button that reads every tracked competitor now on your own connections, and shows each one's Domain Rating, keywords, domain age and sitemap pages.
- A competitor's page now lists all their placements, keywords beside yours, top pages and linking sites, with **Read their search data** to read any competitor on your own SEO connection. Click a link gap to see the exact pages linking to each competitor.
- Competitors suggests more competitors every day from who ranks for your keyword gaps (read on your own SERP connection), and **Find more competitors** reads a batch now. Click a keyword gap to see everyone who ranks for it, with Yes, No or Track beside each site.
- Competitors: Yes and No now sit right beside each suggested site, and the daily look asks your SEO connection for 50 competitors instead of 10 (and up to 25 from your rankings), so the list keeps growing. Up to 50 tracked competitors a site get their weekly numbers.
- Platforms like GitHub, YouTube and Reddit are no longer suggested as competitors, and unless you add one by hand, your SEO connection's budget is never spent reading them.
- Site crawls now tell links in the text apart from menu, sidebar, header and footer links. Each crawled page shows how many pages link to it from their content, and each link says whether it is boilerplate, so you can see which pages your own writing backs, not only which ones every menu repeats.
- PostHog: leave out your project's internal and test users (bots, your team, a crawler with a fake referrer) in Outflare's reports. Tick it for a site on the Analytics page; every report, the daily sync and conversions by landing page then use PostHog's own filters, and the last 90 days are read again.
- Presence has a Found tab: every page that links to or mentions your brand, grouped by site. Each page is read once to check it links to you or names your website, so same-name pages about something else stay apart. About us or Not us settles a page or a whole site in one click, and the Overview counts everything found.

### Fixed

- No-code A/B tests now count every visitor on the test's page, including those who see the original, so a running test no longer shows 0 visitors. A page test can't start until at least one new version changes the page.
- A keyword's results page no longer shows Google redirect links (google.com/goto): every search resolves them to the real page, keyword gaps are read on your web data connection first when it's connected, and pages read before are read again.
- Presence and rank tracking no longer show Google's own redirect links (google.com/goto?url=...) for search results: each result is resolved to the page it leads to, with its real site, and existing finds are fixed on the next daily run. The Presence report now also counts every page and site found.
- Presence: competitor scans no longer slow the app down. Each brand's daily scans start a little apart, and the brand's opportunities are refreshed once every few minutes for all the scans that finished, instead of by each scan at the same time.

## [0.85.0] - 2026-11-05

### Changed

- Two brands in one workspace can now use the same campaign slug, email topic key, template handle, lead email address and lead list name. Each stays unique within its brand.
- Each brand has its own attribution, ad consent and visitor settings, under `/v1/brands/{brand}/.../settings`, and two brands of one workspace can use the same flag key, exclusion group, conversion key or attribution model key. The workspace settings routes are deprecated and act on the first brand.
- Every partner program belongs to one brand, and its partners, links, conversions, rewards and payouts are that brand's alone. A brand's backend reports conversions to its own brand route, which credits only that brand's partners.
- Each brand has its own AI visibility settings (models, samples and monthly limit) and link building settings (pitch cool-down, weekly reads and their budget share). A second brand's opt-out of an address no longer applies to every brand.
- Email topics belong to one brand: each brand lists and uses only its own, and another brand's topic is not found. Topics that were shared by every brand moved to the workspace's first brand. Partner groups and reward rules now carry their program's brand and move with it.

### Fixed

- The changes from v0.84.0, which didn't pass the release checks, are now published.

## [0.84.0] - 2026-11-05

### Added

- Follow competitors' social profiles and review pages: Outflare suggests the ones their website links to, you say yes or no, and their followers, rating and review count are read daily on your own web data account and added to the ranking.
- Competitors is now its own menu. Overview ranks your brand against every competitor you track with a score out of 100, shows where you lead or trail and whether you're gaining share, and says when you pass the one ahead. Each competitor has its own page.
- A GitHub Action for sites kept in a repository: it opens a pull request for each page approved in Outflare and reports the page live once merged, from your own repository and token.
- People has a Developer setup tab per brand: a live checklist of sign-ups, orders, payments, refunds and website/server agreement, each with what to send to fix it, and refunds whose payment is unknown to link by hand.
- Funnels: follow people from an ad click or website visit through product events and conversions to a first payment, with a time limit on any step, and see where they drop off, broken down by campaign or source, over time, and the people behind every step. Save funnels for the team, and agents can run them too.
- A Lifecycle view on the Retention page shows, per day, week or month, how many active people were new, returning or resurrected and how many went dormant. Funnels can run in any order, what converters did differently now includes people's properties, and trend tiles on dashboards carry the brand's notes.
- Funnels now take exclusion steps and run by company, save a step's people or drop-off as a list, and show what converters did differently. A new Paths insight draws where people went step by step, and annotations mark a day on every chart.
- Share a dashboard by link, with a password if you like: anyone with it sees the dashboard read-only and live, and you can take the link back any time. Set an alert on a saved trend (above or below a value, or up or down by a percentage) and hear once when it crosses, like every other alert. Put a dashboard's trends in the weekly digest.
- On Google search campaigns, Paid ads suggests keywords to add (searches that convert), negative keywords (searches that cost money and never convert), and keywords worth an ad group of their own, and shows Google Ads' own recommendations for an account. Each waits for your approval and is measured like other suggestions.
- Paid ads opens at three levels: every network, one network, or one ad account, picked from a trail at the top of the page, and every tab narrows to it. A new **Changes** tab lists every pause, resume and budget change at that level and whether the platform shows it.
- The brand agent can write new ads, ad groups and Google search campaigns as drafts. You approve one under Paid ads, Suggestions, it is made on Google Ads or Meta paused so you can check it, and you turn it on. Nothing spends until you do.
- Paid ads suggests changes to your campaigns: raise or lower a daily budget, pause a campaign that spends without converting, or resume one that did well, each with why. Nothing changes until you approve it; approved changes are measured long enough to judge and marked Worked, Didn't work or Not sure.
- Change how a Google Ads campaign bids, and add or remove audiences and countries on Google Ads and Meta campaigns, from the campaign's page. Paid ads also suggests a target cost per conversion for Google campaigns with enough conversions.
- Paid ads has a Tests tab: try a new target cost per conversion on a Google Ads campaign, or a new daily budget on a Meta campaign, on part of its traffic, see which side did better, then keep it or roll it back.
- Presence has a Competitors tab showing where the competitors you track appear, with each site's domain rating and traffic, "Likely paid" only when there's a reason, and the sites you're not on yet. Top competitors are scanned weekly and the rest monthly, on your own search and SEO connections; change either on the tab.
- Presence now says whether each live placement has taken off, from AI answers that cite it and visits it sends, and the coverage report shows momentum: placements gained and lost in the last 28 days against the 28 before, twelve weeks of trend, and share of voice against tracked competitors.
- Presence has an Opportunities tab: the places worth your team's effort, ranked by SEO and AI answers together, such as pages your competitors are on and you aren't, articles that name you without a link, and links that were removed. Each says what to do next; pick, dismiss or mark one won, and it is marked won by itself once you get there.
- Website analytics now shows what Outflare's own tracking tag counted: visitors, visits, page views, bounce rate and visit length by day, with top pages, sources, websites, countries and devices. It works with no analytics tool connected, and sits next to GA4 or PostHog when you have one.
- Trends chart any number over time, such as visits, sign-ups, revenue or ad spend, with breakdowns, formulas like cost per sign-up and the period before. Retention shows how many people came back in each week or month after their first. Dashboards put saved funnels, trends and notes on one page, and Paid ads, Visitors and Home carry their own panels. Agents can do all of this too.
- Visitors now lists everyone on your site, anonymous visitors included, and opens each one's journey: visits, pages and events. A person's timeline shows when and how they went from anonymous to known, visitor links join a phone where they never sign in, and Website tracking walks you through serving the script from your own domain and checks it works.

### Changed

- Missions leaves the sidebar now that agent runs replaced it; old links still open its history. The Agents page has a slot for a brand's own agent, which opens first where the server provides one, and G then A opens Agents.
- Agents cost much less on Claude: each step of a turn now reads the conversation so far from Anthropic's prompt cache, at a tenth of the price, instead of paying full price for it again.
- The brand's agent is part of the dashboard: an Agent button in the top bar (or Ctrl+J) opens a chat with it beside any page, and Home shows what it is doing and what waits for you, on servers that add one.
- Each brand has its own conversion definitions and attribution models. Attribution credits a brand's conversions with its own definitions, models and visits only, and a refund only nets against a payment in the same brand. Conversion definitions and attribution models have brand routes; the workspace routes are deprecated.
- Each brand has its own event catalog: two brands in one workspace can send events with the same name, such as `order_completed`, and each brand approves and defines its own. Events from a second brand are no longer held because another brand uses the name.
- Flags, conversion definitions and attribution models are found by key within their brand, and an ad conversion rule uses its ad account's brand's conversion definitions. Visitor, ad and attribution settings are read for the brand, ready for each brand to have its own.
- Find links shows a spinner and a "Reading the website" note while it works, and hides the last read's error until the new read is done.
- Brands keep their tracking apart: exclusion groups and their holdouts, holdout reports, ad conversion rules and the conversions sent to ad networks now show and count only the selected brand's data.
- Competitors are one list per brand with no limit. New ones are suggested every day, from your SEO connection and from sites ranking beside you, and you say yes or no with one click. Competitors you add in AI visibility join the list.
- A conversion sent only from your server, for someone your website never saw, now takes its country from the event itself (`context.location.country` or `context.ip`), so it is no longer skipped as "country unknown".
- Each payment counts once, whether it arrives as a transaction from the API, an Order Completed event with revenue or a billing connector's charge, matched on the order id (or the same person, amount and currency within 15 minutes). Transactions also take your own `user_id`.
- Paid ads: Fix tracking links shows its progress while it runs, then how many URL suffixes it changed and each one Google refused with its reason, or why it failed. Also by API and with the `get_ad_tracking_run` agent tool.
- Run an A/B test on any page without a developer: New test → Test a page, then change each version in a bigger visual editor (styles, moves, new buttons, split URLs). Funnels and trends can split by test version, and an agent's changes always wait for a person.
- Send a conversion such as a sign-up from both the website and your server with the same event id, and Outflare sends it to ad platforms once. The website script's `outflare.track()` now takes your own id as `messageId`.
- Conversions sent from both the website and your server count once even when the two copies don't share a `messageId`: they are matched on the event name, the person and the time (within 15 minutes), and the server's copy is sent. Paid ads → Tracking health warns when this happens, with the one-line fix.
- Your own server's events can be kept under one brand of your events workspace, the one whose website tag is on your site, so each person who uses Outflare joins the visits they made before signing in, even if they signed in long ago.
- Partner flows now say which sales they pay on, such as "10% on the first sale, within 1 year of sign-up", instead of "on every sale" when their conditions narrow it.
- A brand's partner program can take its sales from the payments you already send Outflare (the transactions API or Order Completed events): turn on "Also take this brand's payments and refunds sent to Outflare" in the program's settings, and each payment credits the partner once, with no extra call from your backend.
- Partner programs read the events and payments you already send: a sign-up or an event a reward rule names credits the partner, sales come from transactions, and the partner API counts the same customer once.
- Presence now finds where your brand appears every day by itself, from news, backlinks, social watches, pages AI answers cite and new web searches on your own DataForSEO or web data connection. Each find gets its type from its address, such as a YouTube video, forum thread or MCP directory.
- Presence watches each placement for a link to your brand and a mention of it, and shows "Mention removed" or "Offline since" once two checks agree. Placements checked against Knowledge are checked again soon after your published Knowledge changes.
- Coverage is now Presence: tag placements as influencer posts, YouTube videos, forum threads, MCP directories or app store listings, mark them organic (free, unprompted), record who delivered them (in-house, an agency or a partner), link each one to its CRM deal, and keep the agencies, partners and creators involved as CRM records on it.
- Refunds work like payments: send one as a transaction, an Order Refunded event, or both, and it counts once. A refund can name its payment by its order id, its order's messageId or Outflare's id, and one that arrives before its payment counts when the payment comes.
- Refunds reach Google Ads: a partial refund lowers the conversion's value and a full refund retracts it, so bidding learns from net revenue. An order's website and server copies with the same order_id now count as one conversion, without a shared messageId.
- Outflare tracking on Website analytics now shows which website each page is on, with a picker to see one website (such as your app's subdomain) on its own. Visitors opens on Everyone, so anonymous visitors show first.
- Your own server's users are joined to their earlier website visits as soon as they open any signed-in page, without needing to save or change anything first.

### Fixed

- An API token kept to one brand, such as a brand's agent, can now read the link building settings and the brand's SEO connections instead of being refused.
- Journeys, rules and lifecycle stages react to an event only when the event's own brand has approved its name. Approving a name in one brand no longer lets it drive automations in another.
- Find links waits long enough for a brand website behind a bot check: the second read runs as a web data job for up to a few minutes, and a read that still doesn't get through is shown as blocked or too slow instead of an outage of your web data connection.
- Browser tasks that can't start the browser now stop within their time limit with a clear error, instead of waiting for good and holding a browser worker.
- Connected AI apps no longer get "the MCP server is starting" errors during updates and restarts, and leads or unsubscribes that weren't linked to their person records because of an update are now linked.
- One-click DNS approval: a DNS host that fails to answer once is asked again, and a domain whose DNS host doesn't support one-click approval now says so instead of showing a lookup error.
- A mailbox whose mail server keeps refusing its sign-in now asks you to connect it again after 5 failed syncs in a row, instead of failing quietly every two minutes. A mailbox that keeps failing is tried less often, and a server that is only busy no longer counts against it.
- Inbox sync handles bursts of new mail on slow mail servers: it reads new messages a few at a time and keeps what it has read, so replies and bounces no longer arrive late when one sync runs out of time.
- When the app can't reach Outflare (offline, a dropped connection), pages now say so instead of loading forever, and the SEO overview keeps checking back on a first SEO read. A brand's link search that fails because the page reader is out of credits or down now says which.
- A review page the page reader hasn't finished reading after an hour now shows as a slow page and is tried again later the same day, instead of being sent again every hour. When X, LinkedIn or the page reader is down, each account tries again on its own schedule instead of the whole read repeating several times over.
- Reviews: a review page your web data connection can't read (the site fails, or puts it behind a login) now shows why on the page and is tried again later the same day, instead of being retried every hour as an outage of your web data connection or left for a day as a wrong address.
- The database no longer grows with every tracked website visit: finished background work that runs once per batch of events is now cleaned up after 14 days like other finished work, and the events workspace keeps successful API and MCP calls once an hour per person instead of every one.
- Opt-out syncing with EmailBison, Instantly and Smartlead keeps going when reading their blocklist fails: your opt-outs still go out, their list is read every 6 hours instead of every few minutes, and a failed try waits 30 minutes. Website tracking no longer answers with an error when a company lookup for a visitor fails.
- Tolt imports no longer stop over one object Outflare can't store, or over a Tolt page that fails: the object is listed under issues and the rest come over, a failing page is asked for again in smaller pieces, and the import waits longer between attempts while Tolt is down.
- The Tolt import now brings over flows with a "Lead date" condition, such as "first year after sign-up", and reads condition operators however Tolt words them, instead of skipping those flows.
- The notice that some connections or accounts have no brand shows once at the top of the page again. Moving between pages could add another copy each time, pushing the page down.

### Upgrading

- set `OUTFLARE_EVENTS_BRAND` to that brand's id or handle, next to `OUTFLARE_EVENTS_WORKSPACE`.

## [0.83.0] - 2026-11-04

### Added

- Eight new playbooks, one for each channel that had none: test your best page, get listed where buyers look, help in communities, send a monthly newsletter, move ad budget to what works, post what the brand learned, answer Reddit questions, and answer every review. Every change they make waits for a person or stays within the run's grants.
- Approved posts and pages can now go to any website through a webhook, so your own code, automation tool or coding agent can put them in place. The webhook sends the page in Markdown and HTML, plus where it goes, its language, SEO, links, images and why it was written. Your site then reports the live address back.
- A new Content page lists your brand's posts and pages. You can approve one, copy it as Markdown or HTML, download it, and mark where it went live.
- Field labels: every field, built in or your own, and every product event now says how sensitive it is, who on your team owns it, how long its data is meant to be kept and where it may be sent. They're labels for your team and block nothing. Set them from Edit labels in the CRM settings, the API or MCP.
- Paid ads: Outflare can add the tracking template to every Google Ads ad for you. Tracking health shows each URL suffix it would change (the account's, and any campaign, ad group or ad suffix that overrides it without the template), then changes them when you confirm, keeping the parameters already there. Also by API and with the `plan_ad_tracking` and `add_ad_tracking` agent tools.

### Changed

- Company → Accounts: see your brand's external accounts and their health, fill the vault, resolve incidents, take an account over when a sign-in needs a person and hand it back, watch browser tasks, and offboard a brand's accounts.
- Paid ads: click a campaign to open it as its own page, then an ad group for its ads, keywords and searches, with a trail back to every campaign. The campaigns table now shows Outflare's own visits, conversions and revenue per campaign, and paid visits from campaigns Outflare can't match are listed apart with why.
- Paid ads: campaigns show their type, how Google bids and to what target, your own target cost per conversion with over, on target or under, and whether spend is scaling up or down against the days before. Set a target per campaign or per ad account on the page, by API or with the `set_ad_target` agent tool.
- Paid ads is split into tabs: Overview, Campaigns, Tracking, Send conversions and Accounts, each with its own address, so each part is one click away instead of one long page.
- Website and analytics now belong to one brand: site scripts, consent banners, flags, A/B tests, visitors, analytics, and tracking domains, so each brand's emails use its own (move one from the Domains page). Each connection is one brand's, split by site or ad account, or shared; anything unassigned shows in no brand until you assign it.
- Browser tasks now stay signed in to a brand's accounts between runs, like a person's browser, so they don't sign in and meet MFA every time. A session is saved only after the task confirms the right account, holds only that site's cookies, lasts at most 30 days, and is cleared when the site shows the wrong account. Remove the account's session secret to sign the agent out.
- Five more channels for your brand's agent: newsletters (broadcasts to your subscribers), website conversion (A/B tests, experiments, flags), competitor watch, and, through accounts and browser workers, listings and marketplaces and forums and launch sites. Each shows what it needs before it is ready.
- CRM settings now list your product events, so you can give each one a label, a description, a sensitivity, an owner, how long it's kept and where it may be sent, the same way as for fields.
- Google Ads: a sign-up on your own app now joins the visit whose ad click brought them, so the click is credited and sent back to Google; Google clicks know their campaign even without a tracking template, the Google tag's cookie fills in a lost click id, and Gmail addresses match better in enhanced conversions.
- Google Ads: connecting no longer asks for a developer token, which Google retired in September 2026; signing in with Google is enough.
- Hosted page implementations can use the main app shell and selected workspace and brand. Agent Mail has a module location below Company when its page implementation is supplied.
- Link pitches sent without approval now go out at most 10 a day per sequence (1 to 200, in Link building's settings); the rest go out on later days. Pitches your AI is unsure about, or to sites with a spam score of 30 or more, still wait for you.
- Extensions can show a short badge next to their own sidebar links for the workspace you have open, such as an account balance next to a billing link (`GET /v1/workspaces/{ws}/menu-badges`).
- Metrics have their own tab under People: turn them on, add your own, change what one counts, refill it from stored events, or archive it. Products can now be edited after they're added, including their name, aliases and when a customer counts as at risk or churned.
- Missions are retired. What each mission did stays readable on the Missions page, and changing one, or sending a change with the `Outflare-Mission` header, now answers that missions are retired. Agents make changes with their own token on the action layer, and playbooks run as agent runs on the Agents page.
- A rule's page lists who it fired for. On a record you can delete one field value, email or id, or link, and find it under Recently deleted. Bulk edits now start from picked people on the People page, every person shown, or a saved query, as well as from a list.
- Transactions can now be deleted and restored: a wrong or test payment goes to Recently deleted for 7 days, hidden from lists, Query and the person's page, and a billing sync won't add it back. Restoring it puts it back without sending its webhook again.

### Fixed

- Paid ads: an ad account connected for one brand now shows under that brand with its campaigns, spend and clicks, and the page lists each account's name, id and a link to open it in Google Ads or Meta.
- Find links no longer gives up silently on a brand website that shows a bot check or loads slowly: Outflare tries once more with JavaScript on, then tells you under Links why it couldn't read the site, without retrying and spending more web data tokens.
- Find links now waits for the website read to finish, and says why it couldn't read the site even when review page suggestions are already showing.
- CRM settings now save new objects, attributes, choices and field labels to the brand picked in the switcher. Before, they always went to the workspace's first brand.
- Competitor checks no longer fail when a competitor's sitemaps list the same page more than once. Each page is counted once, and a check that can't be saved now shows why and is tried again soon.
- Connectors: Google Ads, Gmail, Slack, Meta, TikTok, Pinterest, Snapchat, Apple, Amazon, Microsoft, LinkedIn, Reddit and X Ads, Ghost, WordPress, Chrome UX Report, Tolt, Postmark, Requesty, Engain, Outbrain, Taboola, Snitcher, Vector, Warmly and others now show the company's own logo instead of initials.
- Sending an email through the API no longer fails with a server error when a link is very long or the content holds a NUL character. Links over 2,000 characters are sent as written without click tracking, and a NUL character in the subject, body, tags, metadata or data is refused with a clear message.
- Setup health now opens its alerts for workspaces where more than one app or token calls the same deprecated API route. Before, the check failed for those workspaces and no install problems, including broken connections and silent website scripts, reached the status page.
- Background jobs no longer stall for up to five minutes when the server restarts or deploys: running jobs are stopped and handed straight back to the queue, and a restart no longer uses up one of a job's retries.

### Security

- Security improvements to the container images.

### Upgrading

- runs migration 0412.
- `OUTFLARE_SHUTDOWN_GRACE` (default `10s`) sets how long running jobs get to stop on `SIGTERM`. Keep it under your platform's stop timeout (docs/scaling.md, "Restarts and deploys").
- agents or scripts that still send the `Outflare-Mission` header, or use the mission tools in MCP or `--mission` in the CLI, need to drop them; their changes then go through the token's own grants.

## [0.82.0] - 2026-11-03

### Added

- Accounts: when a brand's agent meets MFA, a CAPTCHA, a passkey or a consent screen, it asks for a person, who takes the account over for a few minutes and hands it back with the identity checked. Offboarding archives a brand's accounts, empties their vault, revokes the agent's grants and lists what the owner still has to do at each platform.
- Browser workers: for sites with no API, a brand's agent queues a bounded task that runs in a real browser on your own servers, within the hosts, account and ceilings you set. It signs in from the vault without the model ever seeing a secret, asks a person at MFA or a CAPTCHA, and logs every step.

## [0.81.0] - 2026-11-03

### Added

- Accounts: record every external account your brand's agent may act through (social profiles, community and review sites, partner platforms, sites with no API), how each signs in and what it depends on. Every use is tracked with its outcome, failures become incidents with an owner and a next try, and secrets live in a sealed vault that is never read back and that only a person can fill.
- Findings: agents and people can record what they learned about a brand's marketing, with the evidence, how sure they are, where it holds and when to review it. The brand's context carries its active findings, so every agent working on the brand starts from what was learned.
- Bulk edits: owners can change many people or companies at once (from a list, a saved query or by hand): set an owner or a field, add or remove tags, move them in or out of a static list, or archive them, after a preview and with undo for 7 days. Every change is in each record's own history.
- Content: write posts and pages for a brand's website in Outflare, approve them, and publish them on your own WordPress or Ghost site, as drafts there or live. An agent can draft and publish too, within the approvals you set. Each item keeps the post it became, so edits update it rather than making a second one.
- Extensions can set a quota that decides whether a workspace may add more brands, mailboxes, tracking domains or records, or send more email. Refused additions answer `402 quota_exceeded`; refused emails wait and try again. Self-hosted servers have no quota and nothing changes.
- Every source's value of a field is now kept: a record shows what your CRM, enrichment and your own data each say, with **Use this** to pick one. A person's edit wins over the other sources for 180 days by default, then the best other value takes over, as a change in the record's history. Change the days per brand or per field (or set them to never), and **Pin** an edit to keep it for good.
- Import your website into Knowledge: Outflare reads your site's pages and keeps each as an article that support answers use, updating them when you run it again. The support assistant also finds Slack answers by meaning, not just matching words, when you have an embedding model connected.
- Outflare now manages campaigns on your own Google Ads and Meta accounts: pause or resume one, set its daily budget, or pause every campaign of a brand at once, from the Paid ads page, the API or MCP. Each change is read back from the platform. An agent's change goes through the action layer: a budget raise past a fifth always waits for you, and so does anything over its spend caps.
- Published query endpoints: owners and admins can publish a saved query as an endpoint that a BI tool or script calls with a brand API token, with parameters, a row cap, a rate limit and every call logged.
- Lists can now be built from a saved query: pick "A saved query" when making a smart list, and its members are the people in the query's `record_id` column, refreshed every 15 minutes (up to 50,000). If the query fails, the list keeps who it had and says why. Any list of people can also be saved as a static copy of who is in it now, from the list's menu ("Save a copy").
- Metrics: live totals for every company and person, such as spend in the last 30 days, usage this week and last seen, also per product. Rules, journeys and smart lists can act on them. Turning metrics on fills them from your history without sending anything for the past.
- A workspace setting lets only owners and admins edit people, companies and other records; everyone else can still see them. Imports and syncs keep running.
- Smart lists keep their history and can hold companies: see who joined and left a list and when (also on each person's timeline), make a list of companies from their fields, events and product stage, and exclude a list of companies to leave out everyone who works there. Lists can now pick people by lifecycle stage per product.
- The support assistant's Auto email replies now wait a few minutes you choose, so your team can stop one first, and hand-off topics send each handed-over conversation to the right person and team. Knowledge can learn answers from your past support replies, and imported websites are checked again every day.
- Transactions: every payment, refund, chargeback and credit is now one row per brand, from your billing connectors (from when they connect) and from your own billing through the API, where the same id never counts twice. Amounts keep their currency and are converted to the brand's. List them per person or company, query them in Query, and get a webhook for each.

### Changed

- New `agents.read` and `agents.run` scopes cover Outflare's agent: its runs, the playbooks it runs and writing to a run. Tokens and roles with the mission scopes keep working as before while missions are retired. Extensions can start agent runs for the caller.
- The analytics summary answers people and tokens kept to some brands with their brands' sites, like the other analytics reports.
- Analytics → Channels can count only people who signed in on your own product, leaving out browsers known only from a click on an email link, and each credited visit now says whether it was tied to the person by a sign-in or an email click.
- On Outflare Cloud, the link and PR autopilot pages give your brand's agent a link building or press coverage objective instead of starting a separate autopilot, and show where each objective stands; autopilots already running keep going.
- The link and PR autopilot pages open an autopilot's agent run on the Agents page, where you write to it, pause or stop it; autopilots started earlier still open their mission.
- Channels: Outflare now lists every channel an agent can work on, with what each needs connected, the actions an agent may take and their approval category, how results are measured, and whether the channel is ready for your workspace or brand, so an agent starts on what works today and you see what to connect next.
- Coverage now records a partner's approved post or page as a placement, and keeps a sponsorship's placement in step with its deliverable: agreed when the deliverable is added, live when you accept it.
- Coverage now fills itself: a link your outreach wins, press coverage you confirm and a review profile Outflare finds each become a live placement of its publisher, with the evidence and the date, so where your brand appears is one record across link building, PR and reviews.
- After you approve DNS records in Cloudflare, the card shows Outflare checking for them with a spinner and moves on by itself, instead of asking you to press Check now. A just-approved tracking domain no longer shows the record, the ways to add it again or an old error while it's being looked for.
- Extensions can add their own kinds of alert, shown in Settings → Alerts and emailed, posted to Slack and sent as webhooks like the engine's own.
- Edit lifecycle stages and the moves between them in the app: rename, add and remove stages, and change which event, usage or time without usage moves a company or person from one stage to another. The Products page's buttons now show only to people who can change the catalog.
- Link prospects and backlinks are tables in Query, so you or an agent can answer questions like "live links by method this quarter" or "lost links from strong sites" in SQL, for the brands you can see.
- When a link you won is removed from a page that is still up, the page goes back on your link prospects to win it back, with when the link was live, and its pitch says the page used to link to you. Nothing is sent until you pitch it.
- Create another workspace from the app: the workspace menu at the top of the sidebar (and the command palette) now has "New workspace", and the menu lists all your workspaces even when you only have one, so you can switch between them in one place.
- Campaigns on Microsoft Advertising, LinkedIn and TikTok can now be paused, resumed and re-budgeted from Outflare too, as Google Ads and Meta campaigns can, by you or by an agent within its grants.
- Extensions can limit how many website and product events a workspace stores: past a quota, the event APIs and the website snippet answer 402 quota_exceeded and store nothing. Self-hosted servers have no quota and store everything.
- Missions are being retired for agent runs on the action layer: nothing starts a new one. Starting a playbook, the Get started step and automations run as agent runs, the web app and MCP no longer create missions, and an AI app connected with OAuth now has each change held for your approval on the Agents page instead of needing a mission. Missions made before keep working until they end.
- Trying an event rule replays up to a year of events when the ClickHouse event store is set up, not only what Postgres keeps.
- See which pages of any site get the most search traffic: on Link building's top pages, Read search traffic adds each page's visits a month, keywords and top search from your Ahrefs or DataForSEO connection, so you can pick the best page to pitch. It's a paid read that runs only when you ask.
- Website chat: visitors can say whether the support assistant's answer helped. "No" hands the chat to your team right away, and the chat shows when the assistant is typing a reply.
- The support assistant's report in Support settings now shows how often it answered, left conversations to your team, had drafts sent as written and was rated helpful, against the 30 days before, with the commonest reasons it handed off. You're alerted once a day when answers it found wait for review, and the digest has a Support section.
- Support settings say why the assistant's Auto mode is off: on a self-hosted server it is part of Outflare Cloud, and on Outflare Cloud staff can turn it off for one workspace.
- Give a rule its own webhook: pick rules on a webhook endpoint, and events about a rule (such as rule.fired) reach it only for those rules.

### Fixed

- Paid ads: campaigns, keywords and search terms now include today's numbers for ad accounts in time zones ahead of UTC, such as Europe, late in the UTC day.
- Analytics syncs are scheduled again when a connected analytics account has never synced: one such account stopped the scheduler from queueing syncs for every analytics connection on the server.
- The one-click DNS approval ("Approve in Cloudflare") now finds a domain's DNS provider on hosted installs whose resolver didn't answer cleanly for a subdomain, where it answered "lookup failed" and offered only the API connection.
- "Approve in Cloudflare" now appears for subdomains, such as a tracking domain, on servers whose DNS resolver answers a missing subdomain record with an error instead of "not found".
- Connecting Google Calendar no longer fails with "refused the API key (HTTP 403)" right after signing in with Google.
- Static lists now open as static lists, with their member count and a Remove button for each person, instead of as a smart list stuck on "filling". The owner and other member fields show the member's name on a record's page, timeline and history instead of an id.
- The Tolt import reads more of Tolt's commission flows: percentage rewards however Tolt words them, and the "Created date is within N years" condition. A flow it still can't read is listed with its settings, so it can be added by hand or taught to the import.
- The Tolt import now reads commission flows as Tolt's API sends them, so a program's flows come over with their conditions, approval and partner overrides. Tolt's "number of customer transactions" counts the payment itself, and the import adjusts for that, so a "first payment" flow still pays only on the first payment. Paused flows stay behind.
- A verified tracking domain no longer flips to "not pointing here" because of one stale DNS answer: it stays in use unless its record has been missing for over an hour. Its HTTPS certificate is also checked every two minutes at first, so the page shows it sooner.
- A tracking domain no longer stays on "Setting up HTTPS" for good when its certificate checks stop: "Check now" and a server restart start them again.

## [0.80.0] - 2026-11-02

### Changed

- A grant that lets an agent run paid ads changes on its own gets spend caps of $50 a day and $500 a month unless you set your own, so a first autonomous grant on ad spend can't surprise you.

### Fixed

- The changes from v0.79.0, which didn't pass the release checks, are now published.

## [0.79.0] - 2026-11-02

### Added

- Agent actions: turn on the action layer for an API token and set what that agent may change on its own. Everything else waits for a person, who approves changes one at a time or in batches. Every change the agent made is kept.
- A playbook can run as an agent run: Outflare's agent carries it out in turns on the action layer, and the mode you choose (copilot, autonomous with a spend cap, or dry run) sets what it may change on its own. Its changes show on the Agents page, and you write to the run and it answers in its next turn.
- Agents page: see what your agents changed, approve what waits for you one at a time or in a batch, let changes like one you approve run from then on, and set what each agent may do on its own. Mark a token as an agent under API tokens.
- Knowledge now fills its own gaps, with you approving: when your team answers a support question the knowledge couldn't, Outflare suggests that answer (without the customer's details) for Knowledge, Review. AI agents can suggest answers they find on your website or in Slack, and articles that disagree are flagged so you can keep the right one. Nothing is used until you approve it.
- Digital PR: add the journalists you already know, one at a time or from a CSV (with a check first), with their outlet, its tier and the beats they cover. Beats are suggested from the news searches their articles came up in, and the Journalists tab and media lists filter by beat and outlet tier.
- Experts approve their own quotes: on a draft quote, Ask the expert emails them through one of your brand's outreach sequences or gives you a link for Slack. They approve the exact words, suggest a change or decline on a simple page with no account, and you're alerted when they decide.
- Products and lifecycle stages: list the products each brand sells, and Outflare keeps every company and person in a stage per product (trial, active, at risk, churned), so a customer who cancels one product but keeps another is never treated as churned. Each move is an event journeys can start on. Find it under People → Products.
- A labelled cold email reply is now a `reply.labeled` event on the sender's record, so a journey can start when a lead replies interested (to hand them to sales or open a deal), and the automation map shows replies as a source.
- Broadcasts and sequences can hold back a share of their audience on purpose, and then show what they added: who converted and what they paid in the 30 days after, against the people held back, and whether the difference is real. Set it when you draft a broadcast or in a sequence's settings.
- Slack (knowledge): a read-only Slack connection for the support assistant, which reads only the channels you pick to find answers your team already gave. It is separate from the Slack connection that posts alerts.
- Support assistant: Outflare answers customers' chats and emails in a few short sentences from your approved knowledge, asks one question back, or hands the conversation to your team, drafting for your team to send. When it can't answer, it looks on your website and in the Slack channels you pick, and suggests the answer in Knowledge for you to approve.
- Live chat on your website: add a chat button to your site from Support settings. Visitors can search your help centre, then message your team; chats land in the support inbox with email, assignment and response times, and your replies show in their chat within seconds. It shows only on the sites you list, and your site can vouch for signed-in users.

### Changed

- Paid ads: Meta and Google conversions by type, Google keywords and search terms, Meta link clicks, page views, video views and frequency, Outflare's own results beside each ad set and ad, and the ads missing the tracking template. Meta purchases reported several ways now count once.
- Agent actions: approve a pattern once. When you approve an agent's held change, you can let changes like it run on their own from then on, within a daily limit. Held, approved and rejected agent changes can also go to webhooks.
- Start a playbook from the app and Outflare's agent runs it: pick copilot, autonomous with a budget, or a dry run. The Agents page's Runs view shows each run's turns and changes, lets you write to it, and pauses or stops it; an automation's last run opens there too.
- A grant that lets an agent run changes on its own can cap what those changes spend on your own accounts a day and a month. Spend booked while a change runs is linked to it, each change shows what it spent, and once the day's or month's spend plus what the next change is likely to cost would pass the cap, changes wait for a person.
- Analytics → Channels credits companies and deals: what each company's people brought and the deals it won, and the value of won deals per channel, spread by the credit its people earned before the deal closed.
- Leads, lead lists, sequences, replies, meetings and deals now belong to one brand, with API routes under `/v1/brands/{brand}/`: a sequence enrols only its brand's leads, and an opt-out applies to the brand it came from, while bounces, complaints and what an admin applies to every brand stop every brand's email.
- Let approved salespeople connect their own Google account with read-only consent. Select contacts and accounts for private email capture, with internal-mail exclusions, separate sharing controls, draft protection and owner-only disconnect. Deleted copies stay deleted. Administrators manage eligibility; Google Workspace approval remains an external setup step.
- Import personal customer emails with private defaults, explicit sharing and internal-email exclusions. Choose activity-only or full content for specific people or the brand. Exclusions revoke access to older imports; record merges reset sharing. Personal mail stays separate from the shared outreach inbox. Provider sync is not included yet.
- Manage relationships in a sales workspace with editable tables, pipeline boards, saved views, tasks, notes and sales summaries. Add leads using existing companies and contact details, import Attio list entries and linked history, and create follow-ups from workflows.
- Every list in the API now pages one way: pass `next_cursor` back as `cursor` until it is null. The per-list `before`, `after` and `offset` parameters and their `next_before`, `next_after` and `next_offset` fields are gone. The events feed and following logs start from now with `start=latest`.
- AI accounts and Computers include a public Mac installer download for every workspace member. The installer is labelled as a one-upgrade automatic update test.
- Approving DNS records at Cloudflare now works for domains on Cloudflare DNS. When a domain already has a DKIM key at the same selector, a DMARC policy or its own MX record, Outflare offers the other ways to add the records instead, so the approval never puts a second record beside one you already have.
- Pipelines that add people to a campaign can now use a campaign that runs in Smartlead, Instantly or EmailBison. People are pushed into it on your own connection, after the same opt-out and exclusion checks, and the sequencer sends.
- The lead finder page lets you choose where a search looks for companies, add your own list of companies, and compare the places on new companies, fit, replies and cost.
- Google and Microsoft mailboxes connected before mailbox sign-ins were connections now sign in through Gmail and Outlook connections: they are moved automatically, keep sending, and show with your other connections. The older `/oauth-connections` and `/oauth/{provider}/authorize` routes, and `oauth_providers` in `/v1/meta`, are gone.
- The partners table shows each partner's email under their name. The Tolt import skips a promotion that has neither a code nor a partner (a program-wide discount) without listing it as something to check.
- Stories: create a pitch sequence for a brand in one click. It passes every pitch rule (the headline and your AI's opener, one short follow-up, stops on any reply) and waits as a draft for you to add mailboxes and activate it.

### Fixed

- "Approve in Cloudflare" is now a real link you can hover or open in a new tab, and it's the main action on DNS cards. Connecting the Cloudflare API is a secondary option under it. When one-click approval isn't offered, the card says why.
- The Tolt import no longer marks commissions as paid just because they sit in a Tolt payout. Commissions in payouts Tolt hasn't paid yet (pending or "not eligible") keep their own status, and the next import run takes back any it marked paid before, removing the payouts it made for them.
- Tracking domains on Cloudflare DNS can now be added with one click: "Cloudflare (one click)" opens Cloudflare's approval screen with the CNAME record, instead of asking for an API token.

### Upgrading

- nothing to do. The move runs once when the server starts; a mailbox that cannot be moved is logged and needs connecting again.

## [0.78.0] - 2026-11-01

### Added

- Support response times: set how fast your team promises to reply and resolve, in working hours or around the clock, for the workspace or one brand. Each conversation shows when its reply is due, and an Overdue folder collects the ones that ran late.

### Changed

- Analytics → Channels shows which channels bring people new to you and which bring people you already knew, how much of their revenue is first or repeat payments, and what the people each channel first brought have paid to date.
- Analytics → Channels can show identified people only: visits where the person signed in or was named by a form, and the credit on them, leaving out visits known only by a browser cookie. Each credited visit now says how it was matched.
- Connectors and data providers now say how to connect in one place: `auth_options` lists each way (sign in or API key), whether it works on this server and which is recommended. The repeated `auth` and `auth_methods` fields are gone from the API.
- A connected sequencer account's name is now `integration_name` everywhere, beside `integration_id`: on campaign links (was `platform`), platform accounts (was `name`), a mailbox's assignment and its `platforms` list (was `platform`) and a sequence's assigned mailboxes (was `platform`). `connector` stays the connector's id.
- Gmail and Outlook mailbox sign-ins are now connections like any other: signing in through the Gmail or Outlook connector adds the mailbox, and an admin connection (a Google service account or a Microsoft Entra app) adds mailboxes by address. The older mailbox sign-in routes are deprecated.
- Your own role in `GET /v1/me` and `GET /v1/workspaces/{ws}` now reads like a member's: `role` is the exact role as `GET /roles` lists it (a starting role's key or your workspace's own role's id), with its name in `role_name`. `role_id` is gone. The workspace switcher shows the role's name.
- Tools that research people and companies can keep what they learned as ordinary fields on the record, created the first time they're needed. They never overwrite a value you set yourself, and every change shows in the record's history.

### Fixed

- The changes from v0.77.0, which didn't pass the release checks, are now published.

### Upgrading

- register `$PUBLIC_URL/v1/integrations/oauth/callback` as a redirect URI on your Google and Microsoft OAuth apps, and give the Microsoft app the delegated permissions `IMAP.AccessAsUser.All` and `SMTP.Send`.

## [0.77.0] - 2026-11-01

### Added

- A journey's holdout now says what the journey changed: each path of a random split against the people held out, with conversions and revenue in the 30 days after, the lift, and whether it is real or too early to tell. On the journey's page, in the API and in MCP.

### Changed

- Paid ads: Meta Ads and Google Ads now sync ad sets (Google's ad groups) and ads, each with spend, impressions, clicks and conversions per day, and each ad's headline and landing page. Open a campaign to see its numbers by day, its ad sets and its ads, and see every platform's spend by day at the top.
- The automation map now shows where events come from (website, your app, Stripe) and what journeys reach outside Outflare: webhooks, Slack, your CRM, ad audiences and review campaigns. Exported kits leave out webhook keys and ask the other workspace for its own URL, channel and audience.
- Rules, journeys, lifecycle email, alerts and webhooks now belong to one brand, with API routes under `/v1/brands/{brand}/`: a rule or journey acts only on its brand's people, an unsubscribe applies to the brand it came from (an admin can apply one to every brand), and a webhook hears only its brand's events.
- Mailbox allocation now lists the mailboxes it keeps from each sequence (a declined suggestion, an undone automatic choice, or a campaign the sequencer refused), and you can clear one so that mailbox can be given to the sequence again, in the app, the API and MCP.
- Public releases of Outflare for desktop check for updates automatically and install verified updates when the app is idle. The app window and tray menu also offer Check for updates.
- Desktop release testing can exercise an automatic upgrade through an isolated test channel. Test builds are labelled clearly and keep package and version verification enabled.
- Link prospects can now be found through expired domains: a page that still links to a domain that has lapsed is a prospect with the method "Expired domain", to offer one of your pages instead.
- The API calls a reply's tags `tags`: inbox messages return `tags` instead of `labels`, triaging a reply takes `tags`, the `inbox.updated` event reports changed `tags`, and the MCP tool `triage_inbox_message` takes `tags`. "Label" now always means a reply's category, such as interested or not_now.
- The lead finder page counts data provider and email verifier credits in what results cost, priced at the rates you set in Outcomes, and says which providers still need a price.
- The lead finder's results no longer show meetings. Results and cost per result go up to people in campaigns and replies, while a better way to measure outcomes is built.
- A mission's attributed revenue goal now counts payments credited to the broadcasts, social posts and social replies made in the mission, as well as its sequences.
- A lead list's leads now come from one place, `GET /lead-lists/{id}/members`, which also takes the `suppressed` and `verification` filters. `GET /leads` no longer takes `list_id` and says where to look instead.
- Inviting someone and changing a member's role take one `role` field: any role's id from `GET /roles`, a starting role like `admin` or one your workspace made. `role_id` is gone, and members and invites return the same `role` they take.
- Links in sequences now say which step they went out in, and Analytics → Channels can split a campaign by content, so you can see which email in a sequence brought the visit and the sale.
- Support replies can link to your help centre: Articles in the reply box suggests public articles from what the customer wrote, or searches them, and puts the link in your reply. Suggest a reply now offers a link to each article it used.
- A mailbox's `warmup.provider` is now null when no connected sequencer runs its warm-up, and `warmup.platform` is gone: the sequencer's warm-up state and score are its entry in `platforms`. To stop warm-up, set `enabled` to false; the `outflare` provider is no longer accepted.
- Following Outflare and the builder on X now opens X's one-click follow dialog instead of the whole profile. After posting feedback to X, the Feedback box offers a last step, following the builder, and X suggests following both accounts once the post is up.

### Fixed

- Connecting a mailbox supplier such as Cheap Inboxes now reads its mailboxes straight away, and replacing its API key reads them again, without anyone opening the supplier's mailbox list first.

## [0.76.0] - 2026-11-01

### Added

- Query Studio: a Query page for each brand with an SQL editor that completes table and column names, every table and record type beside it, results as a table or a bar, line or area chart, CSV downloads of up to 10,000 rows, and saved queries, private or shared with the brand, with every version kept and restorable.

### Changed

- Partners can submit people they referred on the portal: an email and a note. You approve or reject each one on Partners → Leads, or have them taken at once. An approved lead becomes the partner's referred customer under the program's rules, and a customer another partner referred first stays with that partner.
- More leftovers are gone: `GET /email-domains/{domain}` (use `/domain-lookups/{domain}`), the `member` role name (use `operator`), the old connector ids `hubspot_private_app` and `posthog_cloud`, the never-set `suggested_sender_id` on mailboxes, the `system` value of a reply's `category_source`, and the ignored `v` on connector logos.

## [0.75.0] - 2026-11-01

### Added

- Pitch one partner with a branded website teaser, then reveal their proposal after secure sign-in. Agree on flexible deliverables, cash or product benefits, and held or split payment milestones; partners submit work and brands manually verify it before approving payment.
- Create paid or voluntary partner tasks with fixed prices, ranges or custom offers. Partners add channels, verify websites to unlock private proposals, and submit work through a hosted or embedded portal that shares the dashboard's design system. Approved rewards join your existing ledger.

### Changed

- Building Outflare from source now needs Go 1.26 or later; the Docker image already has it.
- Customize your partner portal's navigation, support, help, landing pages and payment guidance. Require fresh agreement when terms change. Automatically sign customers in with one-use backend handoffs. Partners can request cash payouts and generate brand-approved coupon codes; brands control discounts, schedules and limits through their billing connection.
- Partners → Log shows every conversion your backend or tracker reported in the last 30 days, by the API or a postback URL: what it sent, what Outflare answered, and whether it was credited, with the reason when it wasn't. Search it by order id, customer id, ref, promo code or email.
- Webhook deliveries keep every try, not only the last: open a delivery to see its payload and each attempt's status code, error and answer time, and send it again from there. Each endpoint shows its error rate over the last 24 hours, and its deliveries page shows the last 7 days of tries and failures.

### Security

- Security improvements to the help centre and page reading.

### Upgrading

- Partners review current terms again.
- Review custom portal CSS that targets the old template.

## [0.74.0] - 2026-11-01

### Changed

- People, companies, their history, lists, imports and duplicates now live under each brand, with API routes under `/v1/brands/{brand}/`, and every customer page works inside the brand you pick. Query runs for one brand, and each custom record type is a table of its own.

## [0.73.4] - 2026-11-01

### Fixed

- The changes from v0.73.2, which didn't pass the release checks, are now published.

## [0.73.3] - 2026-11-01

### Fixed

- The changes from v0.73.0, which didn't pass the release checks, are now published.

## [0.73.2] - 2026-11-01

### Fixed

- The changes from v0.73.0, which didn't pass the release checks, are now published.

## [0.73.1] - 2026-11-01

### Fixed

- The changes from v0.72.0, which didn't pass the release checks, are now published.

## [0.73.0] - 2026-11-01

### Changed

- Each brand now has its own people and its own catalog: the same email or user id in two brands is two separate people, never merged, and record types, fields, option lists and event definitions belong to one brand, with every new brand starting from the default set. Approving a held event name needs only the customers permission.
- The partner portal can load your own website script, so partners' visits show in your website analytics, and Google Analytics 4 alongside it, held back until the visitor allows analytics. Pick a site key and, if you like, a measurement id in the portal settings.

### Fixed

- The changes from v0.72.0, which didn't pass the release checks, are now published.

## [0.72.0] - 2026-11-01

### Changed

- Partner programs can block countries: website visits and new customers from them don't credit partners. A website visit's country now comes from the visitor's address, looked up in an IP database that ships with Outflare (IP geolocation by DB-IP), when your own proxy doesn't pass it. The address itself is never stored.

### Fixed

- The changes from v0.71.0, which didn't pass the release checks, are now published.

## [0.71.0] - 2026-11-01

### Added

- Four new playbooks assess sign-ups for partnership potential: a review across partnership types, SDK and bandwidth suppliers, integration partners, and resellers or agencies. Each returns evidence, confidence and suggested next steps for review.

## [0.70.0] - 2026-11-01

### Added

- Undo a merge, and undo an import: a record's page lists its merges with Undo merge, which puts back everything the merge moved. People → Imports lists every CSV import, CRM import and send of product events; Undo… shows what it made and what changed since, then undoes only what's untouched or everything, restorable for 7 days from Recently deleted.

### Changed

- Partner programs can refuse credit for website visits that came from paid ads or social media, for partners who may not bid on your brand or post on social networks.

## [0.69.11] - 2026-11-01

### Fixed

- The changes from v0.69.8, which didn't pass the release checks, are now published.

## [0.69.10] - 2026-11-01

### Fixed

- The changes from v0.69.8, which didn't pass the release checks, are now published.

## [0.69.9] - 2026-11-01

### Fixed

- The changes from v0.69.5, which didn't pass the release checks, are now published.

## [0.69.8] - 2026-11-01

### Fixed

- The changes from v0.69.5, which didn't pass the release checks, are now published.

## [0.69.7] - 2026-11-01

### Fixed

- The changes from v0.69.5, which didn't pass the release checks, are now published.

## [0.69.6] - 2026-11-01

### Fixed

- The changes from v0.69.2, which didn't pass the release checks, are now published.

## [0.69.5] - 2026-11-01

### Fixed

- The changes from v0.69.2, which didn't pass the release checks, are now published.

## [0.69.4] - 2026-11-01

### Fixed

- The changes from v0.69.2, which didn't pass the release checks, are now published.

## [0.69.3] - 2026-11-01

### Fixed

- The changes from v0.69.0, which didn't pass the release checks, are now published.

## [0.69.2] - 2026-11-01

### Fixed

- The changes from v0.69.0, which didn't pass the release checks, are now published.

## [0.69.1] - 2026-11-01

### Fixed

- The changes from v0.68.0, which didn't pass the release checks, are now published.

## [0.69.0] - 2026-11-01

### Added

- Search insights: find traffic opportunities and declines, inspect forecasts, GA4 outcomes and page experience, then save tagged work, create fixes and experiments, keep report snapshots, and schedule data collection with visible run history and failures.

### Fixed

- The changes from v0.68.0, which didn't pass the release checks, are now published.

## [0.68.0] - 2026-10-31

### Changed

- Choose the brands an AI subscription serves explicitly. Discovered subscriptions await assignment, and the workspace report shows all subscriptions with brand labels and filters without counting shared subscriptions twice.

### Fixed

- The changes from v0.67.10, which didn't pass the release checks, are now published.

## [0.67.10] - 2026-10-31

### Fixed

- The changes from v0.67.8, which didn't pass the release checks, are now published.

## [0.67.9] - 2026-10-31

### Fixed

- The changes from v0.67.7, which didn't pass the release checks, are now published.

## [0.67.8] - 2026-10-31

### Fixed

- The changes from v0.67.5, which didn't pass the release checks, are now published.

## [0.67.7] - 2026-10-31

### Fixed

- The changes from v0.67.5, which didn't pass the release checks, are now published.

## [0.67.6] - 2026-10-31

### Fixed

- The changes from v0.67.3, which didn't pass the release checks, are now published.

## [0.67.5] - 2026-10-31

### Fixed

- The changes from v0.67.3, which didn't pass the release checks, are now published.

## [0.67.4] - 2026-10-31

### Fixed

- The changes from v0.67.1, which didn't pass the release checks, are now published.

## [0.67.3] - 2026-10-31

### Fixed

- The changes from v0.67.1, which didn't pass the release checks, are now published.

## [0.67.2] - 2026-10-31

### Fixed

- The changes from v0.67.0, which didn't pass the release checks, are now published.

## [0.67.1] - 2026-10-31

### Fixed

- The changes from v0.66.0, which didn't pass the release checks, are now published.

## [0.67.0] - 2026-10-31

### Added

- Coverage brings earned mentions, paid placements, backlinks and publisher relationships together with CRM context, commercial records and growth reports. Discover placements, monitor page and link changes, and use optional AI assistance to review accuracy against published Knowledge and prepare follow-up drafts.

### Fixed

- The changes from v0.66.0, which didn't pass the release checks, are now published.

## [0.66.0] - 2026-10-31

### Changed

- AI accounts shows the computers reporting each account and who linked each computer, including the person's name and email. Claude reset credits are read automatically when Claude exposes them, alongside Codex's available resets.

### Fixed

- Outflare for desktop detects separate Claude Code profiles and Claude desktop accounts on Mac, adds new account emails without replacing earlier accounts, keeps previous accounts visible with their last-checked time, and detects login changes within seconds. New readings reach the portal without waiting for other tools, and the portal refreshes automatically.

## [0.65.0] - 2026-10-31

### Added

- Request honest customer reviews by email from Reviews or existing workflows. Configure brand destinations, customize starter emails, preview audiences, schedule campaigns, and track sends and review-link clicks with shared frequency limits.

## [0.64.0] - 2026-10-30

### Added

- Recently deleted: deleting a person or company, a field value, an email or id, a link or a product event (such as a test payment) now hides it everywhere at once and keeps it for 7 days, so you can restore it as it was without anything being sent again. Erasing someone's data stays permanent.

### Changed

- Partner programs can read other link parameters besides ref and via, such as aff for links made on another platform. The Tolt import adds the ones its links use.

## [0.63.0] - 2026-10-30

### Added

- Privacy export: from a person's or company's page, or the API and MCP, download everything a brand holds about them as one file (their fields, history, ids, links, events, consent, emails, replies, meetings, deals and payments), only from that brand and never with secrets. The link works for 7 days, and every export and download is in the audit log.

## [0.62.0] - 2026-10-30

### Added

- Link autopilot page: set how many new live links you want a month and a budget, and Outflare's agent runs the link building playbooks toward it. You approve its mission, each automation it sets up and every pitch. Part of Outflare Cloud; other servers show what it does.

### Changed

- The partner portal can take applications from work email addresses only, refusing free, disposable and relay addresses.
- Partner programs can set a payout term (Net 0 to Net 90 after the month a reward was earned) and the payout methods and crypto tokens partners choose from. Partners pick their method on the portal, and the payout CSV says which.

### Fixed

- The changes from v0.61.0, which didn't pass the release checks, are now published.

## [0.61.0] - 2026-10-30

### Added

- Four digital PR playbooks: a data story from your own numbers, a regional version of it for local papers, a reaction to today's news with your expert's approved comment, and your expert offered to journalists on their beat. Each runs as a mission you approve.
- Pitch an approved story to journalists through your sequences. Your own AI writes each opener from the story's sourced facts and the journalist's latest article; a person approves every pitch, and sends wait for the embargo. Replies are read as covering it, wants the data, wants an interview or not my beat, and each story shows its results.

### Changed

- The partner portal can have its own favicon, and its logo and favicon are set in Settings, with a preview of the sign-in page as you type.

### Fixed

- The changes from v0.60.0, which didn't pass the release checks, are now published.

## [0.60.0] - 2026-10-30

### Changed

- Partners: each program can turn the self-referral block off (it stays on by default), and the partner portal shows "Powered by Outflare" at its foot, which any program can hide.
- Partners: the Tolt import brings over clicks on partners' links, so each partner's click count carries over. A second run doesn't count them twice.

## [0.59.5] - 2026-10-30

### Fixed

- The changes from v0.59.4, which didn't pass the release checks, are now published.

## [0.59.4] - 2026-10-30

### Fixed

- The changes from v0.59.2, which didn't pass the release checks, are now published.

## [0.59.3] - 2026-10-30

### Fixed

- The changes from v0.59.0, which didn't pass the release checks, are now published.

## [0.59.2] - 2026-10-30

### Fixed

- The changes from v0.59.0, which didn't pass the release checks, are now published.

## [0.59.1] - 2026-10-30

### Fixed

- The changes from v0.58.0, which didn't pass the release checks, are now published.

## [0.59.0] - 2026-10-30

### Changed

- Journey email steps can send even over the frequency cap, for email a person must get such as a sign-in link, and can skip rather than hold an email due in quiet hours. Skipped emails are logged with the reason, like capped ones.
- Link building playbooks can now start by themselves when a competitor gains a link, a page you could pitch gets a broken link, you publish a page worth promoting, or the news names your brand.
- Partner programs can set a lead expiration: a referred customer who hasn't bought within that many days no longer credits the partner.
- Stories can now use your own AI model to suggest story ideas from your coverage, your experts' topics and the news, and to draft a press release from a story's sourced facts and approved quotes only. Every draft goes through the same check as any release, and a person still approves it.

### Fixed

- The changes from v0.58.0, which didn't pass the release checks, are now published.

## [0.58.0] - 2026-10-30

### Added

- Marketing email guardrails: by default each person gets at most 1 marketing email a day and 3 a week, and nothing between 8pm and 8am in their time zone (held until morning). Change them per workspace. Every capped or held send says why on the journey step and the person's timeline.

## [0.57.0] - 2026-10-30

### Added

- Product events sent to a brand are saved first and answered at once with a receipt per event. Each event then shows whether it was accepted, a duplicate, rejected (with what to fix) or held for an unapproved name, on the brand's new Events page, and rejected or held events can be fixed and replayed for 90 days.

## [0.56.0] - 2026-10-30

### Changed

- Brands keep their customer data apart: every person, company and record, with their details, history and consent, and the fields and types you add to the catalog, now belong to one brand. People with no brand go to your first brand, and duplicates are only found and merged within a brand.

## [0.55.0] - 2026-10-30

### Changed

- Every workspace now starts with a brand, named after the workspace, and workspaces without one got one. Brands are becoming the owner of all their data, so two brands in one workspace never mix: their own API calls live under `/v1/brands/{brand}/`, starting with reading the brand itself.

## [0.54.1] - 2026-10-30

### Fixed

- The changes from v0.54.0, which didn't pass the release checks, are now published.

## [0.54.0] - 2026-10-30

### Added

- Playbooks can now run by themselves, on a schedule or when something happens, such as a lost backlink. You approve each automation once, with spend limits per run and per month; if a limit is reached or a connection goes missing, it stops and tells you why.

### Changed

- The automation map can now show what reaches one person: pick someone, and it marks the events they did, the lists they're in and the journeys they went through, and lights up what those set off next. You can also narrow it to everything that leads to one sequence, list or tag.
- Partners: conversions keep the payment provider (`payment_source`, Tolt's `source`) and billing `interval` your backend sends, and flows can pay on them. The Tolt import brings both over.
- Each partner group has its own sign-up link: partners who apply through it join that group.

### Upgrading

- runs migration 0306.

## [0.53.0] - 2026-10-30

### Changed

- Partners: the Tolt import also brings Tolt's groups (with each partner in theirs, and the default group) and flows (rates, duration, conditions, approval and custom rates). A flow with something Outflare can't map yet isn't imported and is listed for you to add on the Flows tab.

### Upgrading

- runs migration 0305.

## [0.52.0] - 2026-10-30

### Added

- Holdouts for exclusion groups: keep a share of a group's visitors out of every flag in it, then compare them with everyone else on a goal to see what all the group's changes did together.

## [0.51.0] - 2026-10-30

### Added

- Reddit campaigns page: brand mentions and links placed and still live, cost and median lifespan, results by subreddit or tactic, every action with its status and timeline, experiments, and each brand's Reddit limits.

### Changed

- Partners: while Tolt still pays a program you imported, its imported commissions keep Tolt's status and never go into a payout here, so nobody is paid twice. Switch over on the Payouts tab when Tolt stops paying. The import summary now shows how many it read, and partners by status.

## [0.50.0] - 2026-10-30

### Changed

- The API drops everything left from projects and agency accounts: `project_id` fields, the `project.*` webhook events, the old scope names `projects.read` and `projects.manage` (use `profiles.read` and `profiles.manage`) and the whoami token `kind`. Inbox filters use `tag` (not `label`), and report rows use `name`.

### Upgrading

- migration 0316 renames stored `projects.*` grants on tokens and roles to `profiles.*`, removes project events from webhook subscriptions, and drops `project_id` from the query views.

## [0.49.0] - 2026-10-30

### Added

- Reddit campaigns through Engain: place comments, replies, posts, thread groups and upvotes within a per-brand credit budget, run them as experiments, and see brand mentions and links still live, their cost and how long they lasted. Agents get a guide and a ready prompt over MCP.

## [0.48.0] - 2026-10-30

### Added

- Help centre: publish the knowledge you mark for it as a public site customers can read and search, one per brand and on your own domain if you like. Style it to match your brand with a live preview, and see what readers read and what they searched for but didn't find.

## [0.47.0] - 2026-10-30

### Added

- Journeys can wait until something happens, like an order over $50, with a timeout, and split people by who they are or at random, with an optional holdout. You can also cap how many people start a journey each hour or day.

### Changed

- The last numbers from Outflare's old built-in warm-up are gone: `GET /mailboxes/{id}/warmup`, the 7-day warm-up counts and inbox rates on mailboxes, domains, stats, reports, mailbox health and the infrastructure overview, and the ignored `daily_target` and `reply_rate` settings. Warm-up runs in a connected sequencer, and its score is on each mailbox's `warmup.platform`.

## [0.46.0] - 2026-10-30

### Added

- Multivariate A/B tests: add up to two more flags to a test, and see every combination of their versions against the original, the best combination, each change's own effect, and whether the changes affect each other.
- Engain connector: connect your Engain account with your own API key to run Reddit campaigns through Engain's accounts. Connecting shows your projects and credits left.
- Journeys can add people to your ad audiences, or take them out. An "Add to an ad audience" step puts the person in a Meta custom audience or a Google Ads Customer Match list on one of your ad accounts, through your own connection. Only their hashed email and phone number reach the platform, and adding follows your ad consent setting.

### Fixed

- The Tolt import also brings over partners Tolt lists as invited (as pending), with the customers they referred.

## [0.45.0] - 2026-10-30

### Changed

- The permissions for ideal customer profiles and moving records between brands are now called `profiles.read` and `profiles.manage`, and the API reference lists those endpoints under "profiles" instead of "projects". The old names `projects.read` and `projects.manage` still work: tokens, roles and apps that hold them keep their access, and requests may still send them.
- Support inbox: forward your support@ address to your brand's support address (such as northpeak-support@) instead of connecting the mailbox. Each email becomes a conversation, and replies go out through your email provider from the address the customer wrote to. On Outflare Cloud.

### Fixed

- Pipelines that verify an address a person already has no longer stall when your email verifier checked it recently: the recent check is used, at no credit. Each check that does spend a credit is now reported to the feature that asked for it.

## [0.44.0] - 2026-10-30

### Added

- Tracking health on the Paid ads page shows, for your workspace, how each network's paid clicks arrive (with their click id, only the template's ad ids, or untracked), which campaigns spend without a tracked visit, and which platform is refusing the conversions you send back, each with what to fix.
- Tracking templates: paste one into Google, Meta, Microsoft, LinkedIn, Reddit, TikTok, Snapchat, Pinterest, Taboola or Outbrain (Paid ads → Tracking templates) so each paid click lands with its campaign, ad set and ad ids. Website tracking now reads TikTok, Snapchat, Pinterest, Taboola, Outbrain, Quora and Impact click ids, and with consent the Meta and Google pixel cookies.
- Alerts: a hot reply or a hot website visitor raises an alert under Settings → Alerts, sent as the `alert.raised` webhook, counted in the digest and emailed to the person it is for or to owners and admins. Choose who hears about each kind, per workspace or brand, and mute a kind for yourself.
- Your own attribution models: start from any of the five and give each channel its own window (paid ads 7 days, say), leave channels out, or set the position weights and time-decay half-life. Each change is a new version, and reports say which one they used. The default look-back is now 30 days instead of 90 for workspaces that never changed it.
- Define your own conversions: any event, filtered on its properties (a call over a minute, a Pro plan payment), with a fixed value or one from the event, and each ad network's name for it. Start from a preset for SaaS, booked calls, e-commerce or lead generation. Attribution credits channels with them.
- Exclusions: journeys, broadcasts and sequences can leave out people on chosen lists, with chosen tags, or at a tagged company, such as "Competitors" or "Active deal: hands off". Excluded people are skipped with the reason, and a broadcast shows how many it leaves out before you send.
- Feedback and What's new: the Feedback button in the top bar turns a bug, idea or question into a GitHub issue or discussion under your own account, and a post on X, with links to good first issues for anyone who wants to build it. What's new lists every release, day by day.
- Fix tickets: every problem a site audit finds becomes one ticket with all the pages it affects. Your own AI model writes it like an issue for a developer, you approve, reject or ask for changes, and the reasons you give become guidelines every later ticket follows. Copy any ticket as an issue for GitHub, Linear or a coding agent.
- Change a page for one version of a split without code: Edit visually opens a page of your site with that version and an editor over it, where you click anything to change its text, hide it, or change a link or an image. Visitors in the version see the changes, and seeing them counts the visitor in the version's A/B test.
- Playbooks can now set up a whole automation for you: its email drafts, lists, rules and journeys, all as drafts you check and turn on. Four to start: activate new trials, save failed payments and win back cancelled customers, hand off warm prospects the moment they say yes, and catch key accounts at risk. Tell it which events to use, and the journey builder shows where each draft came from.
- HubSpot reply push: pick reply labels on your HubSpot connection, such as hot, and each matching reply adds the contact with a note quoting the reply and a link back, plus, if you choose, a follow-up task for the lead's owner.
- Journeys can now act, not just email: update a field, add or remove a tag, put people in or out of a static list, start or leave another journey, add them to a sequence or stop their whole company's sequences, suppress or unsubscribe them, and alert their owner. Exit criteria end a run at once. People and companies get tags and an owner.
- Journeys can ask your own AI model about a person: sort them into choices such as hot, warm or cold, or write a short note. Later steps branch on the answer or use it in emails and webhooks. It uses your own key and AI spend limits.
- Journeys can create or update people in your HubSpot. A "Create or update in your CRM" step sends their name, company and website, plus any HubSpot properties you fill from their details, through your own HubSpot connection. Deals aren't touched.
- Reviews, a page of its own: your brands' Trustpilot, G2, Capterra and Google reviews, read every day with your own web data connection, a review score that weights stars by how many and how recent, its direction (growing, steady, slipping), and Draft answer with your own AI model for a person to post.
- Slack connector: connect your own Slack and post each kind of alert, such as hot replies, to a channel you pick under Settings → Alerts.
- Snapchat Ads: sign in with Snapchat to read each ad account's campaigns and daily spend, impressions, swipes and purchases, and send your conversions back to a Snap Pixel through the Conversions API.

### Changed

- A/B tests: a checklist before you start says what would stop a test measuring anything, such as pages that don't show the flag yet, and one click ships the winner to everyone and ends the test.
- A/B tests are registered when they start: the hypothesis, goals, plan and the flag's versions and shares are kept as they were, and the hypothesis can no longer change. If the flag is changed while its test runs, the results say exactly what changed, so a test can't quietly measure two different things.
- Make your own attribution models and define your conversions in the web app, under Analytics → Channels: windows per channel, channels left out and weights for a model, and for a conversion its event, conditions, value and each ad network's name for it, or start from a preset.
- Site audits have full crawl settings: how deep to go, whether to read sitemaps, follow nofollow links or ignore URL parameters, which pages to include or leave out, whether to run pages' scripts (through your own web data connection, on a proved site), and the hour, day and time zone a scheduled crawl starts. Each crawl shows the settings it ran with.
- Removes the database tables left over from agency accounts, which were removed in v0.26.0.
- Get started and Home now offer "Follow the builder": follow the person building Outflare in public on X. Feedback posted to X greets the builder and tags @outflareai.
- Journeys can send a transactional email, record a person's consent to a topic, assign an owner (one member, or spread evenly across several), and open or move their deal to a stage. A deal only ever moves forward, never over a stage a person set.
- CRM → Lead finder is rebuilt for big runs: a front page with what needs you and each search's results, and runs whose companies and people load a page at a time with filters and bulk actions. You can also pause a run and keep people out of its campaign.
- Meetings are now events on each person's timeline: meeting.booked, meeting.held, meeting.no_show and meeting.canceled, from your calendars, CRM, the app or the API. Add meeting.booked or meeting.held to your conversion events to credit booked or held calls to the channels that brought them, or start a journey on one.
- Partners: partners who haven't given payout details are easy to find ("No payout details" on the Partners tab, and a `payout_details` filter in the API), and the portal asks them to add theirs. Imported partners (Tolt's API has no payout details) keep their balance until they do, instead of getting a payout nobody can send.
- Site audits start from your selected brand: its website is filled in, and Outflare first looks for a proof you already have (Search Console owning the site, or your Outflare tracking tag on it), so most sites are proved with nothing to add and get the larger crawl limits. You can now change a site's start address in its settings.
- Visitors who haven't consented to analytics keep one id for their whole visit, in the tab's session storage and never in a cookie, so every page counts as the same visitor and shows them the same A/B test version. It is gone when the tab closes.
- Website tracking shows one script per brand with a copy button. Edit sites changes which domains it records and which query parameters it leaves out, in place. Page views now keep the whole query string and the host, so reports can filter on any parameter; sign-in tokens, passwords, codes and email addresses are always left out.
- Partners: New flow starts from a template: one-time, recurring, first 12 months, "10% of the first sale, then 2% for 12 months", or a fixed amount per sign-up.
- Partners: reward rules are now flows, as in Tolt. Each has AND/OR conditions on the partner, the customer and the transaction, its own approval (automatic, after N days or manual), whether it adds to other cash flows or competes with them, other rates for some partners or plans, and can move the partner to another group. A Flows tab edits them. Existing rules keep working unchanged.
- Redirect links: the website tracking snippet ties each click on a redirect link to the visit it started, and every link and campaign shows how many of its clicks reached your site.

### Fixed

- Partners: a program's figures count every partner, not just the first 500, and the partner, conversion, reward and payout lists load 100 at a time with Load more.
- Partners: the Tolt import shows how many of each kind it read beside how many Tolt reports, asks Tolt once more when a list seems to stop early, and says how many partners Tolt's list left out and whether any of them has referrals. A promo code whose partner the import can't find is listed with its field names, and a partner Tolt won't give by id with Tolt's answer.
- Partners: the Tolt import also brings over partners Tolt leaves out of its partner list (those who haven't finished onboarding or were only invited), reading each one that a link, customer or commission names.

### Upgrading

- runs migration 0216.
- migration 0284 drops the `agencies`, `agency_members`, `agency_tokens` and `agency_events` tables and the `workspaces.agency_id` column. Take a backup first if you want to keep that data.
- runs migration 0220.
- `OUTFLARE_BUILDER_URL` sets the builder's X profile (by default Outflare's founder); `off` leaves it out.
- runs migration 0219.
- runs migration 0212.
- to offer one-click Slack sign-in, set `CONNECTOR_SLACK_CLIENT_ID` and `CONNECTOR_SLACK_CLIENT_SECRET` from a Slack app of your own. Without them, people connect with their Slack app's bot token.
- runs migration 0218.
- runs migration 0304.

## [0.43.0] - 2026-10-29

### Added

- Exclusion groups for feature flags: flags in one group split its visitors, so no visitor is in two of their A/B tests at once. The A/B test checklist warns when another test runs at the same time outside the group, and a new interactions check shows whether two overlapping tests changed each other's results.

### Changed

- A/B tests: preview links show you each version on the pages that run the test, without counting you, and you can mark each version as looking right or broken. A broken version stops the test passing its checklist, and a check expires when the version's content changes.

## [0.42.0] - 2026-10-29

### Added

- Partners: `outflare.signup(email)` in the website script credits a partner with a sign-up straight from the browser, for sites whose backend reports nothing. It pays no sign-up reward unless the program allows it. Your backend can also credit a sign-up by the visitor's `anonymous_id`.
- Support inbox: mark a connected mailbox for support and customers' emails become conversations your team assigns, tags, notes, answers in the same thread and closes, with first-reply and resolution times. Suggest a reply drafts an answer from your knowledge, only when you ask.

### Changed

- A/B tests reach an answer sooner: new tests adjust for what each visitor did in the 28 days before they joined (variance reduction, known as CUPED), which removes noise without changing the expected result. Each comparison says how much noise was taken out.

## [0.41.0] - 2026-10-29

### Changed

- Send a conversion you've defined back to an ad platform, not only a raw event: only the events that meet its conditions go (a call over a minute, an order over $100), worth what it says and named as that network expects.

## [0.40.0] - 2026-10-29

### Changed

- CRM → Lead finder shows meetings: how many people each search found went on to book one, with the cost per meeting.

## [0.39.0] - 2026-10-29

### Added

- Journeys can add people to a Smartlead, Instantly or EmailBison campaign, with their name, company and campaign variables. People who are suppressed, already in the campaign or left out by the sequencer are skipped, and the run history says why.

### Changed

- New partner setting, for every program: "Clear payout notes when a partner is deleted" (Partners → Payouts, on by default). When a partner's record is deleted for good, their payouts stay, but each payout's note and reference, which can hold their name or email, are cleared too. A deleted partner's links keep leading to your site.

## [0.38.0] - 2026-10-29

### Added

- Journeys can call webhooks and post to Slack. A webhook step calls any URL (your own system, Zapier, n8n or another tool's API) with the person's details filled in, retries when the receiver is down, signs every call so you can check it came from Outflare, and sends the receiver's own key, stored encrypted. Its answer can be saved and used by later steps, in branches, emails and field updates.

### Changed

- Partners: a Groups tab to add, rename and remove a program's groups, give a group its own commission rate, and pick the default group new partners join. Move partners between groups in bulk from the Partners tab, or through the API and MCP.

### Upgrading

- runs migration 0263.

## [0.37.0] - 2026-10-29

### Changed

- Share a journey with another workspace: Export as a kit writes it out, with the lists, emails and rules it uses, and Apply a kit file on the Playbooks page sets it up there as drafts. Tick the events the other workspace may call something else, and it names its own when it applies the kit.

### Fixed

- Trustpilot and Capterra review pages are now read as web data jobs that can take as long as the page needs, without sending a page twice. A page behind Cloudflare is tried again the same day instead of waiting a day, Capterra no longer asks for a second page that doesn't exist, and a first read that stops partway reads the history again.

## [0.36.0] - 2026-10-29

### Added

- The automation map shows every rule and journey in one picture: what sets each one off, what it does next, and how often in the last 7 days. It also flags what needs a look: loops, two journeys emailing the same people within a day, events nothing uses, and live journeys that never start. Find it under Lifecycle, or ask your AI app through MCP.
- See what each ad campaign brought by Outflare's own count: tracked visits, conversions and revenue credited under your attribution model, beside its spend and what the platform claims. Campaigns that spend without tracked visits are flagged, so a missing tracking template is easy to spot.
- Knowledge answers: ask your knowledge base a question and get a short answer from your own AI model with the articles it used, for your team or for customers (who only ever hear what you've marked for them). Agents and missions now check your knowledge before stating facts, and find it by meaning as well as by words.

### Changed

- Partners: reward webhooks now carry the sale behind them (its amount, the charge id your backend sent, and the customer's own user id), plus when the hold ends, and `partner_payout.paid` lists the rewards it paid. A backend that handled Tolt's commission webhooks finds the same facts without extra calls.
- Feature flags and A/B tests: a page your server renders can hand its flag decisions to the website script, so visitors see their version in the first paint with no flash of the original. The SDKs build the tag. The A/B test checklist now shows how long visitors waited for their version, and says what to do when some may have seen the original first.
- The last of projects is gone from the database: briefs, daily caps, reply rules and tokens moved to brands and sequences in an earlier release, and nothing has read projects since.
- A/B tests: when a test ends, your own AI model writes what it taught, and a Learnings library lists every finished test, searchable, with questions answered over them, such as "what have we learned about headlines?".

### Upgrading

- this release deletes the projects table and every project link. Take a database backup first if you want to keep a copy of the old project rows.

## [0.35.0] - 2026-10-29

### Changed

- Link building has a starter sequence for pitches, ready in one click, with your AI's opener in the first email and one follow-up. Pitches waiting for your approval now show as a count on Link building and its Pitches tab.

### Fixed

- Partners: which cash reward rule pays no longer depends on the rules' order. A first-sale rule beats an every-sale one on a tie, so 10% on the first sale with 2% on every sale pays 10% then 2%, in either order. "First months" rules now take any number of months in the program settings.

## [0.34.0] - 2026-10-29

### Added

- Stories: write a press release for each brand with the facts behind every number and quotes your experts approved. Outflare checks that every number has a source and every quote was approved, flags hype and finance or health claims, and a person approves the release before it is pitched.
- Apple Ads (formerly Apple Search Ads): connect your own Apple Ads API user to read each ad account's campaigns and daily spend, impressions, taps and installs, for App Store and Apple Maps campaigns. It only reads; installs are measured in your app, so nothing is sent back.
- Amazon Ads: sign in with Amazon to read each advertising profile's Sponsored Products campaigns and daily spend, impressions, clicks, purchases and sales, in every marketplace and region the sign-in reaches.
- Outbrain Ads: connect your own Outbrain login with Amplify API access to read each marketer's campaigns and daily spend, impressions, clicks and conversions. It only reads; Outbrain's API has no way to send conversions back.

### Changed

- CRM → Lead finder can send the people it finds into a campaign. Pick one when you start a run: it is saved for that ideal customer profile. People then wait under "Ready for campaign" with an "Add all to campaign" button, or go in by themselves once you turn on "Add people without asking". Unconfirmed emails always wait for you.
- Conversions sent back to TikTok and Snapchat now also match on their pixels' own cookies (`_ttp` and `_scid`), read only once the visitor allows marketing, and the tracking templates for TikTok, Snapchat and Pinterest show which connector sends their conversions.

## [0.33.0] - 2026-10-29

### Added

- Taboola Ads: paste your own Backstage API client id and secret to read each advertiser account's campaigns and daily spend, impressions, clicks and conversions, and send your conversions back to Taboola's event-based conversions as server-to-server actions, matched on its click id.
- Test a journey before it reaches anyone: pick a person, and the flow lights up the path they would take, with each step saying what it would do, the subject of every email they'd get or why they'd be skipped. Nothing is sent or saved. Send any check the other way to see that path too. Also over the API and MCP.
- Pinterest Ads: sign in with Pinterest to read each ad account's campaigns and daily spend, impressions, clicks and conversions, and send your conversions back through Pinterest's Conversions API with your own conversion access token.
- Redirect links: give a campaign link a short address, on your tracking domain, that counts each click before sending people to the tagged page. Use them where the tracking snippet can't run, such as bio links, partner pages and QR codes, per link or for a whole campaign. Link scanners and previews aren't counted.

### Changed

- CRM → Lead finder shows the people found at each added company: where each one came from, their email, and whether they were added to your CRM or stopped by your opt-out list. Settings has a switch to also contact a company's shared inboxes, such as info@ or sales@.
- Partners: program settings have an Install section with the website snippet for HTML, Next.js and Google Tag Manager, with your site key filled in, and the backend calls that report sign-ups and sales.
- Partners: Import from Tolt lists the programs in your Tolt account to pick from, instead of asking for a program id, and picks the only active one by itself.
- Projects are gone from the API and the MCP server: brands and sequences replace them. A brand's mailboxes now send for any of its sequences, a project's reply rules became its sequences' rules, and outcome reports group by sequence or brand. Older clients that still send `project_id` keep working; it is ignored, except on new API tokens and reply rules, where it is refused.
- Deleting a partner's record now keeps what they earned and were paid: their rewards and payouts move to an archived "Erased partner" record with no name, email or payout details, so commission totals and payout history still add up. Their referrals and portal sign-ins are still erased.
- Monitoring now shows how well paid ad clicks are tracked on each network: with the network's click id, with only the tracking template's ad ids, or with neither, plus template macros a network left unfilled. Two new info alerts flag a network whose ads mostly arrive untracked or whose macros don't fill in.
- Partners: approve or reject many held rewards at once. Pick them on Rewards or approve every pending one in a program; through the API and MCP, by ids or for a whole program or partner.
- Partners: give one partner their own commission rate (Commission rate in their menu, or a reward rule with `partner_id`). It replaces the program's rate and their group's for them.
- Partners: a person's record page, and their panel in Visitors and Leads, show the partner programs they are in (code, status, sales and what is due) and the partner who referred them.
- Partners: people and API tokens limited to some brands can now run their brands' partner programs, with their partners, rewards and payouts. They read programs shared by every brand without changing them, and never see another brand's.

### Fixed

- Partners: the Tolt import reads Tolt's current API (v2), so referred customers come over, and partners of every status (pending, rejected and suspended too) are read. Tolt's API has no payout details, so partners give theirs on the portal.
- Trustpilot and Capterra review pages are now read in a browser from the first try, and your web data connection gets up to 60 seconds per page, so pages behind a bot check come back instead of timing out, and each page is sent to your web data connection once.
- Partners: the Tolt import now reads Tolt's customers list. A kind it can't read is noted as `unreadable`, with Tolt's HTTP status and the shape of its answer, instead of stopping the run, so transactions and commissions still come over.
- Pages and API responses built from several lookups now always show one consistent moment. Before, a cookie scan could briefly show as finished with none of its cookies listed.
- Tracking templates now match each ad network's own docs: Quora's template carries its campaign, ad set and ad ids, Outbrain's adds campaign and ad names, Taboola's no longer repeats the click id Taboola adds itself, and LinkedIn's explains that its values are chosen as dynamic values on the campaign.
- Partners: the Tolt import keeps Tolt's own customer id (`cus_...`) on each referred customer, so renewals your backend reports with it still credit the partner (run the import again to add it to customers already imported). An import whose pages don't advance now stops with `pagination_stuck` instead of reading the first page forever.

### Upgrading

- API tokens limited to one project are now workspace tokens with the same scopes, so each can see and change the whole workspace. Review them on the API tokens page and revoke any that should stay narrow.
- runs migration 0262.

## [0.32.0] - 2026-10-29

### Changed

- News searches, competitor looks, SEO sites' weekly competitor and backlink reads, and scheduled lead list refreshes that fail are now tried again soon (after 15 minutes, then 1, 2 and 4 hours) instead of waiting for their next scheduled run.

## [0.31.0] - 2026-10-29

### Added

- Eight link building playbooks, one for each way of earning links: competitors' links, broken links, lost links, competitors' dead pages, resource pages, promoting a new page, unlinked mentions, and partners. Outflare can also now search Google on your own DataForSEO or web data connection.

## [0.30.0] - 2026-10-29

### Changed

- Projects are leaving Outflare: everything now sits in a brand and its sequences. Each project's brief was added to its brand's context (or the company's), sequences in paused projects were paused, and a project's daily cap was split over its sequences, which now have their own daily send limit. Lead lists pick an ideal customer profile to score against.

### Upgrading

- in the release that removes projects, API tokens limited to one project become workspace tokens and keep their scopes, so each can then see and change the whole workspace. Review them under Settings → API tokens and revoke any that should stay narrow.

## [0.29.0] - 2026-10-29

### Added

- Knowledge: one place for what your business knows. Write articles and quick answers (a question and its answer) in folders, per brand or shared, say who may see each one (public, customers through AI, or your team only), and get every version back. Agents can search and add to it over the API and MCP.

## [0.28.0] - 2026-10-29

### Added

- People and Companies in the web app (CRM → People): search and filter records by brand and field, see each one's timeline, history, emails, links and consent, edit them, and merge or dismiss duplicates. Deleting a person now erases them and their activity for good, for erasure requests (also `DELETE .../records/{record}` and `delete_record`).

## [0.27.0] - 2026-10-29

### Added

- TikTok Ads: sign in with TikTok For Business to read each ad account's campaigns and daily spend, impressions, clicks and conversions, and send your conversions back to a TikTok pixel through the Events API.

### Changed

- A/B tests: add guardrails a winner mustn't hurt, get an alert when a test is safe to stop, its split breaks or a guardrail is hurt, freeze a test so new visitors stop joining, and duplicate a test. Visitors from your own company are left out.

## [0.26.0] - 2026-10-29

### Changed

- Agency accounts are gone for now, to keep Outflare simple: a workspace is one company, with its brands inside it. The Agency page, the `/v1/agencies` API, agency tokens (`ofa_`), the MCP tools `list_clients`, `use_client` and `create_client`, and agency white label are removed. Client workspaces stay, with their direct members and data.

### Upgrading

- agency tokens stop working, and people who reached a workspace only through an agency lose access to it; add them as members of that workspace first. No migration runs; the agency tables stay until a later release.

## [0.25.0] - 2026-10-29

### Added

- Journalists: the news watch now reads each new article's byline, finds the author's address at the outlet with the free email finder, and keeps them as a person record with what they wrote. Make a media list of the journalists who cover a topic as a smart list that keeps itself up to date.
- Pitch link prospects from Outflare: your own AI writes an opener for each page, you approve it, and the contact goes into one of your sequences. Replies are read for you, agreed links are checked until they're live, and results show live links per pitch.

### Changed

- Partners: affiliates can get each conversion sent to their own tracker with a postback URL (set on the portal or by your team), and due payouts download as CSV with each partner's payout details for your payout tool.
- A/B tests: break results down by device, source, country, or new and returning visitors, corrected so a lucky group doesn't mislead, and see each version's lift over the original day by day.
- Analytics, Search Console, conversions, ads and SEO snapshot syncs, weekly dead-page link reads, scheduled crawls and AI visibility runs that fail are now tried again within minutes to hours (15 minutes, then 1, 2 and every 4 hours), instead of at their next scheduled time.

### Upgrading

- runs migration 0256.

## [0.24.0] - 2026-10-29

### Added

- Journeys now open in a visual flow builder: the trigger at the top, steps running down, checks splitting side by side, live numbers on every step, and problems marked before you go live. Live journeys change through a draft you publish, and people already in one can finish on their version. A new Rules page reads each rule as a sentence, dry-runs it, and starts a journey from it.

## [0.23.0] - 2026-10-28

### Changed

- Missions can aim at revenue partners refer: credited partner sales less refunds, in whole US dollars, in the mission's brand's partner programs since it started.
- Partners: import a program from Tolt with your own Tolt API key. Partners keep their link codes, so shared links keep working, and their promo codes, referred customers, sales and commissions (paid ones as paid payouts) come over too. Run it again until the day you switch: each run adds only what's new.

### Upgrading

- runs migration 0248.
- runs migration 0247.

## [0.22.0] - 2026-10-28

### Added

- Cookie policy that keeps itself up to date: Outflare scans your site and learns from visitors' browsers which cookies and scripts it uses, matches them to known vendors, flags unknown ones for review, and publishes a versioned policy for your own page, a hosted page, the API and MCP, asking visitors again when a new vendor needs consent.
- Partners: run referral, affiliate and influencer programs. Partners get ref codes, short links, promo codes and a portal (or a widget in your app); your backend reports sign-ups, sales and refunds by API or postback URL (each also counted in attribution); rewards (cash, fees, or actions your own system grants, for one or both sides) wait out a hold, then are approved and marked paid.

### Changed

- A/B tests can now be judged on revenue per visitor, deal value or how many times visitors do something, with a conversion window and a cap per visitor. Tests with several versions are corrected for the extra chances of a false winner, and goals after the first are marked exploratory.

### Fixed

- Reviews now reads G2 and Capterra: G2 pages are always read in a browser to get past its bot check, and Capterra reviews are kept with their dates and the vendor's replies instead of only the score.

### Upgrading

- runs migration 0235. Set `OUTFLARE_PARTNERS_DOMAIN` (with wildcard DNS and a wildcard certificate) to serve partner portals at `<subdomain>.<that domain>`; without it, portals are at `/p/<program>/` and on programs' own domains.

## [0.21.0] - 2026-10-28

### Added

- News watch: save news searches that run on a schedule through your own web data account, follow a topic and who writes about it, and keep every article about your brand as its coverage, on the outlet's company record, to confirm or dismiss.
- Consent banner: the website script now shows your cookie consent banner itself. It follows each region's law (ask first in Europe, opt-out in the US), Global Privacy Control, Google Consent Mode v2 and the Meta, TikTok and Microsoft signals, holds back tags until they're allowed, and keeps each visitor's choice as a consent that joins their record when they sign up.
- Link building checks each prospect's page for you: whether it's up, whether it already links to you, which of its links are broken (with what the Internet Archive kept), what your own AI makes of it, which of your pages to offer, and who to contact. Lost links can be added as prospects to win back in one click.

### Changed

- A/B tests: see how long a test will take on your real traffic before you start it, get test ideas and new versions of your copy from your own AI model, and a plain-English summary of results. The lift range now always agrees with "safe to stop", and a chart shows each version day by day.
- Companies added by a tool, such as an extension finding companies, now record which tool added them, like people already do.
- CRM → Lead finder shows how far each run has got, step by step: how many companies are waiting, being read, judged or reviewed, which were set aside and why, and which step is slowest. A run that pauses (at its page or AI limit, or for a connection to fix) says why, and Resume carries it on, raising the limit first if needed.

### Fixed

- Review pages, social profiles and rank checks that fail to read are now tried again the same day (after 15 minutes, then 1, 2 and every 4 hours), so day-by-day numbers have no gaps. Review pages showing a bot check are read again in a browser. Each says why its last read failed, with a Try again button.

## [0.20.0] - 2026-10-28

### Changed

- Track a keyword's Google rank straight from the Keywords page: every search in Queries, Cannibalisation and a page's breakdown has a Track button, and keywords you already follow show as Tracked.
- Missions can aim at revenue in US dollars: either payments credited to the campaign links in the mission's sequences, or won deals tagged with the mission. Progress shows both.
- Digests now have a Channels section: visits, conversions, revenue and cost across every channel compared with the period before, and the three channels that brought the most conversions.
- Home now shows the last 28 days of visits, conversions, revenue and cost across every channel, below Needs you, once your site has visits. Select By channel to open Analytics → Channels.
- Sequences now tag links to your own website with a campaign link, like broadcasts and journeys do, so visits and revenue from cold email show under Outbound email in Analytics → Channels. Links to other sites and links you tagged yourself stay as written.

## [0.19.0] - 2026-10-28

### Added

- Rules: turn what happens to your customers into business events of your own, such as "reactivated" or "stalled after sign-up". A rule fires when an event happens, a field changes, something doesn't happen in time or someone joins a smart list, once per episode, and journeys and webhooks act on its event. Try one first with a dry run. API and MCP for now.

## [0.18.0] - 2026-10-28

### Added

- Publish the brand's own posts on X and LinkedIn: write one or have your own AI draft it, approve it, and it goes out now or at a set time. A recurring series, such as a daily changelog summary, is approved once and then posts what your script hands in, up to a daily limit.
- Link building: one list of pages that could link to your site, from every way of earning links, scored and followed from pitched to live, one pitch per website at a time. See who links to any page or domain from your own Ahrefs or DataForSEO, and which links point at competitors' dead pages, read weekly within a budget share you set.
- A/B tests: pick a feature flag, choose what counts as success, and see which version wins, with the chance each one beats the original, the likely size of the lift, and a warning when the split looks wrong. Results are safe to check every day.

### Changed

- Rank tracking can now check Google through your own web data connection as well as DataForSEO, at about $0.005 for the top 100. With both connected, choose which one checks each keyword on the Rankings tab.

## [0.17.0] - 2026-10-28

### Changed

- Mailboxes → Domains can now add a domain's missing MX, SPF, DKIM and DMARC records in one click for Google Workspace and Microsoft 365: paste the DKIM key from your admin console, then approve the records once at your DNS provider or add them through your Cloudflare connection. The approval also fixes an SPF record that is missing your mail host.

## [0.16.0] - 2026-10-28

### Changed

- Add a lead by hand on the Leads page with **Add lead**, and click any lead to see its details, edit its name, company, title, links and custom fields, or delete it.

## [0.15.0] - 2026-10-28

### Changed

- See every domain your workspace has set up in one list, with what each is used for (website, SEO, site tracking, cold email, Lifecycle email, tracked links) and what still needs a step, through `GET /domains` and the `list_workspace_domains` MCP tool. `GET /connections` (MCP `list_connections`) lists every connected account, mailbox sign-ins included.

## [0.14.0] - 2026-10-28

### Added

- Channels: see visits, sign-ups, revenue and cost for every channel under Analytics → Channels, credited by last non-direct touch over 90 days, with a switch to compare first touch, linear, 40/20/40 and time decay. Refunds come off the channel that got the sale. Also in the API and for agents.

### Changed

- A lead's name, job title, LinkedIn, phone and company now live on its person record, kept up to date as leads are imported, edited or enriched, without overwriting anything you typed. Cold email reads the person back, so a correction made in the CRM is what goes out.

## [0.13.0] - 2026-10-28

### Changed

- Every deal is now one CRM record. Deals logged for outreach or synced from HubSpot appear under Deals with their people and company, and deals made in the CRM, by a reply rule or by an Attio import count in outreach outcomes and reports.

### Upgrading

- existing deals move over when the engine starts; every workspace gets a Deal type, and a workspace's own `deal` type is kept and filled in.

## [0.12.0] - 2026-10-28

### Added

- DeepSeek is a new AI connection: connect your own DeepSeek key to plan and run missions on DeepSeek's low-cost models. It is tagged **Hosted in China**, because DeepSeek processes prompts, including any customer details in them, on servers in China. Its models are also on OpenRouter, Vercel AI Gateway and Requesty.

## [0.11.0] - 2026-10-28

### Changed

- Choosing what an analytics connection imports says why each site's brand was suggested, waits until every site has a brand, is shared or is left out, and can make a new brand from a site. With one brand picked, Analytics offers that brand's sites from connections it already uses. Records in no brand are listed under Company → Brands to move.
- App links name the brand after the workspace, like `/acme/northpeak/seo`, or `/acme/all/seo` for every brand; older `?brand=` links still open. Switching brand reloads the page, and an item from the other brand closes back to its list. A brand's handle can no longer be the name of a page.

### Fixed

- With a brand picked, every page shows only that brand: Home, SEO, analytics, ad platforms, site audits, mailboxes, warm-up, leads, meetings, deals, email domains and search. Websites read through a shared connection, like Ahrefs or Search Console, count for the brand that owns them.

## [0.10.0] - 2026-10-28

### Changed

- Tracking domain setup is clearer: each domain shows its steps and whose turn it is, and updates on its own. Domains on Cloudflare get a reminder to set the record to DNS only (grey cloud) and a link to the common mistakes.

## [0.9.0] - 2026-10-28

### Added

- The Connectors page shows who built a connector that Outflare doesn't look after: a **Verified** badge for a verified publisher, **Community** for everyone else, with the publisher's name when you connect it ([integrations/README.md](integrations/README.md#trust)).
- Email finder: under CRM → Email finder, propose a likely work email for someone at a company and check whether an address is good to send to. It learns the shape each company uses from your own contacts, so finding is free; checking an address uses your own connected verifier only when it has to. Also available over the API and to agents (`email_pattern`, `find_emails`, `verify_addresses`).
- Five more partner playbooks on the Creator partnerships board: find creators who already cover your competitors, sponsor newsletters and podcasts that pay back, seed your product to creators, renew the creator deals that pay back, and recruit affiliates and creators from your own sign-ups.

### Changed

- Added your DNS records by hand? Press "I've added them, check now" in brand setup or Get started, and Outflare looks for every record straight away and shows which are in place and which are still missing.

### Fixed

- Importing from Attio now shows its first screen within about half a minute on large workspaces, or a clear error if Attio doesn't answer, instead of loading forever.

## [0.8.0] - 2026-10-28

### Added

- Every DNS record a brand needs, in one list: the site's proof, its email domain's records and its tracking domain. Add every missing one with one button through your own Cloudflare, or approve them at your DNS provider one group after another.
- Get started's star and follow step asks, optionally, how you heard about Outflare: one tap from a short list.

### Changed

- Get started now sets up one brand from start to finish: the brand, your AI key (with why Outflare needs it), the website script and every DNS record in one step, the tools for your goal, star and follow, then a first mission. Home shows one set-up checklist at a time.

### Upgrading

- migration 0214 adds two columns to users.

## [0.7.0] - 2026-10-27

### Added

- Support access: a new switch in Account settings says whether your server's staff, such as Outflare Cloud's support team, may view your account to help you. A view is read-only, never shows your inbox or email content, ends after an hour, and is listed under the switch. It's on by default; turning it off ends a view straight away.
- People by name on the Visitors page: connect your own RB2B, Vector or Warmly, turn it on and accept the notice, then paste the webhook address into the tool. New people are added to your CRM tagged with the tool and held for review: hidden from the people list and never emailed until someone approves them.
- CRM → Lead finder: on Outflare Cloud, AI searches the web for companies that fit your ideal customer, reads their websites and tells you which fit and why, for you to review before they're added. It runs on your own web data account and AI key. Self-hosted servers show what it does and where to get it.
- Replies move deals: a reply rule can open or move a deal in Outflare's own CRM when a reply gets a label, such as "interested" to Qualified and "meeting booked" to Meeting. Deals only move forward, never out of won or lost, and never over a stage a person set.
- Indexability report: every page from the crawl, its sitemaps, Search Console and your SEO tool in one list, with whether it can be indexed and whether Google indexed it. Where the sources disagree it says which to trust, flags pages Google skipped, sitemap mistakes and orphans with how to fix each, and tracks it day by day.
- Playbooks can now set up what they need before their first run: a pipeline in the CRM, fields on people and email drafts, shown first and added only with your OK. The first one, Run creator partnerships, adds a Partnerships board for creator, newsletter and podcast deals.
- Feature flags: turn something on for the visitors you pick, or split them between versions of a page to see which works better. Each visitor keeps their version, your website and servers agree on it, and pages don't flicker. Find them under Analytics → Feature flags.

### Changed

- Tracking domains can get their HTTPS certificate automatically on servers that support it, and each tracking domain now shows whether HTTPS is set up (`https_status`). Links only switch to a tracking domain once its certificate is ready.
- Extensions can list their own AI calls as a tool on Settings → AI usage, with a name, a description and a monthly limit of their own, while using the model chosen for a built-in task.
- AI spend limits can now warn without stopping. Under Settings → AI usage, untick Stop for the workspace's monthly AI limit or any tool's limit: owners and admins still get the 80% and 100% emails, but AI calls carry on. Every limit still stops calls by default.
- **Follow us on X** now shows next to **Star us on GitHub** on Home and in Get started, opening Outflare's profile (@outflareai).

### Fixed

- Importing from Attio no longer gets stuck on the first screen for large workspaces. The preview reads your objects in parallel and stops counting after about 15 seconds, showing counts like "5,000+" when there are more.

### Upgrading

- migration 0227 adds two columns to tracking domains. `OUTFLARE_TRACKING_TARGET` optionally sets the host customers point their CNAME at, and `OUTFLARE_EDGE_SECRET` lets an edge proxy in front of tracking domains pass on the domain and visitor address.
- runs migration 0191. A self-hosted server has no staff of its own, so nobody can start a view unless an extension adds them.
- the item is on by default. Set `OUTFLARE_FOLLOW_URL=off` to hide it, or `OUTFLARE_COMMUNITY_LINKS=off` to hide both items.
- runs migration 0221.

## [0.6.0] - 2026-10-27

### Added

- Reply rules: once a reply has a label Outflare is sure of, it acts on it. A "not now" is snoozed until the date it gives (or 90 days), or its sequence restarts then; an out-of-office moves the lead's next email to the day after they're back; a referral or a "wrong person, talk to Sam" adds the person they name as a lead on the referrer's lists, offered as one click by default or added and enrolled automatically. Set them in the inbox under Rules, or per project or sequence through the API (`GET`/`PUT /v1/workspaces/{ws}/reply-rules`) and MCP (`get_reply_rules`, `set_reply_rules`, `add_reply_referral`). Labels the AI is unsure of wait for a person before any rule runs.

## [0.5.0] - 2026-10-27

### Added

- Paid ads can send conversions back to Google Ads, Meta, Microsoft Advertising and Reddit: add a rule on the Paid ads page (or `POST .../ads/conversion-rules`) and each new sign-up, payment or other customer event goes to the platform with the ad click and hashed email and phone, so its bidding learns from real results. Sends follow consent: in the EEA, the UK and Switzerland only with a yes, elsewhere unless the person opted out, or only with a yes if you choose. Reddit needs your own Conversions API token. Upgrading runs migration 0210.
- Smart lists: groups of people on your customer records that keep themselves up to date from rules, such as "plan is pro and paid in the last 90 days". They sit beside lead lists on the Lists page; pick the rules (a field, an event in the last N days, or being in a lead list), see the count as you go, and save. Outflare adds and removes people as they start and stop matching, at once on save and every 15 minutes after. API `/v1/workspaces/{ws}/smart-lists`, eight MCP tools, and Grafana panels and alerts for how fresh they are. Upgrading runs migration 0209.
- Email finding now learns each domain's address shape (first.last, flast, and so on) from your own leads and their verification results, so it can propose a likely work address for a new name at a domain you already reach — with a confidence score and no third-party spend. Finding and verifying run as a waterfall: your own first-party history and the learned pattern come first and cost nothing, and only a low-confidence, unprovable address rolls up to your own connected verifier (Bouncer and the like), which stays on your account. Every check it runs is logged per address so the finder keeps getting more accurate over time. Upgrading runs migration 0190. The pattern is learned and used per workspace; nothing is shared between workspaces. Call it through the API (`GET .../email-finder/pattern`, `POST .../email-finder/find`, `POST .../email-finder/verify`) or the MCP tools `email_pattern`, `find_emails` and `verify_addresses`.
- Connectors can now be added as a manifest instead of Go code: a short YAML file in the new `integrations/` folder (Apache-2.0) that names the service's https address, the key a person pastes and how its answers map onto Outflare's own types. Outflare runs it with no outside code, sends the key only to that address, and checks every manifest against recorded answers in CI. Bouncer and Reoon are the first two, with the same results as before; existing connections keep working. The catalog (`GET /v1/connectors`) now says who built each connector (`publisher`) and how far it is trusted (`trust`: `outflare`, `verified` or `community`) ([integrations/README.md](integrations/README.md)).

### Fixed

- The six-hourly ad sync now runs by itself again; before, ad accounts only synced when you pressed Sync now or added a developer token.
- Entries read from a sequencer's blocklist (EmailBison, Instantly) are filed as `blocklist`, which keeps people out of cold email only. They were filed as unsubscribes and do-not-contact, which also withdrew those people's consent to marketing email; upgrading (migration 0215) relabels them and removes those withdrawals. The Suppression page shows where each entry came from.

## [0.4.0] - 2026-10-27

### Added

- Usage limits for every paid connection: Ahrefs, DataForSEO, lead data providers such as Apollo, email verifiers and web data. Each connection's page under Connectors shows what Outflare's calls used today and this month, and takes a daily and a monthly limit in the provider's own unit (credits, API units, tokens) or in US dollars. At a limit Outflare stops calling (or only warns, if you choose), and owners and admins are emailed at 80% and 100%. The SEO budget is now the connection's monthly limit, and paid email verification and web data page reads during crawls are now booked in Outcomes. API `GET /connection-usage`, `GET`/`PUT`/`DELETE .../integrations/{id}/usage[/limits]`, webhooks `connection_limit.warned` and `connection_limit.reached`, MCP `list_connection_usage`, `get_connection_usage` and `update_connection_limits`. Upgrading runs migration 0205, which carries existing SEO budgets over.
- Meaning-based search: `GET .../search?mode=meaning` (and `mode: "meaning"` on the MCP tool `search_workspace`) finds records, replies, meetings and deals by what they mean, so "unhappy with price" finds "too expensive for us". It is on by default and uses the workspace's own OpenAI, Gemini, Mistral or OpenRouter key (Claude has no embeddings); its cost, usually cents a month, is its own tool in AI usage, and an admin can turn it off under Settings → Meaning-based search, which deletes its vectors. It needs the pgvector extension in Postgres: Outflare adds it where the database allows, and without it everything else works as before. Self-hosted with Docker: switch the database image to `pgvector/pgvector:pg16` and run `REINDEX DATABASE outflare;` once if you move from `postgres:16-alpine`. Upgrading runs migration 0201.
- **Playbooks**: ready-made recipes for growth, such as welcoming new sign-ups, answering interested replies and fixing SEO quick wins. Each one says what it needs and whether your workspace is ready, and starts as a mission draft you approve. Copy its prompt into your own AI app, or call `GET /v1/playbooks`, `POST /v1/workspaces/{ws}/playbooks/{id}/runs` or the MCP tools `list_playbooks`, `get_playbook` and `start_playbook`. Anyone can add one in `playbooks/` with a pull request. Upgrading runs migration 0195.
- Everywhere Outflare asks you to add DNS records (proving a site is yours, a tracking domain, a sending domain's records, an email provider's domain) now offers the same four ways: one click (your DNS host's own approval screen, or your Cloudflare connection), a prompt to paste into your own AI agent, a message for a developer, or doing it yourself. A cold email domain's missing SPF or DMARC record can now be added the same way from its page in Domains (`POST .../sending-domains/{domain}/dns`, `GET .../dns/approval`, `POST .../dns/approved`; `dns_install` and `dns_connections` on the domain; MCP `add_domain_dns`, `get_domain_dns_approval`). Upgrading runs migration 0206.
- New AI connections: xAI (Grok) and two more routers beside OpenRouter, Vercel AI Gateway and Requesty, each with your own key. On the Connectors page, routers are tagged **Router**, and OpenAI and xAI are tagged for subscription sign-in (ChatGPT plans, SuperGrok), which is coming: connect an API key for now. The catalog lists them as `tags` on each connector.
- Company lookups on the Visitors page: with your own People Data Labs or Snitcher key, and only once you turn it on, unknown visitors who accepted cookies are matched to a company from their IP address. The address is never stored, home and phone connections name nobody, and found companies stay off your CRM until someone adds them. Migration 0192.

### Changed

- Social also reads Bluesky, Threads and Mastodon accounts (followers), DEV authors (published posts) and Docker Hub namespaces (pulls across their images) every day, and GitHub accounts now count stars across their repositories instead of followers. Bluesky, Mastodon, DEV, Docker Hub, GitHub, Reddit and Discord are read from their public APIs, a token each through your web data account. Paste several profile addresses at once on Social → Accounts. A Discord channel link needs an invite link (discord.gg/…) instead to read members. Upgrading runs migration 0208.
- The SEO overview tells a brand when its tools are connected but not for it: an SEO tool shared with other brands only ("Not shared", with a button to share it), or a search console or analytics tool that has numbers for other websites of the workspace but not the brand's. It no longer asks you to connect a tool you already have. The API adds `not_shared` to `seo_status.state` and a `sources` object.

### Fixed

- A worker no longer picks up a job it has no handler for, so a job queued by a newer or extended version during a deploy waits for a worker that can run it instead of dying.

## [0.3.0] - 2026-10-27

### Changed

- The open-core decision record (ADR 5) and the releasing guide are shorter: they describe what this repository does, not the hosted service's own setup.

## [0.2.0] - 2026-10-27

### Security

- Removing someone or reducing their access (a lower role, or fewer brands) now needs the new `members.remove` permission, which only owners have by default. Admins keep everything else, including inviting admins, and an owner can give them this too under Settings → Members → Roles. API tokens and AI apps can never hold `members.remove` or `roles.manage`, so `DELETE /members/{user}` now needs a signed-in person.

- Connect-request links (the ones an agent sends so you can add a key without pasting it in a chat) now need you to sign in as an owner or admin of the workspace that asked. Anyone else is told the link isn't for them and sees nothing about the workspace. The page names who asked and warns to connect only when you expected the request.
- AI apps connected with OAuth make changes only inside a mission a person approved; outside one they read, and a write gets `403 mission_required`. Turn **Only inside missions** off for an app under Connect your AI to let it act directly (`require_mission` on `PATCH .../agent-apps/{id}`). Apps connected before this upgrade keep acting directly until they connect again or you turn it on. Upgrading runs migration 0148.
- Self-registered AI apps that Outflare doesn't recognise (anything but Claude, ChatGPT, Cursor, VS Code or an app on the person's computer) may only read until the server's operator checks them with `outflare verify-app <client_id>`; `outflare unverify-app` takes that back. Existing connections of such apps drop to reading the next time they refresh.
- A tracking domain now serves only click, open and unsubscribe links; everything else on it, sign-in included, answers 404.
- AI visibility cohorts count the competitors tracked on the SEO Competitors page for their brand, with one company's hosts grouped under its name and other names; a cohort's own competitors add to them (`tracked_competitors` on each cohort).
- API tokens now follow the person who made them: removing a member revokes their tokens, a lower role takes away the scopes it doesn't include (a token left with none is revoked), and limiting a member to some brands revokes their tokens that reach other brands. Tokens made by a disabled account stop working. Each change is in the audit log as `token.revoked` or `token.updated`. Agency tokens work the same way when their maker leaves the agency or gets a lower agency role.
- Connectors with a self-hosted address (Matomo, Umami, Plausible, PostHog, EmailBison and others) and mailboxes' SMTP and IMAP servers now only reach public internet addresses. If yours run on the same private network as Outflare, set `OUTFLARE_CONNECTORS_ALLOW_PRIVATE=true` when upgrading.
- Changes made with the session cookie are refused when the browser says they came from another site on the same parent domain (`Sec-Fetch-Site: same-site`), as they already were from other sites. Calls with an API token are not affected.
- Every workspace brings its own keys for every third-party API, connectors and AI alike, with no exceptions. At start-up the server now removes vendor keys such as `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` or `GOOGLE_APPLICATION_CREDENTIALS` from its own environment and logs that it ignored them, so nothing can run a workspace's work on the operator's account. Workspaces connect their own keys on the Connectors page.

### Added

- The Backlinks page works like Ahrefs' backlinks report: one link per domain (or per page, or every link), All, New or Lost over a history range, filters for dofollow, nofollow, UGC and sponsored, backlink type, DR, domain traffic, page type and a word or phrase, and columns for DR, UR, traffic, keywords, referring and linked domains, the anchor, and first seen, last seen and lost dates. The referring page and target URLs are shown in full and open on click, with any redirects the target goes through. Filtering reads the stored links, so it costs nothing. Ahrefs connections can also read each link's traffic (`link_traffic` in the SEO settings, about 25 more Ahrefs units a link). The new details fill in on each connection's next weekly read, or straight away with "Snapshot now".
- Get started has a skippable **Star and follow Outflare** step before the first mission, and Home shows the same two optional one-click items, **Star us on GitHub** and **Follow us on X**, to anyone who hasn't opened them. Opening the link ticks the item, since a star or a follow can't be checked; each person has their own and can hide them on Home (`GET`/`PATCH /v1/me/community`); the step is done for whoever opened both. Self-hosted servers turn them off (step included) with `OUTFLARE_COMMUNITY_LINKS=off`; **Follow us on X** shows only when `OUTFLARE_FOLLOW_URL` is set to the profile to open. Upgrading runs migration 0196.
- Live updates: a connection can now get its own webhook address, so a tool tells Outflare when something happens instead of being asked. Your web data connection is first: turning on live updates adds the address in your web data account for you (extract, batch and crawl finished), and every delivery's signature is checked. Repeats are ignored, each event is handled by a worker with retries, and the last 90 days are listed under Logs → Incoming webhooks, where a failed event can be run again. Needs the server reachable at a public https `PUBLIC_URL`. `GET`/`POST`/`DELETE .../integrations/{id}/hook`, `.../hook/rotate`, `.../hook/signing-secret`, `GET .../hook-events`, `POST .../hook-events/{event}/replay`; MCP `get_connection_hook`, `set_up_connection_hook`, `rotate_connection_hook`, `remove_connection_hook`, `list_hook_events`, `replay_hook_event` ([docs/connectors.md](docs/connectors.md#live-updates-webhooks)). New metrics (`outflare_hooks_*`), a Grafana row and three alerts. Upgrading runs migration 0197.
- Cal.com, Stripe and OpenRouter connect with a sign-in as well as a key. OpenRouter needs nothing on the server: signing in makes a key on your own OpenRouter account. Cal.com needs `CONNECTOR_CAL_COM_CLIENT_ID` and `_CLIENT_SECRET`; Stripe needs a Stripe App (`CONNECTOR_STRIPE_CLIENT_ID`, `_CLIENT_SECRET` and the app's install link in `CONNECTOR_STRIPE_AUTH_URL`). See docs/connectors.md.
- One-off DNS approval (Domain Connect): when a domain's DNS host supports it, the one-click button for site verification, tracking domains and sending domains sends you to your DNS host's own "apply these records?" screen. You approve the exact records once and Outflare keeps no access. Anywhere else the button falls back to connecting Cloudflare as before. Self-hosted servers keep the fallback unless they publish their own key. Upgrading runs migration 0202.
- One timeline per person and company across every module: `GET .../records/{record}/timeline` (MCP `get_record_timeline`) now adds website visits, opt-out list entries and journeys to product events, changes, consent, cold email, replies, meetings and deals. A company's timeline includes its people's (`people=false` to leave them out), each item says whose it is (`record_id`), and `brand` filters it.
- Who can be emailed, on every person: `contactability` on records says their contact basis (`opted_in`, `opted_out`, `customer`, `third_party` or `unknown`), the tool they came from (`acquired_via`), and whether receipts, cold outreach and marketing email may go to them. Third-party leads never get newsletters, product updates or journeys without their own consent. `GET`/`PATCH .../contact-rules` (MCP `get_contact_rules`, `set_contact_rules`) choose whether everyone else gets them unless they said no (`inferred`, the default, as before) or only people who agreed (`opt_in`). A broadcast that has not started shows its audience by basis and plain-words warnings, and a scheduled one with a warning is on Home's to-do list ([docs/opt-outs.md](docs/opt-outs.md)).
- One opt-out list with its history: each suppression says how it was added (`method`: `manual`, `automated`, or `ai_suggested` with the AI's `confidence` when it read a reply as an opt-out), who added it, from which tool (`source`) and connection, and filters by them. New reasons `competitor`, `customer`, `partner` and `legal`; customer and partner entries only keep people out of cold email. `POST .../suppressions` takes `source` and `method` for moving a list from an old tool. `POST .../recipients/check` takes `purpose`; with `lifecycle` it also answers by contact basis, with `third_party_no_consent` or `no_consent` for each person it refuses.
- The opt-out list is kept in step with EmailBison, Instantly and Smartlead: every platform sync reads their blocklist into it and writes it to theirs (Smartlead write only), so a send from either side honours both (`GET`/`PATCH .../suppression-syncs`, MCP `list_suppression_syncs`, `update_suppression_sync`). Upgrading runs migrations 0203 and 0204.
- One click for every DNS record Outflare asks for: a site's proof of ownership (Site audits, `POST .../seo-sites/{site}/dns`, MCP `add_seo_site_dns`) and a tracking domain's CNAME (`POST .../tracking-domains/{id}/dns`, MCP `add_tracking_domain_dns`) join sending domains and brand setup. **Sign in with Cloudflare** takes you to Cloudflare and back to the same domain or site, which then lists the exact records it will add, with one button to add them. Each record is then looked for on its own until it ticks off. `dns_install` and `dns_connections` on sites and tracking domains say how it went and which connections can do it. Upgrading runs migration 0194. Cloudflare sign-in needs a Cloudflare OAuth app with Zone read and DNS write (`CONNECTOR_CLOUDFLARE_CLIENT_ID`, `_SECRET`); it now asks for `dns.write` instead of `dns.edit`.
- MoltSets is a data connector. Paste your own MoltSets API key in Connectors to build lead lists from MoltSets people searches (title, seniority, industry, company size, country, company domain, keywords) and to find work emails from a LinkedIn URL, a name with a company domain, or an email, as a step in the email waterfall. Addresses MoltSets grades A or B count as verified, C and F as unverified, and D (spam trap or complaint) are never used. MoltSets bills your plan directly.
- A pipeline runtime for work that moves through stages one item at a time ([docs/pipeline.md](docs/pipeline.md)). Stage workers claim small batches fairly across workspaces, renew their leases, retry or wait per item, pause a run when it runs out of budget or loses a connection, and share rate limits and circuit breakers per outside account. New metrics (`outflare_pipeline_*`), a Pipelines row on the Grafana dashboard and four alerts show which stage is the bottleneck and why items stop. Upgrading runs migration 0175.
- Say what each brand does: brand setup now starts with "What does this brand do?", a searchable list of about 40 business models in 8 groups (B2B SaaS, online store, marketplace, agency, bookings, courses, publisher and more), and an optional "How do customers pay?" (subscription, one-off, usage-based, commission, advertising, donations, free). Change either any time. They decide which reports and funnels suit the brand. `GET /v1/business-models` (no sign-in), `business_model` and `revenue_models` on brands (`PATCH .../brands/{brand}`), MCP `list_business_models` and `update_brand`; a change is recorded as `brand.business_model_changed`. Upgrading runs migration 0189.
- Brand links are found for you: with a web data connection, Outflare reads each brand's website and suggests the social profiles and app store listings it links to, to add (correcting the address if need be) or dismiss under the brand's Links, with the common places it has no link for shown as gaps. X followers now come from each brand's public X profile through your web data account, with no X developer app; the X app stays for posts and their numbers. `POST .../brands/{brand}/links/discover`, `GET .../link-suggestions`, `.../add`, `.../dismiss`; MCP `find_brand_links`, `list_brand_link_suggestions`, `add_brand_link`, `dismiss_brand_link` ([docs/social.md](docs/social.md#finding-a-brands-links)). Upgrading runs migration 0181.

- **AI usage** (Settings → AI usage, `GET /ai-usage`, MCP `get_ai_usage`): every AI call Outflare makes with the workspace's own key is logged with the tool that made it (planning, agent turns, reply labelling, domain research, AI visibility, social replies, email verification, or an extension's own), its provider, model, tokens and cost, so you can see where your AI spend goes. Prompts and answers are never kept, only counts, for 13 months. Upgrading runs migration 0182.
- A monthly AI limit for each tool, on top of the workspace's (`PUT /ai-usage/limits`, MCP `update_ai_tool_limits`). Owners and admins are emailed when AI spend reaches 80% of a limit and when it reaches it, and the `ai_budget.warned` and `ai_budget.reached` webhooks fire.
- Connecting an AI model now asks you to confirm that your provider bills you for every call, that the spend is yours to manage, and that you should set a hard limit with the provider. Outflare pays for no AI.
- `email_verification` is a model task, so verifying addresses with AI can use a cheaper model (Claude Haiku 5.5 by default).
- Metrics `outflare_ai_calls_total`, `outflare_ai_tokens_total`, `outflare_ai_cost_cents_total` and `outflare_ai_budget_alerts_total`, an AI row on the dashboard and two alerts (a tool calling AI in a loop, many limits reached at once).
- Lifecycle sending domains: add the DNS records in one click through your own Cloudflare connection, or hand them to your AI agent or a developer with a ready-made prompt or message (`POST .../email-provider-domains/{id}/dns`, `ai_prompt`, `developer`; MCP `add_email_domain_dns`). Checks now ask the domain's own nameservers, so records show within a minute; Check now starts the every-minute checks again; each record says in words what looks wrong (domain typed twice, proxied CNAME, second SPF record); and a DMARC policy already in place is shown as it is. Upgrading runs migration 0174.

- Extensions can add sidebar items for some people only, with `Host.AddMenuLinks`. They arrive in `GET /v1/me` as `menu_links` (an empty list for everyone else) and show in the sidebar footer under Settings. The hosted Cloud uses it to show its staff console to staff.

- Editable and custom roles. Change what Admin, Member or Viewer allows, or add roles of your own, under Settings → Members → Roles (`/v1/workspaces/{ws}/roles`, MCP `list_roles`). Nobody can give a permission they don't have, or change someone who can do more than they can. Members and invites take `role_id` for a workspace's own role. See [docs/roles.md](docs/roles.md). Upgrading runs migration 0173.
- Owners can invite owners, and anyone but the last owner can leave a workspace (`POST /v1/workspaces/{ws}/leave`).
- Website tracking v2: visits (sessions) with landing page, referrer, tags, rough country, device and browser; first and last touch fields and a referral code on people; bot filtering; serving the snippet from your own domain with a server-set cookie; and visits across several domains (`data-domains`). Migration 0155.
- Website visitors: see which companies and people visited your site, with a score and a `visitor.became_hot` alert. Visitors are named from your product's sign-ins and from clicks on your own emails' links (a signed, one-browser token), never from bought data. Migration 0157.
- Proxy secrets for website tracking: a site that forwards tracking through its own domain can pass on each visitor's real address (`X-Outflare-Proxy` and `X-Outflare-Client-IP`), with per-visitor limits; the snippet now retries failed sends. Migration 0180.

- Ideal customer profiles (Company → Ideal customers, `/v1/workspaces/{ws}/ideal-customer-profiles`): describe who you sell to once, in words and as lead search filters, and every project, lead list fit score and the lead finder uses it. A project's ICP is now its profile's filters: a project given an ICP gets a profile of its own, `profile_id` picks another, and editing either side changes both. MCP tools `list_ideal_customer_profiles`, `create_ideal_customer_profile` and more. Upgrading runs migration 0160, which gives every project that has an ICP a profile named after it ([docs/ideal-customers.md](docs/ideal-customers.md)).
- Brand setup: each brand gets an ordered checklist under **Company → Brand setup** (the website script, verifying the website, then analytics, Search Console, email, payments, ads, social and a CRM), and Home shows "Continue setting up Acme · 3 of 9" until it's done or closed. Every step can be left for later and picked up again. The website script and verify steps each offer every way to do them: one click through your own Google Tag Manager (Outflare adds one tag in a Tag Manager workspace of its own and publishes it, or leaves it for you to publish) or your own Cloudflare (it only adds the TXT record, then checks for it every minute), a ready message to send a developer, a prompt for an AI coding tool, or the snippet itself. New connectors: Google Tag Manager and Cloudflare (sign-in or an API token). `GET`/`PATCH .../brands/{brand}/setup`, `POST .../setup/prepare`, `.../setup/install`, `.../setup/check`, `GET .../setup/tag-containers`; MCP `get_brand_setup`, `update_brand_setup`, `prepare_brand_setup`, `install_brand_setup`, `check_brand_setup`, `list_tag_containers`. Upgrading runs migration 0164. For one-click Cloudflare sign-in, register a Cloudflare OAuth app and set `CONNECTOR_CLOUDFLARE_CLIENT_ID` and `_SECRET`; Tag Manager uses your Google sign-in app (`GOOGLE_OAUTH_CLIENT_ID`) with the Tag Manager scopes added.
- Install health: Outflare now watches what is installed and tells you when something is out of date or broken: a site that never sent a visit or went quiet for two days, a page still running an old copy of the website script (the script now reports its version), an unverified site, a connection that needs signing in again, an SDK, CLI or MCP server below the supported version, and calls to a deprecated API operation. Problems show on the brand's setup page, on Home's **Needs you** list and in `GET /status` (`install_broken`, `install_outdated`), and opening or clearing one is a webhook (`install.issue_opened`, `install.issue_resolved`). `GET .../install-health`, MCP `get_install_health`. Upgrading runs migration 0165.
- Broadcasts, journeys and social replies tag their own links with a campaign: one per broadcast, journey or watch, listed under Analytics → Campaign links. Email links are tagged only when they go to one of your brands' own sites and have no `utm_campaign` already; social reply links now carry `ofc` too. See [docs/campaign-links.md](docs/campaign-links.md).
- **Analytics → Website tracking** puts Outflare's script on a brand's site in three steps: get it, add it (one click through your own Google Tag Manager, a message to a developer, a prompt for an AI coding tool, or paste), then watch for the first visit, which turns the page green. The Analytics page offers it until a site sends its first visit; keys stay under Settings too. A site key's origins take a plain domain (`example.com` covers the site and every subdomain), and http only from localhost.
- Website events are queued before they are stored: `POST /v1/site/events` checks the key, limits and each event's shape, writes the batch to an inbox and answers, and the workers store it in order, as of when it arrived. A slow database, an import or a worker deploy only delays events, never loses them; a batch that keeps failing is set aside with its error. New metrics `outflare_ingest_events_*` and `outflare_inbox_*` ([docs/scaling.md](docs/scaling.md)). Migration 0168.
- Run Outflare as separate services from one image with `OUTFLARE_ROLE`: `api` (app and API), `ingest` (website events and `site.js` on their own address) and `worker` (jobs and background loops), so a deploy or a busy app doesn't drop website events. The default, `all`, keeps one process doing everything ([docs/scaling.md](docs/scaling.md)). `outflare_build_info` gains a `role` label.
- Monitoring: set `OUTFLARE_METRICS_ADDR` (such as `:9091`) and the server serves Prometheus metrics on that private address: requests and latency per route, the job queue's depth and oldest waiting job by kind, job run and wait times, the database pool, Postgres connections, locks, cache hits and largest tables, and CPU, memory and goroutines. `OUTFLARE_METRICS_TOKEN` asks scrapers for a bearer token and `OUTFLARE_METRICS_PPROF=true` adds Go's profiler. Docker Compose turns it on inside its own network, and `--profile monitoring` starts Prometheus and Grafana with an Outflare dashboard and alert rules ([docs/monitoring.md](docs/monitoring.md)).
- Campaign links (Analytics → Campaign links, `/v1/workspaces/{ws}/tracking-campaigns`): one taxonomy of nine channels, each with a fixed `utm_medium` and the usual sources, and a builder that tags any page with `utm_source`, `utm_medium`, `utm_campaign`, `utm_content`, `utm_term` and a short campaign id, `ofc`. Spellings are merged (`fb` and `Facebook` are both `facebook`) and tags never change once a campaign exists, so reports stay clean. New `analytics.manage` scope for making campaigns and links; owners, admins and operators have it. MCP tools `get_tracking_taxonomy`, `list_tracking_campaigns`, `create_tracking_campaign`, `create_tracking_link` and more. See [docs/campaign-links.md](docs/campaign-links.md).
- Connections shared with brands: connect a tool once for the workspace and every brand uses it, or only the brands you pick; a brand with its own account (say, its own Plausible) connects it on that brand, and it replaces the shared one for that brand alone. Choose under **Connectors → More → Choose who uses it**, or when connecting while one brand is picked. In the API, integrations carry `brand_id` and `brand_ids`, `GET /integrations?brand=` lists what one brand uses, and `POST`/`PATCH /integrations` and the OAuth `authorize` link take them (MCP `list_integrations`, `update_integration`). An analytics site read through a brand's own connection counts for that brand ([docs/connectors.md](docs/connectors.md#brands)). Upgrading runs migration 0145.
- Scale product analytics past Postgres: set `OUTFLARE_CLICKHOUSE_URL` and every product event is copied to ClickHouse within about a minute, edits and deletions included. Set `OUTFLARE_S3_*` to an S3-compatible bucket (Cloudflare R2, Amazon S3, MinIO) and `OUTFLARE_EVENT_RETENTION_DAYS` to move events older than that out of Postgres into compressed files in the bucket. All optional; without them nothing changes. Migrations 0149 and 0150 add `customer_events.updated_at` and the mirror and archive tables ([docs/analytics-storage.md](docs/analytics-storage.md)).
- Keep your server's own usage events in one of your workspaces: set `OUTFLARE_EVENTS_WORKSPACE` to its id and every action, API and MCP call, and error is stored there as a product event (source `outflare`), with the people who use your server as customer records identified by their account's email. It works with or without PostHog ([docs/product-analytics.md](docs/product-analytics.md#keep-them-in-a-workspace)).
- Regressions: after every Search Console sync, rank check, conversions sync and crawl, Outflare compares each SEO site's latest numbers with the ones before and lists what dropped on the Keywords page (Drops): a page's search clicks or conversions well under its weekly average, a tracked keyword that fell five places or more, a page out of Google's index, or the site's health score down ten points. Each is a fix on the fixes list until it recovers, so the fix webhooks announce it (`GET .../seo-sites/{site}/regressions`, MCP `get_site_regressions`) ([docs/search-intelligence.md](docs/search-intelligence.md)). Upgrading runs migration 0101.
- Link spam scoring: every backlink gets a spam score from 0 to 100 with the reasons (your SEO tool's own judgement, weak or spammy linking domains, spam anchors, link farms, sitewide links). Review a link or a whole domain as real or to disavow on the Backlinks page or with `PATCH /v1/workspaces/{ws}/backlinks/{id}` (MCP `review_backlink`), and download the disavow file of what you marked for Google Search Console (`GET .../backlinks/disavow`, MCP `get_disavow_file`). Nothing is disavowed on a score alone ([docs/seo.md](docs/seo.md#link-spam)). Upgrading runs migration 0104.
- Link opportunities on the fixes list (source `seo_links`): strong links lost lately to win back, links to pages your latest crawl found broken, strong domains that link to two or more of your tracked competitors but not to you, and toxic links nobody reviewed. They are checked with each weekly links read and after every review, and `get_fix_prompt` lists the links behind each one. Suggestions only: Outflare never contacts a site or disavows a link on its own ([docs/seo.md](docs/seo.md#link-opportunities)).
- Competitor gaps: the keywords your tracked competitors rank for that you don't (or rank well below), and the domains that link to them but not to you, from the weekly snapshot of the first five tracked competitors a site. **Keyword gaps** and **Link gaps** tabs on the Competitors page, `GET /v1/workspaces/{ws}/seo-competitors/gaps`, MCP `get_seo_competitor_gaps`, and a `gap_rows` connection setting (50 by default, 0 turns it off) ([docs/seo.md](docs/seo.md#gaps)).
- A Paid ads page in the app: spend, clicks, CTR, cost per click and platform-reported conversions by platform and campaign for 7, 28 or 90 days, each ad account's brand and sync switch, and a place to add a Google Ads or Microsoft Advertising developer token.
- One to-do list for every module: `GET /v1/workspaces/{ws}/status`, and with it Home's **Needs you** list, now also reports email sending domains the provider wouldn't verify (`email_domain_failed`), broadcasts that stopped this week (`email_broadcast_failed`), social replies waiting for approval (`social_drafts_waiting`), possible duplicate people (`duplicates_waiting`), new product event names held for review (`events_quarantined`) and CRM imports that failed (`crm_import_failed`), in the new areas `lifecycle`, `social` and `customers`.
- 34 more webhook events: customer records (`record.created`, `record.updated`, `record.merged`, `record.bulk_created`, `identity.verified`, `identity.closed`, `relationship.added`, `relationship.ended`), consent (`consent.granted`, `consent.withdrawn`, `consent.bulk_withdrawn`), brands (`brand.created`, `brand.updated`), broadcasts and journeys (`email_broadcast.*`, `journey.*`, `journey_run.entered`, `journey_run.exited`), site audits (`seo_site.*`, `crawl.started`, `crawl.stopped`, `crawl.done`) and social replies (`social_action.drafted`, `approved`, `rejected`, `done`, `failed`). `GET /v1/webhook-events` lists them.
- Conversions by landing page: with your own PostHog connected, the Keywords page (Conversions) shows, for each page, the visits that started on it and how many went on to a conversion event, overall and from search engines, beside its Search Console clicks, position and target keyword. Choose the events that count on the page or with `conversion_events` on `PATCH .../analytics/{id}/sites/{site}` (`GET .../seo-sites/{site}/conversions`, MCP `get_site_conversions`) ([docs/search-intelligence.md](docs/search-intelligence.md)). Upgrading runs migration 0100; PostHog connections start counting at their next sync.
- Home has one **Needs you** list: everything waiting on a person, worst first (red, then amber, then set-up, then the rest), with the next set-up step as one row and a check mark when nothing needs you. `GET /status` reports a new blocker, `fixes_waiting` (area `fixes`), when errors on the fixes list are open and nobody has taken them.
- Leads are people. Every lead is now a customer record (or joins the one that already has its email), and cold email, replies, meetings and deals point at the person, so their timeline (`GET .../records/{id}/timeline`) shows them beside their product events and lifecycle email, with the new kinds `message`, `meeting` and `deal`. Leads have a `record_id`. Merging two records moves all of it. Upgrading runs migration 0120; a background job links existing leads and activity, in batches, within minutes of starting ([docs/customers.md](docs/customers.md#leads-are-people)).
- An unsubscribe from any email, cold or lifecycle, is also a withdrawn consent to all marketing email on the person's record, in every brand. It stays until the person opts in again themselves (a consent with `source` such as `signup_form` or `preference_center`), which clears their unsubscribe suppressions; imports and CRM syncs never do. Upgrading runs migration 0121; existing unsubscribes get their consent, dated when they unsubscribed ([docs/customers.md](docs/customers.md#unsubscribes)).
- Rank tracking: follow where your site ranks on Google for the keywords you choose, or every page's target keyword, by country and device, checked weekly or daily through your own DataForSEO connection within its monthly budget (about $0.02 a check, recorded on Outcomes). The Keywords page (Rankings) shows each keyword's position, movement, best position and the page that ranks, and the history keeps the top 10 for every check (`.../seo-sites/{site}/rankings`, MCP `list_rankings`, `track_keywords`, `check_rankings`, `get_rank_history`) ([docs/search-intelligence.md](docs/search-intelligence.md)). Upgrading runs migration 0099.
- SEO experiments: did a change to a website's pages help? A holdout splits a site's pages into matched treatment and control groups, so a Google update or a seasonal dip isn't mistaken for the change; a before and after experiment compares pages with themselves. Measuring starts on the site's next production deploy or when you say, outcomes (clicks, impressions, CTR or position) come from the workspace's own Search Console data, and the result has the lift, a p-value and a verdict. `/v1/workspaces/{ws}/experiments`, MCP `create_experiment` and `get_experiment`, webhooks `experiment.started` and `experiment.concluded`, and a section in the digest ([docs/experiments.md](docs/experiments.md)). Upgrading runs migration 0108.
- Internal links worth adding: for each page with a target keyword, the Keywords page (Internal links) lists up to five other pages about that keyword that don't link to it yet, from the latest crawl and Search Console. Each is a fix on the fixes list that the next crawl confirms (`.../seo-sites/{site}/internal-links`, MCP `get_site_internal_links`) ([docs/search-intelligence.md](docs/search-intelligence.md)). Nothing to do when upgrading.
- Fix autonomy: how far agents may go with the fixes list on their own. Agents take a fix (`POST .../fixes/{fix}/start`, MCP `start_fix`) before changing the site, and in a mission the approval gate runs or holds it: copilot (the default) holds every fix, rules lets the crawl rules and categories you pick through, autonomous lets every fix through. Fixes that change redirects, canonicals, indexing, robots.txt or URLs always wait for a person, and changing the setting from inside a mission does too. Sites can have their own setting. A production deploy of a watched site now re-checks its fixes with a crawl. Fixes → Agent autonomy in the app, `/v1/workspaces/{ws}/fix-autonomy`, webhook `fix.started` ([docs/fix-autonomy.md](docs/fix-autonomy.md)). Upgrading runs migration 0109.
- Digests: a daily or weekly summary of the whole workspace (what needs a person, fixes, deploys, site health, outreach, lifecycle email and missions, each compared with the period before). Weekly by default, kept in the app and sent as the `digest.ready` webhook; emailed only through the workspace's own email provider, to owners and admins or every member, and anyone can turn it off for themselves. **Settings → Digests**, `/v1/workspaces/{ws}/digest-settings` and `/digests`, MCP `preview_digest` and `update_digest_settings` ([docs/digests.md](docs/digests.md)). Upgrading runs migration 0107.
- Social growth for X and LinkedIn: connect your own X developer app (signed in with OAuth 2.0 and PKCE) or sign in to LinkedIn, and Outflare reads followers every day and your own posts' impressions, likes, replies, reposts and clicks, for X accounts, LinkedIn company pages you administer and your own LinkedIn profile. A Social page, `GET .../social/accounts`, `/social/posts` and `/social/growth`, and six MCP tools show growth per channel and per brand. X bills its developer account owner per read, so reads stay small. Migrations 0133 and 0134 add the tables.
- Cannibalisation: the Keywords page (Cannibalisation) lists the searches Google shows several of your pages for, splitting the clicks, with each page's share and the page to keep. The 50 most shown, and pages that share one target keyword, are fixes on the fixes list, checked again after every Search Console sync (`.../seo-sites/{site}/cannibalisation`, MCP `get_site_cannibalisation`) ([docs/search-intelligence.md](docs/search-intelligence.md)). Nothing to do when upgrading.
- A keyword target for each page: set the search a page should rank for, or accept the query Search Console says brings it the most clicks. Every target is checked against the latest crawl, and a keyword missing from the page's title, H1 or description is a fix on the fixes list (source `seo_keywords`), checked again after every crawl. On the Keywords page (Targets), at `.../seo-sites/{site}/targets` and in the MCP tools `list_page_targets`, `set_page_target` and `accept_page_targets` ([docs/search-intelligence.md](docs/search-intelligence.md)). Upgrading runs migration 0098.
- Reddit, the first channel of the Social module: watches find posts where your brand can help (by phrase, in chosen subreddits or all of Reddit), the workspace's own AI drafts replies that end with a disclosure line, and a person approves each one before it posts from the brand's own account through the workspace's own Reddit app. Each reply's link is UTM-tagged, so visitors and sign-ups come back to it from customer events. No bought upvotes, other accounts or undisclosed posts. New scopes `social.read`, `social.manage` and `social.post` (people only), the `social_replies` AI task, the Reddit page, `/v1/workspaces/{ws}/social/...` and the MCP social tools. Upgrading runs migration 0116 ([docs/social.md](docs/social.md)).
- Site audits check AI crawlers: what robots.txt lets each AI search, user and training crawler (OAI-SearchBot, ChatGPT-User, GPTBot, Claude-SearchBot, ClaudeBot, PerplexityBot and others) read, from the robots.txt the crawl already read, and whether the site has an llms.txt (`GET /v1/workspaces/{ws}/crawls/{crawl}/ai-access`, MCP `get_crawl_ai_access`). Two new audit rules: pages hidden from AI search (`ai_search_blocked`) and no llms.txt (`llms_txt_missing`) ([docs/crawler.md](docs/crawler.md#ai-crawlers)). Upgrading runs migration 0113.
- AI crawler visits from request logs: with a Cloudflare Web Analytics connection whose token has Zone Read and Analytics Read, the daily analytics sync counts requests by each AI search, user and training crawler per host and day (`GET /v1/workspaces/{ws}/ai-bot-hits`, MCP `get_ai_bot_hits`) ([docs/ai-visibility.md](docs/ai-visibility.md#are-ai-crawlers-visiting)). Upgrading runs migration 0114.
- An **AI visibility** page in the SEO module: choose the models to ask (on your own AI connections) and the monthly limit, add cohorts of questions and competitors, run them, and see mention rate, share of voice, citations, fact accuracy, the sites answers cite, and AI crawler visits.
- Reddit, the first channel of the Social module: watches find posts where your brand can help (by phrase, in chosen subreddits or all of Reddit), the workspace's own AI drafts replies that end with a disclosure line, and a person approves each one before it posts from the brand's own account through the workspace's own Reddit app. Each reply's link is UTM-tagged, so visitors and sign-ups come back to it from customer events. No bought upvotes, other accounts or undisclosed posts. New scopes `social.read`, `social.manage` and `social.post` (people only), the `social_replies` AI task, the Reddit page, `/v1/workspaces/{ws}/social/...` and the MCP social tools. AI drafting spend is booked to the Reddit channel with the watch's brand. Upgrading runs migration 0116 ([docs/social.md](docs/social.md)).
- AI visibility checks facts: list what is true about each brand (prices, features, where it sells) and every AI answer naming the brand is checked against it by the workspace's own model for the AI visibility task. Each fact shows how often answers agree or contradict it with the wrong words, each answer its checks, and the overview an accuracy rate; a fact answers get wrong goes on the fixes list. AI visibility's spend is now booked to the AI answers channel with its brand (`/v1/workspaces/{ws}/ai-facts`, MCP `add_ai_fact`, `list_ai_facts`) ([docs/ai-visibility.md](docs/ai-visibility.md)). Upgrading runs migration 0112.
- GitHub and Vercel connectors that watch the repository or project behind each website for production deploys, read only. Each deploy is recorded with its commit and tied to the SEO site it serves, `deploy.detected` and `deploy.failed` are webhooks, and features that wait for a change to go live start from the deploy. **Deploys** on the connection in Connectors, `/v1/workspaces/{ws}/deploy-targets` and `/deploys`, MCP `add_deploy_target` and `list_deploys` ([docs/deploys.md](docs/deploys.md)). The server now also removes `GITHUB_TOKEN` from its own environment, since GitHub is a connector. Upgrading runs migration 0106.
- SEO competitors: each week your DataForSEO or Ahrefs connection suggests the sites competing with each SEO site for the same keywords; track the ones that matter or add your own, and give them names. Tracked competitors get weekly numbers (domain rating, traffic, keywords, referring domains) from your connection and a light look by OutflareBot at their robots.txt, sitemaps and home page only, and every change (new or removed pages, a new title, a move in their numbers) is kept as a feed. New **Competitors** page in the SEO module, `/v1/workspaces/{ws}/seo-competitors` and `/seo-competitors/events`, and MCP `list_seo_competitors`, `add_seo_competitor`, `update_seo_competitor`, `remove_seo_competitor`, `check_seo_competitor` and `list_competitor_events` ([docs/seo.md](docs/seo.md#competitors)). Upgrading runs migration 0103.
- Backlinks: each week your DataForSEO or Ahrefs connection reads the links to every SEO site you audit, and both tools' answers merge into one list, live and lost, with each tool's own view. Every link gained, lost (with why) or regained is kept as an event, so the history stays after the tools forget. New **Backlinks** page in the SEO module, `GET /v1/workspaces/{ws}/backlinks`, `/backlinks/events` and `/backlinks/summary`, MCP `list_backlinks`, `list_backlink_events` and `get_backlinks_summary`, a `links` SEO report, and a `link_rows` connection setting (200 by default). It runs on your own SEO account and counts against the connection's budget ([docs/seo.md](docs/seo.md)). Upgrading runs migration 0102.
- AI visibility: prompt cohorts per brand (the questions people ask AI), asked across the models you pick on the workspace's own AI connections, with or without web search, daily, weekly or on demand. Outflare counts how often answers name the brand and each competitor (mention rate, share of voice, position), which pages they cite, by model and week, and puts prompts whose answers never name the brand on the fixes list. Runs respect the workspace's monthly AI limit and their own spending limit (US$10 a month unless changed) (`/v1/workspaces/{ws}/ai-visibility`, `/ai-cohorts`, `/ai-runs`, `/ai-samples`, MCP `run_ai_prompts`) ([docs/ai-visibility.md](docs/ai-visibility.md)). OpenRouter is a new AI connection: one key for many models, with the cost of each call read from OpenRouter. Upgrading runs migrations 0110 and 0111.
- Paid media: connect Google Ads, Meta Ads, Microsoft Advertising, Reddit Ads, X Ads and LinkedIn Ads with the workspace's own accounts (and its own developer token for Google and Microsoft). Outflare syncs accounts, campaigns and daily spend, clicks and platform-reported conversions every six hours, puts spend in the costs ledger by brand, and answers it through `/v1/workspaces/{ws}/ads/...` and the MCP server. See [docs/paid-media.md](docs/paid-media.md).
- Website tracking: add a site key (Settings → General → Website tracking, `/v1/workspaces/{ws}/site-keys`, MCP `create_site_key`) and paste its snippet (`/site.js`) into your site. Every visit becomes an anonymous page event with its landing page's UTM tags, ad click ids and referral code, exactly as they came, and joins the person once your product sends identify with their userId and anonymousId. The key is public and only writes browser events: it reads nothing, never names or merges a person, never starts a journey, and can be rotated or revoked. Addresses keep only campaign tags, click ids and referral codes, and where consent is needed no cookie is set until `outflare.consent(true)` ([docs/website-tracking.md](docs/website-tracking.md)). Upgrading runs migration 0128.
- Payments from Stripe in customer records. Connect a restricted, read-only Stripe key and every customer becomes a person (joined to your product's user by `metadata.user_id`), their payments, refunds and subscription changes become events on their timeline, and `billing_plan`, `mrr`, `lifetime_revenue`, `customer_since` and `billing_status` are new computed fields on people and companies. It syncs every five minutes; `GET`/`POST /v1/workspaces/{ws}/integrations/{id}/payment-sync` (MCP `get_payment_sync`, `run_payment_sync`) show it and sync now ([docs/connectors.md](docs/connectors.md#payments)). Outflare only reads: nothing is charged or changed in Stripe. Upgrading runs migration 0119.
- Broadcasts: send one lifecycle template to many people on customer records at once, such as a newsletter, now or at a set time. The audience is a topic's subscribers or a list of records; people who left the topic or all marketing email, and prospects who never agreed to marketing email, are skipped with why. Results show deliveries, opens, clicks and unsubscribes (`/v1/workspaces/{ws}/email-broadcasts`, MCP `create_email_broadcast`, the Broadcasts page) ([docs/lifecycle-email.md](docs/lifecycle-email.md)).
- A model per AI task: planning, agent turns, routine check-ins, reply labelling and domain research can each use their own model on the workspace's AI connection (Settings → General → AI model, `task_models` on `PUT /v1/workspaces/{ws}/ai-settings`, MCP `update_ai_settings`). With nothing chosen, Claude Opus 5.5 plans and runs turns, check-ins use Claude Sonnet 5.5, and reply labelling and domain research use Claude Haiku 5.5, so routine work costs less. Other providers use the main model until you pick. Upgrading runs migration 0096.
- Search Console page by page: the analytics sync now also stores 16 months of clicks, impressions, CTR and position for every page and query, how Google indexed each page (URL Inspection: indexed or not, why, and the canonical Google chose), and the sitemaps it read. The new **Keywords** page in the SEO module shows them by page and query, with indexing and sitemaps, and so do `GET .../seo-sites/{site}/search`, `.../search/history`, `.../indexing` and `.../sitemaps` and the MCP tools `get_site_search`, `get_site_search_history`, `get_site_indexing` and `get_site_sitemaps` ([docs/search-intelligence.md](docs/search-intelligence.md)). Existing Search Console connections start at their next sync, with the last 90 days, and fill in the rest over the following days. Upgrading runs migration 0097.
- Journeys: if-this-then-that flows for people on customer records. A customer event (or a manual add) starts a run that sends lifecycle emails, waits, branches on what the person did or which emails they opened, and exits, with an optional goal event that ends it. Each journey shows how many people are in it and what each step did (`/v1/workspaces/{ws}/journeys`, MCP `create_journey`) ([docs/lifecycle-email.md](docs/lifecycle-email.md)).
- Lifecycle email states are on each recipient's customer record timeline: sent, delivered, bounced, complained, not sent (with why) and the first open and click, for people who have a record ([docs/lifecycle-email.md](docs/lifecycle-email.md)).
- Replies the built-in rules can't place are labelled by the workspace's own AI model, when one is connected under Settings → AI model. It acts on its own only when at least 80% sure, and otherwise flags the reply for review; it also reads details such as a return date or a referred contact. Unsure opt-outs are still suppressed and flagged, and changing an opt-out's label lifts the suppression. Calls count towards the workspace's monthly AI limit. Nothing to set up when upgrading.
- Connecting an analytics tool now asks what to import: every site the sign-in sees, ticked or not, with a brand for each (guessed from the site's name; test copies start unticked). Picking a brand uses its website as the site's domain, so no typing. Sites without a brand show as "Finish setting up analytics" on the Analytics page and on Home, and `GET /status` reports `analytics_site_unmapped` and `brand_without_analytics`.
- Replies to your EmailBison campaigns now arrive in the Outflare inbox, labelled by the same rules, with opt-outs suppressed and the lead's sequences stopped. Answer them from the inbox and the reply goes out through EmailBison from the same sender email. The platform sync result has a new `replies` count. Nothing to do when upgrading: connected EmailBison accounts start bringing replies in at their next sync, from the last three days.
- The site audit checks 61 rules, up from 31: redirect loops, temporary redirects and redirects to broken pages; sitemaps that list redirects, broken or non-canonical pages; hreflang codes, self-references, broken and unconfirmed alternates; canonicals that are missing or point to noindex pages; HTTPS, mixed content and links to http pages; nofollow pages and internal nofollow links; links without text, dead ends, pages with too many links or only one link in; long, capitalised, underscored and parameter URLs; short descriptions, empty h1s, missing structured data and missing Open Graph tags or share images. Crawled pages keep `og_title`, `og_image` and `insecure_resources`.
- Hand a fix to a coding agent: `GET .../fixes/{fix}/prompt` (MCP `get_fix_prompt`, **Copy prompt** in the app) turns a site audit's fix into a prompt with its pages, and `POST .../fixes/{fix}/verify` (MCP `verify_fix`) reports it live and starts a crawl that confirms it or opens it again. Webhooks deliver `fix.found`, `fix.updated`, `fix.verify_requested`, `fix.confirmed` and `fix.reopened`.
- An SEO overview that answers whether each brand is growing in search: weekly branded search (search console queries naming the brand), search clicks and visitors, domain rating, backlinks and referring domains with up, down or flat trends, plus site health and open fixes. It reads only stored data, so it costs nothing (`GET /v1/workspaces/{ws}/seo/overview`, MCP `get_seo_overview`, the Overview page in the SEO module) ([docs/seo.md](docs/seo.md)).
- An SEO module in the sidebar, right after Outreach: **Site audits** (add a site, prove it is yours, crawl it and read its health score and issues by URL template), **Fixes** (the workspace's fixes list: take a fix, mark it done or ignore it, leave a note, and see the pages it covers) and **Rankings & backlinks**, the SEO tools page that used to sit under Analytics.
- Customer records, the first part of the customer data platform: one model of people, companies and the record types a workspace adds (deals, subscriptions, partners) that the CRM and lifecycle email will share. Each record has attributes checked against a shared catalog of fields and option lists (country, industry, company size, seniority, lifecycle stage built in), the source that set each value (a manual edit outranks first-party data, a CRM sync and enrichment by default), a change history, links to other records, and identities: emails, phones, device tokens, your product's user ids, payment customers and domains. Only a verified email or phone claims its address, so an unconfirmed sign-up with someone else's email never takes over their record, and a changed email is kept in the history. New endpoints under `/v1/workspaces/{ws}/catalog` and `/v1/workspaces/{ws}/records`, new scopes `customers.read`, `customers.manage` and `catalog.manage` (admins only by default), and MCP tools such as `get_catalog`, `lookup_record` and `create_record` ([docs/customers.md](docs/customers.md)). Upgrading runs migration 0082.
- Duplicate profiles, the third part of the customer data platform. Records that share an email or phone are listed as possible duplicates (`GET /v1/workspaces/{ws}/records/duplicates`), including the ones you already have, for someone to merge, link as the same human or dismiss. Merging (`POST .../records/{id}/merge`) moves identities, links, product events and consent to the record kept, fills in its missing details by source precedence, and keeps the merged record archived so old links still lead to it. Two records with their own user ids, two accounts in your product, are never merged, only linked. When someone proves they own an email held by a record with no user id of its own (an import or CRM sync), product events merge it in automatically. MCP tools `list_record_duplicates`, `merge_records`, `link_record_duplicate` and `dismiss_record_duplicate`. Upgrading runs migration 0084.
- Product events and consent, the second part of the customer data platform. Send what people do in your product in Segment's format (`track`, `identify`, `page`, `screen`, `group`, `alias`) to `POST /v1/workspaces/{ws}/customer-events`, so a product already wired to Segment or RudderStack can point at Outflare as is. Each event is matched to a person by your user id, then a verified email, then the visitor's anonymous id; anonymous visits join the person when they sign up, two user ids are never merged, and identify traits that match catalog fields fill in the record. Every person now has a timeline (`GET .../records/{id}/timeline`) of their events, changes, identities and consent, and consent is recorded per channel and topic with its evidence (`.../records/{id}/consents`), ready for lifecycle email to send marketing only with a grant. New track names enter the catalog quarantined until approved. MCP tools `track_customer_events`, `get_record_timeline`, `list_record_consents`, `record_consent` and `list_customer_events` ([docs/customers.md](docs/customers.md)). Upgrading runs migration 0083.
- Attio connector: connect Attio with its app or an access token, then see what moving your CRM over would bring, with every object, field, pipeline stage and list, and how much is in each (`GET .../integrations/{id}/crm-import-preview`, MCP `preview_crm_import`). It only reads; nothing changes in Attio. The one-off import follows with Outflare's own CRM records.
- Import from Attio, once: people, companies, deals and custom objects become customer records, with their fields, stages, owners and links (`POST .../integrations/{id}/crm-imports`, MCP `start_crm_import`). People and companies already here with the same verified email or domain are filled in, not duplicated; values never overwrite what someone typed in Outflare; running it again updates the same records. Lists, notes and tasks come later.
- Readable URLs for connections, API tokens and webhooks: a connection opens at `/{workspace}/connectors/posthog` (then `posthog-2` for a second PostHog) instead of its ID, and the same URL opens the dialog to connect PostHog when it isn't connected yet. Old ID links still open and are rewritten. `GET /v1/workspaces/{ws}/links/{kind}/{ref}` takes `connection`, `token` and `webhook`.
- Your website can tell when someone is signed in: set `OUTFLARE_SIGNED_IN_COOKIE_DOMAIN` to a parent domain of `PUBLIC_URL`'s host (`example.com` for `app.example.com`) and the server keeps an `outflare_signed_in=1` cookie there while a session lasts, so the site can show "Dashboard" instead of "Sign in". The cookie says nothing else; the session cookie stays HttpOnly on the app's own host.
- Site audits' first part: SEO sites with ownership proof (DNS record, meta tag or file) and OutflareBot, a crawler that obeys robots.txt and Crawl-delay, backs off when a site struggles, never reaches private addresses and honours an opt-out list. Owners of a proved site may crawl more and audit pages their robots.txt hides. New scopes `seo.read` and `seo.manage`; set `OUTFLARE_CRAWL_ALLOW_PRIVATE=true` to crawl an intranet ([docs/crawler.md](docs/crawler.md)).
- Each crawled page keeps its title, description, canonical, h1, hreflang, schema types, word count, images without alt, a duplicate hash and its links in and out.
- Warm up in a connected sequencer instead: a mailbox that is also in EmailBison, Instantly or Smartlead can use that sequencer's own warm-up (`provider` in the warm-up settings, **Warm up with…** on Mailboxes). Outflare switches it there, shows its score, and spots mailboxes already warming there or in both places. Upgrading runs migration 0059.
- Cheap Inboxes can push mailboxes into Outflare: **Turn on push** on its Mailboxes page adds Outflare there as a self-hosted EmailBison, with a token that can only read and add mailboxes. Push the selected mailboxes or all of them; turning push off revokes the token and removes the connection. Needs an https `PUBLIC_URL`. Upgrading runs migration 0058.
- Sequencer warm-up tags: set `config.warmup_tags` on a sequencer's connection (or paste the tag on its Mailboxes card) and the inbox leaves its warm-up mail out, unread and unmoved.

- Lifecycle email: send receipts, password resets, sign-in codes and onboarding email from your own Amazon SES, Postmark or Resend account, under a new Lifecycle module. Each provider has a setup guide with a prompt for your own AI agent; Outflare sets up delivery events, verifies sending domains, keeps versioned templates with live preview, and suppresses hard bounces and complaints. `POST /v1/workspaces/{ws}/emails`, new scopes `emails.read`, `emails.send` and `emails.manage`, webhook events in the `lifecycle_email` category, and MCP tools such as `send_email`. Delivery events need a public HTTPS `PUBLIC_URL` ([docs/lifecycle-email.md](docs/lifecycle-email.md)).
- Lifecycle email: each email records what sent it (an API call from the web app, API, MCP, CLI or a mission, or a test send) and links to that call on the Logs page; templates and sends can turn on open and click tracking (off by default); and every address has a history page and `GET /v1/workspaces/{ws}/email-recipients/{address}`. New webhook events `email.opened` and `email.clicked`, MCP tool `get_email_recipient`.
- Lifecycle email topics and a preference page: add topics such as product updates (everyone gets it until they leave, or opt-in), put lifecycle templates or sends in one, and the unsubscribe link opens a page where people leave one topic, choose others, or turn off all marketing email. Choices are consents on their customer record, and each send checks them. `GET/POST /v1/workspaces/{ws}/email-topics`, `topic` on templates, sends and the emails list, webhook event `email.preferences_changed`, MCP tools `list_email_topics`, `create_email_topic` and `update_email_topic`. Upgrading runs migration 0093.

### Changed

- Connecting your web data connection from the page that needs it is one step: Social, a brand's links and site audits show a connect button with your web data connection's logo, Connectors says what that page uses your web data connection for, and you land back on the same page once it's connected (or if you close it). Your web data connection now shows its own logo everywhere instead of a "G".

- Social opens on Your accounts: each brand's X and LinkedIn accounts, from its links or found on its website, to confirm in one click, with what each needs to be read (your web data connection for X followers, a LinkedIn sign-in) and its followers once read. Confirming one reads it straight away. Numbers, accounts and posts show once there is something to show, instead of zeros. In the menu, Social → X & LinkedIn is now Social → Accounts.

- Social tracks every social profile in a brand's links, not only X and LinkedIn: YouTube channels, Instagram, TikTok, Facebook and GitHub accounts, subreddits, Discord servers and LinkedIn company pages have their followers read every day from their public page through your own web data account, so growth shows day by day (new `followers_change_1d` on social accounts). Add, edit and remove profiles right on Social → Accounts.
- Agents must send a customer record's version when they change or merge it. A change from the MCP server, a connected AI app or a mission without one is refused with `version_required`, and a stale one with `record_changed`, whose message now says which version to send. People and scripts with a plain API token are unchanged.

- The operator role is called Member in the app, and members can connect tools (`integrations.manage`) by default. The API still sends `operator`, and accepts `member` too.

- The web app uses the whole width of the screen. Pages start next to the sidebar instead of sitting in a narrow centred column, tables and charts stretch to fill, and forms and text stop at a comfortable reading width.
- The brand picker moved to the top bar, next to the page name, and a workspace with brands now shows one brand at a time, like a project in PostHog: it opens on the brand picked last, or the first brand. **All brands** is still in the picker for comparing them, and links keep the choice (`?brand=northpeak` or `?brand=all`). Lifecycle (emails, templates, broadcasts, journeys), the inbox, the audit log and site keys now follow it too; `GET /emails`, `/email-templates`, `/email-broadcasts`, `/journeys`, `/email-providers` and `/site-keys` take `?brand=`.
- The brand picker moved to the top of the sidebar, under the workspace (on a phone, the top bar), and a workspace with brands now shows one brand at a time, like a project in PostHog: it opens on the brand picked last, or the first brand. **All brands** is still in the picker for comparing them, and links keep the choice (`?brand=northpeak` or `?brand=all`). Lifecycle (emails, templates, broadcasts, journeys), the inbox, the audit log and site keys now follow it too; `GET /emails`, `/email-templates`, `/email-broadcasts`, `/journeys`, `/email-providers` and `/site-keys` take `?brand=`.

- The sidebar is regrouped so each thing has one home as Outreach grows. Modules, in order: **CRM** (Leads, Lead lists, Do not contact, the suppression list's new name), **Outreach** (Sequences, Mailbox allocation, Sending reports), **Deliverability** (renamed from Sending: Infrastructure, Mailboxes, Domains, Warm-up, Sending servers, Teams & senders), Lifecycle, SEO, **Social** (Social, Reddit) and Analytics. **Company** (About your company, Brands) moved out of Settings into the sidebar. The footer is just Connectors and Settings: AI apps, API tokens, Logs and All tools are under Settings, and Digests moved to Settings → Notifications. Page addresses don't change, so links and pins keep working.
- Every page now counts the same way. **Warming** always means mailboxes in the warming stage (not every mailbox with warm-up on), and **reply rate** is people who replied out of people contacted in the last 7 days, never replies per email sent, so Home, Mailboxes, Domains, Servers, Sequences, Missions and agencies agree. New fields `contacted_leads_7d` and `replied_leads_7d` on workspace stats, sending domains and sending servers, `contacted` in a sequence's `stats`, and an agency's `reply_rate_7d` uses people too. Each state has one word and one colour on every page: a working mailbox is **Sending**, a broken one **Not sending**, a failing connection **Not working**, and paused is grey everywhere. Nothing to do when upgrading.
- Warm-up mail now goes out between 8am and 6pm in the time zone your sequences send in (UTC until you have one), at a third of the volume on weekends, like a person at work. It was spread over the whole UTC day.
- The sidebar is calmer: one module is open at a time and it slides open and shut, clicking a module opens it without leaving the page you are on, and it follows the page you open. Signing out forgets where you were, so the next sign-in starts at Home. Every page in the sidebar can be pinned from the pin that shows on hover, including All tools, Connectors and API tokens. Pins stay, but only for the person who made them on that browser.
- Adding, changing and archiving brands needs `context.manage` instead of `workspace.manage`, so operators and connected AI apps can do it (brands are company context; the workspace's name and URL still need `workspace.manage`).
- Outflare's warm-up now stars about one in five received warm-up emails and files them in an **Outflare Warmup** folder (a Gmail label) after engaging, so they stay out of the real inbox. Mail rescued from spam goes to the inbox first.

### Changed

- API tokens: **New token** opens a short form in plain words (Read only, Can make changes, or area by area, plus the riskier extras) with an expiry, 90 days by default. Click a token to rename it, change what it may do or its limits, or revoke it. `PATCH /v1/workspaces/{ws}/api-tokens/{id}` now takes `name` and `scopes` (event `token.updated`). Tokens of connected AI apps no longer show under API tokens. API tokens has its own row in the sidebar for owners and admins, and a link from Connect your AI.
- Connected AI apps: click one under Connect your AI to change its switches without connecting again (`PATCH /v1/workspaces/{ws}/agent-apps/{id}`, event `agent_app.updated`). Connecting the same app again replaces the old connection. A refused call (`forbidden`) now says where to turn the permission on, with a link.
- The consent screen for AI apps is a list of switches, one per kind of change (context, leads, sequences, replies, infrastructure, missions, spending). Everything starts on except spending. `POST /v1/agent-apps/authorize` takes the exact `scopes`.
- The MCP server tells apps its title, description, website and logo, so they show Outflare's mark instead of a generic icon. Set `OUTFLARE_PRIVACY_URL` and `OUTFLARE_TERMS_URL` to link your policies from the OAuth metadata.
- The Connectors page leads with "Connect your AI agent (MCP)" and its URL, and the AI models section is labelled as models for Outflare's own agent.
- ChatGPT setup steps follow ChatGPT's Plugins flow (Add custom MCP server), and the app picker shows each app's real logo.

### Fixed

- Calls the hosted MCP server refuses (at `/mcp`) now show their reason on the Logs page: `invalid_request` with the JSON-RPC method an app sent before starting a session, `not_found` for an expired session, instead of a bare `http_400`.
- The SEO overview shows Ahrefs or DataForSEO numbers a few minutes after you connect them, instead of after the next weekly snapshot and the end of the week. SEO connections now read your brands' websites first, a newly followed domain is read within the hour, the overview adds organic traffic and keywords, and when numbers are missing it says why (still reading, budget used up, domain not followed, no website) with the next step.
- Jobs that run longer than five minutes, such as large lead list runs, no longer start a second copy on another worker: a running job now renews its lease while it works, and a job taken over after a long database outage stops instead of finishing twice.
- Jobs left over from Outflare's own warm-up no longer show as dead job errors after upgrading. Upgrading runs migration 0144, which deletes any that are still queued or running.
- A dropped MCP or analytics stream is no longer recorded as a server crash (`server.panicked`).
- Asking a mailbox supplier for a Google app password while it is briefly down no longer retries until the job dies; the next round asks again.
- A mailbox whose Google or Microsoft sign-in was revoked no longer fails its inbox sync every few minutes without a word: it turns to error with the reason and asks to be connected again, and syncs stop until it is. Other connection problems while reading mail are shown on the mailbox as "Can't read new mail" and tried again on the next turn.
- Turning off all marketing email on the preference page (or withdrawing marketing consent through the API) now stops cold email from sequences to that person too, not only lifecycle email. Transactional email still reaches them.
- Every email domain research call is now counted in the workspace's AI spend and monthly AI limit. Before, only the first one was booked.
- Website analytics no longer lists a site twice as having no brand when some of its days were stored before its domain was set; each sync moves those days to the site's domain, and clears rows a sync stored for a site someone switched off mid-run.
- The consent screen for AI apps had no switch for SEO, so a connected app could never change SEO sites.

### Removed

- Outflare's own warm-up. Outflare no longer sends warm-up mail between your mailboxes: warm-up runs only in a connected sequencer with its own warm-up (EmailBison, Instantly, Smartlead), which Outflare switches on or off and whose score it shows and uses to decide when a mailbox is ready. Upgrading runs migration 0091, which turns off the warm-up Outflare was running; turn it on again in a connected sequencer from the Warm-up page. In the API, turning warm-up on without `provider` picks the mailbox's connected sequencer, a mailbox in none is a 400, `provider: "outflare"` only goes with `enabled: false`, `daily_target` and `reply_rate` are ignored, and `GET .../mailboxes/{id}/warmup` and the 7-day warm-up counts are deprecated and run down to zero. The MCP tool `get_mailbox_warmup` is gone.

## [0.1.0] - 2026-10-26

The first release. It holds everything built so far:

### Added

- Search over customer records and their history (`GET /v1/workspaces/{ws}/search`, MCP `search_workspace`). It finds people and companies by name, attribute text, email, phone or id, and emails sent and received, meetings, deals and lifecycle email by their words, ranked, with whose each one is. It uses Postgres full-text search with an index per kind. See [docs/workspace-search.md](docs/workspace-search.md).
- Query: read-only SQL over the workspace's own data, for people and agents (`POST /v1/workspaces/{ws}/query`, `GET .../query/tables`, MCP `run_query` and `list_query_tables`). It reads views of people, companies, product events, consents, subscriptions, outreach, replies, deals, meetings, spend, email, visits and social posts. Each query sees only its workspace, the caller's brands and the tables its scopes allow, as a read-only database role per workspace, and never changes anything. Self-hosting needs a database user that can create roles; see [docs/query.md](docs/query.md).
- Monitoring counts running jobs that were put back in the queue because their lease ran out (`outflare_jobs_reaped_total`, by kind), since such a job may run twice. The dashboard shows them and a new alert fires when any appear.
- A new monitoring alert fires when the database grows over 25% in a day (once it is over 1 GB). Tests now check that every alert rule and dashboard panel uses only metrics the engine exports.
- Invites are emailed. **Send invite** on Members emails the person a link to join, through the server's account email service, and still shows the link once to share another way (`emailed` on `POST .../invites` says whether it went). A new invite for the same email replaces a pending one, and inviting someone who is already a member is a `409 conflict`.
- Workspaces, members and roles, sign-in with a password, Google or a passkey, and API tokens with explicit scopes, including project and agency tokens.
- Mailboxes over Google, Microsoft 365 and SMTP/IMAP, connected one at a time with OAuth or in bulk with an admin connection, with health checks and peer-to-peer warm-up between the workspace's own mailboxes.
- Sending domains and sending servers, with DNS and blocklist checks, daily limits and pausing; plans that buy and set up domains and mailboxes through a supplier (Cheap Inboxes first).
- Leads, imports, lead lists and suppression; sequences with a policy check on every email, mailbox allocation between sequences and teams, an inbox that sorts replies, and outcomes (meetings and deals) with cost per meeting.
- Projects with briefs, company context and agent memory, agent missions with approvals, and a workspace status that lists what is blocking it.
- Connectors with OAuth or API keys, including analytics and SEO sources, an EmailBison-compatible API, and outgoing webhooks (Standard Webhooks).
- Connect your AI: the MCP server is hosted at `/mcp`, so Claude, ChatGPT, Cursor and VS Code connect with one URL and an OAuth consent screen (workspace, full or read-only access, spending off by default), and connected apps can be disconnected from the same page. The release image includes Node to run it; from source, set `OUTFLARE_MCP_SERVER`.
- The REST API described by `api/openapi.yaml`, with idempotency keys, cursor paging, rate limits and an error catalog; the TypeScript and Python SDKs, the MCP server and the `outflare` command line client.
- A Get started walk-through for new workspaces, on a page of its own: add your website, pick what to do first, connect your AI, connect the tools that goal needs and plan a first mission; inviting the team follows on Home. Each step is skippable and ticked off however it was done; agents read the same steps, pick the goal and skip steps (`GET /onboarding`, `get_onboarding`, `update_onboarding`).
- Request logs: every API call in a workspace (web app, API, MCP, CLI, missions) with status, duration, error code and who made it, on a Logs page in the sidebar with live follow, filters, a zoomable chart, a detail view that explains each error and what to do, and CSV/JSON export, through `GET /v1/workspaces/{ws}/logs`, the `list_logs` and `get_log` MCP tools and `outflare logs --follow`. Kept 30 days; set `OUTFLARE_LOG_RETENTION_DAYS` to change it.
- Site audits' first part: SEO sites with ownership proof (DNS record, meta tag or file) and OutflareBot, a crawler that obeys robots.txt and Crawl-delay, backs off when a site struggles, never reaches private addresses and honours an opt-out list. Owners of a proved site may crawl more and audit pages their robots.txt hides. New scopes `seo.read` and `seo.manage`; set `OUTFLARE_CRAWL_ALLOW_PRIVATE=true` to crawl an intranet ([docs/crawler.md](docs/crawler.md)). Each crawled page keeps its title, description, canonical, h1, hreflang, schema types, word count, images without alt, a duplicate hash and its links in and out. For a proved site, pages its firewall blocks can be read through a web data connection (the workspace's own key; Outflare never supplies one), never pages robots.txt disallows; pages that are only an empty JavaScript app shell are read the same way with your web data connection's browser. A crawl counts firewalled pages and says whether to prove the site or connect your web data connection. Each crawl ends with an audit: 31 rules (broken pages and links, redirect chains, canonicals, noindex, titles, descriptions, h1, thin and duplicate content, JavaScript-only content, orphan and deep pages, mobile, speed), grouped by URL template like `/blog/*`, with a health score (`GET .../crawls/{crawl}/issues`). One fixes list for the whole workspace, shared by people and agents (`GET .../fixes`, MCP `list_fixes` and `update_fix`), starting with site audits: one fix per rule and URL template of a site, taken, done or ignored, confirmed done by the next complete crawl and reopened if it comes back.
- An anonymous daily usage report and update check, off unless the release image was built with a collector address ([docs/telemetry.md](docs/telemetry.md)).
- Reply labels in four groups (hot, later, closed, automatic), with three new labels (objection, complaint, auto-reply), who set each label, how sure they were and a needs-review flag; inbox filters by group, source, tag, review and confidence; coloured tags and saved inbox views; the `inbox.classified` webhook event, and webhooks that listen for some reply labels only. A complaint suppresses the sender like an unsubscribe.
- Email classification: `POST /v1/workspaces/{ws}/classifications` (MCP `classify_email`) says whether an address or domain is a free mailbox, a private mailbox, an alias service, a throwaway inbox, a school, a government or unknown, with attributes, risk, evidence and a recommended sign-up action. The free built-in verification checks use it, so they now catch about 13,000 throwaway domains, and Proton counts as a private mailbox instead of a free one ([docs/classification.md](docs/classification.md)).
- Domain research for the long tail of email domains: the web data connector reads an unknown domain's homepage and the workspace's own AI model (Claude Haiku is the cheap, accurate choice) says what kind of email service it runs. It uses only your own keys, is off until you turn on domain research under Settings → AI model, stops at a monthly limit (US$200 by default), and never changes Outflare's global classifier data ([docs/classification.md](docs/classification.md)).
