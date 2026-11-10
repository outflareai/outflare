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
- **A program** holds the rules: landing page, credit rule, reward rules, hold, minimum payout and groups. Every program belongs to one brand, and its partners, links, conversions, rewards and payouts are that brand's alone. One person can be a partner in several programs. The API is under `/v1/brands/{brand}/` (`partner-programs`, `partners`, `partner-conversions`, `partner-referrals`, `partner-rewards`, `partner-payouts`, `partner-leads`, `partner-conversion-log`); the workspace routes are deprecated and act across the brands the caller sees.
- **Each partner** has:
  - a ref code (`ana`)
  - a tracking campaign on the `affiliates` channel, named after the code so their real name never shows in `utm_campaign`
  - a short link
  - a campaign link
  - any number of promo codes
- **The referred customer** is a customer record too. Referral, conversion and reward rows point at records, so merging two people moves them. A person's record page (People), and their panel in Visitors and Leads, show the programs they are a partner in and who referred them; through the API, filter `GET .../partners` and `GET .../partner-referrals` by `record_id`.
- **People and tokens limited to some brands** work in their brands' programs: their partners, rewards and payouts. Another brand's programs answer 404. Each brand's backend reports its conversions to `POST /v1/brands/{brand}/partner-conversions`, which credits only that brand's partners, even when a ref code, click id or promo code belongs to another brand's partner. The deprecated workspace route needs a token for every brand, because it can credit any program's partner.

## Links

