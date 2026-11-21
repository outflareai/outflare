# Releasing

Outflare is developed in a private repository and released to the public [`outflareai/outflare`](https://github.com/outflareai/outflare). Nobody tags or publishes by hand: releases are cut, checked and published by automation.

1. **Every pull request that changes what Outflare does adds a change note** in [`changes/`](../changes/README.md) (or a line under `## [Unreleased]` in [`CHANGELOG.md`](../CHANGELOG.md), the older way).
2. **A release is cut once a day, at 01:17 UTC**, with everything merged since the last one (or by hand, for a release that can't wait such as a security fix). App.outflare.ai doesn't wait for it: it deploys each change as it lands on main. Automation takes the newest commit on main whose CI passed and folds the notes added since the last release into a new section (`node scripts/changes.mjs fold --since <the main commit the last release was cut from>`). If there are any, it makes one commit on top of that main commit that renames `## [Unreleased]` to `## [0.x.y] - YYYY-MM-DD` in the last release's `CHANGELOG.md`, and tags it `v0.x.y`. The version follows [Semantic Versioning](https://semver.org/): before 1.0, a patch (0.0.x) when the section has only Fixed and Security entries, otherwise a minor (0.x.0).
3. **The release commit isn't merged into main.** It changes only `CHANGELOG.md`, and its code is the main commit under it, which already passed CI, so it needs no pull request and no second run of the tests. Each release's full changelog is in its tag and in the public repository; main's own `CHANGELOG.md` stops at the releases made before this.
4. **The tag is published.** A pipeline copies only the paths listed in [`publish.txt`](../publish.txt) and checks the copy. It must pass a secret scan, the fake-data check over every file, a scan for internal names, the bring-your-own-keys check, a build from the copy alone, and a review of everything that changed since the last public release. Only if all of them pass does it push one commit to `outflareai/outflare`, with the release's changelog section as its message, and the tag.
5. **If any check fails, nothing is published.** The failure is filed as an issue for the team, and the next release goes out once it's fixed.

The tag in the public repository runs `.github/workflows/release.yml`. It first reads the tag's section of `CHANGELOG.md` (`scripts/changelog-section.sh`) and stops without publishing anything if there is none.

The workflow publishes:

- **The image** `ghcr.io/outflareai/outflare`, for `linux/amd64` and `linux/arm64`. It gets the tag (`v0.3.0`), the bare version (`0.3.0`, `0.3`) and, unless the tag has a suffix like `-rc.1`, `latest`. The binary reports that tag as its version.
- **A GitHub release** with the changelog section as its notes, and `outflare-v0.3.0.tar.gz`. The tarball holds a `docker-compose.yml` that pulls that image instead of building from source, plus `.env.example` and a short README, so people can self-host without cloning (`scripts/release-bundle.sh` builds it). A tag with a suffix is marked as a pre-release.

## Libraries

The libraries in [`sdk/`](sdks.md) are released with Outflare and carry its version: the libraries in release `v0.4.0` are all `0.4.0`. Nobody sets a library's version by hand.

- **The version is written once.** As a release is copied for the public repository, `node scripts/library-version.mjs <version>` writes the version into every library (its package file and the version it reports with each event). `--check` fails when any of them disagrees. The script lists every place, and its test fails when a listed place is gone, so a library that moves is moved there too.
- **Three libraries are read straight from the tag.** Go reads the tag `sdk/go/v0.4.0`, pushed with each release. Composer reads [`composer.json`](../composer.json) and Swift Package Manager reads [`Package.swift`](../Package.swift) at the top of the repository at the tag, which is why those two files sit there and not in `sdk/`.
- **The others are packages.** The tag also runs `.github/workflows/publish-libraries.yml`, beside the release workflow and separate from it, so a registry that is down never stops the image or the GitHub release. Each job checks that the library says the tag's version, runs the library's tests, and only then publishes: `@outflare/sdk` and `@outflare/react-native` to npm, `outflare` to PyPI, RubyGems and pub.dev, and `ai.outflare:outflare-android` to Maven Central. A version the registry already has is skipped, so a failed run can be started again. A tag with a suffix like `-rc.1` publishes no libraries.
- **A registry is switched on by name.** A job runs only once its registry is in the `LIBRARY_REGISTRIES` repository variable of the public repository (`npm pypi rubygems maven pub`). A registry nobody has set up is skipped, never failed. npm, PyPI, RubyGems and pub.dev trust the workflow itself (trusted publishing), so the repository holds no key for them; a package's first version on npm needs an `NPM_TOKEN` secret, and on pub.dev a person publishes the first version. Maven Central needs a Central Portal token and a signing key as secrets; the workflow's header lists them. An upload to Maven Central waits in the Portal for someone to press Publish unless the `MAVEN_PUBLISHING_TYPE` variable is `AUTOMATIC`.

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
