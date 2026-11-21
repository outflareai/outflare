# Revenue

The Revenue page shows what a brand earns every month and how that moved, what it collected, the renewals coming up, and how much revenue sits with customers who aren't healthy. It reads data Outflare already keeps, so there is nothing to set up for it, and every card hides until the brand has data for it: a brand that only tracks renewals sees only renewals.

Revenue belongs to one brand and is in the brand's currency. Everything here works the same from the REST API (`GET /v1/brands/{brand}/revenue`), the MCP server (`get_revenue`) and the app (Measure → Revenue). Reading it needs `customers.read`.

## Where the numbers come from

| Part | Comes from |
| --- | --- |
| Recurring revenue and how it moved | The subscriptions your billing connection keeps on each person or company (your own Stripe, Paddle or Chargebee account; see [customers.md](customers.md)). Active and past-due subscriptions count; trials and cancelled ones don't. |
| Collected | The brand's [transactions](transactions.md): payments that went through, less refunds and chargebacks. |
| Renewals | Any record with a "Renews on" date (`renews_on`): a company, an agreement or a type of your own. The agreements and account-signals kits add the field. |
| Revenue at risk and account health | The brand's [health score](scores.md) (the score with the key `health`). |

Outflare never reads another account for this: without a billing connection the recurring and collected cards stay hidden.

## Recurring revenue

Every 15 minutes Outflare adds up each paying customer's subscriptions as a monthly amount in the brand's currency, compares it with the last count, and books the difference to the day (UTC):

- **New**: a customer who had never paid before.
- **Expansion**: a customer paying more than at the last count.
- **Came back** (reactivation): a customer who had stopped paying and pays again.
- **Contraction**: a customer paying less.
- **Churned**: a customer who stopped paying.

Each day also keeps where it ended: monthly recurring revenue, paying customers, the revenue of customers whose health score is low, and how many records are in each band of each score. The page draws these as a bar per day (up to 31 days), week (up to 180 days) or month.

The first count for a brand only notes where everyone stands, so revenue that was already there is not booked as new. Movements exist from that day (`counted_since`) on; Outflare doesn't rebuild the months before from the billing provider.

**Net revenue retention** is the revenue at the start of the period, plus expansion and reactivation, less contraction and churn, as a share of the start: 105% means the customers you had are worth 5% more now. **Gross revenue retention** leaves expansion out, so it never passes 100%. New customers count in neither. Both are empty when the period started at zero.

Amounts in another currency are converted with the day's European Central Bank reference rate; a subscription whose currency has no rate is left out until one arrives.

## Renewals

The list runs from a month late to 90 days out, soonest first, and leaves out records whose stage is ended, cancelled or lost. Each line shows the record, the company it belongs to, its value (or what the company pays a month) and the company's health band, so the renewals that need work stand out. To be told about a renewal in time instead of looking for it, set up the "Flag renewals and quiet accounts" playbook, which raises a [signal](signals.md) for the owner.

### Renewal deals

A renewal can open its own deal, so it is worked in the pipeline like any other. The journey step **Open a renewal deal** (`open_renewal_deal`, in a journey about companies) looks at the company's own "Renews on" date and at each record that names it as its company (an agreement), and for every one due within `within_days` (90 when left out) opens a deal:

- named "Renewal: " and the company (and the agreement, when it is one);
- at `stage`, or the first of your deal stages;
- worth the record's value, or for a company with none twelve times what it pays a month;
- closing on the renewal date, linked to the company, so it counts for the company's owner in the forecast.

One renewal gets one deal: a renewal that has had a deal for that date never gets a second, whatever became of the first, so the step is safe to run every day. The "Flag renewals and quiet accounts" playbook does this 60 days before a renewal, after it raises the signal and the task.