- **The short link** is `/r/<code>` on your [tracking domain](website-tracking.md), or `/r/<workspace-id>/<code>` on the Outflare server until you add one.
  - It counts the click and sends the visitor to the program's landing page, tagged with the partner's campaign, `ref=<code>` and `ref_click=<click id>` (also as `ofclk`, so the website snippet ties the visit to the click).
  - The click id is a 16-character key; links clicked before November 2026 have a uuid, and both are taken as `click_id`. Clicks are kept with [redirect link](campaign-links.md#redirect-links) clicks: written a moment after the click, with the visitor's region, city, network and device, and a keyed hash of their address, never the address. Clicks from link scanners and previews are kept but not counted.
  - `?sub=<sub-id>` keeps the partner's own sub-id, as `utm_content` and on the click.
  - `?to=/blog/post` (or a full address on the same site) lands on another page. Other sites are ignored, so the link can't be used as an open redirect.
  - When the partner or program isn't active, the link still lands, untagged and uncounted.
- **The campaign link** is the landing page with the tags already on it and no redirect. The website snippet reads the tags on arrival.

Program settings, Tracking, gives the website snippet ready to paste as an HTML tag, a Next.js `<Script>` or a Google Tag Manager Custom HTML tag, with your site key filled in, and the calls your backend makes (API, Node.js or the postback URL).

Either way, the visit is stored with its `ref` ([website tracking](website-tracking.md)). A visit only *proposes* a partner. The partner is credited (*bound*) only when that person signs up or pays: from the events and transactions you already send, or a report to the partner conversions API.

## From your events and transactions

A brand's programs read the data your product already sends Outflare, so they need no partner-specific code:

| What happens | What you already send | What the program does |
| --- | --- | --- |
| Someone signs up | `identify` with their `userId` and the browser's `anonymousId` (`outflare.anonymousId()`), or a `Signed Up` track event ([customer events](customers.md)) | A `signup` conversion (source `events`): the partner whose link they came from is credited, or the one the event's `ref` or `promo_code` property names. |
| They do something a reward rule pays for | A track event with that name, such as `Trial Started` (a rule whose trigger is `trial_started`) | A `custom` conversion, once per event. |
| They pay or get a refund | A payment or refund to the [transactions API](transactions.md), or an Order Completed event | A partner sale or refund, once per payment, when the program takes its sales from transactions (the default for a brand's program). |

- **A sign-up is a person's first user id**: the first event that carries their `userId` (usually your sign-up's `identify`), or a `Signed Up` event. Signing in again is not a sign-up. An `identify` whose `created_at` trait is more than a day before the event is an existing user whose events just started arriving, and isn't taken as a sign-up.
- **Once per customer.** A customer signs up once in a program, however many ways it is reported (an event, the API, an import), and a payment is credited once by its id.
- **Order doesn't matter.** A payment that reaches Outflare before the sign-up that links the referral (up to an hour before it) is credited when the referral is made.
- **A deleted transaction** (Recently deleted) takes its sale's reward back, as a full refund would. Restoring it doesn't credit it again: add the reward by hand if you need it.
- **Only the brand's own programs.** Programs shared by every brand don't read events or transactions, so one brand's customers never credit another's partners. Browser events never credit anyone: anyone can send them.

## Other ways to record a sign-up

- **No code: the website snippet and your billing.** Put the snippet on your site and connect Stripe (Program settings → Tracking → Stripe). Sales come from Stripe; there is no lead until the first payment.
- **A little code: `outflare.signup(email)`.** Call it in the browser once your sign-up form succeeds, on a page with the snippet. The lead shows at once (a `signup` conversion with source `browser`). Anyone can call it with any address, so it pays no sign-up reward unless the program turns on **Pay for sign-ups the browser reports** (`pay_browser_signups`, off by default). Money still comes only from your server or billing.
- **The partner conversions API.** Report the sign-up with the conversion API or the postback URL (below), with the partner's `ref`, a `promo_code`, the visitor's `anonymous_id` (`outflare.anonymousId()` from the page), or just the customer's email or id when they visited with a ref.

Doing both is fine: the browser makes the lead, and your server's report of the same customer finds the same referral.

## Reporting conversions from your backend

If your product already sends events and transactions, you don't need this API (above). It is a shortcut for a backend that reports to Tolt's API today: it reports every `signup` (or `lead`), `sale`, `refund` and `custom` conversion, server to server, and Outflare credits the ones a partner referred. [Moving from Tolt](partners-tolt-migration.md) maps each Tolt call to this one.

What you report is stored as the same events and transactions your product would send, with the same ids, so a customer reported both ways counts once. A `sale` or `refund` with an `external_id` and an amount, for a customer of a brand, is saved as that brand's [transaction](transactions.md) with `source_id` set to your `external_id`: sending the same payment to the transactions API too is that one transaction, with one `payment.succeeded`. Every other conversion becomes a customer event on the person's timeline, whether a partner referred them or not, so [attribution](attribution.md) credits it with no setup. The event names are the ones attribution reads by default:

| `type` | Customer event |
| --- | --- |
| `signup` | `signed_up` |
| `sale` | `payment.succeeded`, with `payment_id` (your `external_id`), `amount_minor`, `currency`, `product_id` and `recurring` |
| `refund` | `payment.refunded`, with the sale's `payment_id` |
| `custom` | your `event_name` (such as `trial_started`), with `amount_minor` when you send a value |

A billing connection such as Stripe records its own payments: a sale also reported here is matched to the same payment when it has the same id, or the same person, amount and currency within 15 minutes.

```sh
curl -X POST https://outflare.example.com/v1/workspaces/acme/partner-conversions \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"type": "sale", "customer_id": "u_123", "email": "bo@example.com", "amount_cents": 4900,
       "currency": "USD", "external_id": "ch_1001", "product_id": "residential", "ref": "ana"}'
```

Use a token with only the `partners.conversions` scope. It can report conversions and fulfil action rewards, and nothing else.

- **The customer** is `customer_id` (your own user id), `email` or `record_id`. Send the same one each time, so later sales and refunds find them.
- **`recurring: true`** marks a sale as a subscription renewal rather than a new purchase. Rules decide renewals by `recurrence`.
- **`payment_source`** (the payment provider, such as `stripe` or `paddle`; Tolt calls it `source`) and **`interval`** (`day`, `week`, `month`, `year` or `one_time`) are kept on the conversion as you send them, so flows can pay on them (`transaction.payment_source`, `transaction.interval`). Postback URLs take `source` and `interval` too.
- **`country`** (two letters, such as `DE`; `country_code` on postback URLs): the customer's country as your backend knows it, from their billing address say. A program that blocks that country doesn't take them as a new referral (reason `country_blocked`). Send it with promo codes especially, since a code used at checkout has no website visit to judge.
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
  - `lead_expired`: the customer was referred longer ago than the program's lead expiration and hasn't bought yet

Report every sale and let Outflare pick out the referred ones.

### Sales from your billing account

Turn on `sales_source: "stripe"` on a program (Program settings → Tracking → Stripe) and the payments and refunds Outflare reads from your connected Stripe account become partner sales too, beside what your backend reports. A referral already bound to the customer, or their own visits, decide the partner, as for the API. They count once: the payment's own customer event comes from the Stripe sync.

A brand's program takes the payments and refunds you already send Outflare for that brand: Program settings → Tracking → "This brand's payments in Outflare" (`sales_source: "transactions"`, on by default for a new brand program). Every payment the [transactions API](transactions.md) or an Order Completed event counts (each once, however it arrives) becomes a partner sale for that brand's programs, and every refund (with `refund_of`) takes its reward back. Your backend then needs no partner call for sales at all ("From your events and transactions" above). Programs shared by every brand don't take them, so one brand's sales never credit another's partners.

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

### Conversion log

Partners → Log lists every conversion your backend or tracker reported in the last 30 days, by the API or a postback URL, newest first: what the call sent, what Outflare answered, and the outcome.

- **credited:** it earned the partner a referral or reward.
- **not credited:** it was stored for the customer, but no partner got it, with the reason (`no_partner`, `lead_expired`, `country_blocked` and the rest; see the conversion API).
- **duplicate:** a retry of a call already counted.
- **rejected:** the call was wrong, with the error code (a missing `amount_cents`, an unknown program).
- **error:** something broke on Outflare's side; retry it.

Search by the `external_id`, `customer_id`, `ref`, promo code, email address or request id a call sent. The log never keeps the email: it shows "(sent)", and a search by the full address still finds the call. Use it while switching over to check that every sale your backend sends arrives and is credited the way you expect (`GET .../partner-conversion-log`, MCP `list_partner_conversion_log`).

### Postbacks to your partners' trackers

Affiliates who run their own tracker (Voluum, RedTrack, Binom and the like) want each conversion sent back to it. A partner's `postback_url` is that tracker's address, set by the partner on the portal (Settings) or by your team (`PATCH .../partners/{id}`, or Postback URL in the partner's menu). It must start with `https://` and can carry these placeholders:

| Placeholder | Value |
| --- | --- |
| `{sub_id}` | The sub on the partner's link (`/r/ana?sub=...`), usually their own click id |
| `{click_id}` | Outflare's click id (the `ref_click` the link added) |
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

Program settings is a page of its own (Partners → Program settings), in tabs, each with its own address (`/partners/settings/<tab>`): **General** (links and credit, pause or archive), **Payouts** (rewards and payouts), **Tracking** (where sales come from, the postback URL, install on your website), **Fraud** (fraud protection), **Portal** (the partner portal; a dot shows whether it is open) and **Import** (from Tolt). Edits are kept while you switch tabs, a tab with unsaved changes is marked, and **Save changes** at the foot of the page saves them all at once; the portal has its own Save button. Archiving asks first.

Each program keeps its own credit rule. It never follows the workspace's [attribution](attribution.md) settings, so changing how the CMO view credits channels never changes what partners earn.

- **`credit_window_days`:** how long after a visit or click a conversion still credits the partner.
- **`credit_model`:** `first` or `last` visit with a ref inside the window.
- **`codes_win`:** a promo code beats a link when a conversion carries both.
- **`block_self_referrals`:** a partner's own sign-ups and purchases earn them nothing (the conversion answers `reason: self_referral`). On by default; turn it off for programs where partners may buy through their own link.
- **`url_params`** (Program settings → General → Other link parameters): query parameters besides `ref` and `via` that carry a partner's code on your website, such as `aff` or `fpr` for links made on another platform. A page view with no `ref` or `via` is credited by the first of these it carries. Up to 5 lowercase names; not `ref`, `via`, `utm_…`, `of…` or an ad network's click id, which the website script reads already.
- **`block_ad_clicks`** and **`block_social_clicks`** (Program settings → Fraud, "Ignore visits from paid ads" and "Ignore visits from social media"): a website visit with a partner's ref that came from a paid ad click (it carries an ad network's paid click id, such as `gclid` or `msclkid`) or from social media (`fbclid`, which Facebook adds to every outbound link, paid or not, or a social network such as Facebook, X, LinkedIn, YouTube or Reddit as referrer) doesn't credit the partner. Use them for programs whose partners may not bid on your brand or post links on social media. They judge website visits only: a conversion that names the partner itself (`ref`, a promo code) is credited as before. Both are off by default.
- **`blocked_countries`** (Program settings → Fraud → Blocked countries): two-letter codes, such as `BR` and `IN`, for countries whose customers don't earn partners anything, for programs that sell only in some countries or see fake sign-ups from others. A website visit from one of them doesn't credit the partner, and neither does a new customer whose conversion call says one in `country`. A visit's country comes from your own-domain proxy or the visitor's address in the engine's IP database ([IP geolocation by DB-IP](https://db-ip.com)), never from the browser's time zone, which is too easy to get wrong: a visit whose country isn't known that way isn't blocked. A customer already referred keeps their partner. Empty by default.
- **`lead_expiry_days`** (Program settings → General → Lead expiry): a referred customer who hasn't bought within this many days of being referred no longer credits the partner (`lead_expired`). Once they have bought, their later sales credit the partner as the flows say, whatever the lead expiration. Empty (`null`, the default) means leads never expire.

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

### Flows

Rules are flows, as in Tolt (the Flows tab, or `PUT .../rules`). Each flow has:

- a trigger: a sale or payment, a sign-up, or a custom event
- an action: a share of the sale (`percent`), a fixed amount (`fixed`), an action for your own system (`action`), or moving the partner to another group (`group`, with `target_group_id`)
- who it's for: every partner, a group (`group_id`) or one partner (`partner_id`)
- conditions (`conditions`): groups of conditions, all (AND) or any (OR) of which must hold, on
  - the partner: sales, customers who bought, referred customers, revenue, earnings, country, id
  - the customer: earlier sales, what they paid so far, first sale and referral date within N days, months or years
  - the transaction: amount, product or plan id, renewal or not, billing interval, payment provider
- approval (`approval`): the program's hold (`program`, the default), at once (`auto`), after N days (`after_days`, `approval_days`), or by a person (`manual`)
- for cash, `stacking`: `exclusive` (the default) flows compete and one pays, as above; `adds` pays on top
- other rates inside the flow (`overrides`) for one partner or one product or plan; a partner's beats a product's

New flow starts from a template: one-time (a share of the first sale), recurring (every sale), the first 12 months, 10% of the first sale then 2% for 12 months (two flows: the first-sale one wins on the first sale, as the narrower one), or a fixed amount per sign-up. Every field stays editable.

Rules made before flows keep working: they have no conditions, follow the program's approval and are exclusive. A group move is recorded as `partner.group_switched`.

### Groups

Each partner is in one group at most, such as Gold or Agencies, and a group's own rules (`group_id`) apply to its partners. On the Groups tab you add, rename and remove groups, give a group its own rate (Commission rate), and make one the default that new partners join (`default` on `POST .../groups` or `PATCH .../groups/{group}`). To move partners, pick them on the Partners tab and choose Move to, or call `POST .../groups/{group}/partners` with up to 1,000 `partner_ids` (`none` takes them out of every group). Removing a group removes its rules and moves its partners to no group.

**Sign-up link per group.** Each group has its own link to the portal's application form (`signup_url`, the portal address with `?group=<id>`; Groups tab → Copy link). Partners who apply through it join that group instead of the default, with the program's usual review (`auto_accept`). Anyone with the link can apply into the group, so share it only with those you'd put there. A link to a group that has since been removed signs partners up into the default group.

To give one affiliate a better deal, open their menu and choose Commission rate (a percent of each sale), or add a rule with their `partner_id` through `PUT .../rules`. Rewards already earned keep their amounts.

A program with no rules tracks only. For influencers on a fixed fee, log each fee on the partner (`POST .../partners/{id}/fees`). It is approved at once and counted as a cost, so cost per sale and ROAS work.

### The ledger

Rewards are an append-only ledger: an amount never changes once written.

- **Hold.** A new reward waits for the program's hold (`hold_days`), then is approved: on its own when `auto_approve` is on (checked every minute), otherwise by a person. A person can also approve or reject one before the hold ends, or many at once: pick them on Rewards, or use Approve all pending (`POST .../partner-rewards/bulk` with `ids`, or a `program` or `partner_id`, up to 1,000 a call). Each is recorded as if done alone, with its webhook.
  Before acting, the dashboard calls bulk with `dry_run` and says what it will touch, counted by source: made by Outflare, imported and paid here, and imported but still paid by the old platform.
  Imported rewards the old platform still pays are left out unless you include them (`include_held_at_source`), and are listed in `skipped` as `held_at_source`.
- **Cost.** Approved cash and fees for partners become spend entries on the `affiliates` channel, so the CMO view counts them as cost.
- **Refunds.** A refund rejects the sale's rewards still on hold. Approved or paid cash gets a negative `adjustment` entry, pro rata for a partial refund, which the next payout nets. An approved or fulfilled action gets a reversal entry (`partner_reward.reversed`) naming the action, so your system can take back what it gave.
- **Adjustments.** `POST .../partners/{id}/adjustments` corrects a balance either way, with a note saying why.

## Payouts

`POST .../partner-payouts` makes one payout per partner whose approved, unpaid balance reaches the program's minimum. It can run for one partner, one program or all of them. Each payout fires `partner_payout.created`, so a payout tool can pick it up.

Pay it from your own account, then mark it paid with a note and the payment's reference: `POST .../partner-payouts/{id}/paid`. That fires `partner_payout.paid`. Canceling a due payout puts its rewards back into the balance.

To pay them in bulk, download the due payouts as CSV: Payouts → Download CSV, or `GET .../partner-payouts/export` (`status` `due` by default, or `paid`, `canceled`, `all`; `program`). Each row has the partner's code, name, email, payout details, the amount as a decimal and the currency, ready for your payout tool's bulk upload. The file holds payout details, so it needs `partners.manage` and each download is in the audit log (`partner_payouts.exported`).

Outflare has no PayPal, Wise or crypto integration and never stores bank details. A partner's `payout_method` is free text, such as "PayPal: ana@example.com", stored sealed like connector keys. Tax forms may come later.

**Payout term.** A program's `payout_term_days` (Program settings → Payouts → When partners are paid) sets Net 0 to Net 90: a reward joins a payout only that many days after the end of the UTC month it was earned in, so with Net 30 everything approved for October is paid from 30 November. Rewards whose term hasn't passed stay in the balance for a later payout. Left empty (`null`, the default), a reward is paid once it is approved.

**Payout methods.** A program's `payout_methods` lists what partners choose from: `paypal`, `wise`, `bank_transfer`, `crypto` and `other`, and `payout_tokens` the crypto tokens (such as "USDC on Base"). On the portal, a partner picks one (`payout_type`, and `payout_token` for crypto) and adds their details in their own words. With no methods listed, any method is allowed. The method and token are on the partner (`PATCH .../partners/{id}` can set them too) and in the payout CSV's `payout_type` and `payout_token` columns.

**Partners without payout details.** The portal's dashboard asks a partner with no payout details to add them in Settings. On the Partners tab, "No payout details (N)" lists them (`GET .../partners?payout_details=missing`; the program's `stats` count them as `partners.missing_payout_details`). A partner brought in by an import whose platform didn't give their details (Tolt's API has none) gets no payout until they add them: "Make due payouts" leaves their balance as it is and counts them in `waiting_for_details`, and their next payout includes it. Partners added in Outflare are paid as before, details or not.

