# Sales workflow

Sales is a brand-scoped workflow over canonical customer records. The existing
Sales workspace opens the queues and full-page conversation; its directory,
collections, reports and settings remain reachable through “People, companies &
CRM”. SDRs and AEs share New leads. Taking reserves an owner and queues the
original EmailBison introduction in the same transaction. Handoffs transfer
ownership only after the personal sender's provider accepts the introduction.
Customer confirmation and optional value are independent of payments.

## Screens and roles

SDRs and account managers enter at **New leads**. Each row includes the
prospect's latest incoming reply, arrival time and available website, LinkedIn
and email links from the canonical CRM records. **Open conversation** is a
read action; **Take & open** separately claims the lead and queues its
introduction. A disconnected rep can read eligible conversations while sending
and taking remain disabled until personal consent is complete.

Managers enter at **All leads**, with status and teammate filters, the latest
message, ownership and recorded activity. They can also review **New leads**
and open full conversations without taking ownership or assuming a rep's role.
Personal settings are secondary; **My email** explains why a manager does not
need a sending connection to review work. Workspace administration and a
person's assigned Sales role remain separate, so an administrator assigned as
an SDR or account manager retains that role's working queues.

Queue excerpts contain at most 1,200 characters. They are read from the stored,
sealed conversation after workspace, brand and queue access checks; superseded
provider copies are omitted. Manager activity is not included in rep queue
responses. Source links accept only HTTP(S) URLs in the UI. An empty queue shows
an empty state, never fictional customer data. The API panel remains available
on demand and does not inherit an open state from another product page.

## Data and consent

Sales tables belong to Outflare's database and all carry workspace and brand
IDs. Leads, messages, drafts and events follow their canonical
`record_id` during merges; physical record deletion cascades through evidence.
A brand move moves the related workflow; the receiving brand must have its own
eligible members and connection access. Every workflow mutation adds protected
evidence and a pointer in that record's attribute history, plus an audit event.

Templates retain every version. Applied drafts retain the selected version and
resolved text, edited drafts append revisions, and sent messages reference the
exact draft. Provider identities and RFC Message-IDs deduplicate messages;
provider copies retain mailbox-local thread IDs. A receipt racing synchronization
supersedes the duplicate view without deleting its evidence. All content and
credentials are sealed and included in key rotation.

Sales Google authorization is separate from read-only personal CRM capture. It
requires `gmail.readonly` and `gmail.send`, the invited user's own verified Google
email, a live session, state, nonce and PKCE. Add
`/v1/sales/google/callback` to the existing Google OAuth application's redirect
URIs. The person explicitly agrees to sharing these Sales conversations with
assigned colleagues and Sales managers. Workspace/brand membership and Sales
role changes revoke the corresponding grants. No workspace API token can grant
personal consent or send a personal reply.

The original EmailBison connection is selected by the source account mapping.
Its reply ID and sender ID are verified before sending. The colleague's CC is
preserved and every To/CC recipient passes the normal outbound policy. Personal
replies preserve RFC references and resolve Gmail's mailbox-local thread ID.
Provider calls happen only in jobs.

Each connected mailbox polls every five minutes. Polling reconciles from each
conversation's beginning, follows every page and resolves reference chains even
when a provider returns children before parents. Reconnection uses the same
historical starting point. Draft/spam/trash mail and unrelated conversations stay
out; explicit privacy exclusions still apply. Configure CRM internal domains
before enabling personal capture.

A send persists `sending` before the network call. A crash, timeout or ambiguous
server response never automatically resends it. Gmail reconciliation searches
Sent by the generated RFC Message-ID. Definitively refused sends can be retried
with the same saved message after repairing the connection or policy. EmailBison
does not expose a documented idempotency key: when its send receipt is lost,
the result remains uncertain and requires a manager to verify the original
provider thread. Automated resolution of that case is not implemented. Acceptance
is displayed as provider acceptance, not delivery confirmation.

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

## Local validation

The local browser fixture is `web/tests/sales-smoke.mjs`. It requires a disposable
local engine/database, the production web build and `OUTFLARE_CONNECTORS_ALLOW_PRIVATE=true`
for its local mocked EmailBison server. It cannot send real email.

The fixture exercises full-page conversations, template application, editable
drafts and separate sending controls. Provider tests cover thread headers,
recipient policy, deduplication, reconnect catch-up and uncertain sends.

The workflow does not archive attachments, confirm email delivery, generate
AI suggestions or provide manager payment reporting.
