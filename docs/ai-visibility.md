# AI visibility

People ask ChatGPT, Claude, Gemini and Perplexity which tool to buy. AI
visibility asks AI models the same questions on a schedule and counts how
often their answers name your brand, how that compares with your
competitors, and which pages they cite.

Questions go two ways, both on **your own accounts**:

- **As people see it.** ChatGPT and Gemini on their own screens, as a
  signed-out person in the cohort's country sees them, read through your own
  **DataForSEO** account (its LLM Scraper, about US$0.004 an answer). These
  answers come with the web searches the assistant ran, every page it read,
  the brands it named and a link to ask the same question again.
- **Through the models' APIs**, on your own AI connection (Claude, OpenAI,
  Gemini, Mistral, xAI, DeepSeek, or a router such as OpenRouter, Vercel AI
  Gateway or Requesty). An API answer often cites other pages than the
  assistant's screen does for the same question, so it measures the model
  rather than what your buyers see.

Outflare never supplies a key: with neither connection, AI visibility stays
off and says which connection to add, and each answer's cost goes to your
account with that provider.

In the web app **AI visibility** is its own menu item, one page per
question:

- **Overview.** The score with its change, your rank, share of voice,
  recommended rate and position; the trend; who wins AI answers; what to do
  next (open fixes, pages to get onto, wrong facts, brands to count), and
  where you are strong and weak by how much the asker knows and what they
  ask. Until the brand's first run, this page is a guided setup: confirm the
  brand, tick competitors, keep the drafted questions, pick the assistants,
  see the cost per run on your own accounts, and start.
- **Questions.** Every tracked question by topic, awareness and question
  type. Open one to read each assistant's latest answer side by side, with
  the brands it named, its sources and a link to ask it yourself.
