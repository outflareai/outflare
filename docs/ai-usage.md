# AI usage and limits

Every AI call Outflare makes uses the workspace's own key with its own provider (Claude, OpenAI, Gemini, Mistral, OpenRouter and the others under Connectors), and the provider bills the workspace's account. Outflare pays for no AI, in Cloud or self-hosted ([`docs/adr/0009-ai-models.md`](adr/0009-ai-models.md), AGENTS.md "Bring your own keys"). This page is how a workspace sees and caps that spend.

## The usage log

Each call is a row in `ai_calls` (migration 0182): the tool that made it, the provider and model, input, output and cached tokens, web searches, and the cost in US cents, rounded up. The cost is the model's price times the tokens, or the provider's own figure when it reports one (OpenRouter). Prompts and answers are never stored. Rows are kept for 13 months and deleted hourly after that (`RunAIUsage`).

The tool is the model task the call was opened for: `plan`, `turn`, `check_in`, `reply_labelling`, `classify_domain`, `ai_visibility`, `ai_visibility_sampling`, `social_replies`, `social_posts`, `email_verification`, `semantic_search` (the embeddings behind [meaning-based search](workspace-search.md#meaning-based-search), on by default and turned off under Settings) and `fix_tickets`. An extension that calls `Host.AI().Chat` names its own tool with `Host.AddAITool`: an id such as `lead_finder`, the name and description the usage page shows, and the model task whose model its calls use (such as `classify_domain`), so its calls are listed and limited on their own without a model setting of their own. Anything that isn't a lowercase id is logged as `other`.

Every call is booked in one place, `recordAISpend` in `engine/internal/api/ai_settings.go`, which writes the log row, the spend ledger entry (`spend_entries`, which the monthly limit counts) and the metrics, once per `ref` so a retried job is not counted twice. New AI features book through it by opening their model with a task name (`aiModel`, `AIModels().ForTask`, `AIVisibilityModels()` or `Host.AI()`), never by calling `llm.New` and spending on their own.

`GET /v1/workspaces/{ws}/ai-usage?month=YYYY-MM` returns a month by tool, by model and by day, with the 50 latest calls. In the app it is Settings → AI usage.

## Limits

- **The workspace's monthly AI limit** (`monthly_limit_cents` in `PUT /ai-settings`, US$50 by default) covers every AI call.
- **A limit per tool** (`PUT /ai-usage/limits`, stored in `workspace_ai_settings.task_limits`) caps one tool on top of that. 0 stops the tool.

Before a call, `checkAIBudget` refuses it with `409 ai_budget_reached` when the month's spend plus the call's estimate would pass either limit. After a call, `warnAIBudget` checks whether the month reached 80% or all of a limit; the first time each month it records `ai_budget.warned` or `ai_budget.reached` (audit actions, product events and webhooks) and queues an `ai.budget_alert` email to each owner and admin with a confirmed address (`ai_budget_alerts` keeps it to once a month per limit and level). The email is Outflare's own account email (`sysmail`) and says the provider bills the workspace, not Outflare.

Every limit stops calls by default. Either can be set to warn only (`hard_stop` and `hard_stops` in `PUT /ai-usage/limits`, stored in `workspace_ai_settings.limit_warn_only` and `warn_only_tasks`, migration 0226): `checkAIBudget` then lets calls past it, while `warnAIBudget` still records both events and sends both emails, which say the calls carry on. This matches the paid connection limits ([`docs/usage-limits.md`](usage-limits.md)).

## Telling people whose bill it is

Connecting an AI model (the Connectors dialog, Get started and connect-request links) asks the person to confirm that the provider bills their account for every call, that managing that spend is their responsibility, and that they should set a hard limit at the provider. The AI model settings and the AI usage page repeat it. The limits in Outflare are a second line, not a guarantee: a provider's own hard limit is the one that can't be passed.

## Metrics

`outflare_ai_calls_total`, `outflare_ai_tokens_total`, `outflare_ai_cost_cents_total` (by `provider` and `task`) and `outflare_ai_budget_alerts_total` (by `level` and `scope`), on the AI row of the dashboard. Alerts fire when one tool makes over 5 calls a second for 15 minutes, a likely loop spending workspaces' money, and when over 20 limits are reached in an hour ([`docs/monitoring.md`](monitoring.md)).