In the [forecast](#forecast) a renewal deal stands in for its renewal: it is counted once, under renewals, at the weight of the company's health band and not of the deal's stage (`kind` is `renewal`, `weight_from` is `health`). When the deal is won it counts in full, as won. Move the record's "Renews on" date to the next term when you win it; the next renewal then gets a deal of its own.

## By product

When payments name a product (your billing connection or the API sends one with the transaction), **By product** splits what was collected in the period: what each product brought in, its share, how many people and companies paid for it, and whether that is up or down on the same number of days before. Payments that name no product are one line at the end, so the split always adds up to what was collected. The card stays away until some payment names a product (`products` in the report is empty).

## Spending more, spending less

For brands whose customers pay as they go, with no subscription to count, the page lists the people and companies whose payments moved most between this period and the same number of days before: up to ten who paid less and ten who paid more. It reads the same transactions as Collected, so it works for any way of paying.

## Revenue at risk

Paying customers whose health score is in its low band, the biggest first, with the total at the top of the page. It needs a score with the key `health`: CRM → Customer data → Scores has a starter.

## Forecast

**Forecast**, at the top of the Revenue page, answers one question: what can you expect to book this quarter, and how far is that from the target? It adds three things for the quarter you pick (this one or one of the next three):

| Part | What counts | How much of it |
| --- | --- | --- |
| Won | Deals won since the quarter started. | All of it. |
| Renewals | Any record with a "Renews on" date in the quarter; in the current quarter also those up to 30 days late that still carry the old date. The value is the record's value field, or twelve times what the company pays a month when it has none. | A share that depends on the health band of the company: 95% high, 80% medium, 50% low, 85% with no score. |
| Open deals | Deals whose close date falls in the quarter; the current quarter also takes deals with no close date or one that has passed. A deal with someone who already pays is **expansion**, the rest **new business**. | A share that depends on the stage, spread evenly in the order of your stages: four stages count 20%, 40%, 60% and 80%. A brand that keeps a deal [score](scores.md) with the key `win` counts each deal at that score instead (`weight_from` is `score`). |

Every line shows its value, its weight and what it adds, so the total can be checked by hand. **Best case** counts every renewal and open deal in full. Set the quarter's **target** on the page to see the gap, and change the weights under **How it's weighted** to match how your renewals and deals really turn out; an empty box goes back to the default. Deals and renewals with no amount are left out and counted at the bottom, so you know what to fill in.

**By owner** splits the same forecast by teammate (`owners` in the response): what each has won this quarter, what is expected from the renewals and open deals they own, and their best case. A deal or renewal with no owner of its own counts for the owner of the company it is with; what nobody owns is the last line, so the split adds up to the forecast. It shows once at least one deal or renewal has an owner.

**How the forecast moved** lists what the forecast said in each week of the quarter (`history` in the response): the forecast, how much of it was already won, the target that week, and how far it moved from the week before. Outflare saves a reading of every brand's current quarter once a day, and the last reading of a week stands for that week, so a new brand sees its first line within a day and a new one each week. Readings are kept for the quarter in progress; a quarter ahead has none until it starts. Nothing here is predicted: each line is the same arithmetic as the forecast, kept as it stood.

The forecast is arithmetic on what the CRM holds. No model predicts anything, and nothing is stored but the target, the weights and each week's reading, so correcting a deal or a health score changes the forecast the next time you look.

```http
GET /v1/brands/{brand}/revenue/forecast?quarter=2026-Q4
PUT /v1/brands/{brand}/revenue/forecast
{ "quarter": "2026-Q4", "target_minor": 25000000, "renewal_weights": { "low": 40 }, "stage_weights": { "proposal": 60 } }
```

Reading needs `customers.read`; setting the target or weights needs `catalog.manage`. Only what is sent changes: a null target removes it and a null weight goes back to its default. MCP: `get_revenue_forecast`, `set_revenue_forecast`.

## Sales team and commission

Who is asked to win what each quarter, and what they earn on it, are on their own pages under Revenue: see [Sales team and commission](sales-team.md).

## The API

```http
GET /v1/brands/{brand}/revenue?days=90
```

`days` runs from 7 to 730. The answer has `recurring` (null without counted subscriptions), `periods`, `collected` (null without payments), `renewals`, `at_risk`, `spending` and `bands`. Money is in the brand's `currency`, in its minor unit (cents).
