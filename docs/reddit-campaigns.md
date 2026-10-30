# Reddit campaigns

Place comments, replies, posts, thread groups and upvotes on Reddit through [Engain](https://engain.io), which posts from its own pool of aged accounts, and see which tactics leave brand mentions and links that stay live. People and agents work the same way, over the API or MCP, and every action spends only within the limits a person set for its brand.

## Set up

1. Connect **Engain** under Connectors with your own Engain API key (Engain's Max plan has API access). Connecting reads your projects and credits; nothing is posted.
2. For each brand, set its limits with `PUT /v1/workspaces/{ws}/reddit/settings/{brand}`: credits a month and a day, actions per subreddit a day (default 2), upvotes per item (default 50), whether a person approves each action, and the Engain project when the account has more than one. Only a signed-in person can change limits or approval; an agent never raises its own budget.
3. Optional: connect **Geonode**. Reddit turns many servers away, so checks read Reddit through Geonode's network when it is connected.

## Actions

`POST /v1/workspaces/{ws}/reddit/actions` with a `kind`:

| Kind | `target_url` | About (Engain credits) |
| --- | --- | --- |
| `comment` | the thread | 5 |
| `reply` | the comment's permalink (ours too) | 5 |
| `post` | `https://www.reddit.com/r/<name>`, with `title` | 10 |
| `thread` | any of those, with `children`: replies from up to 10 other pool accounts, each with `delay_minutes` | 10, plus 5 a reply |
| `upvotes` | a thread or comment, or `parent_action_id` for one of ours once live | 0.1 an upvote |

A comment, reply or post can carry an upvote plan (`upvotes_goal`, `upvotes_per_day`, `upvotes_start`). Each action records what it tests (`experiment_id`, `strategy`, `link_type`, `brand_mentioned`, `competitors`, `tone`, `account_tier`, and the `preflight` verdict), so the report can compare tactics. Links with tracking parameters (`utm_`) are refused, so posts read as organic; mentioning the brand with no link is a mention too.

Unless the brand needs approval or `submit` is false, the brand's limits are checked and the action is sent to Engain at once, in the background, exactly once. Over a limit it is kept as a draft with `reddit_budget_reached` or `reddit_limit_reached`; approve it later (`.../approve`) or withdraw it (`.../cancel`).

`POST /v1/workspaces/{ws}/reddit/preflight` runs Engain's free rule check on up to 25 drafts; `GET .../reddit/subreddits/{name}` shows Engain's removal rates there beside the brand's own; `GET .../reddit/threads` lists threads Engain found (`type` new or seo).

## Tracking

After sending, each action is checked on Engain until it is published, then on Reddit as a logged-out visitor sees it: every 10 minutes for the first two hours, hourly for a day, every 6 hours for a week, daily for a month, then weekly until 6 months. Removed and deleted items are looked at daily for a week, since moderators sometimes restore them. `POST .../actions/{id}/check` checks one now.

| Status | Means |
| --- | --- |
| `draft`, `approved`, `sending` | Not with Engain yet |
| `submitted` | Engain took it; not on Reddit yet |
| `live`, `frozen` | A logged-out visitor sees it (frozen: the thread is locked or archived) |
| `removed` | Taken down by moderators, AutoModerator, Reddit's filters or admins (`removal_category` says which) |
| `gone` | Deleted, or the account is |
| `failed` | Engain refused it, didn't answer, or never published it within 14 days |
| `done` | An upvote order finished |

`GET .../actions/{id}` shows the timeline (`events`), every check (`snapshots`) and Engain's last answer.

## Results

`GET /v1/workspaces/{ws}/reddit/report?brand=` answers brand mentions and links placed and live now, credits and cost (at the brand's `credit_price_cents`), credits per mention, median and mean days live, and the share that survived 1, 7 and 30 days. `group_by` (`subreddit`, `strategy`, `kind`, `link_type`, `account_tier`, `experiment`, `tone`, `author`) compares tactics; `experiment_id`, `subreddit` and `from` narrow it. `GET .../reddit/budget` shows what the brand has spent and has left.

Experiments (`POST .../reddit/experiments`) hold one hypothesis each; close one with its conclusion (`PATCH` with `status: closed`), so the next person or agent starts from what was learned.

## Agents

Over MCP, an agent reads `get_reddit_guide` (also the resource `outflare://reddit-guide`): what Engain can do, costs, rules, how to experiment and what to report back. The prompt `run_reddit_campaign` starts one on a brand with a budget. See [mcp/README.md](../mcp/README.md).

## Scopes

`social.read` reads, `social.manage` creates experiments and drafts, `social.post` sends actions (spending credits).
