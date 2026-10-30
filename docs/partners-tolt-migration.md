# Moving from Tolt to Outflare Partners

Partners replaces Tolt for referral, affiliate and influencer programs ([partners.md](partners.md)). The move is a hard switch on one day, not a parallel run:

1. Import ahead of time.
2. On the day, run a final import.
3. Change your backend's calls.
4. Swap the snippet.
5. Check a test conversion.
6. Cancel Tolt.

Partners' links and codes stay the same, so nothing they've shared breaks.

Outflare has its own conversion API. There is no Tolt-compatible endpoint, so each Tolt call becomes one Outflare call, as mapped below.

Tolt's API docs couldn't be fetched while this guide was written. The fields of Tolt's transactions and commission webhooks below were read from a real Tolt account. **Field names still marked _check_ come from Tolt's public docs as remembered: check them against your own Tolt integration** before you change your code.

## Before the day

- **Create the programs.** Make one program per Tolt program, with its rewards, hold (Tolt's "commission delay") and minimum payout. The credit window replaces Tolt's cookie duration.
- **Connect Tolt.** Connectors → Tolt, with your own Tolt API key (Tolt: Settings, Integrations). Outflare only reads it.
- **Turn auto-approve off** on the program until the day, so Tolt's own decisions on commissions come over instead of Outflare approving them first.
- **Import from Tolt.** Partners → program → Settings → Import from Tolt (or `POST .../partner-programs/{id}/imports`, or the `start_partner_import` MCP tool). Run it as often as you like before the day: see [the import](#the-import). When your Tolt account has more than one program, pick it from the list (`GET .../partner-programs/{id}/import-sources`, or `list_partner_import_sources`); with none chosen, the import takes the only active one.
- **Make a backend token.** Make an API token with only the `partners.conversions` scope, for your backend.
- **Add a tracking domain** (optional). With one such as `go.example.com` ([website tracking](website-tracking.md)), partner short links read `https://go.example.com/r/<code>`.

## The import

Each run reads the whole Tolt program and brings in what isn't in Outflare yet. Every object is matched by its Tolt id, so running it again never adds anything twice: it adds what's new and brings over what changed.

| From Tolt | Becomes |
| --- | --- |
| Groups | Groups of the same name (made when missing). Tolt's default group becomes the program's default when it has none. Read from `/groups` _check_; if Tolt doesn't list them, the run notes `groups_unavailable` and goes on. |
| Partners | Partners, each a customer record found by email. Each joins their Tolt group (`group_id` _check_); on a later run a partner the import made follows a group change in Tolt. Their code is their first link's value, so `?ref=<code>` and `?via=<code>` links keep working. Every status is read (active, pending, suspended, rejected and invited) and kept; invited partners come over as pending. Tolt's API has no payout details, so partners give theirs on the portal; until they do, their payouts wait (their balance is kept). |
| Links | The partner's code. A link with another parameter, such as `?aff=`, adds it to the program's `url_params`, so the website reads it too (`link_param_added`). A partner's second link, or a parameter that can't be added (a name Outflare reads already, or the program has 5), is listed under issues. |
| Flows | Flows on the Flows tab, for the same group: the trigger (new payment or new signup), the action (a percentage or fixed commission, or switch group), the duration (lifetime, first payment, first N months), approval (automatic, after N days, manual), AND/OR conditions and custom rates per partner or plan. Read from `/flows` _check_, with every field name _check_. A flow with anything Outflare can't map (a condition on an attribute it doesn't know, say) isn't imported, so nothing pays that shouldn't: it is listed under issues, for you to add on the Flows tab. |
| Promo codes | The partner's promo codes, kept as they are: they already exist in your billing account. |
| Customers | Referred customers, matched by your own `customer_id` and email the same way the conversion API matches them. Tolt's own customer id (`cus_...`, what `tolt.signup()` returned) is kept as one of their user ids, so if your backend stored it and reports renewals with `customer_id: "cus_..."`, those still credit the partner. Running the import again adds it to customers an earlier run brought over. |
| Transactions | The partner's sales (and refunds), with source `import`. They are not new events on the customer's timeline: your billing or backend already told attribution about them. |
| Commissions | Rewards with their status: pending ones wait out the program's hold, approved ones are owed in the next payout, paid ones are in paid payouts (one per Tolt payout, or per partner and day) and rejected ones are rejected. Approved and paid commissions count as spend on the day they were earned. |
| Clicks | Clicks on the partner's links (`/v2/clicks`), with when and the referring host only, so each partner's click count carries over. Each counts once, however often you run the import. They start nothing: no events, no attribution. |

Importing sends no reward webhooks, so your system never grants an old reward twice. New partners do send `partner.created`.

On a later run, a partner's status follows Tolt unless someone changed it in Outflare, and a commission still pending in Outflare follows Tolt (approved, paid or rejected). A commission Tolt rejects after Outflare approved it is listed under issues, for you to correct with an adjustment.

Each run lists what it couldn't bring over, by Tolt id and reason (never names or addresses):

| Reason | What to do |
| --- | --- |
| `no_email` | Add the partner's email in Tolt and run again, or add the partner in Outflare. |
| `code_changed` | The code was taken or has characters Outflare doesn't allow, so the partner got a new one. Send them their new link. |
| `extra_link`, `link_param` | Old links with that value or parameter stop crediting. Tell the partner to share their Outflare link. |
| `link_param_added` | The program now also reads that parameter on your website (Settings → Other link parameters), so those links keep crediting. Nothing to do. |
| `partner_missing`, `customer_missing` | What it belongs to didn't come over (see its own issue). |
| `partner_unavailable` | A partner a promo code, customer or commission names that Tolt's list left out, and that Tolt wouldn't give by id either; the message has the HTTP status. |
| `partner_id_missing` | A promo code with no partner under the fields the import reads. The message lists the promo code's field names (never values): send it to us so the import can read the right one. |
| `partners_unlisted` | Tolt reports more partners than its list gave. The message has the counts, and says whether any of those has referrals (a customer, commission or promo code naming a partner who didn't come over). |
| `promo_code_invalid`, `promo_code_taken` | Add the code by hand, with a new name if it's taken. |
| `already_referred` | Another partner already has that customer in Outflare: the first partner keeps them. |
| `status_conflict` | Rejected in Tolt after Outflare approved it: add a negative adjustment. |
| `flow_condition_unsupported`, `flow_action_unsupported`, `flow_trigger_unsupported`, `flow_duration_unsupported`, `flow_approval_unsupported`, `flow_custom_rate_unsupported`, `flow_invalid` | That flow didn't come over. Add it on the Flows tab (a template may be close). |
| `groups_unavailable`, `flows_unavailable` | Tolt didn't list them for this account. Add the groups and flows by hand. |
| `group_missing`, `too_many_groups`, `custom_rate_partner_missing` | What the flow or partner belongs to didn't come over, or the program already has 50 groups. A flow whose custom rate names a missing partner comes over without that rate. |
| `customer_id_taken` | Another customer record already has that Tolt `cus_...` id, so conversions sent with it credit that record's partner, if any. Merge the two records if they are the same person. |

| `unreadable` | Tolt's answer for a whole kind couldn't be read (not JSON, no list in it) or Tolt refused the request. The import read the other kinds; the issue's message names the kind, the HTTP status, the content type and the answer's top-level keys. Send us that message. If no kind could be read, the run fails with it. |

The import reads Tolt's v2 API (`https://platform.tolt.com/v2`, the same API key as v1): it pages with `next_cursor` until `has_more` is false, reads partners one status at a time so none is left out (an account whose list refuses the invited filter just has none listed), then reads by id any partner a link, promo code, customer or commission names that the list left out (Tolt lists only onboarded partners), waits as Tolt asks when it is rate limited (25 calls a second per key), and reads amounts as minor units (cents). A commission in a Tolt payout comes over as paid. Tolt's v2 has no payout details: the portal asks each partner to add theirs, "No payout details" on the Partners tab lists who hasn't, and their payouts wait until they do. The import reads a list however Tolt wraps it: `data` as a list, `data.data` (as Tolt's customers list answers), or a list under the kind's own name, with `has_more` as a boolean or a string. When Tolt sends `total_count`, the summary shows it beside what was read ("partners: 500 read of 934 Tolt reports"), per status for partners (`reported_by`), and the engine log notes it with the first page (`tolt list`). If Tolt says a list has ended right after a full page, the import asks once more after the last object's id, in case the list stops at a cap rather than at its end; a list that really ended answers with nothing.

If Tolt answers the next page with the same objects again, the run stops with the error code `pagination_stuck` rather than reading forever. Tell us: it means Tolt pages differently from what the import expects.

Tolt's API docs couldn't be read from where this was built, so the import reads each field under the names Tolt has used (_check_). **Run it on your account well before the day and check the counts and issues against Tolt.**

## The calls, side by side

| What happens | Tolt | Outflare |
| --- | --- | --- |
| A visitor lands from a partner link | `tolt.js` reads `?ref=` and keeps a referral cookie (`tolt_referral` _check_) | The Outflare website snippet reads `?ref=` and records the visit on the person, no cookie code of yours needed. Keep `ref` in your own cookie too if you want to send it explicitly. |
| Someone signs up | Create a customer: `POST /v1/customers` with `email`, `customer_id`, and the referral (`partner_id`, `click_id` or the cookie value _check_) | `POST /v1/workspaces/{ws}/partner-conversions` with `{"type": "signup", "customer_id": "...", "email": "...", "ref": "..."}`. `ref` is optional when the snippet saw their visit. |
| Someone pays | Create a transaction: `POST /v1/transactions` with `amount` (integer cents), `customer_id`, `charge_id`, `billing_type` (`subscription`, or one-off), `interval` (`month`), `source` (`STRIPE`), `click_id` and `created_at` | `{"type": "sale", "customer_id": "...", "amount_cents": 4900, "currency": "USD", "external_id": "<charge_id>", "product_id": "...", "recurring": true}`, with `recurring` for a renewal. Send the same `customer_id` you sent Tolt: if that was Tolt's own `cus_...` id, the import keeps it on the customer, so renewals still credit the partner. |
| A payment is refunded | Refund a transaction (by `charge_id` _check_) | `{"type": "refund", "refund_of": "<charge_id>", "external_id": "<refund id>"}`. Leave out `amount_cents` to refund it all, or send it for a partial refund. |
| Anything else that should pay out | Not in Tolt | `{"type": "custom", "event_name": "trial_started", "customer_id": "..."}`, with a reward rule whose trigger is `trial_started` |

Auth changes from Tolt's key to `Authorization: Bearer <your partners.conversions token>`.

**`click_id` changes meaning.** Tolt's `click_id` is Tolt's own uuid from `tolt.js`; it means nothing to Outflare. After the switch, `click_id` is the id of the partner link click Outflare recorded (`ref_click` on the landing URL). You usually don't need it: the Outflare website snippet ties the visit to the person on its own, and the conversion credits the partner of their last (or first) partner visit. Send `ref` (the partner's code) when your backend knows it, for example from your own sign-up form.

