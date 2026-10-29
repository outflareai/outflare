# Workspace features

Some parts of Outflare are switched on for a few workspaces before everyone gets them, so the first workspaces can test them. Each one is off everywhere until it is switched on for a workspace, and then it is on for everyone in that workspace. Nothing about it shows (no menu item, no API routes) while it is off.

| Feature | What it is |
| --- | --- |
| `ai_accounts` | AI Accounts: every AI subscription the workspace's people pay for, with usage, resets, renewals and spend. Being built. |

## Switching one on

On a self-hosted server, the operator runs:

```sh
outflare features list                                   # every feature
outflare features list --workspace acme                  # which are on for a workspace
outflare features enable --workspace acme ai_accounts
outflare features disable --workspace acme ai_accounts
```

`--workspace` takes the workspace's id or its slug (its name in web app addresses). The command needs `DATABASE_URL`, like `outflare migrate`.

Each change is in the workspace's audit log as `workspace.feature_enabled` or `workspace.feature_disabled`, with the feature's name.

## For developers

- Add a feature to `Catalog` in `engine/internal/features`. Names are a promise like event names: add freely, never rename.
- Check `features.Enabled` in the feature's routes and jobs. While it's off, answer as if the route didn't exist (404 `not_found`).
- The web app reads `features` on `GET /v1/workspaces/{ws}` to decide what to show.
- Extensions switch features with `Host.SetWorkspaceFeature` and read them with `Host.WorkspaceFeatures`.
- This is separate from [feature flags](feature-flags.md), which workspaces use on their own websites.
