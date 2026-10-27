# Usage stats and update checks

A self-hosted Outflare server sends one small, anonymous report a day. In return it learns whether a newer version or a security fix exists, and shows that under **Settings → This server**. It works the same way as WordPress, GitLab, Grafana and Discourse: the update check and the usage count are the same call.

We ask because it is the only way to know how many servers run Outflare, which versions are still in use when a security fix ships, and which connectors people rely on. The report is anonymous, the counts are ranges, and it is easy to turn off.

## Turn it off

Any of these stops it completely. Nothing is sent, and the update check stops too.

- On the first-run setup form, switch off **Send anonymous usage stats**.
- Later, under **Settings → This server** (only the person who set up the server can change it), or `PATCH /v1/instance/telemetry` with `{"enabled": false}`.
- On the server: `OUTFLARE_TELEMETRY=off`, or `DO_NOT_TRACK=1` ([Console Do Not Track](https://consoledonottrack.com)). In Docker Compose, add it to `deploy/.env`. The environment wins over the setting.
- Point it at your own collector instead with `TELEMETRY_URL=https://...`, or set `TELEMETRY_URL=` (empty) to send nothing.

Builds from source send nothing unless they set a stats server: official releases are built with one (`-ldflags "-X github.com/outflareai/outflare/engine/internal/telemetry.DefaultURL=..."`, or the `TELEMETRY_URL` build argument in the Dockerfile). The release workflow takes it from the `TELEMETRY_URL` repository variable; see [releasing.md](releasing.md).

## What is sent

`GET /v1/instance/telemetry/preview` (or **See exactly what is sent** under Settings → This server) returns the exact report. A typical one:

```json
{
  "schema": 1,
  "instance_id": "6f1c0c9e-3b0e-4c55-9c0e-6b2f0d1f8a43",
  "version": "v0.3.0",
  "os": "linux",
  "arch": "amd64",
  "go_version": "go1.25.0",
  "container": true,
  "postgres_major": 16,
  "install_age_days": "26-50",
  "counts": {
    "users": "2-5",
    "workspaces": "1",
    "mailboxes": "51-100",
    "active_mailboxes": "26-50",
    "leads": "1001-5000",
    "active_sequences": "2-5",
    "sent_24h": "251-500",
    "replies_24h": "6-10",
    "projects": "1",
    "missions": "0",
    "api_tokens": "2-5",
    "active_users_7d": "1"
  },
  "connectors": ["emailbison", "hubspot", "zerobounce"]
}
```

| Field | What it is | Why |
| --- | --- | --- |
| `schema` | Report format version | So old servers keep working when it changes |
| `instance_id` | A random id made when the database was created. It names this install and nothing else | To count servers, not reports |
| `version` | Outflare version | Who still runs a version with a known problem |
| `os`, `arch`, `go_version`, `container`, `postgres_major` | Platform | Which platforms to build and test for |
| `install_age_days` | Days since the database was created, as a range | Whether people keep using it after trying it |
| `counts.*` | Sizes as ranges (`0`, `1`, `2-5`, `6-10`, `11-25` … `100000+`), never exact | Whether servers are actually used, and how big they get |
| `connectors` | Ids of connectors in use, such as `hubspot` | Which integrations to invest in, like a plugin directory's active-install counts |

## What is never sent

Names, email addresses, domains, the server's URL or hostname, lead or company data, email subjects or bodies, sequence or project names, credentials, API tokens, connector account details, and anything you type. The collector does not store the IP address the report came from; it may derive a country from it before discarding it.

## What comes back

The stats server answers with the newest version and any notices for the version you run, for example a security fix. The server keeps them as plain data: at most ten notices, links only if they are `https`, nothing is run. **Settings → This server** shows them.

## How it runs

The first report goes a few minutes after the server starts, then one a day at a random minute. A failed report is retried an hour later and never blocks anything else. Where it goes, when it was last sent and the last error are on the same page.

The hosted Cloud does not send these reports; it measures its own usage.
