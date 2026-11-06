# Support inbox

The support inbox is where a team answers customers who write in. Each email from a customer becomes part of a **conversation**: the team reads it, assigns it, leaves notes for each other, replies in the same email thread and closes it. It lives under **Support** in the web app, beside Knowledge and the help centre, and is separate from the sales inbox, which holds replies to cold email.

## Where the mail comes from

**A support mailbox.** Any mailbox connected with its login (Settings, Mailboxes) can answer support instead of sending cold email: choose it under Support, Settings, or call `PUT /v1/workspaces/{ws}/support/mailboxes/{id}`. From then on:

- its new mail goes to the support inbox, not the sales inbox;
- sequences and mailbox allocation never send from it (a mailbox that still sends for a sequence or campaign is refused with `mailbox_in_use` until it is taken off them);
- replies go out through it, so customers see your own address.

`DELETE` on the same path sends its mail back to the sales inbox. Its conversations stay, but can't be answered by email until a support mailbox is set again.

**A forwarding address** (Outflare Cloud): keep `support@yourdomain` where it is and forward it to your support address at the inbound domain. Support addresses read like support inboxes: the brand's handle and `-support`, such as `northpeak-support@`, one per brand, so mail lands under its brand; rename one under Support, Settings. Set up forwarding in Google Workspace (a routing rule that adds the address as a recipient), a Google Group (add the address as a member), Microsoft 365 (forward and keep a copy) or Gmail; when Gmail asks to confirm forwarding, the confirmation shows on the address for an admin to open. Outflare reads the customer from the forwarded email (and from Reply-To when a Google Group rewrites the sender), and remembers the address they wrote to. See [inbound email](inbound-email.md).

Replies to forwarded conversations go out through the workspace's own email provider (Amazon SES, Postmark or Resend, under Lifecycle), from the address the customer wrote to, threaded under their email, with replies coming back to that address. The address's domain must be a verified sending domain with the provider; until it is, a reply says so (`no_email_provider` or `email_domain_not_verified`). The reply's status is the email's: sent once the provider takes it, failed if it bounces or is refused.

## Conversations

A new email joins the conversation its headers answer (`In-Reply-To` and `References`); with no such headers, a recent conversation (30 days) from the same address with the same subject; otherwise it starts a new one. The customer gets a customer record if they have none (origin first party, acquired via support), so their conversations show on their record and follow it when records merge.

Statuses:

| Status | Means | Changes when |
| --- | --- | --- |
| `open` | Waiting on the team | The customer writes (an out-of-office doesn't reopen it) |
| `pending` | Waiting on the customer | The team replies (the default after a reply) |
| `closed` | Done | Someone closes it, or replies with `status: closed` |

Each conversation can be assigned to a member and a team (`PATCH .../support/conversations/{id}`), carry tags, and holds notes only the team sees.

## Replies

`POST .../support/conversations/{id}/replies` stores the reply as sending and a job sends it through the support mailbox, threaded under the customer's latest email. The address must pass the policy check for mail a person asked for (the transactional purpose): an unsubscribe from marketing doesn't stop an answer to someone who wrote in, but a bounce, complaint or block does. A reply that can't be sent is marked failed with the reason and the conversation opens again.

**Suggest a reply** (`POST .../suggest-reply`) drafts an answer to the customer's latest email from the knowledge customers may be told (public and customers-only items), with the workspace's own AI model and its sources. It runs only when someone asks, so it never spends on its own, and nothing is sent until a person edits and sends it. Without an AI model it says so and still lists what the knowledge holds.

**Articles** (`GET .../support/conversations/{id}/articles`) finds the public articles and quick answers a customer can be sent: without `q`, the best matches for what they last wrote; with `q`, a search. Each comes with its page on the help centre of the conversation's brand (the brand's own, else the one for every brand), and picking one puts its title and link in the reply. Only what the help centre shows is offered, never team or AI-only knowledge; with no live help centre the articles show without links. Suggest a reply's sources carry the same `url`, so a draft can link to the article it came from.

## Times

`GET .../support/stats` gives, for the last 30 days (or `days`), how many conversations started, were answered and closed, how many missed an SLA target (`sla_missed`), and the median time to the first reply and to closing.

## Chat on your website

