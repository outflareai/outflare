# Sales workflow

Sales is a brand-scoped workflow over canonical customer records. The existing
Sales conversations page (CRM, Sales conversations) opens the queues and full-page
conversation; the CRM's people, companies and deals, with their lists, reports and
settings, are their own links in the sidebar's CRM group. SDRs and AEs share New conversations. Assignment reserves an owner without sending. Sending an introduction queues the
original EmailBison introduction in the same transaction. Handoffs transfer
ownership only after the personal sender's provider accepts the introduction.
Customer confirmation and optional value are independent of payments.

## Screens and roles

SDRs and account managers enter at **New conversations**. Each row includes the
prospect's latest incoming reply, arrival time and available website, LinkedIn
and email links from the canonical CRM records. Click the person or row to open
the conversation. The small **Assigned to** dropdown in Owner & status puts it
in that person’s My conversations as Not started and makes them the person's
Owner. After the conversation is taken, its owner or a manager can still pass it
to another SDR or account manager from the same dropdown. **Send introduction** starts
work and queues the introduction separately. A disconnected rep can read eligible conversations while sending
and taking remain disabled until personal consent is complete.

Queue rows offer **Archive** for new leads and the rep's own active leads.
Archiving needs no mailbox connection and sends no email. **Undo** reopens the
same version of the lead; a stale undo is refused if someone else changed it.
A conversation that has never been taken returns to New, retaining any
assignment; previously started conversations return to Active.
The latest archiver can undo an unassigned lead even after it leaves their queue,
without gaining read access to other archived conversations. Managers can find
unassigned archives in All conversations → Archived and reopen them from the row;
reps can reopen their own archived leads from My conversations → Archived.

Managers enter at **All leads**, with status and teammate filters, the latest
message, ownership and recorded activity. They can also review **New leads**
and open full conversations without taking ownership or assuming a rep's role.
A manager's own settings (templates, introduction, email) are listed under the queues; **My email** explains why a manager does not
need a sending connection to review work. Workspace administration and a
person's assigned Sales role remain separate, so an administrator assigned as
an SDR or account manager retains that role's working queues.

Queue excerpts contain at most 1,200 characters. They are read from the stored,
sealed conversation after workspace, brand and queue access checks; superseded
provider copies are omitted. Manager activity is not included in rep queue
responses. Source links accept only HTTP(S) URLs in the UI. An empty queue shows
an empty state, never fictional customer data. The API panel remains available
on demand and does not inherit an open state from another product page.

## The Inbox and the CRM

Sales conversations is part of the same product as the Inbox and the CRM, so
it stays in step with both:

- **Owner.** One owner, in both places. Assigning a conversation, or taking it,
  makes that teammate the person's Owner (the `owner` attribute), and an
  accepted hand-off makes the account manager the Owner. Passing a taken
  conversation to someone else (its owner or a manager can) changes the Owner
  too, unless an email for it is still being sent; try again once it has gone.
  Assigning replaces any Owner set earlier on the person. Unassigning a new conversation clears the Owner only if it was that
  assignee. Each change is in the person's own history as the teammate who did it.
- **Deals.** Confirming a customer moves the person's open deal to Won, or
  opens one there (`deal`, `stage`). Reopening the conversation leaves the deal
  as it is. A brand whose deal type no longer has that shape is skipped.
- **From support.** New conversations also lists what support handed to the
  sales team from the Inbox ("Sales opportunity"). Those are support
  conversations: they open in the Inbox, where the reply goes out.
- **In the Inbox.** For Sales teammates, the Inbox's Sales group links New
  conversations and My conversations, with a dot when something is new.
- **Wording.** Screens say conversation, person or company, never "lead". The
  API paths and event names (`/sales/leads`, `sales.lead_taken`) keep their
  names: they are a shipped contract.

## Data and consent

