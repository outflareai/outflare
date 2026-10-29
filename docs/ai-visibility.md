# AI visibility

People ask ChatGPT, Claude, Gemini and Perplexity which tool to buy. AI
visibility asks AI models the same questions on a schedule and counts how
often their answers name your brand, how that compares with your
competitors, and which pages they cite.

Every question goes to a model on **your own AI connection** (Claude,
OpenAI, Gemini, Mistral, xAI, DeepSeek, or a router such as OpenRouter, Vercel AI
Gateway or Requesty, connected on the Connectors page with
your own key), and the cost goes to your account with that provider.
Outflare never supplies a key: with no AI connection, AI visibility stays off
and says which connection to add.

In the web app it is **SEO → AI visibility**: the models and monthly limit,
each cohort with its numbers and a **Run now** button, the brand's facts,
and AI crawler visits.

## Cohorts and prompts

A **cohort** is a set of prompts about one brand: the questions a buyer
would type, such as "best proxy provider for web scraping" or "Acme vs
Northpeak". Each cohort has:

- the brand it measures (or the workspace's company when you have no brands);
- other names the brand goes by (aliases), so "Acme Cloud" counts as Acme;
- its competitors: the ones tracked on the SEO Competitors page for the
  brand's sites count by themselves (hosts of one company grouped under its
  name, with its other names), and the cohort can add others or name one
  differently (each with a name, domains and aliases). The cohort's own come
  first, and a run counts at most 25. `tracked_competitors` on the cohort
  lists the tracked ones it counts;
- a locale (`en-US`) that each question mentions, so answers suit that market;
- a schedule: `off` (run by hand), `daily` or `weekly`. A scheduled run that fails, or gets no answer from any model, is tried again after 15 minutes, then 1 and 2 hours, then every 4 hours, so the day (or week) still has answers; a run a person stopped, or one the AI budget stopped, waits for the next scheduled time.

Add prompts in bulk (up to 100 at a time) with an optional intent:
`discovery`, `comparison`, `problem` or `brand`.

```sh
curl -X POST "$OUTFLARE_URL/v1/workspaces/$WS/ai-cohorts" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"name": "Proxy buyers", "brand_id": "'$BRAND'", "schedule": "weekly",
       "competitors": [{"name": "Northpeak", "domains": ["northpeak.example"]}]}'

curl -X POST "$OUTFLARE_URL/v1/workspaces/$WS/ai-cohorts/$COHORT/prompts" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"prompts": [{"text": "best residential proxy provider", "intent": "discovery"}]}'
```

## Models

Under AI visibility settings (`PUT /v1/workspaces/{ws}/ai-visibility/settings`)
pick up to 8 models to ask. Each names one of your AI connections and a
model, and whether to let it search the web first (Claude and OpenRouter
models, and OpenAI's search models). Answers with web search cite real pages,
which is what AI search products show people. Asking the same model and
prompt more than once (`samples_per_prompt`, 1 to 5) evens out answers that
change from one try to the next.

**OpenRouter** is the easiest way to sample many models: one key reaches
OpenAI, Anthropic, Google, Perplexity, Mistral and others, and OpenRouter
reports the cost of every call, so you don't enter prices. For a model on a
direct connection Outflare uses Claude's list prices, or the price you enter.

## Runs

A run asks every chosen model every active prompt in a cohort. Start one with
`POST /v1/workspaces/{ws}/ai-cohorts/{cohort}/runs` (or MCP
`run_ai_prompts`), or let the schedule do it. One run per cohort goes at a
time; stop it with `POST .../ai-runs/{run}/stop`. Runs happen in the
background, a few answers at a time, and each answer is kept with its model,
text, tokens, cost and how long it took (`GET .../ai-samples`).

A run stops early, with the reason, when:

- the workspace's monthly AI limit is reached (`ai_budget_reached`), the
  same limit missions and reply labelling use;
- AI visibility's own monthly limit is reached (`sampling_limit_reached`,
  US$10 unless you change `monthly_limit_cents`).

An answer that fails (a refused key, a timeout, a model the connection can't
use) is kept as failed with its error code, and the run carries on.

## What is counted

For each answer Outflare finds which names it mentions (the brand, its
aliases, each competitor), in what order, and which web pages it cites:
the model's own citations first, then links in the text. A cited page is the
brand's, a competitor's or another site's (a review site, a forum) by its
domain.

`GET /v1/workspaces/{ws}/ai-visibility?days=28` answers, per cohort:

