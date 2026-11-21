# Sales team and commission

Revenue, Sales team shows who is asked to win what in a quarter and how far along they are. Revenue, Commissions works out what each person earns from rules you write, line by line. Both read their people from the [team directory](team-directory.md) (Settings, Team): names, job titles and who reports to whom live there and nowhere else.

Outflare works commission out. It never pays anyone: you approve a month, pay it your own way, and mark it paid.

## Who is on the team

Someone in the team directory is on a brand's sales team for a quarter when they have a quota that quarter, own something in the [forecast](revenue.md#forecast), have a role on the brand's sales desk (SDR, account executive or sales manager), or are covered by a commission plan that is on. Their managers are on it too, up to the top.

Anyone in the team directory can own records, with or without a sign-in, so a salesperson who never opens the app still has their leads, deals, quota and commission counted. The page marks people with no sign-in. When they get one later (Settings, Team, edit the person), everything they own stays theirs.

## Giving someone a lead, a company or a deal

Every person, company and deal page has an **Owner** control in its header: pick a teammate, yourself, or nobody. In the CRM list, tick several rows and choose **Assign to…** to hand them all to one person. [Routing](routing.md) gives new people and companies an owner by rule, so nobody has to do it by hand.

A deal counts for its own Owner field. When that is empty it follows the owner of its person, then of its company. Renewals count for the owner of the company. A deal also has **Sourced by**, for the person who found it when that is someone other than its owner, and **Lead source**: inbound (they came to you) or outbound (you went to them). With no lead source, a deal tied to outreach is outbound and any other is inbound.

**Two owners.** A deal two people worked has a **Second owner** and **Second owner's share (%)**, from 1 to 99; empty, they share it evenly. The deal's amount is split between the two everywhere: what each has won against quota, their forecast, and commission. A rule that pays the owner pays each on their share (10 percent of a 10,000 deal split 70/30 pays 700 and 300), a flat amount is divided the same way, and a manager's rule pays each owner's manager on that owner's share. Rules for the company's owner and for whoever sourced the deal still pay on the whole deal.

## Quotas and the quarter

**Set quotas** takes an amount per person for the quarter, in the brand's currency. Each person's row shows what they have won, their forecast (won, plus the renewals and open deals they own weighted as in the forecast), their open lines and their percent of quota. A manager's row carries a second line for their team: the same numbers for them and everyone below them in the org chart.

Under each person, a line says how they handle the [signals](signals.md) given to them: how many they closed this quarter and how many of those were won, how long it usually takes them from the signal being raised to closing it, and how many wait on them now, with the late ones called out. `GET /v1/brands/{brand}/revenue/team` has the same numbers under `signals`.

Someone who can manage the catalog sees the whole team, the total and what has no owner on the team. Everyone else sees their own numbers and those of the people who report to them.

## Commission plans

A plan says who it is for and holds a list of rules.

- **Who it is for.** Job titles from the team directory (someone new with that title is covered by themselves), roles on the sales desk, or people by name.
- **Rules, tried from the top.** For each deal won and each payment, the first rule that applies pays. A rule marked "on top" also pays when a rule above it already paid the same person, which is how a manager's share or a bonus is added.
- **Status.** A new plan is a draft and pays nothing until it is turned on. An archived plan is kept and pays nothing.

Each rule says:

| Part | Choices |
| --- | --- |
| What it pays on | Any deal won; a deal won with a new customer; a deal won with someone who already pays (expansion); a renewal won; or money collected. |
| Who gets it | The deal's owner (for a payment, the customer's owner); the company's owner; whoever sourced the deal; or the owner's manager. |
| How much | A percent of the amount, or a fixed amount per deal or payment. |
| Inbound or outbound | Either, only inbound, or only outbound. |
| New or coming back | Any customer, only new customers, or only customers who came back after they had stopped paying. |
| Days since they started paying | For money collected: only payments made between two days, counted from the customer's first payment. So the first 30 days can pay 10% and days 31 to 100 pay 2%. |
| Rates by quota | Percent rates that take over once the person has won a share of their quarter's quota, such as 12% from 100%. |
| A monthly cap | The most the rule pays one person in a month. |
| Refunds | For money collected: take the commission back when a payment is refunded. |
| Extra conditions | Up to five conditions on the deal's or payment's fields: `amount` (in whole units), `stage`, `product`, and the company's fields under `company.`, such as `company.country`. |

**Coming back.** A plan sets how many days without a payment make the next payment a return (90 to start with). A customer who returns counts as coming back, and their days start again from 0. If customers pay once a year, use 400 so a yearly renewal is not mistaken for a return.

New plans can start from an example: a percent of every deal; more on money collected in the first month; outbound paying more than inbound; or a better rate once quota is reached.

## Earnings and approving a month

Commissions lists everyone a plan covers for the month, with their total. Open a person to see every line: the deal or payment, its amount, whether it was inbound or outbound, new or coming back and which day, the rule that paid it, the rate and what it pays. A refund taken back shows as a negative line.

- An **open** month is worked out each time it is read, so it follows your deals, payments and rules as they change.
- **Approve** keeps the month's lines and total as they are. Later changes no longer move it.
- **Mark as paid** records that you paid it.
- **Reopen** drops what was kept and works the month out again.
- **Download** saves the month as a CSV file, one row for every line, for payroll: the person, the date, the deal or payment and its customer, the plan and rule, the rate, their share of a split deal, and the amount. From the API, `GET /v1/brands/{brand}/commissions?month=2026-11&format=csv`. You get the rows of the people you may see.

## The API

| Call | What it does |
| --- | --- |
| `GET /v1/brands/{brand}/revenue/team?quarter=` | The team's quarter, person by person, with each manager's roll-up. |
| `PUT /v1/brands/{brand}/revenue/team/quotas` | Set people's quotas for a quarter; a null target takes one away. |
| `GET /v1/brands/{brand}/commission-plans` | The brand's plans and rules. |
| `POST /v1/brands/{brand}/commission-plans`, `PATCH` and `DELETE .../{id}` | Add, change or delete a plan. |
| `GET /v1/brands/{brand}/commissions?month=` | Each person's statement for a month. |
| `PUT /v1/brands/{brand}/commissions/{staff}` | Approve a person's month, mark it paid, or reopen it. |

Reading needs `customers.read`; setting quotas, changing plans and approving need `catalog.manage`. The MCP server has the same calls: `get_sales_team`, `set_sales_quotas`, `list_commission_plans`, `create_commission_plan`, `update_commission_plan`, `delete_commission_plan`, `get_commissions` and `set_commission_statement`.
