# Support inbox

The support inbox is where a team answers customers who write in. Each email from a customer becomes part of a **conversation**: the team reads it, assigns it, leaves notes for each other, replies in the same email thread and closes it. It lives under **Support** in the web app, beside Knowledge and the help centre, and is separate from the sales inbox, which holds replies to cold email.

## Where the mail comes from

**A support mailbox.** Any mailbox connected with its login (Settings, Mailboxes) can answer support instead of sending cold email: choose it under Support, Settings, or call `PUT /v1/workspaces/{ws}/support/mailboxes/{id}`. From then on:

- its new mail goes to the support inbox, not the sales inbox;
- sequences and mailbox allocation never send from it (a mailbox that still sends for a sequence or campaign is refused with `mailbox_in_use` until it is taken off them);
- replies go out through it, so customers see your own address.

`DELETE` on the same path sends its mail back to the sales inbox. Its conversations stay, but can't be answered by email until a support mailbox is set again.

**A forwarding address** (coming next): forward `support@yourdomain` to an Outflare address instead of connecting the mailbox. It uses the same forwarding addresses as the rest of Outflare and will feed the same conversations.

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

## Times

`GET .../support/stats` gives, for the last 30 days (or `days`), how many conversations started, were answered and closed, and the median time to the first reply and to closing. Service-level targets come later; the times are tracked from the first conversation.

## Trying it

In a test workspace, `POST /v1/workspaces/{ws}/test/support-email` delivers a made-up customer email to a support mailbox (`in_reply_to` continues a conversation), so the whole flow works without real mail.

## Access

Reading conversations takes `inbox.read`; replying, notes and changes take `inbox.manage`; choosing support mailboxes takes `mailboxes.manage`. Brand-limited members see their brands' conversations and those of mailboxes with no brand, and change only their brands'.

## Events and monitoring

Actions are audit events and product events: `support.conversation_started`, `support.message_received`, `support.replied`, `support.reply_sent`, `support.reply_failed`, `support.note_added`, `support.conversation_assigned`, `support.conversation_updated`, `support.conversation_closed`, `support.conversation_reopened`, `support.reply_suggested`, `support.mailbox_added` and `support.mailbox_removed`. The **Support inbox** row of the Grafana dashboard shows messages, replies sent and how long they take; alerts fire when replies fail, wait or are slow ([monitoring](monitoring.md)).
