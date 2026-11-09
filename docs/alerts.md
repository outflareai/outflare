# Alerts

An alert says something needs a person now. Any part of Outflare raises one, once per thing, in the same transaction that found it. Each alert is:

- **Kept in the app** under Settings → Alerts, and returned by `GET /v1/brands/{brand}/alerts` (MCP `list_alerts`), newest first. An alert belongs to the brand of what raised it (or, from a part of Outflare not on brands yet, the workspace's first brand), and is listed only there. What an alert says is read from what it is about when shown, so it never keeps its own copy of a name or a message.
- **Sent as the `alert.raised` webhook**, with ids only, so Slack, a CRM or Zapier can act on it.
- **Counted in the [digest](digests.md)**, by kind.
- **Emailed** to the person it is for, to every owner and admin, or to nobody, as each brand chooses per kind (brands share no settings).
- **Posted to Slack**, in a channel each brand picks per kind, through the workspace's own Slack connection.

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
| `story_quote.approval_failed` | The email asking a PR expert to approve a quote didn't go out: its outreach sequence blocked it (a suppressed or unverified address) or it failed or bounced, once per request. | Who asked the expert, else the owners and admins. |
| `telegram.waiting` | A customer wrote to a company Telegram account and nobody has answered for four hours ([team directory](team-directory.md#summaries-and-alerts)), once per wait. Older waits from imported history are left alone. | The account's holder, else the owners and admins. |
| `telegram.handover` | A synced company Telegram account was given to someone; its customer chats from the last 30 days are summarised for them. | The new holder, else the owners and admins. |

A module adds a kind with `alerts.Register` (an id, a title, the scope needed to see it, and how to describe one) and raises it with `alerts.Raise`. An extension does the same with `Host.AddAlertKind` from its Setup (an id like `area.event`) and `outflare.RaiseAlert` in its transaction; its kinds appear in Settings → Alerts like the engine's. A view's path is an app page under the workspace (`inbox/<id>`), or, starting with `/`, a page of the extension's own on the same server (`/billing`).

## Email

Alert emails are Outflare's own account email (the one that sends sign-in links), so they work with no setup. They name what happened and link to it, and never quote a reply, a review or a page. Each person gets at most 50 a day; the rest wait in the app, and the digest says how many. Who caused the alert (the person who labelled the reply) is not emailed about it.

- `PUT /v1/workspaces/{ws}/alerts/settings` with `kind` and `email`: `owner` (the default: the person it is for, else the owners and admins), `admins` or `off`; it is the route's brand's (`PUT /v1/brands/{brand}/alerts/settings`), else `brand_id`'s, else the workspace's first brand's; `null` goes back to the default. Needs `workspace.manage`.
- `PUT /v1/workspaces/{ws}/alerts/mutes` with `kind` and `muted`: anyone can stop a kind for themselves.

## Slack

Connect Slack under Connectors, through Slack's consent screen (when the server has a Slack app configured, `CONNECTOR_SLACK_CLIENT_ID`) or with a bot token from the workspace's own Slack app. Outflare only posts: the scopes are `chat:write`, `chat:write.public`, `channels:read` and `groups:read`. `GET /v1/workspaces/{ws}/alerts/channels?integration_id=` lists the channels, and `PUT .../alerts/settings` with `slack_integration_id` and `slack_channel` picks one for a kind (both null stops posting). A post has the alert's title, its line of detail and a link; links don't unfurl. Without a Slack connection nothing is posted.

## Seeing alerts

Listing alerts needs `workspace.read` and the scope of the part of Outflare each kind comes from (`inbox.read` for replies, `customers.read` for visitors). A brand's alerts are `GET /v1/brands/{brand}/alerts`; the workspace route (`GET /v1/brands/{brand}/alerts`) is deprecated and lists the brand `?brand=` picks, else the workspace's first. Who hears about each kind is the workspace's setting (`.../alerts/settings`), and a brand's own choice (`PUT /v1/brands/{brand}/alerts/settings`) wins over it for that brand's alerts. Mutes are each person's own.

## In your CRM

Hot replies can also land in HubSpot as a note on the contact and a follow-up task for the lead's owner, through the workspace's own HubSpot connection. Set `push_replies` and `reply_tasks` on that connection ([connectors](connectors.md#calendars-and-crms-outcomes)).
