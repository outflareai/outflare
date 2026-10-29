# Partners: referrals, affiliates and influencers

Partners is one platform for three kinds of program that differ only in their settings:

| | Customer referral | Affiliate | Influencer or creator |
| --- | --- | --- | --- |
| Who | An existing customer | A marketer, review site or agency | A creator with an audience |
| Usually earns | Credit, a free month or a discount, often for both sides | Cash: a share of each sale, a fixed amount per sale or lead, tiers | A fixed fee per post or month, or nothing (track only) |
| Delivered by | Your own system, told by a webhook | A payout you make from your own account | An invoice outside Outflare |

Pick a kind when you create a program and it starts from that preset. Every setting the preset fills in can be changed afterwards.

Outflare never holds or sends money. It works out what each partner is owed, and you pay from your own account or payout tool. Then you mark the payout paid, by hand or through the API.

## How it fits together

- **A partner is a customer record.** It has no separate partner list: the same person can be a customer, a lead and a partner. Joining a program adds `partner.joined` to their timeline.
- **A program** holds the rules: landing page, credit rule, reward rules, hold, minimum payout and groups. Each brand can run its own programs, and one person can be a partner in several of them.
- **Each partner** has:
  - a ref code (`ana`)
  - a tracking campaign on the `affiliates` channel, named after the code so their real name never shows in `utm_campaign`
  - a short link
  - a campaign link
  - any number of promo codes
- **The referred customer** is a customer record too. Referral, conversion and reward rows point at records, so merging two people moves them. A person's record page (People), and their panel in Visitors and Leads, show the programs they are a partner in and who referred them; through the API, filter `GET .../partners` and `GET .../partner-referrals` by `record_id`.
- **People and tokens limited to some brands** work in their brands' programs: their partners, rewards and payouts. They can read programs shared by every brand but not change them, and another brand's programs answer 404. Reporting conversions (`partners.conversions`) needs a token for every brand, because a conversion can credit any program's partner.

## Links

- **The short link** is `/r/<code>` on your [tracking domain](website-tracking.md), or `/r/<workspace-id>/<code>` on the Outflare server until you add one.
  - It counts the click and sends the visitor to the program's landing page, tagged with the partner's campaign, `ref=<code>` and `ref_click=<click id>`.
  - `?sub=<sub-id>` keeps the partner's own sub-id, as `utm_content` and on the click.
  - `?to=/blog/post` (or a full address on the same site) lands on another page. Other sites are ignored, so the link can't be used as an open redirect.
  - When the partner or program isn't active, the link still lands, untagged and uncounted.
- **The campaign link** is the landing page with the tags already on it and no redirect. The website snippet reads the tags on arrival.

The program's settings (Install) give the website snippet ready to paste as an HTML tag, a Next.js `<Script>` or a Google Tag Manager Custom HTML tag, with your site key filled in, and the calls your backend makes (API, Node.js or the postback URL).

Either way, the visit is stored with its `ref` ([website tracking](website-tracking.md)). A visit only *proposes* a partner. The partner is credited (*bound*) only when your backend reports a sign-up or sale for that person.

## Three ways to record a sign-up

Pick the one your site allows. Each credits the partner whose link the visitor came from, by their own visits with a `ref`, under the program's window and credit rule.

- **No code: the website snippet and your billing.** Put the snippet on your site and connect Stripe (Settings → "Also take sales and refunds from connected Stripe"). Sales come from Stripe; there is no lead until the first payment.
- **A little code: `outflare.signup(email)`.** Call it in the browser once your sign-up form succeeds, on a page with the snippet. The lead shows at once (a `signup` conversion with source `browser`). Anyone can call it with any address, so it pays no sign-up reward unless the program turns on **Pay for sign-ups the browser reports** (`pay_browser_signups`, off by default). Money still comes only from your server or billing.
- **Developers: your backend.** Report the sign-up with the conversion API or the postback URL (below), with the partner's `ref`, a `promo_code`, the visitor's `anonymous_id` (`outflare.anonymousId()` from the page), or just the customer's email or id when they visited with a ref.

