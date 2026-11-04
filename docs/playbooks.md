# Playbooks

Playbooks are ready-made recipes for growth, such as welcoming new sign-ups, answering interested replies within the hour or getting named in AI answers. They live as files in [`playbooks/`](../playbooks/), one folder each, and anyone can add one with a pull request: [playbooks/README.md](../playbooks/README.md) has the format and the rules.

## What a workspace sees

**Playbooks** in the sidebar lists every playbook with whether the workspace is ready for it:

- **Ready**: every required connection and data source is in place.
- **Connect first**: a connection is missing. Only the workspace's own accounts count; Outflare never lends its keys, so the playbook won't start until it's connected.
- **Needs data**: the workspace doesn't collect what the playbook reads yet (for example, no site added to SEO). It can still start, and the agent reports what it found.

Opening one shows what it needs, its steps and the API operations each step uses, what it won't do, how to tell it worked, and its prompt with the inputs filled in.

## Running one

- **In Outflare:** Start on a playbook starts it as an agent run (below): Outflare's agent carries it out in turns on the workspace's own AI model, and every change it makes goes through the action layer, so nothing runs that the mode doesn't allow.
- **From your own AI app:** copy the prompt into Claude, ChatGPT or Cursor with Outflare connected. The agent can read the steps with `get_playbook`, and its changes go through the action layer as its token.
- **From any agent or script:** `POST /v1/workspaces/{ws}/playbooks/{id}/agent-runs` with `inputs`, or the MCP tool `run_playbook`. It answers with the run, or `409 connector_needed` while a required connection is missing. Starting a playbook as a mission (`POST .../playbooks/{id}/runs`, `start_playbook`) is retired and answers `410 mission_retired`.

Runs started from a playbook carry `playbook_id` and `playbook_version`, so results can be compared per playbook and version.

### As an agent run on the action layer

**Start** on a playbook in the web app starts it as an **agent run**, with a mode picker (copilot, autonomous with a budget, or dry run); the run opens on the Agents page, under **Runs**. Over the API, `POST /v1/workspaces/{ws}/playbooks/{id}/agent-runs` (`agents.run`) starts the playbook as an agent run instead of a mission: Outflare's agent carries it out in turns as an agent token of its own, and the mode (`copilot`, `autonomous` with `max_spend_cents`, or `dry_run`) sets the token's grants, so every change it makes runs, waits for a person or is refused by the action layer and shows on the Agents page ([actions.md](actions.md), "Outflare's agent on the action layer"). `GET .../agent-runs` lists runs; people write to one with `POST .../agent-runs/{id}/updates`. Missions are retired; what the ones made before did stays readable on the Missions page.

## Running one by itself

A playbook can run by itself, on a schedule or when something happens. This is a **playbook automation**:

- **On a schedule:** every day, week or month, at the hour you choose (UTC). Each run looks only at what is new since the last one.
- **When an event happens:** any event from the webhook catalog (`GET /v1/webhook-events`), such as `backlink.lost` or `competitor.page_removed`. Choose how it takes events:
  - **each**: a run per event, at most a daily cap (10 by default); the rest wait for the next day.
  - **hourly** or **daily**: the events are gathered into one run, so a burst is one run, not dozens.

  Events from before you turn it on never start a run. With a brand chosen, only that brand's events count.

A playbook can suggest what suits it (its `automation:` section), and setting one up starts from that suggestion. The link building playbooks suggest, for example, a monthly run for resource pages and a daily run on lost links for reclaiming them.

**A person approves it once.** An automation starts as a draft, and nothing runs until a person approves it, with:

- the **mode** each run runs in (dry run, approve each write, or within limits; approve each write by default);
- a **spend limit per run** and **per month**, on the workspace's own connected accounts.

Then each run starts by itself, as an **agent run** of Outflare's agent on the action layer ([actions.md](actions.md), "Outflare's agent on the action layer"): the mode becomes the run's grants (dry run refuses and keeps every change, approve each write holds every change for a person, within limits runs them with the spend limit per run as the cap), and its changes show on the Agents page. It acts with the approving person's rights as they are when it runs, never more. The **external runner** (your own agent's token running each run as a mission) is retired with missions: setting it is refused, and an automation set up with it before is held when it next comes due, until a person sets the runner to Outflare's agent and approves it again. Changing anything but the name sends the automation back to draft for approval. An agent can set one up, but only a person can approve it.

**It stops for a person, never around them.** An automation is **held**, with the reason shown, when:

- the month's runs reach the monthly limit;
- a connection the playbook needs, or the AI model, is missing;
- the person who approved it can no longer run and approve missions.

Approving it again turns it back on, and a held automation keeps the events that waited. Link pitches still wait for approval as always.

Deciding which playbooks to run toward a target, and changing course from the results, is an autopilot. It is not part of this.

