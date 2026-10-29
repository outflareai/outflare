# Inbound email

Inbound email gives each workspace addresses that people forward mail to, so Outflare can read it for them. Each address has a **purpose** that decides what happens to its mail:

- **Support**: a business forwards its own support mailbox (`support@yourdomain`) here, and every customer email becomes a conversation in the [support inbox](support-inbox.md).
- **AI receipts**: members forward the receipts from Anthropic, OpenAI, Cursor and GitHub, and AI Accounts keeps each account's price, seats and renewal date up to date.

Inbound email is part of **Outflare Cloud**. On a self-hosted server the setup pages say so, and the API lists no addresses (`available: false`). Self-hosted support connects its mailbox instead.

## Addresses

Addresses live at Outflare Cloud's inbound domain, `in.outflare.ai`.

- **Support addresses read like support inboxes**: the brand's handle (or the workspace's name) and `-support`, such as `northpeak-support@` followed by the inbound domain, one per brand, so mail to each lands under its brand. An admin can rename one (`help-northpeak@`); the old name then refuses mail, and taking an old name back works.
- **Other addresses have letters nobody can guess**, one per workspace, such as `acme-7q4m2x9kpt@`, so nobody outside can send fake receipts. An admin can replace one if it leaks.

Admins make them under the purpose's settings, or with `POST /v1/workspaces/{ws}/inbound-addresses` (`purpose`, and for support `brand` and `name`). `GET` on the same path lists them and the purposes the workspace can use.

## Who can forward

Each purpose decides:

- **Support** takes mail whoever forwarded it: it is the business's own support mailbox, and customers write to it in the open anyway.
- **AI receipts** only takes mail someone in the workspace forwarded: a member's own work address (a Google Workspace admin can add one routing rule that copies receipts from every inbox), or a personal address a member linked first. Mail forwarded by anyone else is refused (`unknown_forwarder`).

**Gmail asks to confirm forwarding.** When a Gmail or Google Workspace mailbox is set to forward, Gmail first sends a confirmation to the address. Outflare keeps it: for a support address it is shown on that address for an admin to confirm with one click; for receipts, to the member who linked their personal address. Outflare never opens the link itself. Each message's DKIM signatures are checked and kept.

## What is kept

Every message's details (when it arrived, who sent and forwarded it, its subject, which signatures checked out and what became of it) are kept with the workspace, and admins with `audit.read` see the latest under `GET /v1/workspaces/{ws}/inbound-emails`. The raw message is deleted after 30 days. Message content is only ever read as data.

## For operators and extensions

An extension turns inbound email on with `Host.EnableInboundEmail(domain, secret)`, receives mail for the domain at its own edge, and delivers each message to `POST /v1/inbound/email`: the raw message as the body, the recipient in `X-Outflare-To`, and `X-Outflare-Timestamp` with `X-Outflare-Signature` from `outflare.SignInboundEmail`. A 404 means there is no such address and the message should be refused.

## For engine code

A purpose registers with `inboundemail.Service.Register(name, Purpose{...})`: its handler, whether any address may forward to it (`AnyForwarder`), and, for readable per-brand addresses, a `Suffix` such as `support`. Only registered purposes get addresses. New purposes are also added to the check on `inbound_addresses.purpose`.
