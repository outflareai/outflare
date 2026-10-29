# Lifecycle email

Outflare sends the email people asked for (receipts, password resets,
sign-in codes, onboarding and product updates) through the workspace's own
email provider account. Cold outreach stays on mailboxes and sequences; this
path never sends it.

## Providers

| Provider | What to paste | Delivery events | Domains |
| --- | --- | --- | --- |
| Amazon SES | Access key ID, secret access key and region of an IAM user with the `OutflareSES` policy (shown in the guide) | A configuration set `outflare` and an SNS topic `outflare-email-events`, made and subscribed on connect | Easy DKIM plus a custom MAIL FROM subdomain (`mail.<domain>`) |
| Postmark | Server API token, and optionally the account API token | A webhook on the server | With the account token, Outflare adds the domain; otherwise add it under Sender Signatures |
| Resend | A Full access API key | A webhook, with its signing secret kept sealed; if Resend refuses, the provider page shows the URL and a box for the secret | Added on the account |

Each provider has a guide in the app (Lifecycle, Email providers) and at
`GET /v1/workspaces/$WS/email-provider-guides`. Every guide carries an
`agent_prompt`: a prompt for the person's own AI agent that does the
provider-side clicking and tells the agent never to ask for, print or save
the key. The person creates the key and pastes it into Outflare themselves.

Delivery events need a public HTTPS `PUBLIC_URL`. Without one the provider
shows "needs one step from you", and bounces still show when the provider
refuses a send.

The first provider connected is the default. A provider can be limited to
transactional or lifecycle email (`purposes`), paused, or given a brand.

## Sending domains

`POST /v1/workspaces/$WS/email-provider-domains` registers a domain with the default
(or named) provider in a job, then lists the DNS records it needs: DKIM,
SPF and MAIL FROM records, plus a recommended `_dmarc` record if the domain
has none. Outflare asks the domain's own nameservers, so a record shows as
soon as the DNS host has it rather than when a cache expires, and checks it
and the provider every minute for 15 minutes, every 10 minutes until two
hours, hourly until a day, then every 6 hours, and gives up after three days
(`email_domain.verification_failed`). Nobody has to press anything once the
records are in. `POST .../email-provider-domains/{id}/check` checks now and
starts the every-minute checks again. A send's `from` must be on a verified
domain or a subdomain of one.

Each record says whether it is in place, and when it isn't, what looks
wrong when Outflare can tell (`hint`): a different value (shown under
`found`), the domain typed twice in the name, a CNAME proxied by
Cloudflare, or a second SPF record. A DMARC policy already in place counts,
and `found` shows it so nobody copies the suggested one over it.

Besides copying the records, there are three ways to add them:

- **Cloudflare, one click.** Press **Sign in with Cloudflare** on the
  domain (or use an API token). You come back to the same domain, which
  lists the exact records it will add; press **Add these records**
  (`POST .../email-provider-domains/{id}/dns` with one of the domain's
  `dns_connections`). It adds only the records not in place yet,
  each marked "Added by Outflare", never changes or deletes one, keeps a
  DMARC policy already there, and refuses to add a second SPF record on a
  name that has one. `dns_install` says how it went.
- **Your AI agent.** `ai_prompt` lists every missing record exactly as it
  must be added, for Claude, ChatGPT or any agent that can reach the DNS
  host.
- **A developer.** `developer` is a ready-to-send message with the same
  records.

## Templates

Templates have a draft and published versions. Editing changes the draft;
`POST .../email-templates/{id}/publish` makes it the next version, which
sends use. A send can pin `version`, or use `draft: true` for a test.

| Syntax | Meaning |
| --- | --- |
| `{{first_name}}` | A variable, HTML-escaped in the HTML part |
| `{{first_name\|there}}` | With a fallback when it is empty |
| `{{user.plan}}` | A field of an object variable |
| `{{{receipt_html}}}` | Not escaped, for HTML you built |
| `{{#if coupon}}…{{else}}…{{/if}}` | A part shown only when set |
| `{{unsubscribe_url}}`, `{{email_id}}` | Filled in by Outflare |

Variables can be required (a send without one gets `422
template_variables_missing`), have a default, and have an example for
previews. `POST .../email-templates/preview` fills content without saving.

## Sending

```sh
curl -X POST "$URL/v1/workspaces/$WS/emails" \
  -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: $(uuidgen)" \
  -d '{"to": "ana@customer.example", "from": "Acme <billing@acme.example>",
       "template": "receipt", "data": {"amount": "$49.00", "plan": "Pro"}}'
```

