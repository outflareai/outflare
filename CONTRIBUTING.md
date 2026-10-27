# Contributing

Thanks for helping build Outflare.

## Before you start

- Open an issue for anything larger than a small fix, so we can agree on the approach first.
- Read [AGENTS.md](AGENTS.md). It describes the architecture and conventions for humans and coding agents alike.

## Contributor License Agreement

Contributions require agreeing to the [Contributor License Agreement](CLA.md). You keep your copyright. The CLA lets the project ship the AGPL core and Apache-2.0 SDKs, run the hosted Cloud on the same code (see [ADR 5](docs/adr/0005-open-core-cloud.md)), and change licensing later if needed. On your first pull request a bot asks you to sign by replying with one sentence; it covers all your later contributions.

## Pull requests

Outflare's maintainers work in a private repository, and each release is published here as one commit. Pull requests here are welcome all the same: a maintainer brings yours into that repository with you credited as its author, it goes through the same checks as everyone's, and it ships in the next release. Your pull request is closed with a link to that release.

- Keep each PR focused on one change.
- `make test` and `pnpm -r build` must pass, and `pytest` in `sdk/python` if you change the Python SDK. CI runs all three.
- Go code is `gofmt`ed. Every migration is a new numbered file in `engine/internal/db/migrations/` that only expands the schema, so the previous version still runs on it; never edit one that has shipped. See [docs/migrations.md](docs/migrations.md).
- Add a line under `## [Unreleased]` in [CHANGELOG.md](CHANGELOG.md) when your change is something a self-hoster would notice.
- API changes start in `api/openapi.yaml`.
