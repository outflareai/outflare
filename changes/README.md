# Change notes

Every pull request that changes what Outflare does adds one small file here: a note that says, for the people using Outflare, what changed. The [daily changelog on outflare.ai](https://outflare.ai/changelog/) and the notes for each self-hosted release are both built from these files, so no two pull requests edit the same lines.

## Writing one

Add `changes/<anything>.md`, for example `changes/rank-tracking.md`. The name doesn't matter; the pull request that adds it is found from git.

```md
---
kind: launch
area: seo
---
Rank tracking: follow where your site ranks on Google for the keywords you choose, by country and device.
```

- **kind**: `launch` (something new), `improvement` (something that works better or differently), `fix` or `security`.
- **area**: `agents`, `crm`, `outreach`, `deliverability`, `inbox`, `lifecycle`, `seo`, `social`, `paid-ads`, `partners`, `analytics`, `records`, `connectors`, `api`, `workspace`, `self-hosting` or `general`.
- **The text**: one or two sentences, at most 400 characters, in plain words. Say what someone can now do or what behaves differently, not how it was built.
- **Upgrade steps** for self-hosters (a new setting, a migration that takes a while, something to turn on) go in a last paragraph that starts with `Upgrade:`. They appear only in the self-hosted release notes.
- **Security fixes** say only `Security improvements to <area>.` (for example `Security improvements to sign-in.`), so the changelog never explains an unfixed weakness. The full account goes in a security advisory once the fix has shipped (`SECURITY.md`).

A pull request that changes the engine, web app, API, SDK, MCP server or CLI fails the **Change notes** check without a note. If nobody using Outflare would notice the change (a refactor, a test, CI), add the `no-note` label instead.

## Where they go

- **The daily changelog.** Once a change is live on Outflare Cloud, its note is added to that day's entry on outflare.ai, tagged **Open source** (`node scripts/changes.mjs list <from> <to>` lists the notes between two commits). A day is final at midnight UTC.
- **Self-hosted releases.** After every merge, the automatic release cut runs `node scripts/changes.mjs fold`, which adds the notes not released yet to `CHANGELOG.md` under Unreleased, then turns that into the next version ([docs/releasing.md](../docs/releasing.md)).

Notes stay in this folder after they ship; each run picks them by the commits in its range.