Sales tables belong to Outflare's database and all carry workspace and brand
IDs. Leads, messages, drafts and events follow their canonical
`record_id` during merges; physical record deletion cascades through evidence.
A brand move moves the related workflow; the receiving brand must have its own
eligible members and connection access. Every workflow mutation adds protected
evidence and a pointer in that record's attribute history, plus an audit event.
Queue archive, undo and reopen use that same transaction: the sealed Sales event
keeps the before/after state, actor, time and request ID; record history and the
audit event link to it. Product events contain identifiers and outcomes only.
Failed or stale actions emit the normal API failure event without a success event
or a state/history change. Undo adds history rather than removing the archive.

Templates retain every version. Applied drafts retain the selected version and
resolved text, edited drafts append revisions, and sent messages reference the
exact draft. Provider identities and RFC Message-IDs deduplicate messages;
provider copies retain mailbox-local thread IDs. A receipt racing synchronization
supersedes the duplicate view without deleting its evidence. All content and
credentials are sealed and included in key rotation.

Sales Google authorization is separate from read-only personal CRM capture. It
requires `gmail.readonly` and `gmail.send`, the invited user's own verified Google
email, a live session, state, nonce and PKCE. Consent
returns to `/v1/auth/google/callback`, the redirect URI Google sign-in already
registers on the same OAuth client, so it needs no redirect URI of its own (its
state starts with `sales.`, which tells the sign-in callback to hand it to
Sales). The person explicitly agrees to sharing these Sales conversations with
assigned colleagues and Sales managers. Workspace/brand membership and Sales
role changes revoke the corresponding grants. No workspace API token can grant
personal consent or send a personal reply.

Instead of signing in, a rep can use the company's Google Workspace connection
([ADR 15](../adr/0015-google-layer.md), the one CRM email reads with): **Use
Google Workspace** in My email (`POST /v1/brands/{brand}/sales/mail/workspace`),
offered when the brand's CRM email rollout names a Workspace connection and the
rep is eligible under it (`workspace_available` on `/sales/me`). Sync reads
their mailbox with `gmail.readonly` through the service account, and sending
asks for `gmail.readonly` and `gmail.send` together, so the connection needs
`gmail.send` delegated. Eligibility is checked on every use: taking the rep off
the rollout, excluding them by account or address, or pointing the rollout at
another connection disconnects Sales at once (`mail_status`
`workspace_not_allowed`), and a missing delegation shows `send_not_allowed`.
`mail_via` says which way a rep is connected. Deleting the Workspace connection
removes the Sales connections that use it.

The original EmailBison connection is selected by the source account mapping.
Its reply ID and sender ID are verified before sending. The colleague's CC is
preserved and every To/CC recipient passes the outbound policy. A connected SDR
or AE assigned to this lead (including the pending handoff recipient) can receive
a CC copy when their domain has an outreach-only `blocklist` suppression. This
does not remove that suppression or allow prospecting to the domain. Address-level
blocks, other domain suppression reasons, withdrawn consent and review holds still
stop the copy. Unassigned colleagues and other CC recipients receive the normal
checks, as do all To recipients. Workspace grants must remain eligible under the
brand's rollout. Personal
replies preserve RFC references and resolve Gmail's mailbox-local thread ID.
Provider calls happen only in jobs.

Each connected mailbox polls every five minutes. Polling reconciles from each
conversation's beginning and follows every page. It keeps every email between
the assigned rep and the person, in any thread: a rep who writes the person a
new email instead of replying in the original thread is still having this
conversation. Reconnection uses the same historical starting point.
Draft/spam/trash mail and mail that includes anyone outside the conversation
stay out; explicit privacy exclusions still apply. Configure CRM internal domains
before enabling personal capture.

A send persists `sending` before the network call. A crash, timeout or ambiguous
server response never automatically resends it. Gmail reconciliation searches
Sent by the generated RFC Message-ID. Definitively refused sends can be retried
with the same saved message after repairing the connection or policy. EmailBison
does not expose a documented idempotency key: when its send receipt is lost,
the result remains uncertain and requires a manager to verify the original
provider thread. Automated resolution of that case is not implemented. Acceptance
is displayed as provider acceptance, not delivery confirmation.

## Sales channels

