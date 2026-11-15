# Scores

A score is one number from 0 to 100 that Outflare keeps on every company, person or deal and keeps up to date: a health score for customers, a lead score for people who signed up, churn risk, the chance a deal is won. You decide what goes into it, so it means what your business needs it to mean, and every record can say why it scores what it does, where the score has been, and what kind of number it is.

Scores are part of the [customer data platform](customers.md) and belong to one brand. Everything here works the same from the REST API (`/v1/brands/{brand}/scores`), the MCP server and Query. In the app, CRM → Customer data → Scores adds, changes and archives scores (people with `catalog.manage`; everyone else can read them), and a company's, person's or deal's page shows their scores with a "Why?" that lists the factors, a small line for the last 90 days and how far the score moved in 30.

## How a score is worked out

Everyone starts at the same number, the **base**. Each **factor** adds points, or takes them, when it is true for the record. The result is kept between 0 and 100.

A factor has:

- a **label** in plain words, shown on the record: "Usage fell by a third";
- one to five **conditions** on the record's fields, the same tests [rules](rules.md) use: `metrics.usage_7d_change` `lt` `-29`, `renews_on` `lt` `+60d`, `plan` `eq` `enterprise`. With several, `match` says whether `all` must hold or `any`;
- **points** from -100 to 100.

A score has up to 30 factors, and a brand keeps up to 20 scores.

```json
{"key": "health", "label": "Health score", "record_type": "account", "base": 60,
 "high_from": 70, "low_below": 40,
 "factors": [
   {"label": "Used the product in the last 7 days", "points": 20,
    "conditions": [{"attribute": "metrics.usage_7d", "op": "gt", "value": 0}]},
   {"label": "Usage fell by a third or more on the week before", "points": -20,
    "conditions": [{"attribute": "metrics.usage_7d_change", "op": "lt", "value": -29}]}
 ]}
```

