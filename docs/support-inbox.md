# Support inbox

The support inbox is where a team answers customers who write in. Each email from a customer becomes part of a **conversation**: the team reads it, assigns it, leaves notes for each other, replies in the same email thread and closes it. It lives in the **Inbox** in the web app: the views column there has a Support group (Your inbox, Unassigned, Overdue, Open, Waiting on customer, Closed) and a Sales group for replies to cold email, so one page holds both and each team picks the views that are theirs. An old Support address opens the same conversation in the Inbox.

Each brand has its own support inbox. Its routes are under `/v1/brands/{brand}/support/` (the `...` below), apart from support mailboxes and the assistant settings and SLA policies set by scope, which stay under `/v1/workspaces/{ws}/support/`.

## Where the mail comes from

**A support mailbox.** Any mailbox connected with its login (Settings, Mailboxes) can answer support instead of sending cold email: choose it in the Inbox (the cog at the top of the views column), or call `PUT /v1/workspaces/{ws}/support/mailboxes/{id}`. From then on:

- its new mail goes to the support inbox, not the sales inbox;
- sequences and mailbox allocation never send from it (a mailbox that still sends for a sequence or campaign is refused with `mailbox_in_use` until it is taken off them);
- replies go out through it, so customers see your own address.

`DELETE` on the same path sends its mail back to the sales inbox. Its conversations stay, but can't be answered by email until a support mailbox is set again.

**A forwarding address** (Outflare Cloud): keep `support@yourdomain` where it is and forward it to your support address at the inbound domain. Support addresses read like support inboxes: the brand's handle and `-support`, such as `northpeak-support@`, one per brand, so mail lands under its brand; rename one under Support, Settings. Set up forwarding in Google Workspace (a routing rule that adds the address as a recipient), a Google Group (add the address as a member), Microsoft 365 (forward and keep a copy) or Gmail; when Gmail asks to confirm forwarding, the confirmation shows on the address for an admin to open. Outflare reads the customer from the forwarded email (and from Reply-To when a Google Group rewrites the sender), and remembers the address they wrote to. See [inbound email](inbound-email.md).

Replies to forwarded conversations go out through the workspace's own email provider (Amazon SES, Postmark or Resend, under Email), from the address the customer wrote to, threaded under their email, with replies coming back to that address. The address's domain must be a verified sending domain with the provider; until it is, a reply says so (`no_email_provider` or `email_domain_not_verified`). The reply's status is the email's: sent once the provider takes it, failed if it bounces or is refused.