### When a partner's record is deleted

Deleting a partner's person record for good (`DELETE .../records/{record}`, for an erasure request) keeps what they earned and were paid, so commission totals and payout history still add up. Their memberships, rewards and payouts move to a new archived record named "Erased partner", with no name, email or identities. The membership is suspended and loses its payout method, note, sign-up answers and postback URL, but keeps its code and promo codes: the partner's links still lead to the program's landing page, without crediting anyone. Their referrals, conversions and portal sign-ins are erased.

A payout's note and reference can hold the partner's name or email, so they are cleared too, unless the workspace's partner setting is off (Partners → Payouts → "Clear payout notes when a partner is deleted", or `PATCH .../partner-settings` with `erase_payout_notes`; on by default, and the same for every program). The payout's amount, dates and status always stay.

## Partner pages and proof

Partners → Partners finds any partner in the program, not only the newest page.
Search matches a partner's code, promo codes, name and email, a partner's or their record's whole id, and an imported partner's id on the platform it came from (`GET .../partners?q=`).
Pasting a whole partner id opens that partner.
Sorting (`sort`: `newest`, `oldest`, `name`, `code`, `clicks`, `customers`, `sales`, `balance`, `paid`) also runs across every partner.

Each partner has a page at `/{workspace}/{brand}/partners/{partner id}`, which the list and a customer's person page link to.
It shows the partner's code and link, status, group, payout method, when and how they joined, and their totals: clicks, referrals, customers, sales, commission earned, on hold, approved, paid and due.
Its tabs list their referrals (each customer, how they were credited, the first sale and lifetime sales), conversions, rewards, payouts and history.
The history (`GET .../partners/{id}/activity`) comes from the audit log: joining, status, group and payout detail changes, rewards made, approved, rejected or reversed, and payouts made, paid or canceled, each with who did it.
It names changed fields and never shows payout details.

