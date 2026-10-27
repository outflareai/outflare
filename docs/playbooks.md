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

## API

| Call | What it does |
| --- | --- |
| `GET /v1/playbooks` | Every playbook, public, no key needed |
| `GET /v1/playbooks/{id}` | One playbook |
| `GET /v1/workspaces/{ws}/playbooks` | Playbooks with `readiness` and `runs` for the workspace (`missions.read`) |
| `GET /v1/workspaces/{ws}/playbooks/{id}` | One, with its readiness (`missions.read`) |
| `POST /v1/workspaces/{ws}/playbooks/{id}/runs` | Start one as a mission draft (`missions.run`) |

## Events

Starting a playbook records `playbook.started` (with `playbook_id`, `playbook_version`, `mode` and `publisher`) beside `mission.created`. The web app records `ui.playbook_opened` and `ui.playbook_prompt_copied`.
