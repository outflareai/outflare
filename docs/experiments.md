# SEO experiments

An experiment tells you whether a change to a website's pages helped, instead of guessing from a chart. Outcomes come from your own Search Console data (connect Google Search Console for the site first), and the measurement starts when the change actually goes live: on the site's next production deploy ([deploys](deploys.md)) or when you say.

## Two designs

| Design | How it works | Use it when |
| --- | --- | --- |
| Holdout (the default) | Outflare splits the pages into a treatment group and a control group. You change only the treatment pages. The control pages show what would have happened anyway, so a Google update, a seasonal dip or a bank holiday isn't mistaken for your change. | The change can go to some pages and not others: titles, descriptions, internal links, schema, page templates behind a flag |
| Before and after | The pages are compared with themselves before and after the change. | The change has to go to every page at once. It can't tell your change from anything else that happened at the same time. |

The groups are matched: the pages are ranked by their baseline and dealt into the groups in turn, so both get the same mix of busy and quiet pages. An experiment takes the 1,000 busiest pages that match its pattern.

## Running one

1. **Make it.** Pick the site, a path pattern (`/blog/*`; `*` stands for anything, empty is every page), the metric (clicks, impressions, CTR or position), the share of pages held back (50% by default) and how many days to compare before and after (28 each by default).
2. **Change the treatment pages.** `GET .../experiments/{id}/pages?group=treatment` lists them. Leave the control pages alone until it ends.
3. **Start it.** With `start_on: deploy` (the default), `POST .../start` waits for the site's next production deploy on a watched GitHub repository or Vercel project, and the deploy starts it. With `start_on: manual`, or by passing `changed_at`, it measures from when you say the change went live, up to 90 days ago.
4. **Read the result.** While it measures, `GET .../experiments/{id}` works out the result from the days Search Console has sent so far. It ends by itself once every measured day is in (Search Console runs two or three days behind) and records `experiment.concluded`. `POST .../stop` ends it early.

## The result

| Field | Meaning |
| --- | --- |
| `treatment`, `control` | Each group's metric per page before and after (a day's average for clicks and impressions), and the change |
| `lift` | For a holdout, how much better the treatment did than the control: `(1 + treatment change) / (1 + control change) - 1`. For before and after, the treatment's change. For position, a positive lift means the pages rank higher |
| `p_value` | The chance the difference is noise, from a test of each page's change (Welch's t-test). Null with fewer than 5 pages in a group |
| `verdict` | `better` or `worse` when `p_value` is under 0.05, otherwise `no_clear_change`; `too_early` in the first 7 days and while a result isn't clear yet |

The day the change went live is left out, since it is half one and half the other.

## API, MCP and webhooks

| Endpoint | MCP tool |
| --- | --- |
| `GET /v1/workspaces/{ws}/experiments` | `list_experiments` |
| `POST /v1/workspaces/{ws}/experiments` | `create_experiment` |
| `GET`, `PATCH`, `DELETE /v1/workspaces/{ws}/experiments/{id}` | `get_experiment`, `delete_experiment` |
| `GET /v1/workspaces/{ws}/experiments/{id}/pages` | `list_experiment_pages` |
| `POST /v1/workspaces/{ws}/experiments/{id}/start` | `start_experiment` |
| `POST /v1/workspaces/{ws}/experiments/{id}/stop` | `stop_experiment` |

Scopes are `seo.read` and `seo.manage`, and an experiment belongs to its site's brand. `experiment.started` and `experiment.concluded` are webhooks, and the [digest](digests.md) lists what is measuring and what ended.