**Proof per reward.** Every reward has a Proof button, which opens the chain behind it (`GET .../partner-rewards/{id}/proof`):

- the click or website visit that credited the partner, or the promo code or ref code that did;
- the referral and the customer, with a link to their person page;
- the sale or refund, with your own id for it, the brand's transaction, the payment source and when it happened;
- the rule and rate that priced it, as it was when the reward was made;
- who approved or rejected it, how (after the hold, by hand, by a refund) and when, with the note;
- the payout it is in, with the date paid, the method and the reference.

A link with no record shows why instead of being left out, such as "Credited by a promo code, so there is no click or visit".
Rewards imported from another platform show that platform's ids for the partner, customer, commission, sale and payout.
Outflare keeps the visit, the promo code and the rule from the moment a partner is credited.
Rewards made before that show the rule as it is now, marked so, or "rule since replaced" when the program's rules were saved since.

**Downloading proof.** A payout's Download proof, or Download proof on a partner's page with a date range, saves one row per reward with every link of its chain as columns (`GET .../partner-proofs/export` with `payout_id`, or `partner_id` and `from` and `to`; `format` `csv` or `json`; up to 10,000 rewards).
It holds customers' names and emails but never payout details, and each download is in the audit log (`partner_proofs.exported`).

**Rewards and Conversions.** Each reward row names its partner, customer, sale, rule, source (made by Outflare or imported), who decided it and its payout, and each conversion row its partner and customer.
Both search by partner code or email, customer email or your id for the sale (`q`), and Rewards filters by status and source.
A refund's correction links to the reward it takes back, and that reward shows what is left after it, such as "Paid, then taken back, net $0.00".