The send answers `202` with the email, `queued`. A job hands it to the
provider on a queue of its own, so a big lifecycle send never holds up a
password reset. Every recipient passes the policy check first: suppressed
addresses are not sent (`suppressed`). Transactional email still reaches
people who unsubscribed from updates, since they need it; bounces,
complaints, blocks and do-not-contact entries stop it. Lifecycle email gets
a one-click unsubscribe link (and `List-Unsubscribe` headers) to a
preference page, and skips anyone who left its topic or all marketing
email (see [Topics](#topics-and-the-preference-page)).

Statuses: `queued`, `scheduled` (`send_at`), `sending`, `sent`, `delivered`,
`delayed`, `bounced`, `complained`, `suppressed`, `failed`, `cancelled`.
Hard bounces and complaints add the address to the suppression list.
`GET .../emails/{id}` has the content and the timeline.

The built-in agent's sends wait for a person's approval, like its other
outbound actions.

## What sent it

Every email records its `trigger`: `kind` is `api` for a call to
`POST .../emails` (with `surface` saying web, api, mcp, cli, sdk or
mission, and the API token, person or mission behind it) or `test` for a
test send; events, journeys and broadcasts will add their own kinds with
`ref` pointing at them. `request_id` is the call that queued it, so the
email page links to that call on the Logs page, and
`GET .../emails?request_id=` lists the emails a call sent.

## Opens and clicks

Both are off unless a template turns them on (`track_opens`,
`track_clicks`, which apply at once without a publish) or a send sets
`track: {"opens": true, "clicks": true}`, the same opt-in as click tracking
in sequences. Opens use an invisible image; clicks go through the same
`/c/` redirect links as sequences, on the workspace's tracking domain when
it has one. Loads and clicks by mail scanners and privacy proxies right
after sending stay on the timeline (`opened_automatic`,
`clicked_automatic`) but are not counted. Opens are a rough signal: Apple
Mail loads images for privacy, and some apps block them. Opens and clicks
a provider reports count too. Each email has `opens`, `clicks`,
`opened_at` and `clicked_at`; the first of each is the `email.opened` or
`email.clicked` event.

Broadcasts and journeys also tag the links that go to your brands' own
sites with a campaign (one per broadcast or journey), so the visits and
sign-ups they bring are credited to lifecycle email. This is separate from
click tracking and is always on; links to other sites and links you tagged
yourself stay as written. See [campaign-links.md](campaign-links.md).

## Topics and the preference page

Topics are what people can choose to get, such as product updates or a
newsletter (`.../email-topics`). Put a lifecycle template in one
(`topic` on the template, which applies at once without a publish), or
name it on a send (`"topic": "product_updates"`). Transactional email
never has a topic: people asked for it.

A topic is `subscribed_by_default` (everyone gets it until they leave) or
opt-in (only people who joined). The unsubscribe link opens a preference
page that names the sender: one button leaves the email's topic, a box per
topic lets people choose, and one more turns off all marketing email. A
mail app's one-click unsubscribe leaves the email's topic, or, for an email
with no topic, suppresses the address for all lifecycle email as before.
Private topics show on the page only to people who get them.

What people choose is a consent on their customer record (channel `email`,
topic = the topic's key, purpose `marketing`), so the CRM, the API and
journeys see the same answer. Choosing makes a person for the address if
there is none, with the address verified, since they followed a link to
that inbox. Your app can record consent itself through
`POST .../records/{record}/consents`. When an email goes out, each recipient's
latest choice decides: leaving the topic or all marketing stops it, and an
opt-in topic needs a grant. A skipped recipient shows as `suppressed`, with
why. Turning off all marketing email also stops cold email from sequences
to that address (reason `marketing_withdrawn` in the send policy), though
transactional email still reaches them. A topic's key never changes, because consents point at it; archive a
topic to stop new sends naming it.

## Broadcasts

A broadcast sends one published lifecycle template to many people on
customer records at once, such as a newsletter or a launch
(`.../email-broadcasts`, MCP `create_email_broadcast` and
`send_email_broadcast`, the Broadcasts page under Lifecycle). It is a draft
until it is sent, now or at `send_at` (within 30 days).

- **Audience.** `{"kind": "topic"}` (the default) is every person record
  with a verified email address, of the broadcast's brand when it has one;
  `{"kind": "records", "record_ids": [...]}` is just those records (up to
  10,000). Kind `list` is reserved for saved lists and segments. A draft's
  `audience_size` is how many people that is now.
- **Who is skipped.** Each person's latest consent decides, as for any
  lifecycle email: leaving the broadcast's topic or all marketing email,
  or never joining an opt-in topic, skips them. Prospects (records from
  lead lists, origin `prospect`) are skipped unless they agreed to
  marketing email themselves: cold email goes through sequences. With the
  contact rule set to `opt_in`, everyone who never agreed is skipped too
  ([docs/opt-outs.md](opt-outs.md)). A draft shows its audience by contact
  basis (`audience_bases`) and plain-words `warnings`.
  `GET .../email-broadcasts/{id}/recipients?skipped=true` lists who was
  skipped and why.
- **Sending.** Sending checks the email can go (published template,
  verified sender domain, a provider that sends lifecycle email) and
  freezes the template's published version. A job then goes through the
  audience in batches and makes one email per person through the same
  path as `POST /emails`, with trigger `broadcast`, the topic's unsubscribe
  link and the send-time policy check. Each person's fields fill the
  template under `person` (`{{person.first_name}}`), beside the broadcast's
  own `data`. A retry never emails anyone twice.
- **Results.** The broadcast shows emails made, delivered, opened, clicked,
  bounced, complained and unsubscribed; `GET /emails?broadcast={id}` lists
  its emails. Cancelling stops the rest of the fan-out and cancels its
  emails that have not gone yet.

## Journeys

A journey is an if-this-then-that flow for people on customer records,
such as onboarding or win-back (`.../journeys`, MCP `create_journey` and
`activate_journey`). It is a draft until it is activated.

In the app, Lifecycle → Journeys draws each journey as a flow from the top
down: the trigger card first, then the steps, with a check's Yes and No
side by side and End where a path finishes. Press **+** on any link to
add a step there from the searchable step picker, and select a step (or
the trigger) to change it in the side panel, which also lists the
template fields it can use (`{{person.first_name}}`, `{{event.plan}}`).
Each step shows its live numbers (an email's sends with its open and
click rates, a check's Yes and No split) and how many people are on it
now; its side panel lists who. Anything that would stop it going live (an
unpublished template, a step nothing leads to, a check with no condition)
is marked on the step in red, and on the map a long flow gets in its
corner. Changes save as you go, and Undo and Redo (⌘Z, ⇧⌘Z) step through
them. **Review and activate** (or **Review and publish**) is the last look
before it goes live: who starts it, a checklist of every step, what
changed and what happens to the people in it now. The trigger card links to the [rule](rules.md) that emits its event,
with the rule's dry run. On a phone the flow is read-only.

- **Trigger.** Kind `event` starts a run when a customer event with that
  name lands on a person's timeline (`{"kind": "event", "event":
  "signed_up"}`) and the event is approved in the event catalog, if the
  person and the event meet its `filter`: attribute
  conditions on the record (`{"attribute": "plan", "op": "neq", "value":
  "free"}`) and property conditions on the event. The event can be one a
  [rule](rules.md) emits, such as `customer.stalled` after a week without
  activation, so a journey can follow a change, an absence or a smart
  list join. Kind `manual` starts
  only when someone adds a person (`POST .../journeys/{id}/runs` with
  `record_id`, MCP `add_to_journey`); any active journey takes manual adds.
- **Entry.** `once` (the default): a person goes through it once ever.
  `again`: again after their run ends. A person is never in the same
  journey twice at once.
- **Steps.** The first step starts; each names the one after it, and a
  journey only moves forward (no loops, at most 100 steps).
  - `email` sends a published lifecycle template (`template`) through the
    same path as `POST /emails`, with trigger `journey`. The person's
    fields fill it under `person` and the trigger event's properties (or
    the data they were added with) under `event`. Their topic and
    marketing choices apply as for any lifecycle email: a skipped person
    goes on to the next step.
  - `wait` waits `wait_seconds` (a minute to 90 days).
  - `branch` goes to `then` when its `conditions` hold (`match` `all`, the
    default, or `any`) and to `else` when they don't. Besides attribute and
    property, a condition can ask whether they did an event since the run
    started (`{"event": "upgraded", "op": "exists"}`) or whether an earlier
    email step's email was delivered, opened or clicked (`{"email":
    "opened", "step": "welcome", "op": "exists"}`).
  - `exit` ends the run. An empty `next` ends it as completed.
- **Actions.** Steps that do something besides email, each going on to
  `next`:
  - **Data.**
    - `set_field` sets, clears or increments a field (`field`, `change`, `value`). `value` can copy one with `{{event.x}}` or `{{person.x}}`. It never overwrites a value a person set by hand, unless `overwrite_manual` is on; the step is then `skipped`.
    - `add_tag` and `remove_tag` put a tag on the person or take it off.
    - `add_to_list` and `remove_from_list` move them in or out of a static list ([lists.md](lists.md#static-lists)).
  - **Other campaigns.**
    - `enter_journey` starts another journey.
    - `exit_journey` ends their run in another journey (`scope` `journey`) or in every other journey (`others`).
    - `enroll_in_sequence` adds them to a cold-email sequence through their lead, which it makes from their verified email if they have none. Suppressed people are skipped, and so are people already active elsewhere unless `allow_elsewhere` is on.
    - `stop_sequences` stops their cold email: in one sequence, in every sequence (`all`, the default), or for everyone at their company (`company`).
    - `suppress` adds their addresses, or their work domain, to the suppression list.
    - `unsubscribe_topic` withdraws their consent to a topic, or to all marketing email.
  - **Notify.** `alert` raises an alert (`title`, `text`) to the record's `owner` or to the owners and admins. It is emailed or sent to Slack as alert settings say.

  Each action is done, `skipped` with why, or `failed`. A failed step does what its `on_error` says: `stop` (the default) fails the run, `continue` goes on to `next`, and a step id goes there. `GET .../journeys/step-kinds` (MCP `list_journey_step_kinds`) lists every kind with its settings as JSON Schema and the fields that name the next step. The flow builder's step picker reads it.
- **Goal.** When the person does `goal_event`, their run ends with reason
  `goal`.
- **Exit criteria.**
  - `exit_events`: doing any of these ends the run at once, even mid-wait, with reason `exit_event`.
  - `exit_conditions`: attribute conditions. When any holds, the run ends before its next step (`exit_condition`). Someone who already meets one never starts. This includes a manual add, which answers 409.
- **Changing it.** Activating checks every email step's template is
  published, and that the lists, journeys and sequences action steps name
  are the workspace's. A journey that hasn't gone live yet changes freely. A live
  one is read-only: **Edit** makes a draft version beside it (`POST
  .../journeys/{id}/draft`, then `PATCH .../draft`; MCP
  `edit_journey_draft`) while the live version keeps running, and
  **Publish** (`POST .../journeys/{id}/publish`, MCP `publish_journey`)
  swaps it in as the next version, with its exit criteria, after the activation checks and a check
  that every step can be reached. You choose what happens to the people
  already in it: they finish on the version they entered on (`runs`
  `keep`, the default), or move onto the new version at the same step
  (`move`), where anyone on a step it removed exits. Every version that
  went live is kept (`GET .../journeys/{id}/versions`), with how many
  people are still going through it, and can be opened read-only and
  compared with the live one. Its name and description change any time.
  A paused journey can still be changed directly (`PATCH`), which moves
  everyone in it onto the change. Pausing holds every run where it is;
  activating again moves them on. Archiving ends its runs.
- **Results.** A journey shows how many people entered, are in it,
  completed, exited and failed, and per step how many were sent, skipped,
  waited, or went yes or no, and how many are on it now (`at_step`). `GET .../journeys/{id}/runs` lists the people
  in it, and one run lists what each of its steps did.

## One person's history

`GET .../email-recipients/{address}` is everything an address was sent:
counts, whether it is suppressed and why, the lead with that address, and
every event of every email to it, newest first. In the app, select an
address on an email, or search Sent emails by recipient.

People who have a customer record also get each email on their record's
timeline (`GET .../records/{record}/timeline`), beside their product events
and consents: `email.sent`, `email.delivered`, `email.bounced` (not soft
bounces), `email.complained`, `email.rejected` (not sent to them, with
why) and the first `email.opened` and `email.clicked`. Each has source
`lifecycle` and the `email_id`, `purpose`, `template` and `topic`, and is
stored once per email and person. Sending to an address never makes a
record, and test sends stay off the timeline.

## Scopes and events

`emails.read` reads providers, domains, templates, topics, broadcasts,
journeys and emails; `emails.send` sends and cancels emails and
broadcasts and adds people to journeys; `emails.manage` changes
providers, domains, templates, topics and journeys, and drafts
broadcasts. Operators get all three, viewers `emails.read`.

Webhook events (category `lifecycle_email`): `email.queued`, `email.sent`,
`email.delivered`, `email.bounced`, `email.complained`, `email.failed`,
`email.suppressed`, `email.cancelled`, `email.unsubscribed`, `email.opened`,
`email.clicked`, `email.preferences_changed`,
`email_domain.verified`, `email_domain.verification_failed`,
`email_provider.events_failed`, `email_template.published`.

## Channels

Providers, templates and emails carry a `channel`. Email is the only one
today; SMS and push providers will join as providers of their own channel
with the same templates, sends and events.