A chat widget puts a chat button on the business's site (`POST .../support/chat-widgets`, or **Support settings** in the app). Paste its `embed_code` into the site. A visitor's messages open a conversation in the support inbox (`channel: chat`), with the same assignment, notes and SLAs as email, and the team's replies show in the chat within a few seconds. With `show_articles`, visitors can search the live help centre first.

- **Where it shows.** Only on `allowed_origins`: the chat page tells the browser which sites may show it (`frame-ancestors`), so it can't be put on someone else's site.
- **Who's who.** A visitor is known by a random token their browser keeps; only its hash is stored, and a visitor only ever sees their own conversation. `require_email` asks for an email before the first message. An email the visitor types is just what they typed. To have a signed-in user trusted, make an identity secret (`POST .../chat-widgets/{id}/identity-secret`, shown once), sign the user's lowercase email on your server (hex HMAC-SHA256), and set `window.OutflareChat = { identity: { email, name, signature } }` before the embed code. Such conversations show `identity_verified`.
- **Isolation.** The chat runs in an iframe on Outflare's address, with a strict content security policy: the site can't read the conversation, the chat can't read the site, and messages are shown as plain text only.
- **Limits.** Requests, new chats and messages are rate limited per network and per visitor, and a message is at most 5,000 characters. A switched-off or removed widget disappears from the site.

## SLAs

