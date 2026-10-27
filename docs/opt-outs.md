# Who can be emailed, and the opt-out list

Outflare connects the tools you send email with; it is not an email service provider, and it expects you to play by the rules. It still keeps you from the mistakes that get domains blocked. It has smart defaults, a few hard stops where a send would clearly be wrong, and plain-words warnings for everything else.

Every person has a **contact basis** that says how you got them and what may go out to them. Every send checks one **opt-out list** that every tool adds to, and that list is kept in step with the sequencers you connect.

## The contact basis

Each person record carries `contactability` (on `GET .../records/{record}` and every record list):

| `basis` | Who | Cold outreach | Marketing email (newsletters, product updates, journeys) |
| --- | --- | --- | --- |
| `opted_in` | Agreed to marketing email: a sign-up form, the preference page, the consents API | Yes | Yes |
| `opted_out` | Took that back | No | No |
| `customer` | Origin `first_party`: signed up, bought or used your product | Yes | Unless they said no (see the rule below) |
| `third_party` | Origin `prospect`: lead lists, data providers (Apollo and others), scrapes, sequencer imports, visitor reveals | Yes | **Never** without their own consent |
| `unknown` | From a CRM, an import or added by hand, with nothing saying how you got them | Yes | Unless they said no (see the rule below) |

`acquired_via` names the tool the record came from, such as `csv`, `apollo`, `attio` or `emailbison_import`. It is set as records are made and can be set with `POST`/`PATCH .../records` (`acquired_via`). `allowed` says yes or no for `transactional`, `cold_outreach` and `marketing`, and `why` says it in a sentence.

Receipts and account email (`purpose: transactional`) reach anyone who is not blocked outright. Cold email goes through sequences, never through broadcasts.

### The rule for people who never said yes or no

`GET`/`PATCH /v1/workspaces/{ws}/contact-rules` (MCP `get_contact_rules`, `set_contact_rules`) holds `customer_marketing`:

- `inferred` (the default): customers and people of unknown origin get marketing email unless they said no.
- `opt_in`: only people who agreed get it.

Either way third-party leads never get marketing email without agreeing. The rule applies to broadcasts, journeys and every lifecycle email at the moment it is sent. A skipped person's reason says which rule kept them out.

### Warnings, not walls

A broadcast that has not started shows its audience by basis (`audience_bases`) and `warnings` in plain words. One warning says how many people are third-party leads who won't get the email and what to do instead. Another appears when a quarter or more of the audience has no record of how you got them. A scheduled broadcast with a warning is on Home's to-do list (`email_broadcast_audience_warning` on `GET .../status`).

### Checking many people at once

`POST .../recipients/check` (MCP `check_recipients`) answers for up to 1,000 addresses with a reason code each. With `purpose: lifecycle` it also applies the contact basis: `third_party_no_consent` or `no_consent`, beside the opt-out list's `suppressed_email:<reason>`, `suppressed_domain:<reason>` and `marketing_withdrawn`. With `cold` (the default) only the opt-out list counts, and with `transactional` unsubscribes let receipts through.

## The opt-out list

`GET /v1/workspaces/{ws}/suppressions` (MCP `list_suppressions`) is the one list every send checks: every unsubscribe, bounce and complaint from any tool, and the entries people add. Each entry says:

- **why** (`reason`). `unsubscribed` stops cold and marketing email but not receipts. `competitor` stops cold and marketing email. `customer`, `partner` and `blocklist` (on a connected sequencer's blocklist) keep people out of cold email only. `bounced`, `complained`, `manual`, `legal` and `do_not_contact` stop every email.
- **how** (`method`): `manual` (a person), `automated` (a bounce, an unsubscribe link, a sync) or `ai_suggested`, when the AI read a reply as an opt-out. An AI suggestion has the AI's `confidence` and the reply in `evidence`.
- **who** (`added_by`, `added_by_type`) and **from which tool** (`source`: `web`, `api`, `mcp`, `reply`, `smtp`, `unsubscribe_link`, `email_provider`, `bounce_report`, `<connector>_sync`, `<tool>_import`, or a label you give). `integration_id` names the connection for entries read from a sequencer.

Filter with `reason`, `method`, `source` and `integration_id`.

Add up to 100,000 emails or domains at once with `POST .../suppressions` (MCP `add_suppressions`). That is how a list moves over from an old tool: give `source` (such as `hubspot_export`) and `reason` (say, `competitor` for rivals' domains).

## Kept in step with your sequencers

When EmailBison, Instantly or Smartlead is connected, every platform sync (every five minutes) does two things:

1. It reads the platform's blocklist into the opt-out list, with reason `blocklist` and source `<connector>_sync`. A blocklist only says not to cold email someone: it isn't an unsubscribe, so it changes nobody's consent and still lets receipts and marketing email through. The Suppression page shows where each entry came from. Smartlead's list cannot be read, but its unsubscribed leads arrive with campaign imports.
2. It writes the opt-out list to the platform's blocklist, each entry once. Entries read from that platform are not written back to it.

So a campaign sent from the sequencer honours Outflare's list, and the other way round. `GET .../suppression-syncs` (MCP `list_suppression_syncs`) shows each one's counts and last error, and `PATCH .../suppression-syncs/{integration}` turns `pull` or `push` off. Nothing is ever removed from the platform's list.

Both run on the workspace's own connection to the sequencer.
