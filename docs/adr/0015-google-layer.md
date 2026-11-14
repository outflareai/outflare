# 15. One Google layer for every module

Status: Accepted (13 November 2026). Step 1 (the decision, the Workspace connection reporting what it may do, and sending as a member) is in place; the steps after it are listed below. Keeps [ADR 11](0011-open-core-line.md): the layer is a tool, open like every tool.

## Decision

Gmail and Google Calendar are reached through one layer that every module shares, not a connection, consent and sync of each module's own. CRM capture, Sales conversations, the relationship check, meetings, and anything later that reads or sends a person's work email use it.

- **One connection for the company.** The **Google Workspace** connection (`google_workspace`) is the company's own service account with domain-wide delegation (bring your own keys). It reports which scopes Google lets it use (`gmail.readonly`, `gmail.send`, `calendar.readonly`), and a module offers what those allow. Adding a scope is one line in the Google Admin console, never a change to Outflare's OAuth app.
- **One mailbox per person.** Each member whose mail Outflare reads has one mailbox: how it is reached (the Workspace connection, or the person's own Google sign-in for teams without a Workspace admin), the scopes it has, where its mail and calendar syncs got to, and the brand it belongs to. Modules never keep their own grant or copy of the token. Today this is `crm_google_connections`; it becomes workspace-level as the steps below say.
- **One personal sign-in.** For a person signing in themselves, there is one Google consent and one callback for every module, asking for more scopes only when a module needs them (incremental authorisation). A module that needs to send asks for `gmail.send` on top of what the person already gave.
- **One sync, one store.** A person's mail is read once, by the CRM capture sync, into `crm_mail_messages`, under its filters, sharing rules and AI privacy check; their calendar meetings once into `crm_calendar_events`, private like their email. Every module reads those. No module syncs Gmail itself, so the same email is never stored twice under different rules.
- **One way to send as a person.** Sending from a member's own address goes through the send service (`POST /v1/brands/{brand}/crm/mail/sends`): the recipients pass the outbound policy check (`policy.CheckRecipientsIn`, like any reply), the send runs in a job, and the sent email is kept on the record like captured mail. A module never calls Gmail's send API itself.

## Why

Without one layer, each module that needs mail would add its own Google sign-in, sync and send: people would approve Google several times, the same mail would sit in several stores under different privacy rules, and some sends would skip the policy check every email must pass. Outreach mailboxes already sign in to Gmail on their own. One layer gives every module the same access, privacy and audit, and an admin one place to grant or take it away.

## Consequences

Steps, each its own pull request:

1. **This decision.** The Workspace connection reports its scopes; the send service sends as a member through it, after the policy check, from a job, without sending twice on a retry; the sent email is kept on the record.
2. **Mailboxes become workspace-level.** `crm_google_connections` (one per brand and person) becomes one mailbox per person with the brand it belongs to, so a person spans brands without connecting twice.
3. **One personal sign-in with incremental scopes.** The CRM capture consent becomes the shared one (`/v1/google/callback`); a module asks for an extra scope and the person approves only that.
4. **Modules move onto it.** Sales conversations read captured mail and outreach messages (matched on Message-ID) and send through the send service. Outreach mailboxes that sign in with Google may later use the same mailbox.

Also decided:

- **A person sends from the address they choose:** their own (this service) or the outreach mailbox a lead was contacted from. So the send service grows to send through outreach mailboxes too, through the same policy check, rather than a module calling the sending engine itself.
- **Meetings are as private as email:** a member's calendar meetings are their own and shared only as their email is, never in the team-visible `meetings`.
- **Sorting inbound email with AI on its own is a loop** ([ADR 11](0011-open-core-line.md)): it reads the store this layer fills, through the engine's hooks.
- **The Workspace connection comes first.** The shared personal sign-in (step 3) is for teams without a Workspace admin.

Microsoft 365 follows the same shape when it comes: one connection for the tenant, the same mailbox per person, store and send service.