An SLA is how fast the team promises to answer and to resolve. Each brand has its own (`PUT .../support/sla-policies/{brand}`, or `default` for the workspace's first brand); brands share none. In the app it's under **Support settings**.

- **First reply**: from the customer's first message to the team's first answer.
- **Next reply**: from each later message the team hasn't answered to its answer.
- **Resolution**: from when the conversation opened (or reopened) to closing it.

Targets count only in the team's working week (`hours`, per weekday, in `timezone`), or around the clock without it, so a message on Friday evening isn't late on Saturday. A conversation waiting on the customer (pending) has no reply due; closing it stops both clocks.

Each conversation's `sla` says when its next reply and its resolution are due, and when either was missed. A check every minute marks the misses and records `support.sla_breached` (`target`: reply or resolve). The inbox shows what is due and overdue: `?sla=overdue`, `?sla=due_soon` (a reply due within the hour), `?sort=due`, and the `overdue` count. Saving or removing an SLA recomputes every conversation not closed (up to 5,000 at once).

## The support assistant

The assistant answers customers, by chat and email, from your approved knowledge: only articles and answers customers may be told. Switch it on per channel in **Support settings** (`PUT .../support/assistant-settings/{brand}` with its id or handle, or `default` for the workspace's first brand; each brand has its own and they share none). It uses the workspace's own AI key (the **Support replies** task in AI settings) and nothing else.

- **Draft**: it drafts a reply to each new customer message, shown above the composer for a teammate to send, edit or discard. **Draft with AI** asks it any time (`POST .../support/conversations/{id}/assistant`).
- **Auto**: it sends replies that pass every check on its own, signed as the assistant ("Northpeak help (AI assistant)"). Anything it hands off waits for the team; on chat it tells the visitor once that a person will reply. Auto is part of Outflare Cloud: an extension allows it per workspace (`Host.SetSupportAutoReplies`), and `auto_available` says whether it is allowed; the operator's staff can still stop it for one workspace with the `support_auto_blocked` [workspace feature](workspace-features.md). Without that, the engine only drafts.
- **The email hold**: by email, an Auto answer first waits `email_hold_minutes` (5 by default, 0 to 60, per brand) as a draft showing when it goes ("The assistant sends this in 4:12"). A teammate can **Stop** it (`POST .../support/assistant-runs/{id}/stop`; it stays a draft), **Edit** it (which stops it first) or discard it. It never goes if someone on the team replies first, the customer writes again, the conversation is closed or Auto is turned off meanwhile. Chat answers at once.
- **On chat**, each answer the assistant sent has **Did this help? Yes / No** under it (`POST /chat/{key}/messages/{id}/feedback`). "No" hands the conversation to the team: it reopens, the visitor is told once that a person will reply, and the assistant stops answering in it (`not_helpful`). While the assistant is writing a reply in Auto, the chat shows it typing (`typing` on the chat's poll).
- **Try it**: ask it a made-up question in Support settings (`POST .../support/assistant/test`) to see what it would answer, ask or hand off. Nothing is stored or sent.

For each message it does one of three things:

- **Answers** in a few short sentences (chat at most about 70 words, email about 140), the answer first, citing what it used.
- **Asks one question** back when that settles it ("Which plan are you on?").
- **Hands off** to a person, with the reason: the knowledge doesn't cover it, it needs their account or an action such as a refund, the customer is upset, or anything is unclear.

Rules in code, which no message can talk it out of:

- Before any AI: it doesn't answer automatic mail or no-reply senders, a conversation a teammate is already answering, or more than 3 times in one conversation. A request for a person, and disputes, chargebacks, legal, security and personal data requests, always go to a person.
- After: a reply must cite at least one article, and a second check must find every fact in what it cites (else `not_grounded`). Style rules catch filler ("Great question", "I hope this helps"), dashes, headings, more than one link, links outside the business's own sites, and addresses or phone numbers that aren't in the articles; it rewrites once, then hands off (`style`).
- Customers' messages, articles, web pages and Slack messages are data in its prompts, never instructions, and it has no tools: it can only write text.

**When the knowledge has no answer**, the conversation is marked as a gap, so the teammate's reply is offered as a new answer in **Knowledge** to review. It also looks for one itself: on the website (`find_on_website`, on by default) and then in the Slack channels you pick, from the last year (`slack_integration_id` and `slack_channels`, through a **Slack (knowledge)** connection, which reads only those channels and never posts; with an embedding model on the workspace's own key and meaning-based search on, threads are ranked by meaning as well as words, so one that answers in other words is found too; `GET .../support/assistant/slack-channels` lists them). What it finds is suggested in Knowledge with the page or message it came from ("Is this correct, and can it be used for support from now on?"), and nothing is used until a person approves it.

**Hand-off topics** (Support settings, `GET/POST .../support/handoff-topics`) say what conversations are about, such as Billing, Bugs or Sales, with a short description for the assistant. Each conversation it looks at is tagged with the topic it fits (`topic` on the conversation, `topic_id` to filter the inbox), and when it hands one to the team and nobody has it yet, it goes to the topic's person and team. A topic marked **Always hand it to your team** (`always_handoff`) is never answered by the assistant (`topic`). Workspace topics apply to every brand; a brand's own topic of the same name replaces it. At most 30 per brand.

Each run is stored (`GET .../support/conversations/{id}/assistant`); sending a draft with `assistant_run_id` on the reply marks it sent, or edited when changed, and `GET .../support/assistant/stats` reports how often it answered, asked or handed off, why, how many drafts went out as written and what chat visitors said, as rates against the period before, with a line a day. Support settings show the last 30 days. When answers it found wait for review, the owners and admins get a `knowledge.review_waiting` [alert](alerts.md) (at most once a day), and the [digest](digests.md) has a Support section.

## Trying it

In a test workspace, `POST /v1/workspaces/{ws}/test/support-email` delivers a made-up customer email to a support mailbox (`in_reply_to` continues a conversation), so the whole flow works without real mail.

## Access

Reading conversations takes `inbox.read`; replying, notes and changes take `inbox.manage`; choosing support mailboxes takes `mailboxes.manage`. Brand-limited members see their brands' conversations and those of mailboxes with no brand, and change only their brands'.

## Events and monitoring

Actions are audit events and product events: `support.conversation_started`, `support.message_received`, `support.replied`, `support.reply_sent`, `support.reply_failed`, `support.note_added`, `support.conversation_assigned`, `support.conversation_updated`, `support.conversation_closed`, `support.conversation_reopened`, `support.reply_suggested`, `support.mailbox_added`, `support.mailbox_removed`, `support.sla_updated`, `support.sla_removed`, `support.sla_breached`, `support.chat_widget_created`, `support.chat_widget_updated`, `support.chat_widget_deleted`, `support.chat_secret_rotated`, `support.assistant_updated`, `support.assistant_removed`, `support.assistant_ran`, `support.assistant_sent`, `support.assistant_discarded` and `knowledge.answer_searched`. The **Support inbox** row of the Grafana dashboard shows messages, replies sent and how long they take, SLA misses and conversations past their SLA; alerts fire when replies fail, wait or are slow, and when the SLA check fails, stops or is slow. The **Support assistant** row shows its runs by action, how long it takes to decide and its searches of the website and Slack; alerts fire when its runs fail or are slow, and when searches keep failing ([monitoring](monitoring.md)).
