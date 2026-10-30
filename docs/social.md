# Social

Outflare finds threads where your brand can genuinely help, drafts a reply that says who you are, and posts it from the brand's own Reddit account once a person approves it. Visitors and sign-ups come back to the reply that brought them, through customer events.

Reddit is the first channel of the Social module. On X and LinkedIn, Outflare reads growth (followers and your own posts' numbers, every day) and publishes the brand's own posts, written by you or drafted by your AI and approved by a person, now or at a set time (below). Listening and replies on X and LinkedIn come next, through the same watches, posts and actions.

## Growth on X and LinkedIn

The Social page shows, per channel and per brand, followers day by day, the posts you published and their impressions, likes, replies, reposts and clicks, and the engagement rate (likes, replies, reposts and quotes over impressions). Outflare reads them with your own connections only:

| Channel | Connect | What is read |
| --- | --- | --- |
| X, followers only | Connect your own [Geonode](connectors.md) account (Connectors, SEO, Geonode) and add the brand's X profile to its links (or let Outflare find it, below). No X app needed. | The followers and following counts, name and picture shown on the account's public profile page, once a day. One Geonode token per account per day. |
| X, with posts | Create an app in the X developer console with OAuth 2.0 turned on (type **Web App**, a confidential client) and the redirect URI `/v1/integrations/oauth/callback` on your Outflare. Paste its client id and secret under Connectors, Social, X, then sign in to the brand's X account on the Social page. | The account's followers, following and post counts, and its own posts from the last 7 days (not reposts or replies) with their public and private numbers. |
| LinkedIn | Sign in to your own LinkedIn account under Connectors, Social, LinkedIn. Self-hosted, the operator registers the LinkedIn app first ([connectors.md](connectors.md#connecting)). | Your profile's followers where LinkedIn allows it, and every company page you administer: followers, and its posts with impressions, clicks, likes, comments and shares. LinkedIn does not let apps list a person's own posts, so profiles show followers only. |

With Geonode, Outflare reads `x.com/<handle>` for every X link on an active brand, through your Geonode account; a page that shows no follower count (a login wall, a suspended account) is an error on the account, never zero, and is tried again the same day. A handle your own X app already reads is left to it. Taking the link off the brand stops reading it.

X bills the owner of the developer app for each post it reads (pay per use). Outflare reads each X account once a day and at most 50 of its posts, and you pay X directly. LinkedIn is read twice a day. **Read now** on the Social page (`POST .../social/accounts/sync`) reads every connection straight away.

A read that fails (a bot check, a login wall, the connection down or out of credits) is tried again the same day, so every day has a follower count and day-on-day growth stays right: after 15 minutes, then 1 and 2 hours, then every 4 hours until one works. Only a wrong address waits for the next regular read. Each account shows why its last read failed (`last_error_code`: `blocked`, `slow_page`, `page_not_found`, `invalid_address`, `no_follower_count`, `save_failed` or the connection's own `connector_*`) and `failures`, the reads in a row that failed. **Read now** reads failed accounts again too, and **Try again** on an account (`POST .../social/accounts/{id}/sync`, the `sync_social_account` MCP tool) reads just that one. Accounts that keep failing with an error Outflare has to fix raise the `OutflareSocialAccountsStuck` alert ([monitoring](monitoring.md)). A site that keeps blocking the page reader (`blocked`) doesn't: getting past it is the page reader's job, and the account shows why on the Social page.

Each page and profile becomes a social account. When the workspace has one active brand, new accounts get it; otherwise pick the brand on the Social page or with `PATCH .../social/accounts/{id}`, and its posts follow. Pause an account to stop reading it. When a connection no longer reaches an account (you stopped administering a page), it is marked disconnected and kept, with its history.

## Finding a brand's links

With Geonode connected, Outflare reads a brand's homepage through your own Geonode account (one or two tokens; again with JavaScript on when the first read finds nothing) and suggests the social profiles and app store listings it links to: X, LinkedIn, YouTube, Instagram, TikTok, Facebook, Discord, Reddit, GitHub and app stores. Share buttons and single posts are left out. It runs when a brand is added with a website, when its website changes, when Geonode is first connected, and on **Find links** in the brand's Details. Nothing is added on its own: under Links, add each suggestion (correcting the address if it's wrong) or mark it not the brand's, and it isn't suggested again. The common places with no link yet show as gaps to fill in by hand. An X link added this way is read for followers straight away.

API: `POST .../brands/{brand}/links/discover` (202; 409 `connector_needed` without Geonode), `GET .../brands/{brand}/link-suggestions` (`data`, `gaps`, `can_discover`), `POST .../link-suggestions/{id}/add` (optional `url`, `kind`, `label`) and `.../dismiss`. MCP: `find_brand_links`, `list_brand_link_suggestions`, `add_brand_link`, `dismiss_brand_link`.

API: `GET .../social/accounts`, `GET`/`PATCH .../social/accounts/{id}`, `GET .../social/accounts/{id}/days`, `GET .../social/posts` and `GET .../social/growth` (scope `social.read`; changes need `social.manage`). MCP: `list_social_accounts`, `update_social_account`, `sync_social_accounts`, `get_social_account_days`, `list_own_social_posts` and `get_social_growth`.

## Publishing on X and LinkedIn

Write a post for one of the brand's accounts on the Social page (or with the API or an agent), or give a short brief and let your own AI model draft it. A person approves it, and it goes out from the brand's own account straight away or at the time you set. Published posts join the account's posts, so their impressions, likes and clicks are read every day like any other post.

- **Accounts.** Any X account or LinkedIn company page or profile your connections reach (above). Posting needs the X connection signed in to the brand's account, or the LinkedIn sign-in; both already ask for the right to post, so nobody signs in again.
- **Length.** X takes 280 characters and counts every link as 23; LinkedIn takes 3,000. Outflare counts the way the channel does and refuses a post that won't fit, when it is written, edited and approved.
- **Links.** A link is tagged with UTM (source the channel, medium social, one campaign per account, and the post's own `utm_content`) and goes after the text on its own line, so visitors and sign-ups come back to the post that brought them.
- **AI drafts.** With a prompt and no text, your own AI model (the **Social posts** task under Settings, AI model) drafts the post in the background, on your AI bill and booked to organic social. It never invents numbers or claims. Ask it again with a new prompt as often as you like.
- **Approval.** Only a signed-in person approves a post (scope `social.post`); API tokens and agents get 403. Editing an approved post that hasn't gone out takes the approval away. Cancel one that hasn't gone out at any time.
- **Never twice.** A post is marked as going out before the call. If the channel refuses it, or doesn't answer so it may or may not be up, it is marked failed with the reason and never sent again; check the account and write a new one. Only a rate limit, answered before anything was posted, is tried again.
- **Costs.** LinkedIn's API is free. X bills the owner of the developer app for each post created (pay per use, more for a post with a link); you pay X directly, on your own app.

### Recurring posts

A series posts on a schedule you run, such as a daily summary of your changelog on the product's own X account. Create the series with its account, a name, a template (what it will post, for the person who approves it) and a daily limit (1 to 24). A person approves the series once. After that, your script or agent hands in each post's text (`POST .../social/series/{id}/posts`) and it goes out with no other approval, up to the daily limit in any 24 hours. Changing the account or template, or raising the limit, needs approving again; pausing stops it.

API: `GET`/`POST .../social/publications`, `GET`/`PATCH`/`DELETE .../social/publications/{id}`, `POST .../{id}/approve` and `POST .../{id}/draft`; `GET`/`POST .../social/series`, `GET`/`PATCH .../social/series/{id}`, `POST .../{id}/approve` and `POST .../{id}/posts`. Reading needs `social.read`, writing `social.manage`, approving `social.post` and a person. MCP: `list_social_publications`, `create_social_publication`, `update_social_publication`, `draft_social_publication`, `cancel_social_publication`, `list_social_series`, `create_social_series`, `update_social_series` and `add_series_post` (there is no approve tool).

## What it never does

- **No bought upvotes, no networks of other accounts, no undisclosed posts.** Every reply goes out from the brand's own account and ends with a disclosure line, such as "Disclosure: I work at Northpeak." The API refuses a reply without it.
- **No reply or post without a person.** Only a signed-in person can approve a reply (`POST .../social/actions/{id}/approve`, scope `social.post`), a post, or a recurring series once. API tokens and agents get 403, and there is no MCP tool for it. Original posts carry no disclosure line: they come from the brand's own account.
- **No shared keys.** Outflare searches and posts with the workspace's own Reddit app, and drafts with the workspace's own AI model. With no app connected, nothing runs; with no AI model, people write replies themselves.

## Set up

1. Signed in to the brand's Reddit account, create an app at reddit.com/prefs/apps and choose **web app**.
2. Set its redirect URI to your Outflare's `/v1/integrations/oauth/callback` (the Reddit page shows the exact address, as does `GET /v1/workspaces/{ws}/social`).
3. Under Connectors, Social, Reddit, paste the app's client id and secret. Outflare checks them with Reddit before saving.
4. On the Reddit page, choose **Sign in to post** and sign in to the brand's account. Outflare stores the refresh token sealed on the connection (`GET .../integrations/{id}/sign-in`; agents get the link with `format=json` or the MCP tool `get_social_sign_in_link`).

Watches can search after step 3; replies post after step 4. Reddit's Data API terms apply to your app.

## Watches

A watch searches Reddit every hour (`check_every_minutes`, 15 to 1,440) for posts from the last week that contain any of its phrases and none of `exclude_keywords`, in the subreddits you list or all of Reddit. Adult posts, removed posts and the brand's own posts are left out. Each match becomes a post (`social_items`) with the phrases it matched.

| Field | What it does |
| --- | --- |
| `keywords` | Phrases people use when they need you, such as "which proxy" or "passive income app". |
| `communities` | Subreddit names without `r/`. Empty searches all of Reddit. |
| `disclosure` | The line every reply ends with. Defaults to "Disclosure: I work at <brand>." when the watch has a brand. |
| `guidance` | What replies should know and sound like: the product, what it's good and bad at, the tone. |
| `landing_url` | The page a reply may link to. Each reply gets its own UTM tags. |
| `auto_draft` | Have the workspace's AI draft replies to up to 5 new posts a check. |
| `daily_limit` | Most replies approved in 24 hours (1 to 50, default 5). |
| `conversion_events` | Customer events that count as a conversion. Default `signed_up`. |

## Drafts and approval

A reply is drafted by a person (`POST .../social/actions`), an agent, or the workspace's own AI model (`POST .../social/items/{id}/draft`, the `social_replies` task, on the workspace's AI bill, booked to the Reddit channel with the watch's brand). The AI answers the question first, mentions the brand only when it fits, links at most once, and ends with the disclosure. When it can't honestly help, it says so and the draft is rejected with its reason. The post's text is treated as data, never as instructions.

A person reads each draft on the Reddit page, edits it if needed, and approves or rejects it. Approval checks that the connection is signed in, the post is still open, and the watch is under its daily limit. A post takes one reply. The reply then posts in the background, once; a failure is never retried in a way that could post twice.

Drafts waiting and posts waiting show on the [fixes list](seo.md) too (source `social`), next to site audits, so people and agents see one list of work.

## Results

Each reply's link carries `utm_source=reddit`, `utm_medium=social`, `utm_campaign=<watch name>`, `utm_content=<the reply's tag>` and the watch's campaign id, `ofc`: every watch is a [campaign link](campaign-links.md) of its own. Send page views with those tags as customer events (Segment-style `context.campaign.content`, or `properties.utm_content`), and each reply shows the visitors it brought and the known people among them who then had a conversion event, with their customer records. Each check also reads the reply's score on Reddit and whether a moderator removed it.

## API and MCP

`GET /v1/workspaces/{ws}/social` (overview), `/social/watches`, `/social/items`, `/social/actions`, with scopes `social.read`, `social.manage` and `social.post`. The MCP tools are `get_social_overview`, `list_social_watches`, `create_social_watch`, `update_social_watch`, `delete_social_watch`, `check_social_watch`, `list_social_posts`, `ignore_social_post`, `draft_social_reply`, `write_social_reply`, `update_social_reply`, `reject_social_reply`, `list_social_replies`, `get_social_reply` and `get_social_sign_in_link`.

## Engain

Engain's API is only on its top plan, so Outflare uses Reddit's own API for discovery. An Engain discovery source can be added later. Outflare will never use Engain's posting, upvoting or account network.
