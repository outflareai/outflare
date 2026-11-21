# Sales channels

Many sales teams run on Slack channels that other tools post into: a website form, a "new qualified contact" notice, a support handover, a usage alert. Nobody talks there; people read the feed and then go and work somewhere else. **Sales channels** lets a brand pick those channels. Outflare reads each post and turns it into a person, a company, a Sales conversation or a signal, so there is one place that says who someone is and who is on it.

It reads Slack, and never reads direct messages or a channel you did not pick. It posts only if you turn on [Sales alerts](#sales-alerts): one message per new conversation, in the one channel you choose, with emojis that follow the conversation. Nothing else in Slack is posted, changed or reacted to.

This version does not send email to people who arrive this way. Such a conversation can be assigned to a teammate and archived; writing the first email from Outflare is planned. A conversation from Slack has no EmailBison thread, so it has no **Send introduction** (see [Sales workflow](engineering/sales-workflow.md#sales-channels)).

## Set it up

A manager sets it up (Sales, Sales channels). The channels come from your own Slack app, in five steps.

1. **Create a Slack app of your own** at api.slack.com/apps. It must be your workspace's own app, not a shared one, and Outflare connects to it with a bot token. Slack holds apps that are distributed but not listed in its Marketplace to low limits on reading history; a workspace's own app keeps the normal ones.
2. **Add the bot scopes** `channels:read`, `groups:read`, `channels:history` and `groups:history` (OAuth & Permissions). These only list and read channels; the app cannot post.
3. **Install the app** to your Slack workspace and copy the Bot User OAuth Token (it starts with `xoxb-`).
4. **Invite the bot to each channel** you want read (`/invite @your-app` in the channel). A channel the app is not in cannot be read; it shows `slack_channel` until you invite it.
5. **Connect it** as a Slack (sales channels) connection, paste the bot token and, if you want the Slack search below, the search token. Then in Sales, Sales channels pick the channels.

The search token is optional. To get one, add the user scope `search:read` to the same app and copy the User OAuth Token (it starts with `xoxp-`). Slack's message search works only with a person's token, not a bot's, so the search shows what that person can see.

Connecting is by keys only, so every workspace brings its own Slack app. Outflare keeps no Slack app of its own for this.

Per channel you choose:

- a **kind** (below);
- for a Signal channel, the signal type to raise (default Upsell);
- how many days to read back when the channel is added, 0 to 7 (default 3);
- on or off. A channel that is off is not read. Turned on again, it reads from that many days before now: what was posted earlier while it was off is not read, and the posts it had stored but not read yet are read then.

**What a channel shows first.** Every source carries different fields: a form's questions, a ticket's fields. On each channel's row, **Shown first** lists the fields its posts lead with, and **Change** lets anyone with a Sales role pick them from the labels the channel's recent posts carry and put them in order, at most 6. The queue shows the first four; the conversation page shows all of them first, then the rest. The whole team sees the same. With nothing chosen, Outflare shows the first three answers that are not the person's name, address, phone, company or website.

**Try** a channel before saving it. It reads the channel's latest posts and shows what each would make. Nothing is saved, and when the workspace has its own AI the try spends it like a real read, which is why it is a person's decision and has no MCP tool. At most 50 channels per brand.

**Removing a channel** (the bin on its row, then Save) stops reading it and takes it off the list. The posts already read from it stay on their people, companies and conversations, which still name the channel. Add it again later and it is the same channel: the posts it has are not stored or read twice, and reading starts again the chosen number of days before now.

**The Slack connection.** A channel is the same channel whatever connection reads it. Saving moves every channel on the list to the connection in use, for example a brand's own connection that replaced the workspace's, with its posts. When a connection is deleted its channels stay, with their posts, and show "Slack connection removed. Reconnect and save to keep reading."; they are not read until they are saved with a connection again.

## The four kinds

| Kind | A post makes |
|---|---|
| **New person** | The person and their company, and a Sales conversation in New conversations with the post as its first entry. |
| **Handover** | The same, labelled as a handover (for example from support). |
| **Signal** | A [signal](signals.md) of the channel's type on the person's company, or on the person when they have no company. The signal's text is the post, kept readable on the signal (up to 2,000 characters), with a link back to Slack in its `data`. The usual signal limits apply (one open per type on a record, five on a record); a refused signal still leaves the post on the timeline. |
| **Context only** | Only the post, on the person's record. No conversation and no signal. Use it for a channel whose posts Outflare already gets another way, so nothing is counted twice. |

When the person already has a Sales conversation that is not archived, a New person or Handover post joins it and shows as updated to its owner; it does not return to New conversations. Posts about the same person land on the same record whichever channel they came through.

## How the person is found

Every post is read in two steps. First rules, with no AI: they pull out email addresses, links, phone numbers and `Label: value` lines. Then, when the workspace has its own AI, the AI reads the post as data.

- **One outside address:** that is the person.
- **Several outside addresses:** the AI picks one of them. Without AI, or when it picks none, the post goes to Needs a look with the addresses listed.
- **No address:** the post is matched to a company by a website in the post. It then goes on the company, makes no conversation (a conversation is always about a person) and waits under Needs a look so someone can name the person. With no company either, it waits attached to nobody.
- **Teammates are never the person.** The address of any member of the workspace, and any address on the brand's own domains, is left out. A personal mail provider (such as a free webmail address) gives a person but no company.
- **An owner named in the post.** A teammate's address after a label such as "owner", "contact owner", "account owner" or "assigned to" becomes the person's Owner, when they have none. A teammate with no Sales role becomes Owner only; the conversation stays unassigned.
- **No owner named.** The brand's routing rules pick one, as for any new person. A new conversation for a person who already has an Owner starts assigned to that Owner when they are an SDR or account manager.

The person and company are found or created once, with "Slack" as the source in the record's history. Values a person set are never overwritten.

## The AI step

The AI step runs on the workspace's own AI (Settings, AI, task "Sales channels", with its own model and monthly limit). It picks out the person who wrote in, their name, company and website, their own message, a one-line summary and whether the post is spam. The post is passed as data inside a fixed prompt; nothing in it can change the task. Outflare supplies no AI: with no connected model, the step is off and the channel page says which connection to add.

Without it, posts with one clear outside address still work. Everything else waits under Needs a look, and nothing is marked spam. If the AI fails on a post, the job retries, and its last attempt reads the post with the rules alone. A post that still can't be read on the last attempt, for any reason, is marked failed and waits under Failed.

## Needs a look, Spam and Failed

Nothing a channel carries is dropped silently. Posts that are not turned into a person wait in three lists, under Sales, Sales channels (all channels, or one channel from its row's count), for anyone with a Sales role or a manager.

- **Needs a look:** no clear person (several addresses, only a company, or nothing).
- **Spam:** the AI was at least 90% sure it is spam. It is kept, makes nothing, and can be brought back.
- **Failed:** the post could not be read: the AI failed and it is being tried again, or every try failed.

Three actions decide a post:

| Action | What it does |
|---|---|
| **This is the person** (`assign_person`, with an `email`) | Makes that outside address the person who wrote in and reads the post with it. For a post with no person yet: one that needs a look (also when only a company was found for it) or one that failed. On a failed post it is the way to try again. |
| **Not spam** (`not_spam`) | Reads a post marked spam as a normal post, without asking the AI again. With no clear person it then needs a look. |
| **Ignore** (`ignore`) | Sets aside a post that needs a look, is spam or failed. It makes nothing. |

## What a rep sees

- **Source.** Every queue row has a Source badge: the app that posted the Slack post ("Fillout", by the name Slack gives the app), the channel when a person posted it, or **Email reply** for a conversation that started from a reply. A source keeps its colour. Under it are the kind and channel. Above the list, one button per source narrows the queue (**All**, each app, **Email reply**), and a second row has one button per channel when the brand reads more than one. Several can be on at once, for example two apps and Email reply, or three of four channels: the list then shows conversations from any of them. **All** turns the rest off. A brand with no Sales channels sees Sales unchanged.
- **New ones.** A conversation you have not opened that nobody has picked up yet is marked **New**, and the row is tinted. The list refreshes every 15 seconds; a conversation that arrives while it is open is marked **Just in**. **New conversations** in the menu shows how many are waiting for an owner.
- **Details.** The queue row shows the newest post that is not spam, set aside or deleted in Slack, as the fields its channel shows first (above), each with its label; a chosen field the post lacks reads "Not given". Under them is what the person wrote in their own words, when that is not already one of the fields. A form or a ticket gets no sentence saying what it is. A post with no fields at all (someone typed it) shows the AI's summary, else the post as written. Up to 1,200 characters.
- **Seen.** A conversation counts as seen by you when you open it, press **Seen**, or open its website, LinkedIn or email link from the queue row.
- **Since you last looked.** Above New conversations, a count says how many arrived since the last conversation you looked at (opened, marked seen, or opened a link of), and those rows have a green edge.
- **Inbound or outbound.** Every person has a **Lead source category**, worked out from their Source: Inbound (Source Inbound or Sign-up) or Outbound (Cold, or Events and conferences); Invited, Customer and Other have none. A person a Sales channel brings in gets Source Inbound and the channel as their Sub-source (named after the channel; rename it under the brand's sub-sources), unless they were already here or someone set their Source. The queue has **Lead source** buttons (All, Inbound, Outbound) and shows the category under each row's source; the People list filters by the same field.
- **Assigned to.** New conversations can be narrowed to All new, Unassigned, Assigned to me, or any teammate from the **Assigned to…** list, which shows how many new conversations each one holds, so the team sees how they are shared out.
- **Marks.** Under Owner and status, three buttons mark how good a conversation was: **Good**, **Poor fit** or **Junk**; click the mark again to clear it. Anyone with a Sales role who can see the conversation can mark it, the mark is in the conversation's history, and it does not move the conversation in its list. A filter beside the sources narrows the queue to one mark or to those not marked. This is separate from **Feedback**, which is a written comment for the team.
- **Entries.** On the conversation page, Slack posts sit in the same timeline as emails, laid out for reading rather than as Slack shows them: the source badge with the kind and channel, the fields the channel shows first, what the person wrote, then the post's other details as a table (the AI's one-line summary shows only for a post with no fields) (a form's questions and answers, a ticket's fields) with tidy labels, addresses that copy and links that open. Thread replies follow. **The post as written** folds away underneath, next to Open in Slack. A post deleted in Slack is kept and marked as such, with the text it had before. The posts also appear on the person's and company's pages.
- **A post that can't be opened** (its stored copy is damaged) is left out of the conversation page and the lists, which still load; the server log and a `sales_feed.item_open_failed` event say which.
- **The whole context.** The conversation page also shows the newest 50 posts about the person or their company that are on no conversation (from Signal and Context channels, or a post that named only the company), marked "On their record"; never spam, posts set aside or posts deleted in Slack. **Open person** and **Open company** go to their pages, and a **Needs attention** block lists their open signals when they have any.
- **In Slack.** A panel beside the conversation (below).

## Sales alerts

Outflare can post every new Sales conversation to one Slack channel, so the team's Slack and the Sales queue say the same thing. A manager turns it on under Sales, Sales channels, **Post new conversations to Slack**: pick the channel, and tick which conversations are posted: those from email replies, and those from each Sales channel.

- **The message** names the person (a link to the conversation in Outflare), their company and website, where they came from (the app and channel, or Email reply), the fields their channel shows first, and the owner once there is one. It carries no email address. It is posted once, and edited when the owner or those fields change.
- **Emojis** on the message say where the conversation stands, and are added and taken off as it moves:

| Emoji | Means |
|---|---|
| 👀 | Someone on the team has seen it |
| 🙋 | It is assigned |
| 💬 | An introduction was sent |
| 🤝 | Handed to an account manager |
| 🎉 | Customer |
| 👍 👎 🗑️ | Marked Good, Poor fit, Junk |
| ❌ | Archived |

- **Your Slack app needs two more bot scopes**, `chat:write` and `reactions:write`, and must be in the channel (`/invite @your-app`). After adding scopes, reinstall the app; if Slack gives a new bot token, paste it into the Slack (sales channels) connection. Without them the card says so (`slack_auth`), and `slack_channel` when the app is not in the channel.
- **Only conversations that arrive after alerts are turned on** are posted, so turning them on never floods the channel.
- **The channel can't be one Outflare reads**, so a post is never read back as a new person.
- Posting runs in a job (`sales.alert`), on the workspace's own Slack connection. Erasing a person removes their conversation and Outflare's record of the message; the message itself stays in your Slack, as the original posts do.

## Quality by source

The Sales channels page has a **Quality by source** table: for each source (each posting app, Email reply, and posts a person typed), the conversations of the last 30 days and how many the team marked Good, Poor fit and Junk, with each mark's share of the marked ones. It answers which source is worth the team's time.

## The Slack search

When a conversation opens, the In Slack panel searches the workspace's Slack live for the person's email, the company's domain (never a free-mail one) and the company's name, and shows up to 20 hits, newest first, each with channel, time, up to 500 characters of text and a link. (The API also gives each hit's author as a Slack user id.)

- **It uses one person's token.** Slack's search works only with the search token on the connection, so it shows what that person can see.
- **Only public channels and the brand's own Sales channels are shown.** Direct messages, group messages and other private channels are dropped, also a private channel the brand removed from its Sales channels.
- **Nothing is stored.** Hits are fetched when the panel opens and shown, never saved.
- With no Sales channels or no search token, Slack is not called, and the panel offers **Copy search for Slack**: the same words to paste into Slack's own search.

## Limits

- **Where reading starts.** From the day a channel is added, turned on again or added back, minus the 0 to 7 days you chose. Older posts are not read; use the Slack search for older context. After Slack was unreachable with the channel left on, reading carries on from the last post stored.
- **How often.** Each channel is polled about every two minutes.
- **Edits, deletions and replies.** An hourly re-read of each channel's last 14 days updates an edited post, marks a deleted one as deleted in Slack (it is kept, with its text) and finds new thread replies. Edits, deletions and replies older than 14 days are not followed. A reply also sent to the channel stays a reply: it is not a post of its own.
- **Removing a channel** stops reading it and keeps its posts (see Set it up).
- **Each post is stored once**, identified by channel and Slack timestamp, so a retry, a re-read, a new connection or adding a channel back never doubles it.
- **A person moved to another brand** takes their posts along; the old brand's channel does not store them again.
- **Free Slack plans** only keep 90 days of messages.

## One owner

A person has one Owner. Assigning a conversation from Slack, as for any Sales conversation, sets the person's Owner, in the person's history; see [Sales workflow](engineering/sales-workflow.md). A post about a person with no Owner whose open conversation already has an owner makes that owner theirs, so the two agree. Everything Sales channels changes on a record is in that record's own history, with who or what made the change.

Erasing a person erases their posts and the signals their posts raised, also those on their company. Slack still has the posts, so Outflare keeps only where each one was (its channel and Slack timestamp, nothing about the person) and never stores or reads it again, so the person is not made again.

## API and MCP

The routes are under `/v1/brands/{brand}/sales/`.

| Method and path | Scope | What it does |
|---|---|---|
| `GET /feeds` | `customers.read` | The brand's channels, what each carries, the last 7 days counted by what became of the posts, and whether AI and search are available. |
| `PUT /feeds` | `members.manage` | Saves the whole list of channels (at most 50) on one connection. A channel left out stops being read and keeps its posts; one added back is the same channel. |
| `GET /feeds/channels?integration_id=` | `members.manage` | The channels the Slack connection can see, to pick from. |
| `PUT /feeds/alerts` | `members.manage` | Where new conversations are posted in Slack and which: `enabled`, `channel_id`, `replies`, `feed_channels`. |
| `GET /feeds/{id}/fields` | `customers.read` | The field labels the channel's newest 50 posts carry, most common first, and the ones it shows first. Needs a Sales role or a manager. |
| `PUT /feeds/{id}/layout` | `customers.manage` | Saves the ordered labels the channel shows first (`preview_fields`, at most 6; empty lets Outflare pick). Needs a Sales role or a manager. |
| `POST /feeds/test` | `members.manage` | Reads a channel's latest posts and says what each would make. Saves nothing. |
| `GET /feed-items?status=` | `customers.read` | Posts with status `needs_look`, `spam` or `failed`, newest first (optional `feed_channel`, `cursor`, `limit`). Needs a Sales role or a manager. |
| `POST /feed-items/{id}/actions` | `customers.manage` | `assign_person` (with `email`), `not_spam` or `ignore`. Needs a Sales role or a manager. The answer leaves out the conversation the post joined when the caller can't open it. |
| `GET /leads/{id}/slack-search` | `customers.read` | The live Slack search for a conversation. |

`GET /leads` takes `source_category` (`inbound` or `outbound`) and each conversation carries `source_category`. `GET /leads` takes `quality` (`good`, `poor_fit`, `junk` or `unrated`), each conversation carries `quality`, and `POST /leads/{id}/actions` takes the action `rate` with `quality` (empty clears the mark). `GET /feeds` also returns `source_quality`.

The MCP tools are `get_sales_feeds`, `list_sales_feed_items`, `list_sales_feed_fields`, `search_sales_lead_slack`, `set_sales_feeds`, `set_sales_feed_layout`, `set_sales_alerts` and `resolve_sales_feed_item`. There are no tools for listing Slack's channels or for trying a channel.

## Events and errors

Every action and error is a product event; the full list is in [product analytics](product-analytics.md). The Sales channels events are `sales_alert.updated`, `sales_alert.posted`, `sales_alert.post_failed`, `sales_feed.updated`, `sales_feed.layout_updated`, `sales.quality_set`, `sales_feed.tested`, `sales_feed.item_read`, `sales_feed.item_needs_look`, `sales_feed.item_spam`, `sales_feed.item_refreshed`, `sales_feed.item_resolved`, `sales_feed.item_failed`, `sales_feed.signal_failed`, `sales_feed.read_failed`, `sales_feed.schedule_failed`, `sales_feed.slack_failed`, `sales_feed.test_ai_failed`, `sales_feed.slack_searched`, `sales_feed.slack_search_failed` and `sales_feed.preview_failed`, and in the web app `ui.sales_feed_tested`, `ui.sales_feed_layout_opened`, `ui.sales_quality_marked`, `ui.sales_feed_item_opened`, `ui.sales_slack_hit_opened` and `ui.sales_crm_link_opened`. They never carry post text, names or addresses.

A post that failed on every try carries `sales_feed_item_failed` in its `sales_feed.item_failed` event. A channel that cannot be read shows one of these codes until a clean read clears it; its `sales_feed.read_failed` event is sent when the code changes, not on every read:

| Code | Meaning |
|---|---|
| `slack_auth` | Slack refused the token, or the connection is gone or no longer serves the brand. Reconnect Slack (sales channels). |
| `slack_channel` | The app is not in the channel, or it was archived. Invite the app. |
| `slack_unavailable` | Slack could not be reached. Outflare keeps trying. |

Slack's rate limit is not an error: the read waits a minute and carries on.
