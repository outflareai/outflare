# Accounts: the external accounts a brand's agent acts through

Some channels act through accounts rather than APIs: the brand's social profiles, community and review site accounts, partner and affiliate platforms, and sites with no API. Accounts (`engine/internal/accounts`, routes under `/v1/brands/{brand}/accounts`) lists every such account a brand's agent may use, how it signs in, whether access works, what depends on what, every attempt made on it and what stops it. It is the "Accounts" section of the brand agent plan, and the account service the browser workers will resolve sign-ins through.

## An account

| Field | What it is |
| --- | --- |
| `platform`, `label`, `identity`, `url` | The site or service, what the account is called there, the username or address it signs in as. |
| `sign_in` | A typed sign-in path: `local_password`, `social_sign_in` (through the identity in `depends_on`), `api_oauth` (through `integration_id`), `browser_session`, or `mailbox_verification` (through `mailbox_id`). An account can be recorded once per path it has. |
| `depends_on` | The brand's identity it signs in through (its Google account, its mailbox). Dependencies show as a chain (review site profile → Sign in with Google → the brand's Google account), and one upstream failure is one incident. |
| `status` | `unverified` (recorded, never used: recorded is not verified), `healthy` (the last use worked), `needs_attention` (an open incident), `blocked` (a person took it out of automated use, or a wrong identity or lock did), `archived`. |
| `secrets` | The kinds the vault holds for it (`password`, `totp_seed`, `recovery`, `session`), never their values. |
| `last_verified_at`, `last_attempt_at` | Freshness: when a use last worked and when one was last tried. |

Views: `GET .../accounts?view=all|needs_attention|dependencies|archived`. Brand-scoped like everything under `/v1/brands/`: another brand never sees a shared identity.

## Tracking is the tools' job

Every managed sign-in, verification and session use records an attempt (`POST .../accounts/{id}/attempts`) before acting and its evidenced outcome after, so nothing quietly uses an account outside the managed paths. Evidence is what was seen (a page title, a status, a provider's error code), never a secret, a code or page text. The outcome keeps the account's health in the same transaction (`accounts.Record`):

| Outcome | What happens |
| --- | --- |
| `authenticated` | The account is healthy; its open incidents resolve (a wrong identity stays until a person clears it). |
| `verification_pending` | An `info` incident, tried again in 6 hours. |
| `mfa_required` | A `warning` incident: waiting for a person; codes are never shown or stored. |
| `session_expired` | A `warning` incident: reconnect or approve scopes. |
| `credential_failed` | A `critical` incident: attempts stop until an owner updates or verifies the credential. |
| `wrong_identity`, `locked` | A `critical` incident and the account blocked: it never acts through another brand's session, and a locked account is escalated to its owner. |
| `rate_limited` | An `info` incident with a one-hour cool-down, kept apart from credential failures. |
| `unresolved` | Recorded; nothing changes. |

Incidents (`GET .../account-incidents`) have a kind, a severity, an owner, a repeat count and a next retry; a repeat merges into the open incident of its kind, and an account that signs in through a failing identity gets its incidents linked to that one as the root. A person owns, snoozes (monitoring goes on) or resolves them (`PATCH .../account-incidents/{id}`); resolving the last one puts the account back to unverified until its next use. Health updates from real use, never from repeated sign-ins across every account.

## The vault

`PUT .../accounts/{id}/secrets/{kind}` seals a secret with the server's key (`secrets.Box`, the same sealing as connector credentials, with the account and kind as associated data so a value never opens for another account); `DELETE` removes it. A secret is never returned over the API: the account shows only which kinds it holds. An agent asking to set or remove one always waits for a person, whatever its grants (`heldForBuiltin`), so secrets never pass through a model. Workers open a secret for one authorised use of the account (`accounts.Vault.Open`) and keep it out of context, observations, screenshots, logs and reports.

## Human takeover

When a managed use meets MFA, a CAPTCHA, a passkey or a consent screen, the tool asks for a person: `POST .../accounts/{id}/takeovers` with a `reason` (`mfa`, `captcha`, `passkey`, `consent`, `other`), a note for the person (never a code) and the incident it is for. A takeover is:

- **Short-lived.** A person starts it (`POST .../account-takeovers/{id}/start`, 15 minutes unless they ask for up to 60) and hands it back (`.../handback`); one left running expires on its own.
- **Account-scoped and exclusive.** One takeover waiting or under way per account; a second request gets 409. Only a person starts or hands back; an agent may request or cancel a waiting one.
- **Checked on handback.** The person says which `identity` they left signed in. The brand's own identity records an `authenticated` attempt (the account is healthy again and the incident it waited for resolves); any other records `wrong_identity` and blocks the account like any other wrong identity; none given records `unresolved`. Nothing about what the person did (codes, pages) is kept.

`GET .../account-takeovers` lists the ones waiting or under way (`status=all` for the history). Audit actions `account.takeover_requested`, `.takeover_started`, `.takeover_handed_back`, `.takeover_cancelled`.

## Offboarding

`POST .../accounts/offboard` takes a brand's accounts out of automated use in one transaction: every account is archived, every secret leaves the vault, takeovers are cancelled, open incidents are resolved, and the brand's action grants are revoked so no agent acts for it. It always waits for a person when an agent asks. The response says what was done (`archived`, `secrets_removed`, `incidents_resolved`, `takeovers_cancelled`, `grants_revoked`) and lists `needs_owner`: for each account, the action only the owner can take at the platform (`change_password`, `remove_access`, `disconnect_integration`, `sign_out`, `reconnect_mailbox`) and why. Audit action `brand.accounts_offboarded`.

## Scopes, events, monitoring

- `accounts.read` lists accounts, attempts and incidents; `accounts.manage` records, changes, blocks and archives accounts, records attempts, sets and removes secrets and handles incidents. The operator role has both.
- Audit actions: `account.created`, `.updated`, `.blocked`, `.unblocked`, `.archived`, `.secret_set` and `.secret_removed` (the kind, never the value), `.attempt_recorded` (purpose, outcome, whether an incident followed), `.incident_updated`.
- `outflare_accounts_attempts_total{purpose,outcome}`, `outflare_accounts_incidents_open{severity}`, `outflare_accounts_takeovers_total{step}`, `outflare_accounts_takeovers_waiting` and `outflare_accounts_takeovers_oldest_seconds`; the **Accounts** dashboard row; `OutflareAccountIncidentsCritical` when a critical incident stays open for a day and `OutflareAccountTakeoverWaiting` when a takeover waits four hours for a person ([monitoring.md](monitoring.md)).
- MCP tools: `list_accounts`, `create_account`, `record_account_attempt`, `list_account_incidents`, `update_account_incident`, `request_account_takeover`, `list_account_takeovers`, `offboard_accounts`.

## The page

**Company → Accounts** in the web app (`web/src/pages/BrandAccounts.tsx`): the brand's accounts by view, with their health, what they sign in through, what the vault holds (add or remove a password, an authenticator seed or recovery codes; the value is never shown again), block, unblock and archive; the open incidents with resolve; the takeovers waiting for a person (take over for 15 minutes, hand back saying who is signed in, cancel); the browser tasks under way; and offboarding, with the list of what the owner still does at each platform. Browser events `ui.account_recorded`, `ui.account_takeover_started`, `ui.account_takeover_handed_back`, `ui.accounts_offboarded`.

The browser workers that sign in through the vault, record attempts and ask for takeovers are in [browser-workers.md](browser-workers.md).
