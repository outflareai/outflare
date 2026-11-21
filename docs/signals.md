# Signals and Needs attention

A **signal** is something about a person or a company that someone should act on: a customer whose usage dropped, an account ready for a bigger plan, a renewal coming up, a failed payment. Each signal has an owner, a due date and, once it is closed, an outcome, so a brand can see what was acted on and whether it helped.

The open signals are the brand's **Needs attention** queue (Customers, Needs attention). The account page shows a record's open signals first under "Needs attention".

## Where signals come from

- **An event.** Each signal type lists the events that raise it. A [rule](rules.md) that emits `account.churn_risk` raises a "Churn risk" signal on the company once the type lists that event. No code, and it works for any event on a record's timeline: a rule's event, a deal, a support conversation, an event your product sends. Events sent from a browser, and events that arrive more than a day late, raise nothing.
- **A journey.** The step **Raise a signal** (`raise_signal`, with `signal_type`, and optionally `title` and `text`) puts whoever is in the journey in the queue, so a flow can wait, check something and only then ask a person to step in.
- **A person.** "Flag for attention" on a person or company, "Flag for sales" in a support conversation or a Telegram chat, or `POST /v1/brands/{brand}/signals` (with `conversation` to say which conversation it came from).
- **A conversation.** With "Read conversations for signals" on, the brand's own AI reads what customers say and raises what sales should act on. See "Signals from conversations" below.
- **An agent or the API.** The same endpoint, and the MCP tool `raise_signal`.
- **A Slack channel.** A post in a Slack channel a brand picked for signals raises that channel's signal type on the person's company, or on the person when they have none. Its `source` is `feed`, and its `data` has `link`, `feed_channel_id` and `item_id`.

A signal goes to the record's owner (its `owner` attribute), or to `owner_id` when given. With no owner it waits in the queue under "No owner".

## Limits that keep the queue useful

- **One open signal per type on a record.** A second churn risk signal about the same company is refused (`409`) until the first is closed.
- **Five open signals on a record.** The sixth is refused until one is closed.
- **A due date.** Each type has a number of days (2 by default). Past it the signal shows as late.
- **Escalation.** An open signal nobody accepted two days past its due date is handed to the workspace's owners and admins once, as a "Signal not picked up" alert.

## Working a signal

| Action | API | What happens |
|---|---|---|
| Accept | `PATCH .../signals/{id}` `{"status": "accepted"}` | The owner is on it. A signal nobody owns becomes the caller's. |
| Hand over | `{"owner_id": "..."}` | Another member owns it; `null` puts it back under "No owner". |
| Snooze | `{"snoozed_until": "2026-12-01T09:00:00Z"}` | It leaves the queue until then; `null` wakes it. |
| Done | `{"status": "done", "outcome": "won"}` | Closed. `outcome` is `won` (it led to what you wanted) or `no_change`; default `no_change`. |
| Not useful | `{"status": "dismissed", "outcome": "not_useful"}` | Closed. `outcome` is `not_useful` or `duplicate`; default `not_useful`. |
| Reopen | `{"status": "open"}` | Refused when the record has another open signal of the type. |

`GET /v1/brands/{brand}/signals` lists the queue, soonest due first. `status` is `open` (default), `snoozed`, `closed` or `all`; `owner` is `me`, `none` or a user id; `record` and `type` narrow it. It answers up to 500 signals. `counts` has the brand's open signals: all, the caller's, the late ones and those with no owner.

## Signal types

A brand starts with eight types: Churn risk, Upsell, Cross-sell, Renewal, Billing, Feature request, Competitor mentioned and Other. `GET /v1/brands/{brand}/signal-types` lists them with how each type's signals ended in the last 90 days (`closed_90d`, `won_90d`, `not_useful_90d`), which shows which kinds are worth raising. A type that is mostly "not useful" needs a tighter rule.

`PUT /v1/brands/{brand}/signal-types/{key}` adds or changes one: `label`, `description`, `events` (up to 20 event names) and `due_days` (0 to 365). `DELETE` archives it: nothing raises it any more, and its signals stay.

## Signals from conversations