### Webhooks: Tolt to Outflare

If your backend receives Tolt's webhooks (for example commission events at your own `/webhooks/tolt/commission`), point a new Outflare webhook endpoint at it (Settings → Webhooks) and map the fields. Both sign the same way: Outflare follows Standard Webhooks (`webhook-id`, `webhook-timestamp`, `webhook-signature`), which is what Svix signs Tolt's with, so your signature check keeps working with the new secret.

| Tolt | Outflare |
| --- | --- |
| Envelope `{type, timestamp, data}` | `{id, type, created_at, workspace_id, actor, target, data}`; `target.id` is the changed object's id |
| `commission.created` | `partner_reward.created` |
| `commission.updated` (approved, paid, rejected) | `partner_reward.approved`, `partner_reward.rejected`, `partner_reward.reversed` (a refund took it back), `partner_payout.paid` (it was paid in a payout; `reward_ids` lists the rewards) |
| `transaction.created` _check_ | `partner_conversion.created` with `type` `sale` (or `refund`) |
| `customer.created` _check_ | `partner_referral.created` |
| `partner.created` _check_ | `partner.created` |

The fields of a commission event, one by one:

| Tolt `commission.*` data | Outflare `partner_reward.*` data |
| --- | --- |
| `id` (`comm_...`) | `target.id` (the reward's id) |
| `amount` (string, cents) | `amount_cents` (integer) |
| `revenue` (string, cents) | `revenue_cents` (integer, the sale's amount; for a reversal, the refund's) |
| `currency` (often null) | `currency` (always set, the program's) |
| `status` (`pending`, ...) | the event type, and `status` on `.approved` and later |
| `approval_type`, `scheduled_approval_date` | `hold_until`; the program's hold sets it |
| `type` (`reward`) | `kind` (`cash`, `fee`, `action`, `adjustment`) |
| `partner_id` (`part_...`) | `partner_id` (Outflare's id for them; the import maps each Tolt partner to one) |
| `customer_id` (`cus_...`) | `customer_id` (the user id your backend sent for them, "" if it never sent one) and `record_id` (Outflare's customer record) |
| `transaction_id` (`txn_...`), `charge_id` | `conversion_id`, and `external_id`: the `charge_id` you sent with the sale |
| `program_id` (`prg_...`) | `program_id` |
| `flow_id` (`flw_...`) | not on the reward yet |
| `payout_id` | `target.id` of the `partner_payout.*` events, whose `reward_ids` name the rewards |
| `is_self_referral` | never credited: a self-referral isn't rewarded |
| `charge_status`, `source`, `organization_id`, `campaign_id`, `is_ad_click`, `deducted_commission_amount` | no equivalent |

Outflare answers `201` when a partner was credited and `200` with `credited: false` and a `reason` when not. Neither is an error, so report every sign-up and sale. Each one also lands on the customer's timeline for [attribution](attribution.md).

Postbacks a partner's own tracker made to Tolt go to the program's postback URL instead (Partners → program → Settings).

## The day

1. **Final import.** Run the Tolt import once more, so partners, codes and commissions up to now are in Outflare, and check its issues.
2. **Change the backend calls** in the table above and deploy.
3. **Add the Outflare webhook endpoint** (Settings → Webhooks) if your backend handled Tolt's webhooks, with the mapping above, and turn the Tolt one off.
4. **Swap `tolt.js`** for the Outflare website snippet (Company → Setup) on every page that had it.
5. **Check a test conversion.**
   1. Open a partner's link in a private window and sign up with a test account.
   2. Check that the sign-up shows under the partner's Conversions, credited.
   3. Report a small test sale, then refund it.
   4. Check the reward and its reversal in the ledger.
6. **Switch over** (Payouts → Switch over, or `POST .../partner-programs/{id}/switch-over`) once Tolt has made its last payout. Until then, commissions imported from Tolt keep Tolt's status: re-runs bring Tolt's approvals and payments, but Outflare never approves them after its hold or puts them in a payout, so nobody is paid twice. Afterwards, the ones still unpaid are approved and paid here like any other.
7. **Turn auto-approve back on** if the program uses it.
8. **Cancel Tolt** once the test passes. Partners' existing links keep working because the codes are the same: `?ref=<code>` on your site, and `/r/<code>` on your tracking domain.

## After the move

- **Payouts.** "Make due payouts", pay from your own account or payout tool, then mark each one paid with its reference. The `partner_payout.created` webhook can start a payout tool.
- **Commissions** approve on their own when the hold ends. `partner_reward.approved` tells your system about action rewards, such as credit to grant.
