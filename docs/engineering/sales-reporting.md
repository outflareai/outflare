# Sales reporting

Sales reporting sits beside Sales conversations in CRM, with a Reporting link in the Sales manager navigation. Open `/<workspace>/<brand>/sales-reporting`. The default page reads real API data; **View UI demo** opens a separate, explicitly fictional prototype, including when the API is unavailable. A failed request never falls back to examples.

Without a Lead Portal connection, it shows **Outflare data only**: native SDR pickups and confirmed customers remain available, while discovery, research, verification, email, replies, costs and capacity stay unavailable. Confirmed customers do not require payment or a revenue amount; optional expected monthly revenue stays separate from payment reporting.

## Connections and access

`GET /v1/brands/{brand}/sales/reporting` and `/sales/reporting/leads` require `customers.read` and either a signed-in Sales manager or `members.manage`. Automation tokens cannot borrow their creator's Sales manager role. API and MCP share these checks.

Connect **Lead Portal** under Connectors, using that portal's URL and admin `lp_` API key, and assign the connection to its one brand. Credentials stay in the workspace's sealed connector store. A shared connection is deliberately ineligible: a portal report describes the entire source portal, so it cannot safely act like independently filtered data for several brands. Several eligible connections require an explicit choice. No operator or environment key is used.

The source portal must expose `/api/v1/reports/sales-reporting` version 1. Its admin gate applies to session and API-key access. Reporting is read-only apart from the normal API-key last-used timestamp. Balance and mailbox observations are read from saved snapshots; opening a report does not buy tools, refresh provider tokens, enroll leads or send email.

## Counting

- Discovery is the first Sales research queue entry per company domain, or a directly submitted Sales lead's creation if no research job exists.
- Research is the first completed research job timestamp available for that domain. The job store uses `updated_at`, not an immutable completion log. Historical edits to a completed job can change its recorded timestamp.
- Verification is the first `verify → ready_for_outreach` event. Bison enrollment is not another funnel stage. First email uses the earliest confirmed email enrollment send timestamp.
- Reply is the first recorded email reply per company, using the portal's event receipt time. LinkedIn replies and repeat replies do not inflate it. This report deliberately differs from older reports that count every event or reply.
- SDR pickup is the first native Outflare `lead_taken` event with actor role `sdr`. A direct AE pickup does not become an SDR pickup.
- Closed is the first native `customer_confirmed` event. An archive is not evidence of a lost deal. Displayed deal values are the current recorded values, grouped by currency; missing values and unconnected payments are not zero. Historical portal SDR/deal outcomes are not imported by this report.

All ranges are `[from,to)` in UTC. 24/48 hours use complete hours; week/month/year use 7/30/365 complete days. The lines compare independently dated milestones. The **Same leads** cohort follows companies discovered within that period through their latest progress, even if later stages occurred outside the period. Pending or unobserved later milestones are not counted as lost.

The interval inspector requests every selected stage and returns 50 records per stage with independent counts and pagination. The same report lifecycle supplies the counts and lists. Native records link to their canonical Sales conversation; no copied identities are stored in a reporting table.

## Original uploader and agent statistics

Queues, lead details and milestone lists show the original uploader separately from the current Sales owner. Identity is the named discovery worker or API key, never a credential value, hash or prefix. Revoking a key does not erase its historical attribution. Two keys owned by the same user remain distinct; current researchers and Sales owners do not inherit discovery credit.

The portal derives origin from the first accepted discovery event for the company domain. Direct submissions use the first submit event's API-key principal and its owner, a legacy named Sales agent, or an explicitly recorded manual principal. Missing evidence remains Unknown. The report never guesses an uploader from the current research job assignment. Earlier discovery events without structured domain evidence are also Unknown.

Outflare joins authorized native lead source IDs to `GET /api/v1/reports/sales-reporting?lead_ids=...` in batches of at most 100, with at most four concurrent reads and an eight-second overall deadline. Only a brand-owned portal connection can resolve identities. A source outage leaves queues usable and labels origin unavailable. Existing imports work without a backfill, a new engine table, or switching on the outbound bridge. Several portal connections make queue origin unavailable until there is one unambiguous source; reporting can use the explicitly selected connection.

