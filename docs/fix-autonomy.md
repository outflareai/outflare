# Fix autonomy

Fix autonomy says how far agents may go with the [fixes list](crawler.md#the-fixes-list) on their own. It works through the same approval gate as every other write an agent makes in a [mission](../README.md#missions): what the setting lets through runs, and everything else waits for a person to approve it.

Copilot is the default, so nothing changes until an owner or admin chooses otherwise.

## How it works

An agent takes a fix before it changes the site: `POST /v1/workspaces/{ws}/fixes/{fix}/start` (MCP `start_fix`). That marks the fix in progress, assigns it to the agent and answers with the prompt to work from. Inside a mission, the gate decides first:

| Mode | What happens when an agent takes a fix |
| --- | --- |
| `copilot` (default) | Every fix waits for a person (`202`, `approval_required`), even in an autonomous mission. |
| `rules` | Fixes whose crawl rule or category is in `allowed` run straight away. The rest wait. |
| `autonomous` | Every fix runs straight away, except the ones below. |

Fixes that change redirects, canonicals, indexing, robots.txt or URLs can move or hide a whole site in search, so they **always wait for a person**, in every mode. So does AI visibility's `ai_fact_wrong`, which often means asking another site to correct a page. The list is `always_person` in the setting, and `allowed` can't name one of them.

Outside a mission, a person or an API token takes any fix: the setting only answers what an agent in a mission could do (`autonomy` in the answer, such as `{"run": false, "reason": "always_person"}`).

Changing the setting from inside a mission always waits for a person, so an agent can't give itself more room.

## Settings

Owners, admins and anyone with `seo.manage` change it on **Fixes → Agent autonomy**, or through the API:

| Endpoint | MCP tool | What it does |
| --- | --- | --- |
| `GET /v1/workspaces/{ws}/fix-autonomy` | `get_fix_autonomy` | The workspace's setting |
| `PUT /v1/workspaces/{ws}/fix-autonomy` | `update_fix_autonomy` | Change it; fields left out keep their value |
| `GET/PUT /v1/workspaces/{ws}/seo-sites/{site}/fix-autonomy` | the same tools with `site_id` | A site's own setting, or the workspace's when it has none (`inherited`) |
| `DELETE /v1/workspaces/{ws}/seo-sites/{site}/fix-autonomy` | | Put the site back on the workspace's setting |
| `POST /v1/workspaces/{ws}/fixes/{fix}/start` | `start_fix` | Take a fix before changing the site |

```sh
curl -X PUT "$OUTFLARE_URL/v1/workspaces/$WS/fix-autonomy" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"mode": "rules", "allowed": ["metadata", "images_no_alt"]}'
```

`allowed` takes rule ids (crawl rules from `GET .../seo-rules`, or another source's, such as AI visibility's `ai_brand_absent`) and crawl rule categories (`metadata`, `content`, `links`, `indexability`, `performance`, `security`, `social`). A category lets through every rule in it except the ones that always wait.

## Re-checking fixes on deploy

With `verify_on_deploy` on (the default) and the site's GitHub repository or Vercel project [watched](deploys.md), each production deploy starts a crawl of the site two minutes later, when the site has open, in-progress or done-but-unconfirmed fixes. A complete crawl confirms what is gone and reopens what is still there, exactly as `POST .../verify` does. Fixes marked done get `fix.verify_requested` with `"by": "deploy"` and the `deploy_id`.

If a crawl of the site is already running, it may have started before the deploy, so the re-check waits ten minutes and tries again, up to six times. A paused or opted-out site is skipped (`fixes.deploy_verify_skipped`).

## Events

`fix.started` (with `autonomy`, the reason the setting gave), `fix_autonomy.updated`, `fix_autonomy.reset`, `mission.action_held` for every fix that waits, and `fixes.deploy_verify_skipped`. `fix.started` is also a webhook.