**Who pays.** Until a program imported from another platform switches over, that platform still pays its imported commissions.
The program's and each partner's totals say how much of what is on hold or approved Outflare pays and how much the old platform pays (`held_at_source` in `stats`).

## Partner portal

Each program can have a hosted portal where partners apply, sign in and see how they're doing. It is off until you open it (Partners → Program settings → Portal, or `PUT .../partner-programs/{id}/portal`).

- **Where it lives.**
  - At `<subdomain>.<partners domain>` when the server has one. The operator sets the partners domain with `OUTFLARE_PARTNERS_DOMAIN`, with wildcard DNS and a wildcard certificate. It can be the server's own domain (portals at `<address>.example.com`): addresses such as `app`, `docs`, `login` and `grafana` are reserved, so a portal can never take or pass for one of the operator's own hosts.
  - On your own domain: add it under Program settings → Portal → Your own domain, or as a tracking domain tied to the program (`POST .../tracking-domains` with `partner_program`), and point its CNAME at the target shown. It is verified and given HTTPS like any tracking domain. A program has one own domain; delete it from tracking domains to use another.
  - Otherwise at `/p/<address>/` on the Outflare server, such as `/p/northpeak/`. The portal's address (Program settings → Portal → Portal address, `subdomain` in the API) is suggested from the program's name and is unique on the server. Without one, the portal is at `/p/<program id>/`, which keeps working after you set one.

  On its own host the portal serves only itself and partners' short links (`/r/<code>`): the rest of Outflare, sign-in included, isn't there.