**Agent performance** in Results includes each stage's event-dated activity, with native SDR pickups and closures joined to the same uploader. Counts reconcile with the main totals, including Unknown. Coverage states how many native milestones could be attributed. These are activity counts, not same-cohort conversion rates. The alternate **Submission quality** view counts submitted, accepted, duplicate, blocked and held discovery candidates, deduplicated per worker/job/domain using the latest outcome in the selected window. Direct submission keys have no equivalent intake ledger and show unavailable quality, not zero. Cost is not allocated per agent without supporting usage evidence.

## Costs and capacity

Cost windows end at the most recent complete UTC hour, matching the source's hourly spend ledger. Each tool and the combined Sales total divide by verified companies in those same 24 hours. AI charges already booked by workers are not added again. Shared metered usage is shown separately with **Unallocated** per-lead cost, never charged in full to Sales. Wallet balances may serve several tools or teams and are not summed. Missing coverage, unavailable prices/balances and zero denominators remain explicit. Recorded tool usage excludes mailbox subscriptions and staff time.

Mailbox capacity uses the source report's Sales department mapping. Configured slots are physical capacity, not a reserved Sales quota; shared campaigns, stale snapshots, unknown limits, source freshness and provider day/timezone remain visible.

## Results and funding

Results compares the current window with the immediately preceding window of the same length, fetched through the same reporting endpoint and brand connection. It shows first replies, SDR pickups, confirmed customers, their recorded expected monthly revenue and the average per valued customer. This uses the Sales workflow's monthly amount, not a lifetime contract value or collected payment. Each period uses event dates; these ratios are activity growth, not same-lead conversion. Zero baselines have no percentage, missing comparisons stay unavailable, and currencies are never added together. The throughput chart appears only in Live flow.

Funding links use a fixed set of provider destinations keyed by the paying wallet. Email finding, BounceBan and LinkedIn lookups funded through Treg link to Treg; AI links to OpenRouter. Unknown wallets have no guessed link. Opening a provider page does not make a payment. The browser records only the bounded wallet key.

## Set up reporting

1. Open Sales reporting for your brand. Native Outflare SDR pickups and confirmed customers are available without a Lead Portal connection. Use **View UI demo** to explore fictional examples, then **Return to live reporting** to see your brand's data.
2. For upstream milestones, connect a compatible Lead Portal service that exposes the version 1 reporting API described above. Assign the connection exclusively to the brand whose pipeline it represents.
3. Verify the connected status, milestone counts, interval lists, cost windows and mailbox observation timestamps. Unavailable source data remains explicitly marked. Connecting reporting does not import leads, activate campaign dispatch or send email.

Outflare's native conversation records and customer-confirmation events supply its Sales outcomes. The connected service supplies its upstream milestones. Reporting reads those sources; it does not transfer their records or database ownership.

## Validation

Run `pnpm -r build`, `pnpm --filter @outflare/web test:sales-reporting-results` and `pnpm --filter @outflare/web test:sales-reporting`. The browser test starts a local Vite server with fictional API responses, covers manager navigation, unavailable source data, explicit demo access after errors, all periods, optional customer amounts, mobile layout, selected-stage drilldowns, comparison retries and navigation to native conversations. Set `OUTFLARE_TEST_SHOTS` to a local directory for screenshots of these fixtures only. With `TEST_DATABASE_URL` pointing to a disposable database, run `go test ./engine/internal/api -run 'TestSalesReporting|TestSalesAttribution'` and `go test ./engine/internal/connectors/leadportal`. The result tests cover equal periods, missing history, zero baselines, separate currencies and the fixed funding destinations; API tests cover access and brand isolation as well as interval details.

For a UI walkthrough, use fictional fixtures. Select several stages and inspect an interval; switch through all five periods; verify that Results has growth comparisons without throughput; make the previous-period request fail and retry it; and check that each funding link follows its paying wallet.