- **Competitors.** The full leaderboard, against every competitor on the
  Competitors page. Brands the answers name next to you are matched to
  their websites by themselves and go to the Competitors page as
  suggestions to say Yes or No to. Each name is sorted once and
  remembered, so it isn't asked about again:
  - a listed competitor's domain written as words ("Bright Data" for
    brightdata.com) is that competitor, and gets the name;
  - a product of a listed competitor ("Bright Data Web Unlocker",
    "Zyte API") counts as that competitor, and is added to its names;
  - for the rest, after each run and once a day, the workspace's model for
    the `ai_visibility` task, on its own key, sorts each name, using the
    sites the answers cited where it was named. A competitor it is certain
    of is a plain suggestion; one it only thinks is right asks "Is this
    their website?", with a Wrong website button. A product of a company
    that isn't listed suggests the company. A name that isn't a competitor
    (an open-source library, a framework, a marketplace) goes under
    "Named, not competitors" with what it is, and an "It's a competitor"
    button. A name that is a kind of product rather than a brand ("web
    scraping API") leaves the list. Names it doesn't know show its best
    guess at their website to add, and are checked again each week.

  A competitor added in the setup goes on the Competitors page too.
- **Sources.** The pages to get onto (sent to Presence), the sites answers
  cite and the kinds of source. The table shows each page's address first
  and its site next; click a row for the page in full (see "One page in
  full" below). The images and files an assistant shows from its own
  servers (ChatGPT's images.openai.com) aren't pages, and are left out
  everywhere.
- **How AI describes you.** Recommended, listed or warned against, for you
  and each competitor; your facts and the answers that get them wrong.
- **Traffic.** Visits, conversions and revenue from AI answers, and AI
  crawler visits.
- **Settings.** The assistants (each on your own account), how often to ask,
  and the monthly limit.

The API calls a brand's question set a cohort; the pages don't.

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
  first, then every tracked one: a run counts the brand's whole competitor
  list, however long. `tracked_competitors` on the cohort lists the tracked
  ones it counts;
- a locale (`en-US`) that each question mentions, so answers suit that market;
- a schedule: `off` (run by hand), `daily` or `weekly`. A scheduled run that fails, or gets no answer from any model, is tried again after 15 minutes, then 1 and 2 hours, then every 4 hours, so the day (or week) still has answers; a run a person stopped, or one the AI budget stopped, waits for the next scheduled time.

Add prompts in bulk (up to 100 at a time). Each can say where it sits in
the cohort's **question library**:

| Field | Values |
| --- | --- |
| `awareness` | How much the person asking knows: `problem` (their problem, not that a kind of product solves it), `solution` (the kind of product, not the brands), `competitor` (a competitor), `brand` (your brand) |
| `question_type` | What they ask for: `how_to` (how do I do this task), `fix` (how do I fix this issue), `best` (who's best), `alternatives` (alternatives to a competitor), `versus` (one against another), `supplier` (the best supplier for a need), `about` (about your brand) |
| `persona`, `topic` | Who asks it and what it's about, as free text for grouping |
| `competitor` | The competitor an alternatives or versus question names |
| `demand` | About how many people ask it a month, if you know |

The older `intent` (`discovery`, `comparison`, `problem` or `brand`) still
works and is filled in from the awareness and question type when you leave
it out. On the AI visibility page, **Questions** on a cohort shows how many
active questions sit in each awareness level and question type, so the gaps
show.

### Drafting questions

`POST .../ai-cohorts/{cohort}/prompts/draft` (MCP `draft_ai_prompts`, or
**Draft questions with AI** on the page) has the workspace's own AI model
(the AI visibility task under Settings, AI model) write up to 60 questions
across every awareness level and question type. It works from the brand's
context and knowledge (what its website says it offers: products, services,
features, integrations, capabilities, who it serves and use cases, read from
the site through the workspace's own Geonode connection when the brand is
created, see [agent-memory.md](agent-memory.md)), the cohort's competitors,
any personas and topics you give, and the brand's own Search Console queries
of four words or more from the last 90 days. A question taken from a search query carries its demand (90-day
impressions divided by 3, `demand_source: search_console`).

With a DataForSEO connection, drafting also reads, on that connection and
within its monthly budget:

- **Real questions people asked AI assistants** whose answers mention the
  brand's or its competitors' websites, or the brand's name (DataForSEO's
  LLM Mentions), most asked first, with the brands each answer named. These
  cover ChatGPT in the US in English and Google's AI Overviews; outside the
  US in English only AI Overviews. A question taken from one carries how
  many times a month people ask it (`demand_source: ai_search`).
- **AI search volume** for every other drafted question (DataForSEO's AI
  keyword data). A question with a volume above 0 carries it as its demand,
  also `ai_search`; one with none keeps the Search Console demand or none.

Both cost a few cents a draft and are spent on the brand's AI answers
channel. Without DataForSEO, drafting uses Search Console alone.

Drafted questions arrive with `status: suggested`, `source: drafted` and a
line on `why` a buyer asks it. Runs skip them until a person keeps one
(`status: active`); delete the ones that don't fit. With no AI model
connected, drafting says which connection to add.

```sh
curl -X POST "$OUTFLARE_URL/v1/brands/$BRAND/ai-cohorts" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"name": "Proxy buyers", "brand_id": "'$BRAND'", "schedule": "weekly",
       "competitors": [{"name": "Northpeak", "domains": ["northpeak.example"]}]}'

curl -X POST "$OUTFLARE_URL/v1/brands/$BRAND/ai-cohorts/$COHORT/prompts" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"prompts": [{"text": "best residential proxy provider", "intent": "discovery"}]}'
```

## Models

Under AI visibility settings (`PUT /v1/brands/{brand}/ai-visibility/settings`)
pick up to 8 models to ask. Each brand has its own settings, monthly limit
and spending; the workspace route is deprecated and acts on `?brand=` or
the first brand.

A **screen** (`surface: screen`) names your DataForSEO connection and the
assistant, `chatgpt` or `gemini`; the settings list the ones you can add
under `screens`. It is asked as a signed-out person in the cohort's locale
(`en-US`: the United States, in English), with `web_search` making the
assistant search the web first. It is not personalised by anyone's history,
so it can differ from what you see in your own signed-in ChatGPT. A screen answer takes about a minute, so a run asks up to
five at once.

An **API model** names one of your AI connections and a
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
`POST /v1/brands/{brand}/ai-cohorts/{cohort}/runs` (or MCP
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

`GET /v1/brands/{brand}/ai-visibility?days=28` answers, per cohort:

| Figure | Meaning |
| --- | --- |
| `mention_rate` | Answers naming the brand, out of all answers |
| `share_of_voice` | Answers naming the brand, out of every answer-mention of the brand and its competitors |
| `avg_position` | Where the brand comes among the names in an answer (1 is first) |
| `citation_rate` | Answers citing one of the brand's pages |
| `recommended`, `listed`, `warned` | Answers that recommend the brand, only list it among others, or warn against it |
| `recommend_rate` | Answers recommending the brand, out of all answers |
| `competitors` | The same figures for each competitor |
| `suggested_competitors` | Brands the answers recommend or list that the cohort doesn't count yet, most named first |
| `source_types` | What kind of sites the answers cite (review, comparison, forum, news, video, social, directory, docs, blog, the brand's or a competitor's own) |
| `models` | Mention rate per model, with and without web search |
| `cited_hosts` | The 15 sites answers cite most, whose they are and what kind of site |
| `weeks` | Mention rate and share of voice week by week |

Counts are per answer: an answer naming the brand three times counts once.

### Question by question

`GET /v1/brands/{brand}/ai-visibility/questions?days=28` answers the
question people come back for: am I shown for this question, yes or no, and
is it moving? For each tracked question it gives the answers that named,
recommended, listed or warned against the brand, its average position, a
score (the mean of recommended 1, named or listed 0.6, warned or absent 0,
0 to 100), the same `by_assistant` and `weeks` (week by week over this
period and the one before, oldest first), the competitors those answers
named (most recommended first), `other_brands` they named that aren't
tracked, and `previous`, the same figures for the period before, with
`change`, the score now minus the score before. Suggested questions (not
kept yet) are left out; `cohort` narrows it to one question set.

On the Questions page each row shows whether the answers name you, how
often they recommend you, the change and the last weeks as bars; sort by
"Not naming you first" to see where to start, and by biggest drops or gains
to see traction. A question's page shows the weeks in full, each assistant,
and who is named instead. The Overview's **What changed** lists the
questions gained and lost by 20 points or more.

### Placements, most valuable first

`GET /v1/brands/{brand}/ai-visibility/placements?days=28` ranks the pages
(nobody's own) that assistants cite, or read before answering, by how much
being on each is worth: over the answers citing a page, the demand weight
of the question answered (as the score weights it), in full when the answer
doesn't name the brand and a quarter when it does, plus a share for answers
that read the page without citing it. `value` is relative (the top page is
100). Each page says how many answers cited it, how many of those named the
brand, which competitors they named and how many questions it answers;
`own_pages` are the brand's own pages the answers cite. The Sources page
shows the same table; the pages to get onto are also Presence opportunities
(below).

### One page in full

`GET /v1/brands/{brand}/ai-visibility/page?page=` is one page the answers
cited or read in the last 90 days: when they first and last did, the
questions they answered and the competitors they named; the site's domain
rating, page rating and traffic (from Presence's reads on the workspace's
own SEO connection, or null); the links to the page, from its latest link
read (counting them is a paid read on the workspace's own SEO connection,
estimated and confirmed first, `POST /v1/workspaces/{ws}/link-reads`); and
who is on the page. OutflareBot reads the page (obeying robots.txt and
opt-outs) and says, for the brand and each tracked competitor, whether it
links to their website (with nofollow and the link text), names them
without a link, or doesn't have them. After each run it reads the 25 pages
the answers cite most that it hasn't read in 14 days;
`POST /v1/brands/{brand}/ai-visibility/page/check` reads one now. This
shows where competitors get named, and how, so you can do the same.

### Downloading the tables

Both tables come as CSV with `format=csv`
(`GET /v1/brands/{brand}/ai-visibility/questions?days=28&format=csv`,
`GET /v1/brands/{brand}/ai-visibility/placements?days=28&format=csv`), one
row per question or page with the same figures, for a spreadsheet or a
report. The Questions and Sources pages have a **Download CSV** button.

### Sharing the page

`POST /v1/brands/{brand}/ai-visibility/share` makes the brand's share link
(`{password, expires_in_days}`, both optional; a password is 8 to 200
characters), replacing one the brand had; `GET` says whether a link exists,
how often it was opened and when, never the token; `DELETE` takes it back,
and it stops working at once. The link, `/share/ai/<token>`, shows anyone
who has it the brand's AI visibility score, rank and share of voice, the
trend against competitors and each question's results for the last 28
days, read-only, and nothing else of the brand (no answers, pages or
people). A link with a password asks for it first. The link is made and
taken back from **AI visibility → Settings → Share by link**.

### In the digest

The brand's [digest](digests.md) has an AI visibility section: the score,
the share of answers naming and recommending the brand and the answers read
in the period, each against the period before; the questions the answers
started and stopped naming the brand for; and objections the answers raised
for the first time.

### The score

Each cohort's brand and competitors get a **score** from 0 to 100: how well
the answers treat them across the cohort's questions, weighted by how many
people ask each one. Per question, an answer that recommends the brand scores
1, one that names or lists it 0.6, and one that warns against it or leaves it
out 0; a question's score is the mean of its answers. Questions are weighted
by their `demand`; those without one count as the cohort's median, and with
no demand anywhere every question counts the same.

`score_low` and `score_high` are its 90% range: where the score would land
nine times in ten if the same questions were asked again. AI answers change
from one try to the next, so a small move means little. A week whose range
doesn't overlap the week before's is marked `real_change`. `grid` gives the
brand's score and mention rate for each awareness level and question type,
so the weak spots show (for example, named when people ask about the brand,
but never when they only know their problem).

### Markets

A brand keeps one question set (cohort) per market: the set's `locale`
(en-US, de-DE) is where the asker is, told to the model with each question
and used to draft questions in that language. The Settings page lists the
brand's sets by market and adds one for a new market with the same
competitors and aliases, then opens the setup to draft its questions;
every page switches markets at the top. The trend compares markets with
`by=market` (below).

### Compare by

`GET /v1/brands/{brand}/ai-visibility/series?by=` is the trend split one way,
week by week (Monday to Sunday, UTC; every line has the same weeks): by
`competitor` (the brand first, then each competitor the answers named, scored
on the same questions), by `assistant` (the brand's score from each
assistant's answers), or by the questions' `awareness`, `question_type`,
`topic` or `persona` (the brand's score on the questions with each value), or
by `market` (the brand's score in each question set's locale).
Each line has its score with its range for the whole period and per week,
how often the brand (or the competitor) was named, and how many answers and
questions it covers; at most 12 lines, the most answered first. The
Overview's "Compare by" switch draws them, so you can see whether a competitor
is pulling ahead, which assistant stopped naming you, or whether problem-aware
questions are the gap.

### Recommended, listed or warned against

Being named is not the same as being picked. Once a run has its answers, the
workspace's own model for the AI visibility task reads each one and says,
for the brand and each competitor it names, whether the answer
**recommends** it (a top or clear pick), only **lists** it as one option, or
**warns** against it, with the answer's own words (`stance` and
`stance_quote` on each mention in `GET .../ai-samples`). It also notes the
other brands the answer recommends or lists (`other_brands`; screen answers
add the brands the screen recognised), and what kind of site each cited page
is on (`source_type`), each with its own website when the answer shows or
links it. A brand with a known website goes straight onto the brand's one
competitor list (`seo-competitors`, the Competitors module) as a suggestion
from `ai_visibility`, to track or dismiss there with one click; the
overview's `suggested_competitors` carry its `host` and `competitor_id`. On
the AI visibility pages, **Also named** lists those brands, and **Yes**
counts one from then on.

Each read is one small call on the workspace's own AI key, counted in the
sampling limit. With no AI model chosen for the AI visibility task, answers
are counted as before and the stances stay unread (`stance_status:
skipped`).

A screen answer says more (`GET .../ai-samples`):

| Field | Meaning |
| --- | --- |
| `searches` | The web searches the assistant ran before it answered |
| `pages_read` | Every page those searches found, in order, whose it is, and whether the answer `cited` it |
| `brands_named` | Every brand the answer named, tracked or not: a quick way to spot competitors you don't track yet |
| `answer_model` | The model the screen said it answered with |
| `check_url` | A link that asks the assistant the same question, to look by hand |

A page the assistant read but didn't cite is close to being a source; a page
it never found means the question's searches don't reach you yet.

### What the answers say about you

With each stance, the reader notes what the answer says about every brand
it names: its **attributes** (what it is, has or is known for, as short
phrases in the answer's terms, each positive, negative or neutral) and its
**objections** (the reasons the answer gives against it, or for picking
another instead, with the answer's words). They are kept in `ai_claims`
and read with `GET /v1/brands/{brand}/ai-visibility/claims`, grouped by
phrase: how many answers said it over the period and the one before, out
of the answers naming the brand (`share`), the questions it came up on, the
assistants that said it, the quote, and the pages those answers cited,
which is where to correct it. `competitors` has the same for each
competitor the answers named. The page "How AI describes you" shows what
you are known for, what is held against you and how the competitors are
described.

An objection (or attribute) that isn't true is disputed with
`POST .../ai-visibility/claims/dispute` (`key`, `kind`, a `note` saying
what is true): it goes on the fixes list as `ai_wrong_claim`, on the latest
answer that made it, with the pages to correct, and the next runs show
whether the answers stop saying it.

### Pages worth being on

After every finished run of a brand's cohort, the pages its answers cite,
over the last 90 days, go into the brand's Presence opportunities (reason
`ai_cited`, source `ai_visibility`). The opportunity's factors count the
answers that cited the page (`ai_citations`) and the competitors those
answers named (`competitors`), which drive its score. A page whose citing
answers all named the brand already is left out. Pages the assistants read
in at least two answers but never cited are added too, with no citations to
their score: being on them is how a page becomes a source. A dismissed
opportunity stays dismissed.

### The benchmark: answers beyond your questions

Your own questions are a sample. Once a day, after a run of a brand's
cohort, your DataForSEO connection counts how many of the AI answers it keeps
(questions people really asked ChatGPT in the US in English, and Google's AI
Overviews everywhere) mention the brand and up to nine competitors, by their
domains and names. Outside the US, or in another language, only AI
Overviews count. The overview shows each one's `benchmark_mentions`, its
`benchmark_share` of all the tracked brands' mentions, and the mentions
gained or lost since the count before (`benchmark_change`), with the day of
the latest count (`benchmark_day`). A competitor the benchmark counts but no
answer to your questions named still shows, with no score. Without a
DataForSEO connection the benchmark is off. A count costs about US$0.10 plus
US$0.02 a brand, on the brand's AI answers spend.

### What AI answers bring

For a brand's cohort, the overview's `traffic` shows what AI answers brought
the brand's website over the same period: visits from AI assistants
(ChatGPT, Perplexity, Gemini, Claude, Copilot and others, the `ai_answers`
channel of [website tracking](attribution.md)), the conversions and revenue
attribution credits to those visits under the workspace's default model,
each assistant's visits and the pages they landed on, and the spend
recorded on the brand's AI answers channel (`spend_cents`). It needs
website tracking on the brand's site; visits an assistant sends without a
referrer or `utm_medium=ai` can't be told apart from direct ones.

## Facts: is what AI says true?

List what is true about each brand as short plain sentences: "Plans start
at $49 a month", "There is no free trial", "Acme sells in 190 countries".
Each brand can have up to 50 facts, each with an optional topic (pricing,
product, company, availability, integrations, other).

```sh
curl -X POST "$OUTFLARE_URL/v1/brands/$BRAND/ai-facts" \
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

A claim the answers make that a person disputes is a fix with the rule
`ai_wrong_claim` (see "What the answers say about you").

A prompt whose answers name the brand but recommend only a competitor
(read from their [stances](#recommended-listed-or-warned-against)) is a fix
too, with the rule `ai_competitor_preferred` and how many answers recommended
a competitor: the questions where you are in the running but losing. It is
confirmed when a later run's answers recommend the brand, or recommend no
competitor. Answers whose stances weren't read count for neither.

A fact that any of the latest run's answers contradict is a fix too (rule
`ai_fact_wrong`, an error), with how many answers got it wrong. It is
confirmed when a later run's answers agree with the fact and none
contradict it.

A page of the brand's own that an [AI check](#your-pages) scores under 80
with changes to make is a fix on its site (rule `ai_page_unclear`, one per
page), with the changes as its count. Its prompt lists what the check said
to change, and the fix ticket's brief is written from it. Checking the page
again confirms it when the score reaches 80; a competitor's page never gets
a fix.

`GET .../fixes/{fix}/prompt` (MCP `get_fix_prompt`) turns the fix into a
prompt for a writer or an agent: the question, who the answers named
instead (or, for a fact, the wrong words), the pages they cited, and the
URL that starts the next run.

## Can AI read the site?

AI answers can only cite pages AI crawlers may read. Each site audit
checks what robots.txt lets every AI search, user and training crawler
read, and whether the site has an llms.txt; pages hidden from AI search go
on the fixes list. See [the crawler's AI checks](crawler.md#ai-crawlers).

### Your pages

The **Your pages** tab (`GET /v1/brands/{brand}/ai-visibility/pages`, MCP
`get_ai_visibility_pages`) shows whether AI answers can read, quote and
trust the brand's own pages. It joins the latest site audit of each of the
brand's sites with the answers of the period, so nothing is fetched and no
AI is called:

- **Your sites**: whether each has an llms.txt, which AI search and user
  crawlers robots.txt keeps out, and what its robots.txt says about AI use
  (`Content-Signal`, `Content-Usage`).
- **What to fix**: how many pages break each AI rule of the audit (see
  [the crawler's AI checks](crawler.md#ai-crawlers)), with why it matters
  and how to fix it. They are on the fixes list too.
- **Pages**: the pages that answers cite or that break a rule, cited pages
  first, with the questions that have no direct answer and what the
  structured data claims that the page doesn't show.
- **Cited pages the audit didn't read**, so the next audit can reach them.
- **Competitors' pages the answers cite** for the brand's questions (up to 20), with how many of those answers named the brand too. Check them with AI as well (below) to compare their score with yours.

**Check with AI.** The rules find what code can see; reading finds the
rest. `POST /v1/brands/{brand}/ai-visibility/pages/review` with up to 10
`urls` on the brand's sites or tracked competitors' sites (MCP `review_ai_pages`, or **Check with AI** on
a page) has the workspace's own AI model, the **AI visibility** task under
Settings → AI model, read each page after OutflareBot fetches it (obeying
robots.txt). It says whether the page answers up front, which questions it
answers, which claims are vague or unsupported, and what to change, with a
score from 0 to 100. It runs as a job and the result shows in `reviews`;
checking again replaces it. Without an AI connection it answers
`409 connector_needed`: Outflare never uses a key of its own. Asking is
`ai_page.review_requested`, a finished check `ai_page.reviewed`, a failed
one the product event `ai_page.review_failed`.

### Are AI crawlers visiting?

With a Cloudflare Web Analytics connection, the analytics sync also reads
the workspace's own Cloudflare request logs once a day and counts requests
by each AI crawler, per host and day (`GET /v1/workspaces/{ws}/ai-bot-hits`,
MCP `get_ai_bot_hits`). Search and user requests mean AI answers are
reading the site; training requests only collect pages for training.
Googlebot and Bingbot are not counted here, though they feed AI Overviews
and Copilot, because they are search engines' crawlers. The
first read covers the last three days, then each day adds the newest
complete days.

This needs an API token with **Zone > Zone > Read** and **Zone > Analytics >
Read** on the zones that serve the site, besides the Web Analytics
permission; a Cloudflare sign-in doesn't include them yet. Without them the
traffic reports keep working, and `connections` in the answer says which
permission is missing. How many days Cloudflare keeps depends on the zone's
plan, and zones beyond the first 20 are not read.

## Cost

A screen answer costs what DataForSEO charges, about US$0.004, paid from your
DataForSEO connection and counted against its monthly budget like any other
DataForSEO call (Connectors, the connection's usage limits), and in AI
visibility's own monthly limit. It is kept in millionths of a dollar
(`cost_micros`). One answer takes about a minute, so a large cohort's run
takes a while.

An API answer costs what the provider charges for its tokens, plus web search
where used (about one cent per search on Claude; OpenRouter includes it in
the cost it reports). A cohort of 20 prompts on 4 models, asked once a week,
is 80 answers a week: typically well under a dollar on small models, more on
the largest ones. Every answer's cost is recorded in the workspace's AI
spend, next to missions and reply labelling.
