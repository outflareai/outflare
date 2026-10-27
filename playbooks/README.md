# Playbooks

Ready-made recipes for growth that anyone can add to Outflare. Each folder here is one playbook: a `playbook.yaml` that says what data and connections it needs, the steps it takes with Outflare's own tools, what it will never do, how to tell it worked, and the prompt that runs it.

Once a playbook is accepted, it ships with the next deploy of Outflare Cloud (app.outflare.ai) and the next release for self-hosted installs. It shows up:

- on the **Playbooks** page in every workspace, with whether that workspace is ready to run it;
- in the API (`GET /v1/playbooks`, public) and to agents through MCP (`list_playbooks`, `get_playbook`, `start_playbook`);
- on [outflare.ai/playbooks](https://outflare.ai/playbooks/), with the prompt to copy.

Everything here is Apache-2.0 (see [LICENSE](LICENSE)), so you can copy any playbook as a starting point. Contributions are covered by the repository's [CLA](../CLA.md).

## How a playbook runs

A playbook is data, not code. Starting one creates a mission draft with the prompt filled in and the steps as its plan, so:

- nothing happens until a person approves it, and the mission's mode decides whether writes are dry runs, wait for approval or run within limits;
- every step uses Outflare's normal API, the same calls (and MCP tools) a person or agent would use, so the contact rules, opt-outs, spend limits and audit log all apply;
- results land where they belong (journeys, the inbox, campaign links, the fixes list), never in a store of the playbook's own;
- every connection is the workspace's own account. Outflare never supplies a key, so a playbook whose connection is missing doesn't start and says what to connect.

People can also copy the prompt into Claude, ChatGPT or Cursor with the Outflare MCP connected and run it from there.

## Folder layout

```
playbooks/<id>/
  playbook.yaml
```

The folder name is the playbook's id: lowercase words joined by hyphens, at most 60 characters.

## playbook.yaml

```yaml
id: welcome-new-signups
version: 1                       # raise it whenever the steps or prompt change
title: Welcome new sign-ups and get them to their first win      # at most 80 characters
summary: A short welcome journey that follows what each new user does.   # one sentence, at most 160
description: |                   # why it works, at most 2,000 characters
  Most welcome emails go out on a timer...
tier: obvious                    # obvious, clever or edge
goals: [activation]              # customers, revenue, activation, retention, partners, search, ai_answers, tracking, replies
publisher: community             # community, or outflare for the Outflare team's own
author: { github: your-handle }

needs:
  data:                          # what the workspace already collects
    - { check: customer_events, label: Sign-up and product events sent to Outflare }
  connections:                   # the workspace's own accounts
    - capability: email.send     # or category: analytics
      label: An email provider for lifecycle email
      recommended: [resend, postmark, amazon_ses]   # connector ids, best first
    - capability: plan
      label: An AI model, unless you run this from your own AI app
      optional: true

inputs:                          # filled in before running, used in the prompt as {{name}}
  - name: brand
    label: Brand
    example: Northpeak
  - name: first_win
    label: What a first win looks like
    help: Leave it empty and the agent suggests one.
    example: created their first project
    optional: true

steps:                           # what the agent does, with the operations it uses
  - say: Read the brand's context so the emails sound like the brand.
    tools: [getBrandContext]
  - say: Build a journey that starts on sign-up and exits on the first win.
    tools: [createJourney]

guardrails:                      # what it will never do
  - The journey is turned on only after a person approves it.

measure:                         # how people tell it worked
  - metric: Activation rate
    how: "Share of new sign-ups who reach the first win within 7 days, before and after."

prompt: |                        # at most 4,000 characters
  Run the "Welcome new sign-ups" playbook for {{brand}}...

example: |                       # optional: a worked run, with fictional data only
  Northpeak's events showed that...
```

### What each part can refer to

- **`tools`** are `operationId`s from [`api/openapi.yaml`](../api/openapi.yaml). Each is also an MCP tool, so the agent knows exactly what to call. `GET /v1/connectors` and the API reference at outflare.ai/docs/api list them.
- **`needs.connections`** name a `capability` (such as `email.send`, `plan`, `llm.sample`, `analytics.read`, `crawl.fetch`, `payments.read`) or a `category` (such as `analytics`, `crm`, `sequencer`). Any connector that offers it counts, so people can use the tool they already have.
- **`recommended`** lists connector ids from `GET /v1/connectors`, best first. This is where you can suggest your own tool: if it isn't a connector yet, add it first ([docs/connectors.md](../docs/connectors.md)).
- **`needs.data`** checks are one of `records`, `customer_events`, `inbox`, `site_tracking`, `seo_site`, `mailboxes`, `leads`. Ask in an issue if you need another.

### Rules the checks enforce

- Unknown keys are errors, so a typo never goes unnoticed.
- Every tool must be a real operation, every capability or category must be offered by a connector Outflare ships, and every recommended connector must offer it.
- Every `{{input}}` in the prompt is declared, and every input is used.
- At least one step and one measure.
- Only fake people and companies: Northpeak, Acme, and addresses and domains ending in `.example`, `example.com` or `.test`. Never a real person, customer or company's data.

Run them:

```sh
go test ./engine/internal/playbooks/
```

## Writing a good playbook

- **Start from the outcome.** The title says what the person gets ("Answer every interested reply within the hour"), not which feature it uses.
- **Keep people in charge.** Anything that sends, posts, spends or changes a site waits for approval. Say so in `guardrails`.
- **Respect the people on the other end.** No buying contact lists, no scraping behind logins, no fake accounts or votes, no messages that pretend to be from someone else.
- **Make it measurable.** Name a number the person can check before and after.
- **Raise `version`** whenever you change the steps or prompt, so results can be compared per version.

## Trust

Every playbook shows who wrote it.

| Badge | Who |
| --- | --- |
| By Outflare | Playbooks the Outflare team looks after (`publisher: outflare`) |
| Community | Everyone else, after review. The playbook works fully; the badge says who to thank, and who to ask |

Only maintainers merge playbooks, and only maintainers may use `publisher: outflare`.

## Sending a pull request

1. Fork [outflareai/outflare](https://github.com/outflareai/outflare) and add `playbooks/<id>/playbook.yaml`.
2. Run the tests.
3. Open a pull request. CI checks the playbook, scans for secrets and real people's data, and asks you to sign the [CLA](../CLA.md) once.
4. A maintainer reviews it and brings it into Outflare with you credited as the author of the commit. Once that is merged, it ships with the next deploy and the next release, and your pull request is closed with a link to it.