| Figure | Meaning |
| --- | --- |
| `mention_rate` | Answers naming the brand, out of all answers |
| `share_of_voice` | Answers naming the brand, out of every answer-mention of the brand and its competitors |
| `avg_position` | Where the brand comes among the names in an answer (1 is first) |
| `citation_rate` | Answers citing one of the brand's pages |
| `competitors` | The same figures for each competitor |
| `models` | Mention rate per model, with and without web search |
| `cited_hosts` | The 15 sites answers cite most, and whose they are |
| `weeks` | Mention rate and share of voice week by week |

Counts are per answer: an answer naming the brand three times counts once.

## Facts: is what AI says true?

List what is true about each brand as short plain sentences: "Plans start
at $49 a month", "There is no free trial", "Acme sells in 190 countries".
Each brand can have up to 50 facts, each with an optional topic (pricing,
product, company, availability, integrations, other).

```sh
curl -X POST "$OUTFLARE_URL/v1/workspaces/$WS/ai-facts" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"brand_id": "'$BRAND'", "topic": "pricing", "statement": "Plans start at $49 a month"}'
```

Once a run has its answers, every answer that names the brand is checked
against the brand's active facts by the workspace's model for the **AI
visibility** task (Settings → AI model; on Claude it defaults to the lighter
model), on the workspace's own key. For each fact the checker says whether
the answer agrees, contradicts it or doesn't touch it, and quotes the
answer's words. The checker is told the answer is data and never to follow
it. Checks count towards the same limits as sampling, and their cost is on
each answer (`facts_cost_cents`).

- `GET .../ai-facts` lists each fact with the answers that agreed and
  contradicted it over the last `days`, its accuracy and the latest wrong
  words.
- Each answer (`GET .../ai-samples/{sample}`) has `facts_status` and its
  `fact_checks`.
- The overview has, per cohort, `facts`: answers checked, answers with
  something wrong, accuracy and how many facts were contradicted.

Changing a fact's statement clears what answers said about the old one.
A check that fails (no model for the task, a bad reply) marks only that
answer, and the run carries on.

## Fixes

A prompt whose answers in the latest run never name the brand goes on the
workspace's shared [fixes list](crawler.md#the-fixes-list) with the source
`ai_visibility` and the rule `ai_brand_absent`, explaining what to do (pages
that answer the question, being listed where the cited sites compare tools).
Mark it done and the next run checks again: it is confirmed when an answer
names the brand and reopened when none does.

A fact that any of the latest run's answers contradict is a fix too (rule
`ai_fact_wrong`, an error), with how many answers got it wrong. It is
confirmed when a later run's answers agree with the fact and none
contradict it.

`GET .../fixes/{fix}/prompt` (MCP `get_fix_prompt`) turns the fix into a
prompt for a writer or an agent: the question, who the answers named
instead (or, for a fact, the wrong words), the pages they cited, and the
URL that starts the next run.

## Can AI read the site?

AI answers can only cite pages AI crawlers may read. Each site audit
checks what robots.txt lets every AI search, user and training crawler
read, and whether the site has an llms.txt; pages hidden from AI search go
on the fixes list. See [the crawler's AI checks](crawler.md#ai-crawlers).

### Are AI crawlers visiting?

With a Cloudflare Web Analytics connection, the analytics sync also reads
the workspace's own Cloudflare request logs once a day and counts requests
by each AI crawler, per host and day (`GET /v1/workspaces/{ws}/ai-bot-hits`,
MCP `get_ai_bot_hits`). Search and user requests mean AI answers are
reading the site; training requests only collect pages for training. The
first read covers the last three days, then each day adds the newest
complete days.

This needs an API token with **Zone > Zone > Read** and **Zone > Analytics >
Read** on the zones that serve the site, besides the Web Analytics
permission; a Cloudflare sign-in doesn't include them yet. Without them the
traffic reports keep working, and `connections` in the answer says which
permission is missing. How many days Cloudflare keeps depends on the zone's
plan, and zones beyond the first 20 are not read.

## Cost

Each answer costs what the provider charges for its tokens, plus web search
where used (about one cent per search on Claude; OpenRouter includes it in
the cost it reports). A cohort of 20 prompts on 4 models, asked once a week,
is 80 answers a week: typically well under a dollar on small models, more on
the largest ones. Every answer's cost is recorded in the workspace's AI
spend, next to missions and reply labelling.