A brand can pick Slack channels that other tools post into; each post becomes a person, a company, a Sales conversation or a signal. See [Sales channels](../sales-channels.md). A conversation from Slack has `source` `slack` and no EmailBison thread, so it has no **Send introduction**: it can be assigned (which sets the person's Owner, as for any conversation) and archived. Its `integration_id` stays empty, because the send path reads any value there as the EmailBison connection.

## Importing conversations

`POST /v1/brands/{brand}/sales/bridge` consumes an existing CRM person record,
a stable source lead ID, original connection/sender/reply IDs, RFC headers and
messages. Optional attribution is sealed with the original mapping. Replays
append evidence without creating a second lead. `import_sales_lead` is the MCP
equivalent; personal mailbox actions require signed-in humans.

An external importer owns its source records and export state. It must preserve
`source` and `source_id` when retrying an import, including after a timeout whose
outcome is unknown. Use an Outflare API token bound to the intended workspace
and brand with `customers.read` and `customers.manage`. Reference an existing
workspace-owned EmailBison connection; provider credentials do not belong in
import payloads. Importing a conversation never authorizes personal mailbox
access or sends mail.

The import endpoint does not coordinate ownership in another application. An
integration that transfers work between applications must prevent concurrent
handling at the source and decide which existing conversations to transfer.

## Replies Outflare reads itself

Outflare adds people to EmailBison campaigns itself (a sequence linked to a
campaign) and reads that connection's replies into the Inbox every few minutes.
A reply from one of those people that is worth a rep's time opens a Sales
conversation with no outside importer involved.

When a reply's label settles (the rules, the workspace's own AI when it is
sure, or a person setting or confirming it), `inbox.QueueSalesReply` queues one
`sales.reply_import` job when all of these hold:

- **Source.** The reply was read from a connected sequencer listed in
  `inbox.SalesSources`: today `emailbison` only. A sequencer is added there
  once its connector can read a reply's thread (`connectors.ReplyThreadSource`).
- **Label.** A person wrote it (kind `reply`) and its label is in the Inbox's
  **hot** group: `interested`, `meeting_booked` or `question`
  (`inbox.Groups["hot"]`, the same group that raises the hot-reply alert). A
  label waiting for review does not count until someone confirms it. Every
  other label (`referral`, `not_now`, `objection`, `not_interested`,
  `wrong_person`, `unsubscribe`, `complaint`, `out_of_office`, `auto_reply`)
  opens nothing.
- **Person.** The reply is linked to a person record in a brand. A reply from
  an address nobody in the CRM has stays an Inbox message.
- **Pushed by Outflare.** Outflare added that person to a campaign on that
  connection: a `sequence.lead_pushed` action names the person (or a duplicate
  merged into them) and the connection, and its campaign is still linked to a
  sequence of the person's brand (`campaign_links`). When EmailBison says which
  campaign the reply answers, it must be one of those. So a reply from someone
  another tool put in the campaign, or from a campaign Outflare did not put
  them in, stays an Inbox message.

The job reads the thread from EmailBison through the workspace's own
connection (`ReadReplyThread`) and writes the conversation through the same
function as the import endpoint (`importSalesConversation`), so it has the same
rows, the same entries in the person's history and the same audit actions
(`sales.lead_arrived`, `sales.message_received`, role Import). Its `source` is
`outflare` and its `source_id` is the connection id and the reply id
(`<integration>:<reply>`), since reply ids are only unique inside one
connection. Messages of the thread that belong to another mailbox, or that the
sequencer holds only in part, are left out. Nobody owns the conversation and
the person's Owner is not set until someone takes it. Nothing is sent.

One conversation, however the reply arrives:

- The job is queued once per connection and reply, and running it again adds
  nothing: conversations are keyed on `source` and `source_id`, messages on
  their `Message-ID` or the sequencer's id.
- A later hot reply from the same person on the same connection adds its new
  messages to the conversation they already have (the newest one that is not
  archived, whatever opened it); it does not open a second one. As with a
  repeated import, an archived conversation stays archived and shows as unread.
