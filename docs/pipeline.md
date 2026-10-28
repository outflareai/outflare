# Pipelines

A pipeline is work that moves through stages one item at a time. A lead finder run, for example, searches the web, reads each company's website, qualifies it with the workspace's AI, waits for a person's review, checks the CRM and the contact rules, finds and verifies contacts, and adds them to the CRM and a campaign. Each company in the run is one item, and each item is in one stage at a time.

The runtime is `engine/internal/pipeline`. Extensions use it through `Host.Pipeline()`.

## Why items, not one long job

A run done as one job breaks at volume. One stuck AI call holds the whole run, a retry starts it over, and nothing shows which step is slow. In the pipeline:

- **One row per item** (`pipeline_items`) holds its stage, its state, when it may next run, its attempts in this stage, and the code and note for its last error or reason. The feature keeps its own data in its own tables, keyed by the item's `ref`.
- **Short batches.** A stage worker claims up to `Batch` due items (default 25) with `FOR UPDATE SKIP LOCKED`, works on them without a transaction open, and writes every outcome in one short transaction. It renews the batch's lease while it works. A crash loses one batch at most, and the lease (default 2 minutes) brings the items back.
- **Fair sharing.** A claim takes each workspace's oldest items first, round robin, and never lets one workspace have more than `PerWorkspace` items in progress in a stage (default 50). A big run gets the spare capacity, never all of it.
- **Retries per item.** A failing item backs off on its own (10 seconds doubling to 15 minutes, up to `MaxAttempts`, default 5). The rest of the run keeps moving.
- **Only live runs are claimed.** A paused or cancelled run's items stay where they are.

Stage workers run in the `all` and `worker` roles (`OUTFLARE_ROLE`, [scaling.md](scaling.md)). Every worker process works on every stage, and `Concurrency` (default 2) is the number of batches of a stage one process works on at once.

## What a stage decides

A stage's `Work` gets a batch of items and returns a `Result` for each:

| Outcome | What happens |
| --- | --- |
| `Advance` | Moves to the stage named in `Next`, due now. |
| `Done` | Went through every stage. |
| `Skip` | Left on purpose, with a code: a reject, a duplicate, an opt-out, an unreachable site. |
| `Hold` | Waits for a person (`Release` sends it on or skips it). |
| `Retry` | Tries again after a backoff, counting an attempt. After `MaxAttempts` it fails. |
| `Wait` | Tries again later without counting an attempt: a rate limit, an open breaker, a slow check still running. |
| `Pause` | Puts the item back and pauses the whole run with a code: out of budget, a missing connection. `Resume` carries on. |
| `Fail` | Gives up, with a code. |

An item missing from the results is retried with the code `no_result`. A `Work` error or panic retries the whole batch with `stage_error`.

Codes are error codes from the API's catalog where one fits (`connector_needed`, `connector_quota`, `ai_budget_reached`), or the stage's own (`no_website`, `rejected`). The note (`Reason`) may quote customer data, so it is kept for staff and the run page and goes to the log, never into events or metrics.

When a run has nothing waiting or being worked on, the pipeline's `OnSettled` function runs, where the feature finishes the run or asks for a review. It can run more than once for a run, so it must be idempotent.

## Shared steps for companies

A feature whose items are companies (the item's ref is the company's domain) can add the engine's own steps to its pipeline with `Host.Pipeline().RegisterCompanySteps`, and send items to the first one, `known`:

| Step | What it does | Outcomes |
| --- | --- | --- |
| `known` | The workspace already has a company record with that domain: links the item to it | Skip `already_known`; otherwise on to `contact_rules` |
| `contact_rules` | Checks the domain against the opt-out list for cold email. Opt-outs and bounces stop it; a competitor, customer or partner entry only warns | Skip `opted_out` or `bounced`; otherwise on to `add_to_crm` |
| `add_to_crm` | Adds the company as a prospect record (`EnsureCompanies`), with the feature as `acquired_via`, and links the item to it with any warnings | Done, or on to the feature's next step |

The feature says which company an item is (`Company`, with the brand to add it for) and records the outcome in its own data (`Linked`, in the transaction that adds or finds the record, and `Stopped`). Every outcome is counted in the run's summaries and metrics like any other stage.

## Limits and breakers

`Limits` hold a rate limit and a circuit breaker per outside account (`integration:<id>`, `ai:<workspace>`), shared by every worker through `pipeline_limits`:

- Before a call, `Take` says whether to go or how long to wait. The stage returns `Wait` for that long instead of calling.
- After the call, `Report` says how it went. A rate limit answer halves the account's rate for 5 minutes, and halves it again if it repeats, down to an eighth.
- Five failures in a row open the breaker for a minute. When the minute is up, one probe call goes through. If the probe fails, the breaker opens for twice as long, up to 15 minutes. A success closes it.

Only failures that say the account is unwell count toward the breaker: a provider error or timeout, not a bad input.

## Seeing where it's slow

- **Run summaries.** `pipeline_stage_stats` keeps counts per run and stage: entered, advanced, done, skipped, held, failed, retried and waited, the time spent and the codes seen. They are written in the same transaction as the items, and they stay after the items are pruned. A run's items are deleted 30 days after it ends. `View` returns a run with its live counts per stage and its oldest waiting item.
- **Metrics** ([monitoring.md](monitoring.md)):
  - `outflare_pipeline_items` (by stage and state) and `outflare_pipeline_oldest_due_seconds`. The stage whose oldest due item keeps getting older is the bottleneck.
  - Outcomes and codes per stage, batch time and size, and the wait before a claim.
  - Waits on limits, call outcomes per kind of account, open breakers, and runs by state.
- **Dashboard and alerts.** The Grafana dashboard has a Pipelines row. Alerts fire on a stage with an item waiting over 30 minutes, over 10% of a stage's items retrying or failing, items failing for good, and a breaker open for 10 minutes.
- **Logs.** There is one line per batch (debug, or warning when something retried, failed or paused) and one warning per item that retried, failed or paused. They carry the workspace, run and item ids, the stage, the attempt and the code.
- **Events.** `pipeline.stage_failed` is recorded once per batch and workspace when items retried or failed, with counts by outcome. `pipeline.run_paused` carries the pause code. `pipeline.settle_failed` means a feature's settled hook returned an error, and `pipeline.write_failed` means a batch's results couldn't be written after three tries (deadlocks and serialization failures are retried); its items are picked up again once their lease runs out.