Customers say what they need in conversations long before it shows in usage: "we're doubling the team", "can you do annual billing?", "we're also trying a competitor". Needs attention, Conversations switches on reading them for a brand (`PUT /v1/brands/{brand}/conversation-signals`). It is off until you switch it on.

- **It runs on your own AI.** Every read is a call on the workspace's own AI account (Settings, AI, task "Conversation signals", with its own model and monthly limit). Without a connected model nothing is read. Outflare supplies no AI.
- **What is read.** Support conversations (email and chat), chats from company Telegram accounts that are linked to a person or company, email the team shares, and call transcripts. Choose the channels with `channels`.
- **Private email is never read.** Email is read only when the brand's team sharing is on, the privacy check found the email to be about work, and its owner shares the contact with the brand. Everything else stays its owner's.
- **Calls come from your own recorder.** Outflare records no calls. Send a transcript with `POST /v1/brands/{brand}/records/{record}/transcripts` (`text`, and optionally `title` and `occurred_at`), from your recorder's webhook, an automation or an agent (`add_transcript`). The text is kept sealed and removed once it has been read.
- **When.** A conversation is read about ten minutes after the customer last wrote, at most once per half hour, and only when they said something new.
- **What is raised.** At most three signals per read, one per type, only when the model is sure, and never a type the account already has open or had closed in the last 30 days. Each signal says what the customer said and what to do, quotes their own words, and links back to the conversation. A person's signal goes on their company when they have one. The usual limits apply (one open per type, five per record).
- **How they sound.** The same read says whether the customer sounds positive, mixed or negative, with their own words, and keeps it on the company as [sentiment](scores.md#sentiment) for scores, rules and lists. A conversation that shows no feeling either way says nothing.
- The signal's `source` is `agent`, and its `data` has `channel`, `ref`, `quote` and `confidence`.

## Credit

Needs attention, Credit (`GET /v1/brands/{brand}/signal-credit?days=90`) shows whose signals turned into revenue. A signal closed as **won** is credited with what the account paid in the 90 days after it was raised (the record's payments, and for a company the payments of the people on it), in the brand's currency. "Who flagged them" ranks the teammates who raised signals by hand, so a support agent who spots an upsell sees what it came to. "Where signals come from" compares support, Telegram, shared email, calls, rules and journeys, and how often each is taken on, won or closed as not useful.

## Alerts

Two [alert](alerts.md) kinds, each with its own email and Slack settings per person: **Signal raised** (`signal.raised`, to the signal's owner, not to the person who raised it) and **Signal not picked up** (`signal.escalated`, to owners and admins).

## History and events

Every step is an audit event and a product event: `signal.raised`, `signal.accepted`, `signal.updated`, `signal.closed`, `signal.reopened`, `signal.escalated`, `signal_type.created`, `signal_type.updated`, `signal_type.archived`, `conversation_signals.updated` and `transcript.added`. A read is `conversation.read` (system), or `conversation.read_failed` when the AI is missing, over its limit or fails. Merging two records moves their signals to the one that stays.

## Monitoring

The Grafana dashboard shows signals raised by source and outcome, and open signals on time and late, with escalations. `OutflareSignalsLate` fires when late signals outnumber the ones on time. Conversation reads are shown by channel and outcome; `OutflareConversationReadsFailing` fires when most reads fail ([monitoring](monitoring.md)).

## MCP

`list_signals`, `list_signal_types`, `raise_signal`, `update_signal`, `set_signal_type`, `get_conversation_signals`, `set_conversation_signals`, `add_transcript` and `get_signal_credit`.

## Starter packs

A [setup kit](playbooks.md) can bring signal types with the rules and journeys that raise them. The playbook **Flag renewals and quiet accounts for their owners** adds a "Went quiet" type, a "Renews on" date on companies, and two rules and two journeys about companies, all as drafts: a company where nobody has used the product for 14 days, and a renewal within 60 days, each land in Needs attention with a due date. **Hand every won deal over to the people who look after it** needs no inputs: a won deal lands in Needs attention as "New customer to onboard" with a kick-off task for the owner, and a support conversation that comes back lands there as "Support trouble".
