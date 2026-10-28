# Playbooks

Playbooks are ready-made recipes for growth, such as welcoming new sign-ups, answering interested replies within the hour or getting named in AI answers. They live as files in [`playbooks/`](../playbooks/), one folder each, and anyone can add one with a pull request: [playbooks/README.md](../playbooks/README.md) has the format and the rules.

## What a workspace sees

**Playbooks** in the sidebar lists every playbook with whether the workspace is ready for it:

- **Ready**: every required connection and data source is in place.
- **Connect first**: a connection is missing. Only the workspace's own accounts count; Outflare never lends its keys, so the playbook won't start until it's connected.
- **Needs data**: the workspace doesn't collect what the playbook reads yet (for example, no site added to SEO). It can still start, and the agent reports what it found.

Opening one shows what it needs, its steps and the API operations each step uses, what it won't do, how to tell it worked, and its prompt with the inputs filled in.

## Running one

- **In Outflare:** Start as a mission creates a mission draft named after the playbook, with the prompt and guardrails as its instructions and the steps as its plan. Like any mission, nothing runs until a person approves it, and its mode decides whether writes are dry runs, wait for approval or run within the limits.
- **From your own AI app:** copy the prompt into Claude, ChatGPT or Cursor with Outflare connected. The agent can read the steps with `get_playbook`.
- **From any agent or script:** `POST /v1/workspaces/{ws}/playbooks/{id}/runs` with `inputs`, or the MCP tool `start_playbook`. It answers with the mission, or `409 connector_needed` while a required connection is missing.

Missions started from a playbook carry `playbook_id` and `playbook_version`, so results can be compared per playbook and version.

## Setup kits

Some playbooks need things set up before their first run, such as a pipeline in the CRM, fields on people and email drafts. Such a playbook names a setup kit (`playbooks/kits/<id>.yaml`). When you open it, **What it sets up** lists each part: **Adds**, **Extends** (an option list of your own gains missing options), **Have** (already there) or **Conflict** (there under the same key but set up differently). Tick **Set up what it needs when I start** and the kit is applied in the same step as the mission draft.

A kit only adds. Nothing you already have changes, email drafts are added unpublished so you read and publish them first, and while anything conflicts it changes nothing. Applying it again changes nothing. Applying needs `catalog.manage`, and `emails.manage` when it adds email drafts.

The first kit, **Creator partnerships**, adds a Partnerships record type with a stage field (Found, Vetted, Pitched, Negotiating, Agreed, Live, Measured, Renewed, Declined), the creator, deal type, fee, deliverables, platform, go-live date, post, content rights, tracked link and promo code; creator fields on people (platforms, audience size, topics, rate card, past sponsors); and four email drafts (brief, go-live reminder, thanks and results, an invite for customers who create). Five playbooks use it: **Run creator partnerships from first find to results**, **Find creators who already cover your competitors**, **Sponsor newsletters and podcasts that pay back**, **Seed your product to creators**, **Renew the creator deals that pay back** and **Recruit affiliates and creators from your own sign-ups**.

## API

| Call | What it does |
| --- | --- |
| `GET /v1/playbooks` | Every playbook, public, no key needed |
| `GET /v1/playbooks/{id}` | One playbook |
| `GET /v1/workspaces/{ws}/playbooks` | Playbooks with `readiness` and `runs` for the workspace (`missions.read`) |
| `GET /v1/workspaces/{ws}/playbooks/{id}` | One, with its readiness (`missions.read`) |
| `POST /v1/workspaces/{ws}/playbooks/{id}/runs` | Start one as a mission draft (`missions.run`); `apply_kit: true` also sets up its kit |
| `GET /v1/kits`, `GET /v1/kits/{id}` | Setup kits, public, with the playbooks that use each |
| `GET /v1/workspaces/{ws}/kits/{id}/preview` | What the kit would add, extend, reuse or conflict with (`customers.read`) |
| `POST /v1/workspaces/{ws}/kits/{id}/apply` | Set it up (`catalog.manage`, and `emails.manage` for email drafts); 409 `conflict` names what's in the way |

## Events

Starting a playbook records `playbook.started` (with `playbook_id`, `playbook_version`, `mode`, `publisher` and `kit_applied`) beside `mission.created`. Applying a kit records `kit.applied` (with `kit_id`, `kit_version` and how many parts were added, extended and reused), and each part records its own action with `kit_id`. The web app records `ui.playbook_opened` and `ui.playbook_prompt_copied`.
