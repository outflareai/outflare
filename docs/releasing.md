# Releasing

Outflare is developed in a private repository and released to the public [`outflareai/outflare`](https://github.com/outflareai/outflare). Nobody tags or publishes by hand: releases are cut, checked and published by automation.

1. **Every pull request that changes what Outflare does adds a line under `## [Unreleased]`** in [`CHANGELOG.md`](../CHANGELOG.md).
2. **A release is cut as soon as there is something to release.** After every merge to main, if Unreleased has entries, automation opens (or refreshes) one pull request, "Release v0.x.y", that renames `## [Unreleased]` to `## [0.x.y] - YYYY-MM-DD` and adds an empty `## [Unreleased]` above it. The version follows [Semantic Versioning](https://semver.org/): before 1.0, a patch (0.0.x) when the section has only Fixed and Security entries, otherwise a minor (0.x.0).
3. **The release pull request merges by itself** once its checks pass, usually minutes after the change it releases. The release's commit is then tagged `v0.x.y`.
4. **The tag is published.** A pipeline copies only the paths listed in [`publish.txt`](../publish.txt) and checks the copy. It must pass a secret scan, the fake-data check over every file, a scan for internal names, the bring-your-own-keys check, a build from the copy alone, and a review of everything that changed since the last public release. Only if all of them pass does it push one commit to `outflareai/outflare`, with the release's changelog section as its message, and the tag.
5. **If any check fails, nothing is published.** The failure is filed as an issue for the team, and the next release goes out once it's fixed.

The tag in the public repository runs `.github/workflows/release.yml`. It first reads the tag's section of `CHANGELOG.md` (`scripts/changelog-section.sh`) and stops without publishing anything if there is none.

The workflow publishes:

- **The image** `ghcr.io/outflareai/outflare`, for `linux/amd64` and `linux/arm64`. It gets the tag (`v0.3.0`), the bare version (`0.3.0`, `0.3`) and, unless the tag has a suffix like `-rc.1`, `latest`. The binary reports that tag as its version.
- **A GitHub release** with the changelog section as its notes, and `outflare-v0.3.0.tar.gz`. The tarball holds a `docker-compose.yml` that pulls that image instead of building from source, plus `.env.example` and a short README, so people can self-host without cloning (`scripts/release-bundle.sh` builds it). A tag with a suffix is marked as a pre-release.

## Usage reports from release images

Release images send the anonymous daily report and update check ([telemetry.md](telemetry.md)) to the `TELEMETRY_URL` repository variable (Settings → Secrets and variables → Actions → **Variables**), for example `https://<cloud host>/v1/cloud/telemetry/ping`. Without it, the release image sends nothing, like a build from source, and the workflow warns. Set it only to a host we run: Outflare Cloud's collector.

## What gets counted

- Release downloads: GitHub counts each download of the tarball.
- Image pulls: shown on the package page on GitHub.
- Running servers: the daily report, for servers that didn't turn it off.

## First release checklist

1. Make the package public after the first release in the public repository: on GitHub, open Packages → outflare → Package settings → Change visibility.
2. Set `TELEMETRY_URL` once Outflare Cloud has a public address.
3. Update checks offer the newest release that isn't a pre-release, so only release builds you want people to move to.