| Call | What it does |
| --- | --- |
| `GET /v1/workspaces/{ws}/playbook-automations` | Automations, with status, next run and this month's spend (`missions.read`) |
| `POST /v1/workspaces/{ws}/playbook-automations` | Set one up as a draft (`missions.run`) |
| `PATCH`, `DELETE /v1/workspaces/{ws}/playbook-automations/{id}` | Change (back to draft) or delete it (`missions.run`) |
| `POST .../playbook-automations/{id}/approve` | Turn it on, or back on (`missions.approve`, a person only) |
| `POST .../playbook-automations/{id}/pause` | Pause it (`missions.run`) |
| `POST .../playbook-automations/{id}/run` | Start a run now, inside its limits (`missions.run`) |
| `GET .../playbook-automations/{id}/runs` | The runs it started: `agent_runs`, and `data` for the missions of an external runner from before |

The runs carry `automation_id`; the automation shows `last_run_id` or `last_mission_id`. MCP has the same tools, except approving.

## Setup kits

Some playbooks need things set up before their first run, such as a pipeline in the CRM, fields on people and email drafts. Such a playbook names a setup kit (`playbooks/kits/<id>.yaml`). When you open it, **What it sets up** lists each part: **Adds**, **Extends** (an option list of your own gains missing options), **Have** (already there) or **Conflict** (there under the same key but set up differently). Tick **Set up what it needs when I start** and the kit is applied in the same step as the run starts.

A kit only adds. Nothing you already have changes, email drafts are added unpublished so you read and publish them first, and while anything conflicts it changes nothing. Applying it again changes nothing. Applying needs `catalog.manage`, and `emails.manage` when it adds email drafts.

The first kit, **Creator partnerships**, adds a Partnerships record type with a stage field (Found, Vetted, Pitched, Negotiating, Agreed, Live, Measured, Renewed, Declined), the creator, deal type, fee, deliverables, platform, go-live date, post, content rights, tracked link and promo code; creator fields on people (platforms, audience size, topics, rate card, past sponsors); and four email drafts (brief, go-live reminder, thanks and results, an invite for customers who create). Five playbooks use it: **Run creator partnerships from first find to results**, **Find creators who already cover your competitors**, **Sponsor newsletters and podcasts that pay back**, **Seed your product to creators**, **Renew the creator deals that pay back** and **Recruit affiliates and creators from your own sign-ups**.

## API

| Call | What it does |
| --- | --- |
| `GET /v1/playbooks` | Every playbook, public, no key needed |
| `GET /v1/playbooks/{id}` | One playbook |
| `GET /v1/workspaces/{ws}/playbooks` | Playbooks with `readiness` and `runs` for the workspace (`agents.read`) |
| `GET /v1/workspaces/{ws}/playbooks/{id}` | One, with its readiness (`agents.read`) |
| `POST /v1/workspaces/{ws}/playbooks/{id}/runs` | Retired: `410 mission_retired`. Start one as an agent run (`POST .../playbooks/{id}/agent-runs`, `agents.run`); `apply_kit: true` also sets up its kit |
| `GET /v1/kits`, `GET /v1/kits/{id}` | Setup kits, public, with the playbooks that use each |
| `GET /v1/workspaces/{ws}/kits/{id}/preview` | What the kit would add, extend, reuse or conflict with (`customers.read`) |
| `POST /v1/workspaces/{ws}/kits/{id}/apply` | Set it up (`catalog.manage`, and `emails.manage` for email drafts); 409 `conflict` names what's in the way |

## Events

Starting a playbook records `playbook.started` (with `playbook_id`, `playbook_version`, `mode`, `publisher` and `kit_applied`, and `run_id`). An automation's run records it too, with `automation_id`, `trigger` (schedule, event or manual) and `events`, and `playbook_automation.ran`. Automations record `playbook_automation.created`, `updated`, `approved`, `paused`, `held` (with the reason) and `deleted`. All of these are webhook events. Applying a kit records `kit.applied` (with `kit_id`, `kit_version` and how many parts were added, extended and reused), and each part records its own action with `kit_id`. The web app records `ui.playbook_opened` and `ui.playbook_prompt_copied`.

## Partnership qualification from sign-ups

Use **Assess every sign-up for partnership potential** to evaluate affiliate,
creator (including YouTube and social), SDK or bandwidth supplier, integration,
and reseller opportunities together. A person or company can fit several routes.
For a focused review, use **Find SDK and bandwidth suppliers among sign-ups**,
**Find integration partners among sign-ups**, or **Find resellers and agency
partners among sign-ups**. The existing **Recruit affiliates and creators from
your own sign-ups** playbook covers audience partners and approved invitations.

The qualification playbooks take your sign-up event, a time window and partner
criteria. They return a report linked to existing records, with evidence,
confidence, missing information and next steps. They do not send invitations.
They can be run manually or selected for a scheduled automation; they do not
install a signup webhook or turn on recurring runs. Choose a window that covers
the interval between runs and review new or changed evidence on each run.
