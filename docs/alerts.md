# Alerts

An alert says something needs a person now. Any part of Outflare raises one, once per thing, in the same transaction that found it. Each alert is:

- **Kept in the app** under Settings → Alerts, and returned by `GET /v1/brands/{brand}/alerts` (MCP `list_alerts`), newest first. An alert belongs to the brand of what raised it (or, from a part of Outflare not on brands yet, the workspace's first brand), and is listed only there. What an alert says is read from what it is about when shown, so it never keeps its own copy of a name or a message.
- **Sent as the `alert.raised` webhook**, with ids only, so Slack, a CRM or Zapier can act on it.
- **Counted in the [digest](digests.md)**, by kind.
- **Emailed** to the person it is for, to every owner and admin, or to nobody, as each brand chooses per kind (brands share no settings).
- **Posted to Slack**, in a channel each brand picks per kind, through the workspace's own Slack connection.
- **Posted to Telegram**, in a chat each brand picks per kind, through the workspace's own Telegram bot. Slack and Telegram can both be on for a kind.

## Kinds

| Kind | Raised when | The person it is for |
| --- | --- | --- |
| `reply.hot` | A reply is labelled `interested`, `meeting_booked` or `question` by the rules, a person, or the AI at 80% or more (a label waiting for review alerts once confirmed). Once per reply. | Who the reply is assigned to, else who made the sequence. |
| `visitor.hot` | A known person or company reaches the workspace's hot score on its website ([website visitors](website-visitors.md)), at most once a week each. | Nobody in particular, so the owners and admins. |
| `ab_test.ready` | A running [A/B test](ab-tests.md) has a clear result (a winner, or the control) and is safe to stop, once per test. | The owners and admins. |
| `ab_test.split_broken` | A running A/B test's visitors split far from the flag's weights, once per test. | The owners and admins. |
| `ab_test.guardrail_hurt` | A version of a running A/B test clearly makes one of its guardrails worse, once per guardrail and version. | The owners and admins. |
| `story_quote.decided` | A PR expert approved a quote, suggested other words or declined it through an [approval link](stories.md#asking-the-expert), once per request. | Who asked the expert, else the owners and admins. |
| `knowledge.review_waiting` | Answers Outflare found for questions the knowledge couldn't answer (from the [support assistant](support-inbox.md#the-support-assistant), a teammate's reply, the website or Slack), or articles that disagree, wait for review in Knowledge. At most once a day. | Nobody in particular, so the owners and admins. |
| `insight.threshold` | A saved trend's number crossed the line an [alert on a number](insights.md#alerts-on-a-number) set, once per crossing. | Who set the alert up, else the owners and admins. |
| `error.new_issue` | Your site or server sent an [error](error-tracking.md#alerts) of a kind not seen before, or one you resolved came back. Once per issue (and once each time it comes back), at most ten an hour for a brand; never for an issue you ignored. It opens the issue. | Nobody until you switch it on (the Errors page, **Emails about new errors**); then the owners and admins. |
| `story_quote.approval_failed` | The email asking a PR expert to approve a quote didn't go out: its outreach sequence blocked it (a suppressed or unverified address) or it failed or bounced, once per request. | Who asked the expert, else the owners and admins. |
| `telegram.waiting` | A customer wrote to a company Telegram account and nobody has answered for four hours ([team directory](team-directory.md#summaries-and-alerts)), once per wait. Older waits from imported history are left alone. | The account's holder, else the owners and admins. |
| `telegram.handover` | A synced company Telegram account was given to someone; its customer chats from the last 30 days are summarised for them. | The new holder, else the owners and admins. |
| `ads.sync_failed` | A connected ad platform refused the sign-in, or its sync failed three times in a row, so the numbers in [Paid ads](paid-media.md) are getting old. Once each time it starts failing; it opens the connection. | The owners and admins. |
| `ads.sends_refused` | An ad platform refused a tenth or more of the conversions sent to it in 24 hours (at least ten sent), when that finding opens on the Paid ads page. Once each time it starts. | The owners and admins. |
| `ads.match_quality_low` | An ad platform's match quality score for an event the brand's conversion rules send is under 6 out of 10, or fell by a point or more against the week before ([paid ads](paid-media.md)), so it ties fewer conversions to its ads. At most once a week per event; it opens the send log, which lists what to fix. | The owners and admins. |
| `tracking_domain.expiring` | A verified tracking domain's registration runs out within 30 days, within 7 days, or has run out ([campaign links](campaign-links.md#own-domains-redirect-modes-and-fallback-pages)): every link on it stops working until it is renewed. Once at each, read from the domain's public registration record. | The owners and admins. |
| `tracker.link_broken` | A lander or an offer that a tracker link sends clicks to failed two checks in a row: not found, a server error, or no answer ([campaign links](campaign-links.md#landers)). Once each time it starts. A page that turns automated requests away isn't counted as broken, and an offer's check can be switched off. | Who added the page, else the owners and admins. |
| `crm_mail.team_sharing` | An owner or admin switched team email sharing on (saying when it starts, 48 hours later) or off for a brand ([customers.md](customers.md#email-with-your-contacts)). Once per member per switch. | Every member of the brand. |

A module adds a kind with `alerts.Register` (an id, a title, the scope needed to see it, and how to describe one) and raises it with `alerts.Raise`. An extension does the same with `Host.AddAlertKind` from its Setup (an id like `area.event`) and `outflare.RaiseAlert` in its transaction; its kinds appear in Settings → Alerts like the engine's. A view's path is an app page under the workspace (`inbox/<id>`), or, starting with `/`, a page of the extension's own on the same server (`/billing`).

## Email

Alert emails are Outflare's own account email (the one that sends sign-in links), so they work with no setup. They name what happened and link to it, and never quote a reply, a review or a page. An alert email waits two minutes before it goes, so several alerts of the same kind for the same person in the same brand (twenty landers found broken by one check) arrive as one email that lists them, up to 50 in one email. Each person gets at most 50 alert emails a day; the rest wait in the app, and the digest says how many. Who caused the alert (the person who labelled the reply) is not emailed about it. Every alert email ends with a **Manage notifications** link to the Alerts page, where the kind can be switched off for the brand or muted for yourself. A kind that can come in bursts starts with its email off (`default_email` on the kind): `error.new_issue` does.

- `PUT /v1/brands/{brand}/alerts/settings` with `kind` and `email`: `owner` (the default for most kinds: the person it is for, else the owners and admins), `admins` or `off`, for that brand; `null` goes back to the kind's default. Needs `workspace.manage`.
- `PUT /v1/brands/{brand}/alerts/mutes` with `kind` and `muted`: anyone can stop a kind for themselves. Send `kinds` (a list, up to 200) instead of `kind` to mute or unmute many at once, all or none; the answer's `changed` counts the kinds whose mute changed.

On the Alerts page, **Show** narrows the kinds (not muted, muted, or by who gets the email) and the search box finds kinds by name. One button then mutes or unmutes every kind shown (**Mute all 12 for me**), or only the ones you tick; **Undo** puts back the ones that click changed.

The workspace routes for alert settings and mutes answer 410 `route_moved`: each brand has its own.

## Slack

Connect Slack under Connectors, through Slack's consent screen (when the server has a Slack app configured, `CONNECTOR_SLACK_CLIENT_ID`) or with a bot token from the workspace's own Slack app. Outflare only posts: the scopes are `chat:write`, `chat:write.public`, `channels:read` and `groups:read`. `GET /v1/workspaces/{ws}/alerts/channels?integration_id=` lists the channels, and `PUT .../alerts/settings` with `slack_integration_id` and `slack_channel` picks one for a kind (both null stops posting). A post has the alert's title, its line of detail and a link; links don't unfurl. Without a Slack connection nothing is posted.

## Telegram

Alerts post through a bot of your own, never one of Outflare's. In Telegram, message @BotFather, send `/newbot` and copy the token it gives you; connect it under Connectors as **Telegram bot**. Then add the bot to the group or channel the alerts go to (as an admin in a channel), or send it a message yourself for a private chat.

Under Settings → Alerts, each kind gets a Telegram chat beside its Slack channel. The list shows the chats the bot heard from in the last day, because Telegram has no list of a bot's chats; **Another chat** takes any chat the bot is in by its number (such as `-1001234567890`) or a public channel's `@name`. Over the API, `GET /v1/workspaces/{ws}/alerts/channels?integration_id=` lists the chats and `PUT .../alerts/settings` with `telegram_integration_id` and `telegram_chat` picks one for a kind (both null stops posting). A bot whose updates go to a webhook of its own lists no chats; pick its chat by id.

A post has the alert's title in bold, its line of detail and a link, with no link preview, and never quotes a message. Outflare only posts: it reads the bot's name, and which chats wrote to it so you can pick one, and never the messages. A chat the bot was removed from fails once and isn't tried again; a rate limit is tried again. This is separate from the Telegram connector that reads your company's own Telegram accounts into customer records.

## Seeing alerts

Listing alerts needs `workspace.read` and the scope of the part of Outflare each kind comes from (`inbox.read` for replies, `customers.read` for visitors). A brand's alerts are `GET /v1/brands/{brand}/alerts`; the workspace route (`GET /v1/workspaces/{ws}/alerts`) is deprecated and lists the brand `?brand=` picks, else the workspace's first. Who hears about each kind is the brand's own setting (`PUT /v1/brands/{brand}/alerts/settings`). Mutes are each person's own.

## In your CRM

Hot replies can also land in HubSpot as a note on the contact and a follow-up task for the lead's owner, through the workspace's own HubSpot connection. Set `push_replies` and `reply_tasks` on that connection ([connectors](connectors.md#calendars-and-crms-outcomes)).
