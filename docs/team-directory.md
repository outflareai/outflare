# Team directory

The team directory (Settings, Team) lists everyone who works at the company, whether or not they sign in to Outflare, and the work accounts the company owns and hands to them. It is workspace-level, like members and teams, because one person can work across brands.

## People and departments

Each person has a name, an optional work email (stored in lower case, one per person per workspace), a title, a department, a team and a manager (another person in the directory). A person can be linked to their Outflare sign-in.

A person is `active`, `leaving` (their handover has started) or `left`. Someone who has left is kept for history. Someone added by mistake can be removed, unless they have held a company account: then mark them left instead, so the account's history stays whole.

Departments have a name and a head. A team (Settings, Teams) can sit in a department and have a lead. Deleting a department keeps its people and teams; they just have no department.

## Company accounts

A company account is a Telegram account, a work email or a phone number the company owns. Accounts belong to the company, not the person: when someone leaves, the account passes to the next holder.

- **Address.** Stored normalised: an email in lower case, a phone number in E.164 (`+447700900123`), a Telegram username without the `@` in lower case (or its phone number until it has a username). One account per address and kind in a workspace.
- **Holder.** Who has it now. Handing it on closes the previous holder's time with it and opens the new one; `GET /v1/workspaces/{ws}/staff-accounts/{id}` lists every holder, newest first. Someone who has left can't be given an account.
- **Renews on.** When it must next be renewed or topped up (a phone number's plan), so nobody loses it by letting it lapse.
- **Retired.** No longer used. Retiring ends the holder's time with it. An account that has had a holder can't be removed, only retired.

## Company Telegram accounts in Outflare

A company Telegram account signs in to Outflare once, as a linked device, the way Telegram Desktop does. Outflare only reads its chats, so they can reach customer records; it never sends from it.

1. **Connect your Telegram app** (Connectors, Telegram): the App api_id and api_hash your company registers at [my.telegram.org](https://my.telegram.org/apps). Every account signs in through your own app, never one of Outflare's. Optionally add a proxy (`socks5://user:password@host:port`) from your own provider, in the accounts' country, so Telegram always sees them from the same place.
2. **Sign in** (Settings, Team, the account's menu, Sign in to Telegram): a QR code appears. On the account's phone, open Telegram, Settings, Devices, Link Desktop Device, and scan it. A new code replaces it every 30 seconds; after three minutes without a scan, start again.
3. **Two-step password.** When the account has one (it should), type it once. It is sealed, used to finish signing in, and deleted.

The session is sealed with the server's key and shows in the account's Telegram as a device named Outflare, where it can be ended at any time. Connected accounts are checked every six hours: who they are signed in as, whether a two-step password and login email are set, and how many other devices are signed in. When a session ends, the account shows as signed out and needs a new scan. Retiring an account signs it out; an account signed in here can't be removed.

**Lock it down.** Each account has a checklist so the company never loses it: the number on the company's own account, a two-step password, a company login email, a passkey, the password in the company's password manager, unknown devices signed out, and account deletion set to its longest. The two-step password and login email are read from Telegram; the rest are ticked by hand.

## Syncing chats into the CRM

Once an account is signed in, its chats can be copied into one brand's CRM (the account's menu, Sign in to Telegram, Chats in the CRM): pick the brand and how far back the history goes (a year by default). Outflare copies the history first, a few thousand messages at a time, then reads new messages every two minutes. It only reads: nothing is sent, and messages stay unread on the account's phone.

- **What is left out.** Saved Messages, bots and channels people only read are never copied. A chat with another company account in the team directory is kept as internal, and its messages aren't copied.
- **Sealed.** Names, usernames, phone numbers and message text are sealed with the server's key, like CRM mail. Files aren't copied, only what was attached (a photo, a voice note, a file and its name).
- **Who sees it.** Everyone who can read the brand's customers (`customers.read`) sees its chats, except a chat marked private, which only the account's holder and people who manage the team (`members.manage`) see. A chat marked personal (not company business) has what was copied removed, and nothing more is copied.
- **One brand.** An account's chats stay in the brand they were first synced into.
- **Pausing.** Pause stops new messages; what was copied stays. Start it again to carry on, or pick a longer history to copy further back.

## Match chats to customers

Each synced chat is matched to a customer record (a person, or a company for a group), so a customer's Telegram conversations sit on their record with everything else.

1. **Outflare looks first.** A record that already has the chat's Telegram id is linked straight away. Otherwise Outflare looks for a record with the same phone number or username, an email address the other side wrote in the chat, or, in a group, a company's domain. These are suggestions for a person to confirm.
2. **Then the workspace's AI.** For what's left, Outflare asks the workspace's own AI (Settings, AI, Telegram matching) to pick from records with a similar name, reading the chat's last few messages. It only picks a record it was shown, and it may say a chat looks personal; that is never applied by itself. Without an AI connected, those chats wait as "no match".
3. **A person confirms.** CRM, Telegram, To confirm lists the chats on your own accounts that wait for you: confirm the suggestion, pick another record, create a new one, or mark the chat personal or a colleague. Confirming gives the record the chat's Telegram id (and its username and phone), so the next chat with them, on any company account, links by itself. Changing it later moves the Telegram id to the new record.

## Summaries and alerts

- **Waiting for an answer.** When a customer writes last in a private chat, the chat shows how long they have waited (CRM, Telegram, Waiting for an answer). After four hours the account's holder gets a `telegram.waiting` alert, once per wait, by email or Slack as the brand chooses in Settings, Alerts. Waits more than three days old (history from the import) never alert.
- **Summaries.** Open a chat and press Summarise: the workspace's own AI (Settings, AI, Telegram summaries) writes what they want, what was agreed, what is still open and who owes the next message. It reads the last 150 messages. Summaries are sealed like the chat and say when new messages came in after them. Without an AI connected for the task nothing is written.
- **Handovers.** When a synced account is given to someone else (Settings, Team), its customer chats from the last 30 days are summarised for them, and they get a `telegram.handover` alert linking to the chats.


| What | Endpoints | MCP tools |
| --- | --- | --- |
| People | `GET`/`POST /v1/workspaces/{ws}/staff`, `GET`/`PATCH`/`DELETE .../staff/{id}` | `list_staff`, `get_staff_member`, `create_staff_member`, `update_staff_member` |
| Departments | `GET`/`POST .../departments`, `PATCH`/`DELETE .../departments/{id}` | `list_departments`, `create_department` |
| Company accounts | `GET`/`POST .../staff-accounts`, `GET`/`PATCH`/`DELETE .../staff-accounts/{id}` | `list_staff_accounts`, `get_staff_account`, `create_staff_account`, `update_staff_account` |
| Telegram sign-in | `GET`/`PATCH .../staff-accounts/{id}/telegram`, `POST .../telegram/connect`, `.../password`, `.../check`, `.../disconnect` | `get_telegram_session` |
| Telegram sync | `POST .../staff-accounts/{id}/telegram/sync`, `POST .../telegram/sync/pause` | |
| Synced chats | `GET /v1/brands/{brand}/telegram-chats` (`match_status`, `mine` for the queue, `waiting`), `GET`/`PATCH .../telegram-chats/{id}`, `GET .../telegram-chats/{id}/messages`, `POST .../telegram-chats/{id}/match`, `POST .../telegram-chats/{id}/summary` | `list_telegram_chats`, `list_telegram_messages`, `update_telegram_chat`, `match_telegram_chat`, `summarize_telegram_chat` |

Reading needs the `members.read` scope; changing needs `members.manage`. Synced chats need `customers.read`, and `customers.manage` to sort and match them. Every change is in the audit log: `staff.created`, `staff.updated`, `staff.leaving`, `staff.left`, `staff.reactivated`, `staff.deleted`, `department.created`, `department.updated`, `department.deleted`, `staff_account.created`, `staff_account.updated`, `staff_account.assigned`, `staff_account.unassigned`, `staff_account.deleted`, `telegram_account.connect_started`, `telegram_account.password_entered`, `telegram_account.connected`, `telegram_account.signed_out`, `telegram_account.checklist_updated`, `telegram_account.sync_started`, `telegram_account.sync_paused`, `telegram_chat.updated`, `telegram_chat.matched`, `telegram_chat.unmatched` and `telegram_chat.summary_requested`.