Factors read any field on the record, including [metrics](metrics.md) and their trends, dates compared with today (`today`, `+60d`, `-14d`), fields your own systems send, [sentiment](#sentiment), and the record's other scores (`scores.health_band` `eq` `low`), as long as the score read doesn't itself read scores.

A score can **need** a field (`needs`: `sentiment.value`). A record without it gets no value for that score at all, which is not the same as a low one: a customer nobody has talked to has no sentiment, not a bad one.

## What kind of number it is

Every score carries a `nature`, shown beside it everywhere, so a guess is never read as a fact:

| | |
| --- | --- |
| **Measured** (`measured`) | Worked out from what happened: usage, payments and the record's fields. |
| **AI-read** (`ai`) | Read from conversations by your own AI: a judgement, shown with the customer's words behind it. |
| **Predicted** (`predicted`) | An estimate of what will happen, such as churn risk or the chance a deal is won, built from the other two. |

You choose it, within limits: a score that reads sentiment, or is made of or reads a score that isn't measured, can't be called measured.

For a score such as churn risk, a high value is the bad news. Set `higher_is_better` to `false` and the colours swap (high is red); the number and the bands stay as they are.

## A headline score made of other scores

One number hides what is going on: a customer can use the product every day and still have a failed payment. So a score can have **parts** in place of factors: two to six of the brand's other scores for the same record type, each with a weight. Its value is their weighted average.

```json
{"key": "health", "label": "Health score", "record_type": "account",
 "parts": [{"key": "usage", "weight": 3}, {"key": "billing", "weight": 2}, {"key": "engagement", "weight": 2}]}
```

On the record the parts stay visible under the headline, each with its own "Why?", so one going down is never hidden by another going up. A part the record has no value for is left out of the average. A part can't itself have parts or read scores, and a score that others are made of or read can't be archived until they let go of it.

## Sentiment

When [conversation reading](signals.md) is on for a brand, the same read that looks for signals also says how the customer sounds: positive, mixed or negative, with their own words. A conversation that shows no feeling either way says nothing: most are like that, and unknown never counts as unhappy. It runs on the workspace's own AI connection and stays off without one.

The company (or the person, when they have no company) carries the roll-up under `sentiment`:

```json
{"sentiment": {"label": "negative", "value": 22, "trend": "down", "read_at": "2026-11-14T08:00:00Z", "conversations": 3}}
```

`value` runs from 0 to 100 over the last 90 days of conversations, each counted once as it last sounded and the newest counting most; `label` is positive from 65, negative up to 35 and mixed between; `trend` is up, down or flat against before the latest read. A change of label is in the record's history. Rules, lists, journeys and scores read `sentiment.label` like any field, and a score that reads it shows the latest quotes under "Why?".

## Scores on deals

A deal's score reads the deal's own fields (`stage`, `close_date`), a few facts Outflare works out for it, and its company's fields:

| | |
| --- | --- |
| `deal.days_open`, `deal.days_in_stage` | Days since it was opened, and since its stage last changed. |
| `deal.days_quiet` | Days since anything was logged on the deal or on who it is with. |
| `deal.days_to_close` | Days to its close date; negative once that has passed. |
| `deal.contacts` | How many people are on the deal. |
| `account.…` | Any field of the company the deal is with: `account.metrics.usage_7d`, `account.sentiment.label`, `account.scores.health_band`. |

A deal is scored again when one of its own fields changes and once a day, which is when its company's side is picked up.

The [revenue forecast](revenue.md) counts a deal at the score with the key `win` when the brand keeps one, in place of its stage's fixed weight: the stage is then one factor among the others.

## Checked against what happened

A prediction should earn its trust. `GET /v1/brands/{brand}/scores/{score}/track-record` (MCP `get_score_track_record`; under each predicted score in the app) says how it held up:

- a deal's score: of the deals closed in the last 365 days, how many in each band, as they stood the day before they closed, were won;
- a score on companies or people where high is bad news: of the records in each band 90 days ago, how many canceled their subscription or were marked churned since.

It fills in by itself as history builds up, and says when there are too few records (under 20) to read much into. Outflare never turns a score into a made-up probability: you see the points, and then how often they were right.

## History

Each record keeps its value for every day it changed, for two years. `GET /v1/brands/{brand}/records/{record}/scores` returns the last 90 days as `history` and how far the value moved in 30 days as `change`.

## Bands

Each score also has a band: **high** from `high_from` up (70 unless you change it), **low** under `low_below` (40), and **medium** between. Bands are what most rules and lists want: "health is low" reads better than a number, and stays right when you tune the points.

## Starters

`GET /v1/brands/{brand}/scores` returns `starters`, built on the standard metrics and fields:

| | | |
| --- | --- | --- |
| Health score | Companies | Measured. One plain score from usage and payments. |
| Usage trend, Billing, Fit | Companies | Measured. Sub-scores to put under a headline score of your own. Fit starts from what is known about the company: change it to the industries, sizes and countries you sell to. |
| Sentiment | Companies | AI-read. Only for companies with conversations read. |
| Churn risk | Companies | Predicted, high is bad news. |
| Expansion potential | Companies | Predicted. |
| Lead score | People | Measured. |
| Win likelihood | Deals | Predicted. Comes with a factor for each of your deal stages, sized from the forecast's stage weights. |

They are drafts. Add one as it is with `POST /v1/brands/{brand}/scores`, or change its factors first. In the app they are the cards under "Start with".

## Where the values are

Each record carries its scores in its attributes under `scores`:

```json
{"scores": {"health": 80, "health_band": "high"}}
```

So anything that reads a field reads a score:

- a [rule](rules.md): when `scores.health_band` comes to be `low`, emit `account.health_low`, which can raise a [signal](signals.md) or start a journey;
- a [smart list](lists.md#smart-lists): companies where `scores.health` is less than 40;
- [Query](query.md): `attributes->'scores'->>'health'`.

`GET /v1/brands/{brand}/records/{record}/scores` (MCP `get_record_scores`) gives a record's scores with every factor, its points and whether it holds now, or for a headline score its parts with their values.

## When a record is scored

- **When a field a factor reads changes**, including a metric moving, the record is scored again within moments. Rules and waiting journeys hear of the change.
- **Once a day** (UTC) every record is scored again, because a factor can compare a date with today and new records need a first score.
- **When you add or change a score**, every record of its type is scored straight away, in the background. This pass is quiet: no rule, journey, email or webhook fires for where records already stand, the same as when metrics are filled from stored events. From then on a rule fires when a record comes to match.

A move from one band to another is written to the record's history (`scores.<key>_band`, source `score`), so you can see when an account went from high to low. The number itself changes too often to keep every step.

Archiving a score takes it off every record, quietly, and frees its key.

## API

| | |
| --- | --- |
| `GET /v1/brands/{brand}/scores` | The scores, with how many records are in each band, and the starters. `customers.read` |
| `POST /v1/brands/{brand}/scores` | Add a score. `key` and `record_type` (`account`, `person` or `deal`) never change. `catalog.manage` |
| `PATCH /v1/brands/{brand}/scores/{score}` | Change it. A change to the base, factors or bands makes a new version and scores every record again. `catalog.manage` |
| `POST /v1/brands/{brand}/scores/{score}/archive` | Archive it. `catalog.manage` |
| `GET /v1/brands/{brand}/records/{record}/scores` | A record's scores, why, and where they have been. `customers.read` |
| `GET /v1/brands/{brand}/scores/{score}/track-record` | How a prediction held up. `customers.read` |

MCP: `list_scores`, `create_score`, `update_score`, `get_record_scores`, `get_score_track_record`.

The [Revenue](revenue.md) page uses the score with the key `health`: it adds up the recurring revenue of paying customers in its low band as revenue at risk.

## From a playbook

A [setup kit](playbooks.md) can bring a score with the metrics it reads. The playbook **Score account health and catch usage drops** asks for your usage event and adds a health score for companies, the two metrics behind it, and a rule and journey that flag a company whose usage falls. A score you already keep under the same key stays as it is.

The playbook **See customer health, churn risk and who is ready to grow** sets up the whole picture in one go: usage trend, billing and engagement under a health score, sentiment beside it, churn risk and expansion potential predicted from them, three metrics for meetings, support and website visits, and draft rules and journeys that put a company in Needs attention when its churn risk or its expansion potential turns high.