- **Signing in.** Partners sign in with a one-time link emailed to them through your own email provider, from the sender you set (`email_from`, on a verified sending domain). Without one, the portal can't sign anyone in.
  - The link works once, for 30 minutes. It carries its secret after `#`, so it never reaches server logs.
  - Following it confirms the address and starts a 30-day session on that portal only.
  - Asking for a link answers the same whether or not the address belongs to a partner.
- **Applying.** With `signup_open`, anyone can apply with their name, email, your questions (`signup_fields`: text, long text, URL or a choice) and, when the program has terms, accepting them. They become a customer record and a partner, waiting for approval unless the program accepts everyone (`auto_accept`, off by default; Program settings → General → Review each application).
- **Leads from partners.** With the portal setting `lead_submissions` (Settings → Partner portal → Leads from partners), a partner can submit someone they referred who signed up without their link or code: an email and a note, on the portal's Referrals page.
  - **review:** each waits on Partners → Leads, where you approve or reject it (`GET .../partner-leads`, `POST .../partner-leads/{id}/approve`, `.../reject` with a `reason` the partner sees).
  - **auto:** a lead is taken at once.

  Taking one makes the person the partner's referred customer (via `manual`) through the same path as a conversion, so the program's flows and rules apply, and a customer someone else referred first stays theirs: the lead is rejected with `already_referred`. Partners can't submit themselves, and they see their leads as "Lead 3F9A2C" with the status and reason, never the person's details. A submitted lead isn't the customer's own sign-up, so it records no `signed_up` event for attribution. Off by default.
- **Analytics on the portal.** Pick a site key under Settings → Partner portal → Website analytics, and the portal loads your own website script with it: partners' visits show in your website analytics, and the key's cookie banner asks for consent where it's needed. The key takes events from the portal's address, its own domain or subdomain included, without adding it to the key's origins. Add a Google Analytics 4 id (`G-...`) too, and it loads through that script, held back until the visitor allows analytics. Portal settings `site_key_id` and `ga_measurement_id`.
- **Work email only.** With the portal setting `business_email_only` (Settings → Partner portal → Work email addresses only), applications from free, disposable and relay email addresses are refused (`personal_email`), as Outflare's email classifier sees the domain. Partners you add yourself aren't checked.
- **What partners see:**
  - their links, with sub-ids and any page on the site
  - their promo codes
  - clicks, referred customers (shown as "Customer 3F9A2C", never a name or address) and sales
  - rewards with their status, hold and reason, and payouts
  - assets you add (banners, copy, email swipes)
  - their settings: a free-text payout method, kept sealed, and which emails they want

  By kind: referral programs show only the dashboard and links, and influencer programs read-only stats and assets. Change `pages` to show more or less.
