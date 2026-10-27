# Deploys

Outflare can watch the repository or project behind each of your websites and record every production deploy. Features that wait for a change to go live act on the deploy instead of guessing: fixes are re-checked once the change ships, and experiments start on the real deploy.

It only reads. Outflare never pushes code, opens pull requests, deploys, promotes or rolls back through these connections.

## Connecting

Both run on your own account, with nothing from Outflare in between.

| Connector | How it connects | What a deploy is |
| --- | --- | --- |
| GitHub | Sign in with GitHub (needs a GitHub OAuth app on the server: `CONNECTOR_GITHUB_CLIENT_ID` and `CONNECTOR_GITHUB_CLIENT_SECRET`), or a fine-grained personal access token with read-only Contents, Deployments and Metadata on the website's repositories | A deployment to a production environment (`production`, `Production`, `github-pages`, or one GitHub marks as production) whose latest status is `success`. A repository that records no deployments deploys on push: each commit to its default branch counts |
| Vercel | An access token from Account Settings → Tokens, plus the Team ID for a team's projects | A production deployment that reached `READY` |

GitHub's OAuth apps have no read-only scope for private repositories, so signing in asks for `repo`; Outflare still only reads. Use a fine-grained token if you want the access itself to be read-only.

## Watching a repository or project

On the Connectors page, **Deploys** on a GitHub or Vercel connection lists what it can see. Pick the repository or project behind a website and, optionally, the SEO site it serves (Outflare picks the site whose host matches the repository's homepage or the project's production domains). Over the API:

```sh
# What the connection can watch, with the domains each serves.
curl -s $OUTFLARE_URL/v1/workspaces/$WS/integrations/$INTEGRATION/deploy-options -H "Authorization: Bearer $OUTFLARE_TOKEN"

# Watch one. Without site_id, the matching SEO site is picked.
curl -s -X POST $OUTFLARE_URL/v1/workspaces/$WS/deploy-targets -H "Authorization: Bearer $OUTFLARE_TOKEN" \
  -H "Content-Type: application/json" -d '{"integration_id": "'$INTEGRATION'", "target": "acme/website"}'
```

Each active target is checked every five minutes (`POST .../deploy-targets/{id}/check` checks now). The first check looks back seven days. Each deploy is stored once with its commit, branch, first line of the commit message and a link to it on the provider (`GET .../deploys`).

A production deploy going live records `deploy.detected`; one that fails records `deploy.failed`. Both are audit events and webhooks, with `target_id`, `site_id`, `kind`, `commit`, `ref` and `deployed_at`. A target the provider refuses shows its `last_error`, and a refused token marks the connection as needing attention.

## API

| Endpoint | Scope | |
| --- | --- | --- |
| `GET /v1/workspaces/{ws}/integrations/{id}/deploy-options` | `integrations.read` | Repositories or projects the connection can see |
| `GET`, `POST /v1/workspaces/{ws}/deploy-targets` | `integrations.read`, `integrations.manage` | Watched targets; watch one |
| `GET`, `PATCH`, `DELETE /v1/workspaces/{ws}/deploy-targets/{id}` | `integrations.read`, `integrations.manage` | Change its site, pause it, stop watching |
| `POST /v1/workspaces/{ws}/deploy-targets/{id}/check` | `integrations.manage` | Check now |
| `GET /v1/workspaces/{ws}/deploys` | `integrations.read` | Production deploys, newest first, by target, site or state |

Targets and deploys take the brand of the site they serve, so a brand-limited member sees only their brands' deploys.

## MCP

`list_deploy_options`, `list_deploy_targets`, `add_deploy_target`, `update_deploy_target`, `remove_deploy_target`, `check_deploys` and `list_deploys`.

## For developers

Package `engine/internal/deploys` runs the checks. A connector reports deploys by implementing `connectors.DeployReader` (capability `deploys.read`, category `code`). Features that act on a deploy add a hook to `deploys.Service.OnReady`, which runs in the transaction that records the deploy going live.
