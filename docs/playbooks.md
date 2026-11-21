# Playbooks

Playbooks are ready-made recipes for growth, such as welcoming new sign-ups, answering interested replies within the hour or getting named in AI answers. They live as files in [`playbooks/`](../playbooks/), one folder each, and anyone can add one with a pull request: [playbooks/README.md](../playbooks/README.md) has the format and the rules.

## What a workspace sees

**Playbooks** in the sidebar lists every playbook with whether the workspace is ready for it:

- **Ready**: every required connection and data source is in place.
- **Connect first**: a connection is missing. Only the workspace's own accounts count; Outflare never lends its keys, so the playbook won't start until it's connected.
- **Needs data**: the workspace doesn't collect what the playbook reads yet (for example, no site added to SEO). It can still start, and the agent reports what it found.

Opening one shows what it needs, its steps and the API operations each step uses, what it won't do, how to tell it worked, and its prompt with the inputs filled in.

## A playbook's home in a brand

A playbook with a setup kit shows **In this brand** at the top once the kit is set up: every rule, journey, list, signal type, metric and score it added, whether each is on, and what it has done (a rule's firings in 30 days, who is in a journey now, how many are on a list, how many records a score has scored). Above them are the signals it raised: how many need attention, how many are late, and how many closed as won or not useful in the last 90 days, so you can tell whether the playbook earns its place. Each line opens the rule, journey or queue itself.

**Set it up now, without a run** adds the kit's parts as drafts straight from the page, with the kit's inputs, so a playbook that is only rules and journeys needs no AI model. `GET /v1/brands/{brand}/playbooks/{id}/home` (`customers.read`, MCP `get_playbook_home`) returns the same.

### Did it work?

To find out what a playbook adds beyond what would have happened anyway, pick **Hold 10% back** (or 20%) under **Measure whether it works** when you set it up. Each journey the kit adds then starts with a random split: the held-back share of people (or companies, for an account journey) is left alone, and the rest go through the journey as usual. A journey that already has a holdout of its own keeps it. The split is the same every time for a person or company, so nobody moves between the groups.

The playbook's page then shows **Did it work?**: for each journey, how many went through it and how many were held back, how many of each did the event you measure (a payment by default) in the days after, the revenue from them, and the lift. A company's people count for their company. Until both groups have at least `min_people` it says there isn't enough to tell yet. `GET /v1/brands/{brand}/playbooks/{id}/lift?days=30&event=payment.succeeded` (`analytics.read`, MCP `get_playbook_lift`) returns the same; `holdout_percent` (0 to 50) on the kit's apply call sets the share. Holding people back means some of them miss the journey's help, so use a small share and take the holdout step out of the journey once you know.

## Your own playbooks

**New playbook** on the Playbooks page groups parts a brand already has (rules, journeys, smart lists, signal types, scores, routing rules, emails and metrics) under a name of your own, such as "Save accounts that go quiet". The playbook gets the same home as a ready-made one: every part with whether it is on and what it has done, and how the signals of its signal types ended, so one page answers whether the whole motion works. Under that, **Did it work?** compares each of its journeys that holds some people back at random with the ones it held back, the same way a ready-made playbook is measured; a journey that holds nobody back says so, because nothing shows what would have happened without it. A playbook owns none of its parts: a part can sit in several playbooks, editing a part changes it everywhere, and removing a playbook removes only the grouping, never the parts themselves. A brand can have 50 playbooks of its own, each with up to 50 parts, and one brand never sees another's.

`GET` and `POST /v1/brands/{brand}/own-playbooks`, and `GET`, `PATCH` and `DELETE /v1/brands/{brand}/own-playbooks/{id}` (`customers.read` to read, `catalog.manage` to write; MCP `list_own_playbooks`, `create_own_playbook`, `update_own_playbook`, `delete_own_playbook`). A part is `{"kind": "rule" | "journey" | "smart_list" | "signal_type" | "score" | "routing_rule" | "email" | "metric", "id": "..."}` and must belong to the brand; an email is an email template. `GET /v1/brands/{brand}/own-playbooks/{id}/lift` (`analytics.read`, MCP `get_own_playbook_lift`) is "Did it work?".

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
| `GET /v1/brands/{brand}/playbook-automations` | Automations, with status, next run and this month's spend (`missions.read`) |
| `POST /v1/brands/{brand}/playbook-automations` | Set one up as a draft (`missions.run`) |
| `PATCH`, `DELETE /v1/brands/{brand}/playbook-automations/{id}` | Change (back to draft) or delete it (`missions.run`) |
| `POST .../playbook-automations/{id}/approve` | Turn it on, or back on (`missions.approve`, a person only) |
| `POST .../playbook-automations/{id}/pause` | Pause it (`missions.run`) |
| `POST .../playbook-automations/{id}/run` | Start a run now, inside its limits (`missions.run`) |
| `GET .../playbook-automations/{id}/runs` | The runs it started: `agent_runs`, and `data` for the missions of an external runner from before |

The runs carry `automation_id`; the automation shows `last_run_id` or `last_mission_id`. MCP has the same tools, except approving.

## Setup kits

Some playbooks need things set up before their first run, such as a pipeline in the CRM, fields on people and email drafts. Such a playbook names a setup kit (`playbooks/kits/<id>.yaml`). When you open it, **What it sets up** lists each part: **Adds**, **Extends** (an option list of your own gains missing options), **Have** (already there) or **Conflict** (there under the same key but set up differently). Tick **Set up what it needs when I start** and the kit is applied in the same step as the run starts.

A kit only adds. Nothing you already have changes, email drafts are added unpublished so you read and publish them first, and while anything conflicts it changes nothing. Applying it again changes nothing. Applying needs `catalog.manage`, and `emails.manage` when it adds email drafts.

The **agreements kit** (`agreements`) adds an Agreement record type for what a customer has agreed to (a subscription, a contract, a spend commitment or a retainer): its stage, kind, term, value per term, start and renewal dates, notice period and owner, linked to its company by its `company` field. It suits any pricing model, since the kind and term are lists you can change. The company's account summary lists its agreements and flags a renewal date 60 days ahead ([customers.md](customers.md#the-account-summary)).

The **customer health kit** (`customer-health`) asks for nothing. It adds three metrics (meetings, support conversations and website visits), seven [scores](scores.md) for companies (usage trend, billing and engagement under a health score, sentiment, churn risk and expansion potential), and two draft rules and journeys that flag a company whose churn risk or expansion potential turns high. The playbook "See customer health, churn risk and who is ready to grow" applies it.

The **usage health kit** (`usage-health`) asks for the event that shows someone is using the product and adds two metrics that count the days a company did it (the last 30 days and the 30 before), a health score for companies built on them, a draft rule that fires when those days fall by two fifths, and a draft journey that puts the company in Needs attention as a churn risk with a task for its owner. A health score the brand already keeps stays as it is ([scores.md](scores.md), [metrics.md](metrics.md)). The playbook **Score account health and catch usage drops** sets it up.

The **share of wallet kit** (`share-of-wallet`) adds four fields to companies (yearly spend on your category, how sure the estimate is, where it comes from and other suppliers) and a "Room to grow" signal type. The company's page then shows your share of its spend ([customers.md](customers.md#company-groups-and-share-of-wallet)).

The **partnerships kits** (`partnerships-seo`, `partnerships-sponsorships`, `partnerships-integrations`, `partnerships-affiliates`, `partnerships-comparison-sites`) each add one partnership pipeline: a record type with its own stages and fields, a `company` field for the partner's company and a `stage` field. Presence's **Partnerships** tab sets them up per brand and shows each as a board ([coverage.md](coverage.md#partnership-pipelines)).

The first kit, **Creator partnerships**, adds a Partnerships record type with a stage field (Found, Vetted, Pitched, Negotiating, Agreed, Live, Measured, Renewed, Declined), the creator, deal type, fee, deliverables, platform, go-live date, post, content rights, tracked link and promo code; creator fields on people (platforms, audience size, topics, rate card, past sponsors); and four email drafts (brief, go-live reminder, thanks and results, an invite for customers who create). Five playbooks use it: **Run creator partnerships from first find to results**, **Find creators who already cover your competitors**, **Sponsor newsletters and podcasts that pay back**, **Seed your product to creators**, **Renew the creator deals that pay back** and **Recruit affiliates and creators from your own sign-ups**.

## API

| Call | What it does |
| --- | --- |
| `GET /v1/playbooks` | Every playbook, public, no key needed |
| `GET /v1/playbooks/{id}` | One playbook |
| `GET /v1/workspaces/{ws}/playbooks` | Playbooks with `readiness` and `runs` for the workspace (`agents.read`) |
| `GET /v1/workspaces/{ws}/playbooks/{id}` | One, with its readiness (`agents.read`) |
| `POST /v1/workspaces/{ws}/playbooks/{id}/agent-runs` | Retired: `410 mission_retired`. Start one as an agent run (`POST .../playbooks/{id}/agent-runs`, `agents.run`); `apply_kit: true` also sets up its kit |
| `GET /v1/kits`, `GET /v1/kits/{id}` | Setup kits, public, with the playbooks that use each |
| `GET /v1/workspaces/{ws}/kits/{id}/preview` | What the kit would add, extend, reuse or conflict with (`customers.read`) |
| `POST /v1/workspaces/{ws}/kits/{id}/apply` | Set it up (`catalog.manage`, and `emails.manage` for email drafts); 409 `conflict` names what's in the way |
| `GET /v1/brands/{brand}/kits/{id}/preview`, `POST /v1/brands/{brand}/kits/{id}/apply` | The same for one brand, so each brand gets its own pipeline, smart lists, rules and journeys; the workspace routes set up the workspace's first brand |
| `GET /v1/brands/{brand}/playbooks/{id}/lift` | What the playbook's journeys added, against the people held back (`analytics.read`) |
| `GET /v1/brands/{brand}/playbooks/{id}/home` | What the playbook's kit set up in the brand, whether each part is on, what it has done, and how its signals ended |
| `GET`, `POST /v1/brands/{brand}/own-playbooks`, `GET`, `PATCH`, `DELETE /v1/brands/{brand}/own-playbooks/{id}` | A brand's own playbooks: named groups of its rules, journeys, smart lists and signal types, with the same home (`customers.read`, `catalog.manage` to write) |

## Events

Starting a playbook records `playbook.started` (with `playbook_id`, `playbook_version`, `mode`, `publisher` and `kit_applied`, and `run_id`). An automation's run records it too, with `automation_id`, `trigger` (schedule, event or manual) and `events`, and `playbook_automation.ran`. Automations record `playbook_automation.created`, `updated`, `approved`, `paused`, `held` (with the reason) and `deleted`. All of these are webhook events. Applying a kit records `kit.applied` (with `kit_id`, `kit_version`, how many parts were added, extended and reused, and `brand_id` when it was set up for a brand), and each part records its own action with `kit_id`. A brand's own playbooks record `own_playbook.created`, `updated` and `deleted` (with how many `parts`). The web app records `ui.playbook_opened` and `ui.playbook_prompt_copied`.

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