- **Emails.** Partners are emailed when a customer they referred converts (with what they earned and how long it's held) and when a payout is made due or paid. Each partner chooses which they get in their settings. The emails come from the portal's sender, through your own email provider.
- **Look.** Title, logo, favicon (the browser tab's icon), accent colour, a welcome message and custom CSS. Logo and favicon are https addresses of images. Settings → Partner portal shows a preview of the sign-in page as you type.
- **"Powered by Outflare"** shows at the foot of the portal. Hide it with the portal setting `hide_powered_by` (Settings → Partner portal). It's free either way.
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

A mission can aim at referred revenue: `goal_metric` `revenue_referred`, in whole US dollars. It counts credited partner sales less refunds, in the partner programs of the mission's brand, from when the mission started. Imported history never counts ([attribution](attribution.md)).

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

Every call below is under the brand, `/v1/brands/{brand}/`; the same calls under `/v1/workspaces/{ws}/` are deprecated. The MCP tools take the brand.

| Call | Scope |
| --- | --- |
| `GET`/`POST /v1/brands/{brand}/partner-programs`, `GET`/`PATCH .../partner-programs/{id}` | read / manage |
| `GET .../partner-programs/{id}/stats`: the program's totals across all its partners (partners by status, clicks, customers, sales, rewards on hold, due and paid) | read |
| `PUT .../partner-programs/{id}/rules`, `POST .../groups`, `PATCH`/`DELETE .../groups/{group}`, `POST .../groups/{group}/partners`, `POST .../postback` | manage |
| `GET`/`POST .../partners`, `GET`/`PATCH .../partners/{id}`, `POST`/`DELETE .../promo-codes`, `POST .../fees`, `POST .../adjustments` | read / manage |
| `POST .../partner-conversions`, `POST .../partner-programs/{id}/widget-sessions` | conversions |
| `GET .../partner-conversion-log`: every conversion call from the last 30 days, with `program`, `source`, `outcome` and `q` (50 a page, up to 200) | read |
| `GET .../partner-conversions`, `GET .../partner-referrals`, `GET .../partner-rewards`, `GET .../partner-payouts` (newest first, 100 a page by default, up to 500; pass `next_cursor` as `cursor` for the next page; payouts filter by `program` too) | read |
| `POST .../partner-rewards/{id}/approve`, `.../reject`, `POST .../partner-rewards/bulk` | manage |
| `POST .../partner-rewards/{id}/fulfil` | conversions |
| `POST .../partner-payouts`, `.../{id}/paid`, `.../{id}/cancel` | manage |
| `GET .../partners/{id}/postbacks` / `GET .../partner-payouts/export` | read / manage |
| `GET .../partners?q=&sort=`: find partners across every page | read |
| `GET .../partners/{id}/activity`: a partner's history | read |
| `GET .../partner-rewards/{id}/proof`, `GET .../partner-proofs/export` | read |
| `POST .../partner-programs/{id}/imports` / `GET .../imports`, `.../imports/{import}` | manage / read |
| `GET`/`POST /v1/partner-postbacks/{token}` | the postback secret |

The MCP tools are listed in [mcp/README.md](../mcp/README.md): `list_partner_programs`, `add_partner`, `report_partner_conversion`, `create_partner_payouts`, `list_partner_activity`, `get_partner_reward_proof`, `export_partner_proofs` and the rest.

## Coming next

- A fraud review queue with partner checks (self-referral, matching payment details, cookie stuffing). It's on hold for now.

## Tasks and audience profiles

Creating the first task adds Opportunities and Your profiles to that program's
portal; other programs keep their existing navigation. Every new program,
whichever kind, reviews each application by default: a partner who applies waits
until you approve them. A brand can turn that off (Program settings → General →
Review each application).

A program's Tasks tab offers fixed cash rewards, price ranges, custom quotes,
product rewards delivered through the existing action webhook, or voluntary
unpaid tasks. Choose immediate claims or applications. An application receives
an offer that the partner accepts before starting work. Slots include pending
applications, so the brand cannot overbook a task. Pause stops new claims;
existing partners can still submit their work.

Partners add their websites, social channels, newsletters and communities under
Your profiles. They state their role, topics and reach. Self-reported reach is
labeled; when the workspace connects its own Geonode/page-reader account in
Connections, adding a profile queues a public reach read. Refresh reach asks for
a new read. Missing or ambiguous counts preserve the earlier figure and show
why. A public count is not proof that the partner owns a channel. OAuth-based
social ownership and analytics are not part of this version.

A private proposal names an exact website hostname. Its invitation link is the
program portal followed by `?website=example.com#profiles`. It reveals no offer
terms. After sign-in, the partner adds that website and publishes the supplied
DNS TXT record at `_outflare-partner.example.com`, with value
`outflare-partner=<their challenge>`. Verification runs as a job. Only that
partner's verified website unlocks its proposals. Other profiles and offers
remain private to their program and brand. Creating an invitation does not send
an email; use your normal outreach workflow and its send policy.

Human review works for every task category. Partners can submit a published URL
or a completion note, so tasks such as feedback sessions do not require a
public post. Automatic checking requires evidence
on a verified website and an exact required link, read through the workspace's
own page-reader connection. It checks link presence, not editorial quality.
Automatic checks can hold work for a configurable number of days so a person
can approve or reject it. When the hold ends, the page is checked again before
approval. Missing connections or failed checks leave the work for human review.
Unpaid tasks must also follow the destination platform's rules.

Approved cash enters the existing partner payout ledger exactly once. Product
rewards emit the existing reward approval webhook for the brand's system to
fulfil. This version does not hold brand funds or transfer money: managed,
prefunded payouts require a separate payment-provider integration.

For the full portal inside your product, create a widget session on your backend
as usual, then use `/p/<program>/embed?view=portal#token=<session token>` as the
iframe source (or the program's own portal host and `/embed` path). Keep the
token in the fragment, never a query parameter. The session is held in memory,
removed from the displayed URL, and used as a bearer token. This works without
third-party cookies; the ordinary referral widget stays available.

### Portal design

The hosted and full embedded portal use the dashboard's shared React components,
Geist fonts, design tokens, and light/dark themes. A compact opportunity feed
opens task briefs and submissions in a side panel. Partners manage channels in
profile cards and verify ownership in a step-by-step dialog.

The web build emits `portal.html` alongside the dashboard. Both are included by
`pnpm --filter @outflare/web build`; API-only installs without built web assets
retain the standalone portal. Brand logos, portal titles, custom CSS and brand
colour remain configurable. Custom CSS targeting old template classes should
be reviewed for the new component structure.

### Brand identity, help and program terms

Portal settings include the brand homepage, support page/email, ordered header/footer links, named landing pages, plain-text help articles and payment guidance. The dashboard editor previews identity and groups these settings under Navigation, Help center, Terms & payments and Landing pages. Partner help includes practical getting-started, promotion, tracking, task and payout guides alongside brand-authored articles. Help articles are rendered as text, never executable HTML. Landing pages must use the program landing website or a subdomain; arbitrary external redirects are not accepted.

Set `terms_url` on the program and `portal.terms_revision` when that document changes. Signed-in partners explicitly accept the current address/revision before submitting work or changing their account. Acceptance records its version and time and emits `partner.terms_accepted`. Sign-in and help remain available for reviewing the rules; a team preview cannot consent for a partner. Existing partners must review current terms after this upgrade.

### Automatic hosted sign-in

A brand backend can call `POST /v1/brands/{brand}/partner-programs/{id}/portal-sessions` with `customer_id` and optional `email`/`name`, using `partners.conversions`. Only send an email after verifying it in your own app; otherwise omit it. Identify the customer from the authenticated server session, never an arbitrary browser-supplied user ID. Redirect that customer to `sign_in_url`. The URL carries a single-use code in its fragment and expires after 60 seconds; the portal exchanges it for an HttpOnly cookie and clears the fragment. Never cache the URL, log it, share it, or put an administrative key in the browser.

This uses the same customer identity and membership as embedded widget sessions and conversion tracking. Send the stable customer ID on every call so email changes do not create a new partner. Admission rules and the program terms still apply. This is a backend handoff; it does not configure an external OpenID Connect identity provider.

### Payout requests and memorable website links

An active partner with payout details can request their eligible approved cash from Earnings. The request allocates the existing rewards into a due payout, visible in the brand's Payouts tab. Minimums, Net N terms, source holds and existing payout allocations apply. Repeated requests cannot allocate the same rewards again. The brand sends payment through its own process and marks it paid; Outflare does not transfer funds.

The link builder offers the brand's named landing pages and campaign labels (`sub`/sub IDs). For a memorable link such as `https://northpeak.example/maya`, configure a temporary redirect on that website or its CDN to the complete partner tracking link. A tracking subdomain already serves `/r/{code}`; DNS by itself cannot map a root-domain path. Preserve the chosen tracking destination and campaign parameters, reserve existing website paths, and test that the final landing URL carries referral attribution. Changing a website redirect does not alter historical clicks.

### Checkout discounts and partner-generated codes

A coupon's customer discount is separate from the program's partner reward. Brands can assign a percentage/fixed discount or an existing billing coupon, with start/end timestamps, first-time-purchase eligibility, a minimum order amount, per-code total redemption limit, eligible billing product IDs, and first-invoice, repeating-month or ongoing duration. Codes with a start time are created in billing by the existing job at that time. Expiration and redemption restrictions are enforced natively by the connected Stripe checkout; discount stacking remains a checkout setting. API-reported conversions must represent genuine checkout events, not a substitute for enforcing discounts.

Publish `portal.coupon_templates` to let active partners generate codes from approved rules. Partners choose a template and code, not arbitrary discount values. Changes to a template affect newly generated codes only. Assigned codes can be enabled/disabled through `PATCH .../partners/{id}/promo-codes/{code}`; attribution changes immediately and a job applies the corresponding native checkout update. Check sync status before sharing a code. Local codes are attribution mappings only: their actual discount and enabled state must also be managed in the brand's checkout. Global cross-code redemption budgets and arbitrary per-customer limits are not represented by these per-code limits.

### One-to-one partnership proposals

Partners → Proposals manages privately assigned agreements rather than claimable tasks. Draft for a specific existing partner or recipient email. Each proposal contains cross-platform deliverables, requirements, deadlines, minimum live periods, optional brand-kit/product links, reply-to email, additional terms and manually approved milestones. Milestones may combine cash with product/credit benefits. Commission wording describes the program's existing reward rules; configure calculation in Flows.

Create an invitation link after reviewing the draft, then place it in your own outreach. This action sends no outreach. The unguessable fragment opens a branded teaser showing only the configured website/channel name, URL and icon. The brief, compensation, deliverables and terms are returned only after the assigned recipient signs in. The recipient requests a single-use email sign-in link sent through the workspace's configured sender. Only the assigned, authenticated partner can quote, accept, decline or submit work; forwarding a preview does not transfer that right. Configure the portal and email sender before sending invitations. Regenerating a link or revising a draft invalidates the previous preview link.

Quote requests accept a price and optional note. Discuss other changes by email, revise the draft, turn off quote mode and send a final priced offer. Acceptance locks the version and terms; different terms after acceptance require a new proposal. The portal provides a downloadable text copy, payout-settings access, invoice/receipt instructions and publication evidence submission.

Verify publication manually, then explicitly approve each eligible milestone. Waiting periods start at brand verification, not at an unverified partner-supplied date. A timer never releases payment. A 50/50 arrangement can use two milestones linked to one deliverable: half at verification, half after 30 days. Cash enters the existing ledger and its payout terms; benefit rewards use `partner_benefit` and the existing fulfillment workflow. Approving a milestone twice cannot create a duplicate reward. Completion also requires all deliverables to finish their specified live periods. This first version leaves disputes and cancellation decisions to the brand; it does not automate legal enforcement, monitoring or money transfers.

A program’s currency is locked once it has a proposal, so quoted and accepted cash amounts retain their currency. Accepted agreement downloads include the currency and recipient. Brand changes carry the program’s proposal facts with its other partner data.

Set `partner_site_name`, `partner_site_url`, and optional `partner_site_icon_url` on a proposal to personalize its invitation teaser. The portal uses the site favicon when an icon is omitted, with a globe fallback. Login and signup use the same email-verification flow bound to the invited address. No third-party favicon lookup service is used.
