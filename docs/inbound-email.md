# Inbound email

Inbound email gives each workspace addresses that people forward mail to, so Outflare can read it for them. Each address has a **purpose** that decides what happens to its mail:

- **Support**: a business forwards its own support mailbox (`support@yourdomain`) here, and every customer email becomes a conversation in the [support inbox](support-inbox.md).
- **AI receipts**: members forward the receipts from Anthropic (Claude), OpenAI (ChatGPT) and Cursor, and AI Accounts keeps each account's price, seats and renewal date up to date.

Inbound email is part of **Outflare Cloud**. On a self-hosted server the setup pages say so, and the API lists no addresses (`available: false`). Self-hosted support connects its mailbox instead.

## Addresses

Addresses live at Outflare Cloud's inbound domain, `agentsget.email` (its hosted mailboxes' domain).

- **Support addresses read like support inboxes**: the brand's handle (or the workspace's name) and `-support`, such as `northpeak-support@` followed by the inbound domain, one per brand, so mail to each lands under its brand. An admin can rename one (`help-northpeak@`); the old name then refuses mail, and taking an old name back works.
- **AI receipts have one readable address per workspace**, the workspace's name and `-receipts`, such as `acme-receipts@` followed by the inbound domain. It is made as soon as anyone opens AI accounts (or lists the addresses), so nobody has to press a button; the page still offers **Generate email address** if that ever fails. A receipt only counts when it carries a valid DKIM signature from the AI company or the payment processor that sent it, so a readable address doesn't let anyone fake one. An admin can rename it (`name`) or replace it.

Admins make them under the purpose's settings, or with `POST /v1/workspaces/{ws}/inbound-addresses` (`purpose`, `name` to rename, and for support `brand`). `GET` on the same path lists them and the purposes the workspace can use.

## Who can forward

Each purpose decides:

- **Support** takes mail whoever forwarded it: it is the business's own support mailbox, and customers write to it in the open anyway.
- **AI receipts** only takes mail someone in the workspace forwarded: a member's own work address (a Google Workspace admin can add one routing rule that copies receipts from every inbox; mail such a rule copies, which says nothing about who forwarded it, also counts when it was sent to anyone at the domain of a member's own or linked address, except personal mail providers such as gmail.com; it names no member as its forwarder), or a personal address a member linked first. Mail forwarded by anyone else is refused (`unknown_forwarder`). A member links one with `POST /v1/workspaces/{ws}/inbound-forwarders` (`email`), lists theirs with `GET` (everyone's with `everyone=true` and `ai_accounts.read`), and unlinks it with `DELETE .../inbound-forwarders/{email}`.

**Gmail asks to confirm forwarding.** When a Gmail or Google Workspace mailbox is set to forward, Gmail first sends a confirmation to the address. Outflare keeps it: for a support address it is shown on that address for an admin to confirm with one click; for receipts, to the member who linked their personal address, even when the confirmation came before they linked it (within 30 days). Outflare never opens the link itself. Each message's DKIM signatures are checked and kept.

## What is kept

Every message's details (when it arrived, who sent and forwarded it, its subject, which signatures checked out and what became of it) are kept with the workspace, and admins with `audit.read` see the latest under `GET /v1/workspaces/{ws}/inbound-emails`. The raw message is deleted after 30 days. Message content is only ever read as data.

## For operators and extensions

An extension turns inbound email on with `Host.EnableInboundEmail(domain, secret)`, receives mail for the domain at its own edge, and delivers each message to `POST /v1/inbound/email`: the raw message as the body, the recipient in `X-Outflare-To`, and `X-Outflare-Timestamp` with `X-Outflare-Signature` from `outflare.SignInboundEmail`. A 404 means there is no such address and the message should be refused.

An extension that already receives the domain's mail in its own process (Outflare Cloud's hosted mailboxes) uses `Host.EnableInboundEmailAt(domain)` instead and hands each message to `Host.DeliverInboundEmail(ctx, to, raw)`, which returns `outflare.ErrNoInboundAddress` for an address that doesn't exist. Then the signed POST is refused. When the domain is shared, `Host.SetInboundAddressTaken` tells the engine which addresses the extension uses, so it never picks one, and `Host.InboundAddressTaken` tells the extension which are the engine's.

## For engine code

A purpose registers with `inboundemail.Service.Register(name, Purpose{...})`: its handler, whether any address may forward to it (`AnyForwarder`), and, for readable addresses, a `Suffix` such as `support` (per brand, or one per workspace with `Workspace`). `Auto` makes the workspace's address the first time anyone looks for it. Only registered purposes get addresses. New purposes are also added to the check on `inbound_addresses.purpose`.