Doing both is fine: the browser makes the lead, and your server's report of the same customer finds the same referral.

## Reporting conversions from your backend

Your backend reports every `signup` (or `lead`), `sale`, `refund` and `custom` conversion, server to server. Outflare credits the ones a partner referred. It is the same job your backend does with Tolt's API today. [Moving from Tolt](partners-tolt-migration.md) maps each Tolt call to this one.

Every conversion you report also becomes a customer event on the person's timeline, whether a partner referred them or not, so [attribution](attribution.md) credits it with no setup. The event names are the ones attribution reads by default:

| `type` | Customer event |
| --- | --- |
| `signup` | `signed_up` |
| `sale` | `payment.succeeded`, with `payment_id` (your `external_id`), `amount_minor`, `currency`, `product_id` and `recurring` |
| `refund` | `payment.refunded`, with the sale's `payment_id` |
| `custom` | your `event_name` (such as `trial_started`), with `amount_minor` when you send a value |

Send payments one way: if a billing connection such as Stripe also records `payment.succeeded` for the same payments, attribution counts them twice.

```sh
curl -X POST https://outflare.example.com/v1/workspaces/acme/partner-conversions \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"type": "sale", "customer_id": "u_123", "email": "bo@example.com", "amount_cents": 4900,
       "currency": "USD", "external_id": "ch_1001", "product_id": "residential", "ref": "ana"}'
```

Use a token with only the `partners.conversions` scope. It can report conversions and fulfil action rewards, and nothing else.

