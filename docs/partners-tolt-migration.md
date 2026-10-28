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

Tolt's API docs couldn't be fetched while this guide was written. **Field names marked _check_ come from Tolt's public docs as remembered: check them against your own Tolt integration** before you change your code.

## Before the day

- **Create the programs.** Make one program per Tolt program, with its rewards, hold (Tolt's "commission delay") and minimum payout. The credit window replaces Tolt's cookie duration.
- **Connect Tolt.** Connectors → Tolt, with your own Tolt API key (Tolt: Settings, Integrations). Outflare only reads it.
- **Turn auto-approve off** on the program until the day, so Tolt's own decisions on commissions come over instead of Outflare approving them first.
- **Import from Tolt.** Partners → program → Settings → Import from Tolt (or `POST .../partner-programs/{id}/imports`, or the `start_partner_import` MCP tool). Run it as often as you like before the day: see [the import](#the-import).
- **Make a backend token.** Make an API token with only the `partners.conversions` scope, for your backend.
- **Add a tracking domain** (optional). With one such as `go.example.com` ([website tracking](website-tracking.md)), partner short links read `https://go.example.com/r/<code>`.

## The import

Each run reads the whole Tolt program and brings in what isn't in Outflare yet. Every object is matched by its Tolt id, so running it again never adds anything twice: it adds what's new and brings over what changed.

| From Tolt | Becomes |
| --- | --- |
| Partners | Partners, each a customer record found by email. Their code is their first link's value, so `?ref=<code>` and `?via=<code>` links keep working. Status (active, pending, suspended, rejected) and payout details (stored sealed) come over. |
| Links | The partner's code. A partner's second link, or a link whose parameter isn't `ref` or `via` (the only two the website script reads), is listed under issues. |
| Promo codes | The partner's promo codes, kept as they are: they already exist in your billing account. |
| Customers | Referred customers, matched by your own `customer_id` and email the same way the conversion API matches them. |
| Transactions | The partner's sales (and refunds), with source `import`. They are not new events on the customer's timeline: your billing or backend already told attribution about them. |
| Commissions | Rewards with their status: pending ones wait out the program's hold, approved ones are owed in the next payout, paid ones are in paid payouts (one per Tolt payout, or per partner and day) and rejected ones are rejected. Approved and paid commissions count as spend on the day they were earned. |

Importing sends no reward webhooks, so your system never grants an old reward twice. New partners do send `partner.created`.

On a later run, a partner's status follows Tolt unless someone changed it in Outflare, and a commission still pending in Outflare follows Tolt (approved, paid or rejected). A commission Tolt rejects after Outflare approved it is listed under issues, for you to correct with an adjustment.

Each run lists what it couldn't bring over, by Tolt id and reason (never names or addresses):

| Reason | What to do |
| --- | --- |
| `no_email` | Add the partner's email in Tolt and run again, or add the partner in Outflare. |
| `code_changed` | The code was taken or has characters Outflare doesn't allow, so the partner got a new one. Send them their new link. |
| `extra_link`, `link_param` | Old links with that value or parameter stop crediting. Tell the partner to share their Outflare link. |
| `partner_missing`, `customer_missing` | What it belongs to didn't come over (see its own issue). |
| `promo_code_invalid`, `promo_code_taken` | Add the code by hand, with a new name if it's taken. |
| `already_referred` | Another partner already has that customer in Outflare: the first partner keeps them. |
| `status_conflict` | Rejected in Tolt after Outflare approved it: add a negative adjustment. |

Tolt's API docs couldn't be read from where this was built, so the import reads each field under the names Tolt has used (_check_). **Run it on your account well before the day and check the counts and issues against Tolt.**

## The calls, side by side

| What happens | Tolt (_check_) | Outflare |
| --- | --- | --- |
| A visitor lands from a partner link | `tolt.js` reads `?ref=` and keeps a referral cookie (`tolt_referral` _check_) | The Outflare website snippet reads `?ref=` and records the visit on the person, no cookie code of yours needed. Keep `ref` in your own cookie too if you want to send it explicitly. |
| Someone signs up | Create a customer: `POST /v1/customers` with `email`, `customer_id`, and the referral (`partner_id`, `click_id` or the cookie value _check_) | `POST /v1/workspaces/{ws}/partner-conversions` with `{"type": "signup", "customer_id": "...", "email": "...", "ref": "..."}`. `ref` is optional when the snippet saw their visit. |
| Someone pays | Create a transaction: `POST /v1/transactions` with `amount` (cents _check_), `customer_id`, `charge_id`, `billing_type` (`one_time` or `subscription` _check_), `product_id` | `{"type": "sale", "customer_id": "...", "amount_cents": 4900, "currency": "USD", "external_id": "<charge_id>", "product_id": "...", "recurring": true}`, with `recurring` for a renewal |
| A payment is refunded | Refund a transaction (by `charge_id` _check_) | `{"type": "refund", "refund_of": "<charge_id>", "external_id": "<refund id>"}`. Leave out `amount_cents` to refund it all, or send it for a partial refund. |
| Anything else that should pay out | Not in Tolt | `{"type": "custom", "event_name": "trial_started", "customer_id": "..."}`, with a reward rule whose trigger is `trial_started` |

Auth changes from Tolt's key to `Authorization: Bearer <your partners.conversions token>`.

Outflare answers `201` when a partner was credited and `200` with `credited: false` and a `reason` when not. Neither is an error, so report every sign-up and sale. Each one also lands on the customer's timeline for [attribution](attribution.md).

Postbacks a partner's own tracker made to Tolt go to the program's postback URL instead (Partners → program → Settings).

## The day

1. **Final import.** Run the Tolt import once more, so partners, codes and commissions up to now are in Outflare, and check its issues.
2. **Change the backend calls** in the table above and deploy.
3. **Swap `tolt.js`** for the Outflare website snippet (Company → Setup) on every page that had it.
4. **Check a test conversion.**
   1. Open a partner's link in a private window and sign up with a test account.
   2. Check that the sign-up shows under the partner's Conversions, credited.
   3. Report a small test sale, then refund it.
   4. Check the reward and its reversal in the ledger.
5. **Turn auto-approve back on** if the program uses it.
6. **Cancel Tolt** once the test passes. Partners' existing links keep working because the codes are the same: `?ref=<code>` on your site, and `/r/<code>` on your tracking domain.

## After the move

- **Payouts.** "Make due payouts", pay from your own account or payout tool, then mark each one paid with its reference. The `partner_payout.created` webhook can start a payout tool.
- **Commissions** approve on their own when the hold ends. `partner_reward.approved` tells your system about action rewards, such as credit to grant.
