# Outflare content: pull requests for approved pages

A GitHub Action for sites whose pages live in a repository (Astro, Next.js, Hugo, Eleventy, Jekyll, Docusaurus and the like). It runs in **your** repository, on your schedule, with an Outflare token you keep in your own secrets. Outflare never gets access to your repository.

Each run:

1. Lists the brand's pages approved in Outflare.
2. Writes each one as a Markdown file on its own branch (`outflare/content-<id>`) and opens a pull request, with what to check, where the page goes, which existing pages should link to it, and its images. Approving the page again in Outflare updates the same pull request. A pull request you close isn't opened again until the page is approved again.
3. Once the pull request is merged, reports the page live at its address on your site, so Outflare starts measuring it.

You review and merge as you would any change. Edit the file on the branch as you like.

## Set it up

1. In Outflare, make an API token with the `content.read` and `content.manage` scopes (Settings, API tokens; give it the brand if you have several). Add it to your repository as the secret `OUTFLARE_TOKEN`.
2. Find the brand's id (Settings, Brands, or `GET /v1/workspaces/{workspace}/brands`).
3. Add `.github/workflows/outflare-content.yml`:

```yaml
name: Outflare content
on:
  schedule:
    - cron: "*/30 * * * *"
  push:
    branches: [main]
  workflow_dispatch:

permissions:
  contents: write
  pull-requests: write

concurrency: outflare-content

jobs:
  content:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: outflareai/outflare/integrations/github-action/content@main
        with:
          outflare-token: ${{ secrets.OUTFLARE_TOKEN }}
          brand: 00000000-0000-0000-0000-000000000000
          path: src/content/blog/{slug}.md
          site-url: https://acme.example
          url: /blog/{slug}/
```

Pin `@main` to a release tag or a commit SHA once it works for you. Use the current major version of `actions/checkout`.

## Inputs

| Input | Default | What it does |
| --- | --- | --- |
| `outflare-token` | (required) | An Outflare API token with `content.read` and `content.manage`. |
| `brand` | (required) | The brand's id. |
| `path` | `content/{target}/{slug}.md` | Where each page's file goes. Placeholders: `{slug}`, `{id}`, `{language}`, `{collection}`, `{target}` (`posts` or `pages`). Each value is reduced to letters, digits, dots, hyphens and underscores, and a path outside the repository or in `.git` or `.github` is refused. |
| `site-url` | none | Your site's address. Without it, merged pages aren't reported live. |
| `url` | `/{slug}/` | A page's address, from `site-url`. The same placeholders, plus `{path}`: the page's placement path from Outflare. |
| `report-live` | `true` | Set `false` to report pages live from your own deploy instead, with `POST /v1/brands/{brand}/content/{id}/delivered`. |
| `base` | the default branch | The branch pull requests go into. |
| `labels` | none | Labels for each pull request, separated by commas. They must exist already. |
| `outflare-url` | `https://app.outflare.ai` | Your Outflare server, when you host it yourself. |
| `github-token` | `github.token` | Pull requests opened with the default token don't start your other workflows (GitHub's rule). Pass a GitHub App or fine-grained token with contents and pull requests write if your checks should run on them. |

Outputs: `opened` (pull requests opened or updated) and `reported` (pages reported live).

## The file

```markdown
---
title: "Northpeak vs Ridgeline"
slug: "northpeak-vs-ridgeline"
description: "A side-by-side look."
lang: "en-AU"
tags: ["comparison"]
meta_title: "Northpeak vs Ridgeline"
keyword: "northpeak vs ridgeline"
image: "https://cdn.northpeak.example/vs.webp"
image_alt: "Two dashboards"
outflare_id: "5d0c7b52-1d1e-4a52-9a49-3b0f3f6f2a10"
outflare_revision: 2
---

## Pricing
...
```

Keep `outflare_id` and `outflare_revision`: they tell later runs which version the file holds. Rename or add front matter fields in your site's content config as it needs.

Prefer to put pages in place another way? The `content.approved` webhook sends the same page to any URL ([docs/content.md](../../../docs/content.md)).

## Development

`node --test integrations/github-action/content/index.test.mjs`. The action needs Node 20 or later, `git` and `gh` (both on GitHub's runners) and no packages.