- **The customer** is `customer_id` (your own user id), `email` or `record_id`. Send the same one each time, so later sales and refunds find them.
- **`recurring: true`** marks a sale as a subscription renewal rather than a new purchase. Rules decide renewals by `recurrence`.
- **`external_id`** is your charge or order id. A retry with the same one is stored once. A refund names its sale with `refund_of` (the sale's `external_id`). Without `amount_cents`, a refund takes back what is left of the sale.
- **How the partner is found**, in order:
  1. What the call carries: `promo_code` (ahead of the rest when the program's `codes_win` is on), `ref` (keep it in a cookie from the landing page) and `click_id` (the `ref_click` value).
  2. A referral already bound to this customer in the program. The first partner keeps the customer.
  3. The customer's own website visits that carried a `ref` inside the program's credit window, first or last as the program says.
- **The answer** is `201` with `credited: true`, the conversion and the rewards it earned. Otherwise it is `200` with `credited: false` and a `reason`, which is never an error:
  - `no_partner`
  - `partner_inactive`
  - `program_inactive`
  - `self_referral`: the partner bought through their own link
  - `sale_not_found`
  - `currency`: the sale is in a currency other than the program's

Report every sale and let Outflare pick out the referred ones.

### Sales from your billing account

Turn on `sales_source: "stripe"` on a program (Settings, "Also take sales and refunds from connected Stripe") and the payments and refunds Outflare reads from your connected Stripe account become partner sales too, beside what your backend reports. A referral already bound to the customer, or their own visits, decide the partner, as for the API. They count once: the payment's own customer event comes from the Stripe sync.

Promo codes can be made in Stripe for you: add one with a `coupon` (the id of a coupon you already have) and Outflare makes the promotion code with your own key. The key needs Write access to Promotion codes. Without a coupon, make the code in your billing system yourself; Outflare only matches it.

### Incoming postback URL

For trackers and backends that can only call a URL, make the program's postback URL on its page. It is shown once, and making a new one retires the old one. Call it with GET (query string) or POST (a form or JSON). It takes the same fields plus the aliases networks use:

- `event` or `goal` for `type`, and `event_name` and `recurring` (`true`) as in the API
- `order_id`, `transaction_id` or `txid` for `external_id`
- `user_id` for `customer_id`
- `coupon` for `promo_code`
- `amount` in the main unit (`49.90`), or `amount_cents`

```
https://outflare.example.com/v1/partner-postbacks/ofb_...?type=sale&ref=ana&user_id=u_123&amount=49.90&order_id=1001
```

Anyone with the URL can report conversions to the program, so treat it like a password.

### Postbacks to your partners' trackers

Affiliates who run their own tracker (Voluum, RedTrack, Binom and the like) want each conversion sent back to it. A partner's `postback_url` is that tracker's address, set by the partner on the portal (Settings) or by your team (`PATCH .../partners/{id}`, or Postback URL in the partner's menu). It must start with `https://` and can carry these placeholders:

| Placeholder | Value |
| --- | --- |
| `{sub_id}` | The sub on the partner's link (`/r/ana?sub=...`), usually their own click id |
| `{click_id}` | Outflare's click id |
| `{conversion_id}`, `{type}`, `{event_name}` | The conversion: `signup`, `sale`, `refund` or `custom` and its event name |
| `{amount}`, `{amount_cents}`, `{currency}` | The sale's amount (`49.90`), in cents, and its currency |
| `{commission}`, `{commission_cents}` | The partner's cash for it (negative for a refund's reversal) |
| `{status}` | `pending` (on hold), `approved`, or `reversed` for a refund |
| `{code}`, `{program}` | The partner's code and the program's slug |

```
https://tracker.example/postback?clickid={sub_id}&payout={commission}&status={status}
```

Each conversion credited to the partner, and each refund of one, calls it once (GET), from a background job. A tracker that answers 5xx or 429, or doesn't answer, is tried again up to 5 times; one that answers 4xx isn't. Calls only go to public internet addresses and never follow a redirect. The last 50 calls, with the tracker's answer, are on the portal and at `GET .../partners/{id}/postbacks`.

## Credit rule

Each program keeps its own credit rule. It never follows the workspace's [attribution](attribution.md) settings, so changing how the CMO view credits channels never changes what partners earn.

- **`credit_window_days`:** how long after a visit or click a conversion still credits the partner.
- **`credit_model`:** `first` or `last` visit with a ref inside the window.
- **`codes_win`:** a promo code beats a link when a conversion carries both.

Channel reporting credits a visit by `ofc` first, while commissions credit by `ref` first ([campaign links](campaign-links.md#partner-links-and-ref)).

## Rewards

Reward rules stack: every rule a conversion matches gives a reward.

| Kind | What it gives |
| --- | --- |
| `percent` | A share of the conversion's value in basis points (`2000` is 20%); not for sign-ups |
| `fixed` | An amount per conversion |
| `action` | Something your own system does, such as `grant_credit` with `{"gb": 10}`. Outflare sends it in the `partner_reward.approved` webhook, and your system calls `POST .../partner-rewards/{id}/fulfil` when done. |

Each rule also has:

- a `trigger`: `signup`, `sale`, or a custom conversion's `event_name`
- a `side`: `partner`, or `referred` for two-sided rewards (always an action)
- for sales, a `recurrence`: the customer's `first` sale, `every` sale, or sales in their first `months`
- optional `product_ids`
- optionally a group, or one partner (`partner_id`: their own rate), or a tier (`min_sales`: once the partner has made that many sales)

Action rules all pay, but only one cash rule (percent or fixed) pays per conversion, whatever order the rules are in. The one that pays is, in this order: a partner's own rule over their group's over one for everyone; then the highest tier reached; then the narrower sale (first sale, then first months, then every sale); then a rule for named products over one for all; then the one paying more. So with 10% on the first sale and 2% on every sale, the first sale pays 10% and later ones 2%. Tiers, group rates and partners' own rates replace the base rate instead of adding to it.

### Groups

Each partner is in one group at most, such as Gold or Agencies, and a group's own rules (`group_id`) apply to its partners. On the Groups tab you add, rename and remove groups, give a group its own rate (Commission rate), and make one the default that new partners join (`default` on `POST .../groups` or `PATCH .../groups/{group}`). To move partners, pick them on the Partners tab and choose Move to, or call `POST .../groups/{group}/partners` with up to 1,000 `partner_ids` (`none` takes them out of every group). Removing a group removes its rules and moves its partners to no group.

To give one affiliate a better deal, open their menu and choose Commission rate (a percent of each sale), or add a rule with their `partner_id` through `PUT .../rules`. Rewards already earned keep their amounts.

A program with no rules tracks only. For influencers on a fixed fee, log each fee on the partner (`POST .../partners/{id}/fees`). It is approved at once and counted as a cost, so cost per sale and ROAS work.

### The ledger

Rewards are an append-only ledger: an amount never changes once written.

- **Hold.** A new reward waits for the program's hold (`hold_days`), then is approved: on its own when `auto_approve` is on (checked every minute), otherwise by a person. A person can also approve or reject one before the hold ends, or many at once: pick them on Rewards, or use Approve all pending (`POST .../partner-rewards/bulk` with `ids`, or a `program` or `partner_id`, up to 1,000 a call). Each is recorded as if done alone, with its webhook.
- **Cost.** Approved cash and fees for partners become spend entries on the `affiliates` channel, so the CMO view counts them as cost.
- **Refunds.** A refund rejects the sale's rewards still on hold. Approved or paid cash gets a negative `adjustment` entry, pro rata for a partial refund, which the next payout nets. An approved or fulfilled action gets a reversal entry (`partner_reward.reversed`) naming the action, so your system can take back what it gave.
- **Adjustments.** `POST .../partners/{id}/adjustments` corrects a balance either way, with a note saying why.

## Payouts

`POST .../partner-payouts` makes one payout per partner whose approved, unpaid balance reaches the program's minimum. It can run for one partner, one program or all of them. Each payout fires `partner_payout.created`, so a payout tool can pick it up.

Pay it from your own account, then mark it paid with a note and the payment's reference: `POST .../partner-payouts/{id}/paid`. That fires `partner_payout.paid`. Canceling a due payout puts its rewards back into the balance.

To pay them in bulk, download the due payouts as CSV: Payouts → Download CSV, or `GET .../partner-payouts/export` (`status` `due` by default, or `paid`, `canceled`, `all`; `program`). Each row has the partner's code, name, email, payout details, the amount as a decimal and the currency, ready for your payout tool's bulk upload. The file holds payout details, so it needs `partners.manage` and each download is in the audit log (`partner_payouts.exported`).

Outflare has no PayPal, Wise or crypto integration and never stores bank details. A partner's `payout_method` is free text, such as "PayPal: ana@example.com", stored sealed like connector keys. Structured payout fields and tax forms may come later.

**Partners without payout details.** The portal's dashboard asks a partner with no payout details to add them in Settings. On the Partners tab, "No payout details (N)" lists them (`GET .../partners?payout_details=missing`; the program's `stats` count them as `partners.missing_payout_details`). A partner brought in by an import whose platform didn't give their details (Tolt's API has none) gets no payout until they add them: "Make due payouts" leaves their balance as it is and counts them in `waiting_for_details`, and their next payout includes it. Partners added in Outflare are paid as before, details or not.

### When a partner's record is deleted

Deleting a partner's person record for good (`DELETE .../records/{record}`, for an erasure request) keeps what they earned and were paid, so commission totals and payout history still add up. Their memberships, rewards and payouts move to a new archived record named "Erased partner", with no name, email or identities. The membership is suspended and loses its payout method, note, sign-up answers and postback URL, but keeps its code and promo codes: the partner's links still lead to the program's landing page, without crediting anyone. Their referrals, conversions and portal sign-ins are erased.

A payout's note and reference can hold the partner's name or email, so they are cleared too, unless the workspace's partner setting is off (Partners → Payouts → "Clear payout notes when a partner is deleted", or `PATCH .../partner-settings` with `erase_payout_notes`; on by default, and the same for every program). The payout's amount, dates and status always stay.

## Partner portal

Each program can have a hosted portal where partners apply, sign in and see how they're doing. It is off until you open it (Partners → program → Settings, or `PUT .../partner-programs/{id}/portal`).

- **Where it lives.**
  - At `<subdomain>.<partners domain>` when the server has one. The operator sets the partners domain with `OUTFLARE_PARTNERS_DOMAIN`, with wildcard DNS and a wildcard certificate.
  - On your own domain: add it as a tracking domain tied to the program (`POST .../tracking-domains` with `partner_program`) and point its CNAME at the target shown. It is verified and given HTTPS like any tracking domain. A program has one own domain; delete it from tracking domains to use another.
  - Otherwise at `/p/<program id>/` on the Outflare server.

  On its own host the portal serves only itself and partners' short links (`/r/<code>`): the rest of Outflare, sign-in included, isn't there.
- **Signing in.** Partners sign in with a one-time link emailed to them through your own email provider, from the sender you set (`email_from`, on a verified sending domain). Without one, the portal can't sign anyone in.
  - The link works once, for 30 minutes. It carries its secret after `#`, so it never reaches server logs.
  - Following it confirms the address and starts a 30-day session on that portal only.
  - Asking for a link answers the same whether or not the address belongs to a partner.
- **Applying.** With `signup_open`, anyone can apply with their name, email, your questions (`signup_fields`: text, long text, URL or a choice) and, when the program has terms, accepting them. They become a customer record and a partner, waiting for approval unless the program accepts everyone (`auto_accept`).
- **What partners see:**
  - their links, with sub-ids and any page on the site
  - their promo codes
  - clicks, referred customers (shown as "Customer 3F9A2C", never a name or address) and sales
  - rewards with their status, hold and reason, and payouts
  - assets you add (banners, copy, email swipes)
  - their settings: a free-text payout method, kept sealed, and which emails they want

  By kind: referral programs show only the dashboard and links, and influencer programs read-only stats and assets. Change `pages` to show more or less.
- **Emails.** Partners are emailed when a customer they referred converts (with what they earned and how long it's held) and when a payout is made due or paid. Each partner chooses which they get in their settings. The emails come from the portal's sender, through your own email provider.
- **Look.** Title, logo, accent colour, a welcome message and custom CSS.
- **Seeing it as a partner.** "View as partner" on a partner (`POST .../partners/{id}/impersonate`) opens their portal read-only for an hour, even while the portal is closed. It is recorded in the audit log.

## Referral widget in your app

Show signed-in customers their referral link and what they've earned inside your own app, without a separate sign-in.

1. When the page loads, your backend asks for a widget session with the customer's id (the same `customer_id`, `email` or `record_id` you report conversions with), using a token with `partners.conversions`:

   ```sh
   curl -X POST https://<your Outflare>/v1/workspaces/$WS/partner-programs/refer-a-friend/widget-sessions \
     -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
     -d '{"customer_id": "u-1042"}'
   ```

   The customer becomes a partner in the program if they aren't one yet (active at once when the program accepts everyone, otherwise waiting for approval).
2. Put the answer's `embed_url` in an iframe. The session lasts 12 hours; ask for a new one on each page load and never cache or share the URL.

   ```html
   <iframe src="EMBED_URL" style="width:100%;border:0" title="Refer a friend"></iframe>
   <script>
     addEventListener("message", (e) => {
       if (e.data && e.data.type === "outflare:partner-widget") document.querySelector("iframe").style.height = e.data.height + "px";
     });
   </script>
   ```

The widget shows the program's title and welcome, the customer's link and promo codes, how many people they referred and became customers, and what they've earned and been paid, in the portal's accent colour and custom CSS. It is served from the portal's address even while the portal is closed, and only over HTTPS pages may frame it.

## Importing from Tolt

Bring a Tolt program in with your own Tolt API key (Connectors → Tolt): partners of every status with their link codes, so their links keep working, promo codes, referred customers, transactions and commissions, paid ones as paid payouts. Run it as often as you like until the day you switch; each run adds only what's new. See [moving from Tolt](partners-tolt-migration.md).

## Missions

A mission can aim at referred revenue: `goal_metric` `revenue_referred`, in whole US dollars. It counts credited partner sales less refunds, in the partner programs of the mission's brand (every program when it has no brand), from when the mission started. Imported history never counts ([attribution](attribution.md)).

## Webhooks

`partner_program.created`, `.updated`, `.rules_updated`; `partner.created`, `.updated`; `partner_referral.created`; `partner_conversion.created`; `partner_reward.created`, `.approved`, `.rejected`, `.reversed`, `.fulfilled`; `partner_payout.created`, `.paid`, `.canceled`. They are signed like every Outflare webhook ([events and webhooks](api-conventions.md#events-and-webhooks)). Every `partner_reward.*` event from a conversion carries the sale: `conversion_id`, `conversion_type`, `revenue_cents`, `external_id` (the id your backend sent, such as a charge id), `record_id` and `customer_id` (your own user id for the referred customer); `partner_payout.paid` lists the `reward_ids` it paid. An action reward's `partner_reward.approved` carries `action`, `action_data`, `recipient_id` and `recipient_customer_id` (your own user id for the person to reward).

## Scopes

| Scope | Allows |
| --- | --- |
| `partners.read` | Programs, partners, conversions, referrals, the ledger and payouts |
| `partners.manage` | Everything else: programs and rules, partners, fees, approving rewards, payouts |
| `partners.conversions` | Reporting conversions, fulfilling action rewards and opening the referral widget: the one scope a backend needs |

Operators get all three. Viewers get `partners.read`.

## API and MCP

| Call | Scope |
| --- | --- |
| `GET`/`POST /v1/workspaces/{ws}/partner-programs`, `GET`/`PATCH .../partner-programs/{id}` | read / manage |
| `GET .../partner-programs/{id}/stats`: the program's totals across all its partners (partners by status, clicks, customers, sales, rewards on hold, due and paid) | read |
| `PUT .../partner-programs/{id}/rules`, `POST .../groups`, `PATCH`/`DELETE .../groups/{group}`, `POST .../groups/{group}/partners`, `POST .../postback` | manage |
| `GET`/`POST .../partners`, `GET`/`PATCH .../partners/{id}`, `POST`/`DELETE .../promo-codes`, `POST .../fees`, `POST .../adjustments` | read / manage |
| `POST .../partner-conversions`, `POST .../partner-programs/{id}/widget-sessions` | conversions |
| `GET .../partner-conversions`, `GET .../partner-referrals`, `GET .../partner-rewards`, `GET .../partner-payouts` (newest first, 100 a page by default, up to 500; `before` is the last page's `next_before`; payouts filter by `program` too) | read |
| `POST .../partner-rewards/{id}/approve`, `.../reject`, `POST .../partner-rewards/bulk` | manage |
| `POST .../partner-rewards/{id}/fulfil` | conversions |
| `POST .../partner-payouts`, `.../{id}/paid`, `.../{id}/cancel` | manage |
| `GET .../partners/{id}/postbacks` / `GET .../partner-payouts/export` | read / manage |
| `POST .../partner-programs/{id}/imports` / `GET .../imports`, `.../imports/{import}` | manage / read |
| `GET`/`POST /v1/partner-postbacks/{token}` | the postback secret |

The MCP tools are listed in [mcp/README.md](../mcp/README.md): `list_partner_programs`, `add_partner`, `report_partner_conversion`, `create_partner_payouts` and the rest.

## Coming next

- A fraud review queue with partner checks (self-referral, matching payment details, cookie stuffing). It's on hold for now.