**Intercom** (support imports): keep answering in Intercom, and Outflare keeps a copy of every conversation, so the inbox, people's records, reports and Outflare's AI have the same history as if the team worked here. Connect Intercom under Connectors (an access token from an internal app in Intercom's Developer Hub, which can read conversations, users and admins, or sign in) for a brand: copying starts on its own. For a connection every brand shares, pick the brand under Support, Settings, Intercom, or call `POST /v1/brands/{brand}/support/imports` with the connection's `integration_id` (and `history_from` to copy only recent history).

- **What comes over.** Every conversation with its messages and notes, tags, status (snoozed is pending), priority, the teammate and team who have it (by their names in Intercom; a teammate whose email is a member here is also the assignee), the times it was first answered and closed, how often it reopened, the customer's rating and remark, and what Fin did: how it ended (`resolution`: assumed or confirmed resolution, escalated ...), its last answer's kind, the workflow that ran it, the customer's rating of its answer, and the articles and snippets it answered from. Each message says who wrote it: the customer, a teammate, Fin (`author_type` ai) or a workflow.
- **Customers.** Each Intercom contact is found among the brand's people by email, or added, and keeps their Intercom id (identity `intercom`), so a visitor who never gave an email is still one person across their chats.
- **Kept in step.** Every five minutes Outflare asks Intercom which conversations changed and reads only those again; **Sync now** (`POST .../support/imports/{id}/sync`) looks straight away. The first import reads the whole history, a page of 50 conversations at a time, and can take a while for years of it. Pause and resume with `PATCH .../support/imports/{id}`; `DELETE` stops it and keeps what was copied.
- **Read-only.** A copied conversation (`source` intercom, `via` imported) is answered in Intercom: replying, assigning, closing and suggesting a reply answer `409 conversation_imported`. Notes work, for the team here. No SLA targets run on them and the support assistant never drafts for them, since Intercom (and Fin) answers them.

Intercom limits how many messages one conversation returns to its 500 most recent; older ones in a longer conversation aren't copied.

## Conversations

A new email joins the conversation its headers answer (`In-Reply-To` and `References`); with no such headers, a recent conversation (30 days) from the same address with the same subject; otherwise it starts a new one. The customer gets a customer record if they have none (origin first party, acquired via support), so their conversations show on their record and follow it when records merge.

Statuses:

| Status | Means | Changes when |
| --- | --- | --- |
| `open` | Waiting on the team | The customer writes (an out-of-office doesn't reopen it) |
| `pending` | Waiting on the customer | The team replies (the default after a reply) |
| `closed` | Done | Someone closes it, or replies with `status: closed` |

Each conversation can be assigned to a member and a team (`PATCH .../support/conversations/{id}`), carry tags, and holds notes only the team sees.

## Saved replies and AI drafts

**Saved replies** are answers a brand's team writes once (`/v1/brands/{brand}/saved-replies`: list, add, `PATCH`, `DELETE`). The reply box of a support conversation and of a reply to cold email has a **Saved replies** menu: picking one drops its text in, with `{first_name}` (the person being answered) and `{my_name}` (whoever sends it) filled in, and "Manage saved replies" adds, edits and deletes them. The most used come first.

**Draft with AI** writes an answer from the knowledge customers may be told, with the workspace's own AI model, for a person to edit and send. Support conversations use the support assistant; a reply to cold email uses `POST /v1/brands/{brand}/inbox/{id}/suggest-reply`. Nothing is sent by asking, and with no AI model connected the button says which connection to add.

## Working together

The same four things work on a support conversation and on a reply to cold email, so a team handles both the same way:

- **Assign** it to one person, who owns the answer.
- **Move** it to a team (`team_id`), such as Sales or Billing. Teams are the workspace's (Settings, Team).
- **Loop someone in** (`PUT .../support/conversations/{id}/followers/{user}`, or `PUT /v1/brands/{brand}/inbox/{id}/followers/{user}` for a reply; `DELETE` takes them off). It then shows under that person's **Watching** view (`following=me` on either list), which is how an account manager keeps an eye on a customer's conversations without owning them. Looping in changes neither the assignee nor the team.
- **You see who else is there.** While a conversation is open, a line above the reply box says when a teammate has it open too, or is writing in it right now, so two people don't answer the same customer (`GET /v1/brands/{brand}/inbox-viewing`; the page asks every 15 seconds, and asking is what says you are there). It is a warning, not a lock: you can still reply.
- **The person looped in is told.** Their Watching view shows how many are new, and the Inbox shows a message with an Open button when one arrives (`GET /v1/brands/{brand}/inbox-loop-ins` lists the ones they haven't opened). Opening the conversation clears it. Someone who hasn't opened it after five minutes gets one email from Outflare with a link to it; the email says who looped them in and nothing about the customer. Looping yourself in tells nobody.
- **Add a private note** for the team (`POST .../support/conversations/{id}/notes`, or `POST /v1/brands/{brand}/inbox/{id}/notes` for a reply). Notes show in the conversation in amber and are never sent.

## Handing a conversation to sales

A sales opportunity that comes in through support goes to sales in one step: **Sales opportunity** on the conversation (`POST /v1/brands/{brand}/support/conversations/{id}/hand-off` with `team_id`, and optionally `note` and `signal`).

- The conversation moves to the team with nobody assigned and is marked unread, so the team picks it up. The Inbox offers the team with "sales" in its name first; any team can take it.
- A note only the team sees says where it went and why.
- Whoever handed it over is looped in, so it stays under their Watching view. The customer sees nothing change.
- With `signal` (a [signal type](signals.md) such as `upsell`; needs `customers.manage`), the customer's owner also gets it in Needs attention. The answer's `signal` says what became of it: `raised`, `duplicate`, `capped`, or `none` when the customer isn't known yet.
- Sales finds these under **From support** in the Inbox's Sales views (support conversations whose team is the sales team).

When the brand's own AI reads support conversations for signals ([signals](signals.md)) and finds one in a conversation, the conversation shows "Looks like a sales opportunity" above the reply box with a **Hand to sales** button; the dialog starts with what the AI found as the note.

## Online and Away

At the top of the Inbox each person says whether they are **Online** or **Away** for the brand (`PUT /v1/brands/{brand}/inbox-presence` with `status`; `GET` lists who is online). Someone is online while they say so and their Inbox is open: the page checks in every minute, and three minutes without a check-in counts as away, so closing the tab is enough. An API token is never online.

- **A new website chat goes to someone online**: whoever is online with the fewest open conversations (`support.conversation_assigned` with `via` online). With nobody online it stays unassigned.
- **No answer in 30 seconds, and it moves on** to the next person online who hasn't had it (`via` passed), until everyone online has. A chat a teammate answered, closed or reassigned by hand is left alone.
- **Nobody free, and the chat carries on by email.** When nobody is online, or two minutes pass with no answer, the visitor is told the team will reply by email and is asked for an address if they left none (`waiting` on the chat's session and poll: `email` or `email_needed`). This needs a support mailbox on the brand; without one the chat just waits as before.
- **A reply to a visitor who has left also goes by email.** When the visitor's chat hasn't checked in for two minutes and they left an address, the reply is sent from the brand's support mailbox as well as shown in the chat, and their answer by email joins the same conversation. The reply box says so before you send.

A brand whose team never uses Online and Away works as before: chats arrive unassigned and `team_online` on the chat's session is null.


### Sound and notifications

Under the Online and Away switch, **Sound and notifications** turns alerts on for that browser (it is off to start, and the browser asks once for permission to show notifications). The Inbox looks for something new every 15 seconds while it is open. A new chat, conversation or customer message that is yours or nobody's shows a message with an Open button and plays a short chime; when the Inbox tab is in the background, the browser also shows a notification that brings you back to it. Being looped in sounds the same alert. With alerts off you still get the message with the Open button, without the sound.

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