- The same sequencer reply through the import endpoint and through this path
  is one conversation, whichever comes first. A conversation remembers the
  reply that opened it (`sales_leads.reply_hash`, a hash of the connection and
  reply id), and conversations from before that column are found by the stored
  copies of their messages (`sales_message_copies`). An import under one
  `source` that names a reply another source already holds returns that
  conversation.

There is no setting for this. It only acts for a workspace that connected
EmailBison, linked a campaign to a sequence and added people through Outflare;
unlinking the campaign from its sequence stops it for that campaign. A reply the
job leaves out (relabelled meanwhile, the person deleted or merged, the
connection removed or limited to another brand) is skipped without an error. A
failure (the sequencer refusing the thread, an incomplete reply) is the
`sales.reply_import_failed` event with a code, and the job retries up to five
times; see [monitoring](../monitoring.md), Sales email.

## Local validation

The local browser fixture is `web/tests/sales-smoke.mjs`. It requires a disposable
local engine/database, the production web build and `OUTFLARE_CONNECTORS_ALLOW_PRIVATE=true`
for its local mocked EmailBison server. It cannot send real email.

The fixture exercises full-page conversations, template application, editable
drafts and separate sending controls. Provider tests cover thread headers,
recipient policy, deduplication, reconnect catch-up and uncertain sends.

The workflow does not archive attachments, confirm email delivery, generate
AI suggestions or provide manager payment reporting.

## Lead collaboration and AI previews

New conversations remain in the shared queue while reserved. `assign` sets the
owner without sending; `release` makes the lead available again. SDRs and AEs can
assign or reassign any new lead to a current SDR or AE in the brand.
Reassignment stops once work starts. `take` is the separate **Send introduction**
action and only the assigned rep can use it. The queue filters All new, Unassigned,
and Assigned to me operate on the server before pagination. My conversations
defaults to Open, including assigned new and active leads; Not started, Active,
Customers and Archived remain separate filters. Assigned new leads also count
toward the personal unread indicator.

`read` records opening a conversation or clicking **Seen** in `sales_seen`;
listing the queue does not. **Seen by** displays the latest five current team
members who marked it seen or opened it, with timestamps. This is viewing evidence, not a read receipt for every
message. Queue polling refreshes assignments and viewing indicators every 15 seconds.

**Feedback** is available in each queue row and conversation. The prompt asks what
is wrong or needs checking; categories cover wrong contacts, poor fit, classification,
missing information, questions and other issues. Comments are append-only encrypted
Sales events, with author and timestamp, in the lead's history and audit transaction.
The latest 50 comments are shown to teammates who can access the lead. Feedback
never changes the stage, sends a message or automatically trains classification.

In the owned reply composer, SDRs and AEs browse searchable template previews
or click favorite template buttons. Favorites persist per personal template
without creating new content versions. Applying text resolves name/company
variables only and does not call AI. **Personalize with AI** optionally takes
extra instructions and creates a separate preview. The editor sits beside the
latest-first conversation on desktop, with stacked panels on smaller screens. `sales.draft_assist` uses the
workspace's own configured light model (`sales_reply_draft`) and budget ledger.
The template, saved draft and recent thread are untrusted source context. The
worker checks current membership, brand access, Sales role, ownership and draft
revision before calling the provider and before saving the preview. It does not
send or replace a reply. **Use this draft** checks the saved revision again and
keeps template provenance; **Send reply** remains a separate action.

Requests are UUID-idempotent, personal to their owner, and encrypted in
`sales_draft_assists`. One preview can be pending per owner and lead. A paid call
has a one-minute deadline and no automatic retry. A new request expires work that
was abandoned for more than two minutes, allowing recovery after worker loss.
The table follows canonical record merges, brand moves and erasure. Generating,
applying, expiring and failing previews each write audit/history events. No
provider key comes from the operator environment.

Sales lists and details show the canonical person's country and city, or Unknown.
Company headquarters country appears separately. The Lead Portal bridge can fill
missing contact countries from its existing LinkedIn verification receipts; this
is separate from lead delivery and never replaces an existing CRM country. These
facts use the regular record API, so merge, erasure and attribute history apply.
