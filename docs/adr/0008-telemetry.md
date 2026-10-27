# 8. Anonymous usage stats with update checks

Status: accepted, 2026-10-23

## Context

Downloads tell us little: GitHub reports clones and release downloads for 14 days, and a download is not a running server. We need to know how many servers run Outflare, which versions are in use when a security fix ships, and whether servers are actually used. Self-hosters of an AGPL project expect that to be anonymous, documented and easy to turn off.

## Decision

The engine sends one report a day (docs/telemetry.md) and reads back the newest version and security notices, the WordPress and GitLab pattern: the usage count and the update check are one call, so turning it off also turns off update notices, which gives most people a reason to leave it on.

- On by default, off with `OUTFLARE_TELEMETRY=off`, `DO_NOT_TRACK=1`, the first-run form, or Settings → This server (owner only). The environment wins.
- A random instance id, version, platform, counts as ranges, connector ids. Nothing that identifies a person, company, domain or server URL.
- `GET /v1/instance/telemetry/preview` shows the exact report.
- Builds without a configured endpoint send nothing; official releases set it at build time.
- The collector lives in the private Cloud repo. Cloud itself turns reporting off through `Host.DisableTelemetry`.

## Consequences

Counts undercount servers that turn it off or have no outbound network; we report them as a floor. Answers from the collector are untrusted: notices are clipped, capped and shown as text with https links only.
