# 13. The action layer: one gate for every agent

Status: Accepted (1 November 2026). Step 1 (grants, held changes, single and batch approvals), step 2 (approval by template, webhook events), spend limits on grants, and step 3 (agent runs: Outflare's own agent runs playbooks on the action layer) are in place. Replaces [ADR 7](0007-agent-missions.md) and [ADR 8](0008-builtin-agent.md) once missions are retired; until then both gates work side by side. Keeps [ADR 11](0011-open-core-line.md): the gate is open, like every tool.

## Decision

Every change an agent makes goes through one gate, the **action layer**, whether the agent is someone's own AI app over MCP, a script, a playbook automation or a loop running in Outflare Cloud. An agent no longer has to work inside a mission to be held, limited and logged: those come from its API token.

- **An API token is an agent when its `action_gate` is on.** Its reads run as before. Each change it makes becomes an **action**: the gate decides it, keeps it, and runs it, holds it for a person, or refuses it.
- **Grants say what an agent may do.** A grant names a token, a path pattern (the part after `/v1/workspaces/{ws}/`, with `*` for one segment and a trailing `**` for what is under it), an optional method, a mode (`run`, `hold` or `refuse`), an optional brand and an optional daily limit. The most specific match decides: one naming the method beats one that doesn't, then the longest path; on a tie the stricter mode wins. **No match means hold**, so a new agent starts as a copilot.
- **Some changes always wait for a person**, whatever the grants say: the writes ADR 8 never let Outflare's own agent make alone (mailboxes, sending servers, senders, sign-ins, tokens, webhooks, connections, suppressions, company context) and the fixes [fix autonomy](../fix-autonomy.md) holds.
- **Only a signed-in person sets grants** (`actions.manage`, which no token can hold), and only a person turns a token's gate off. An agent can never widen its own room.
- **Approving replays the stored request** through the full handler as the agent, as ADR 7 did, so every scope, limit, policy check and audit rule applies when it runs. Each action keeps a sha256 of its method, path, query and body: what the person approved is exactly what runs. Deciding is a person's, or an approver token's set up outside the layer: an agent token on the action layer never decides, its own actions or another agent's. A held action nobody decides within 30 days expires.
- **Approvals come one at a time or in batches** (`POST .../actions/decide`, up to 100), each decided on its own.
- **Or by template:** a grant can match fields of the change's body (`match`), so a person approves a pattern once and every change that fits it runs. Approving a held change with `remember` makes one from it.
- **Every action is kept**, run, held, refused or decided, with the grant and rule that decided it, and each decision writes its audit event in the same transaction, so it is a product event.

## Why

Missions mixed two things: a goal an agent works toward, and the safety around what it may change. Agents now come from many places and work for weeks, so the safety belongs to the agent's token, not to a mission someone has to start and approve first. One gate also means an agent in Outflare Cloud is held to exactly the open rules an outside agent is, and self-hosters keep hold-for-approval, limits and the log for their own agents.

Grants build on what [fix autonomy](../fix-autonomy.md) already showed works: a default where a person approves everything, rules that let named kinds of change through, and things that always need a person.

## Consequences

- Missions keep working until they are retired. A token whose `action_gate` is on no longer needs a mission for its changes; one whose gate is off behaves as before.
- **Spend limits on grants.** A grant that runs changes can cap what they spend on the workspace's own accounts a day and a month (`daily_spend_cents`, `monthly_spend_cents`). Spend booked while a change runs is linked to it in the spend ledger; the gate adds up the grant's spend so far plus what the next change is likely to cost (the average of its recent changes) under one lock per grant, and holds the change for a person when the limit would be passed. The estimate is a reservation of the simplest kind: a first change under a grant has no history and runs.
- **Agent runs.** Outflare's own agent runs a playbook as an agent token of its own (`agent_runs`): the mode a person chooses (copilot, autonomous with a monthly spend cap, dry run) becomes the token's grants, the runner takes turns as it did for missions, and every change is an action. Next steps, each its own change: playbook automations starting agent runs, the web app's pages for runs, and then retiring missions, with a deprecation period for the `Outflare-Mission` header.
- The `MissionHeld` 202 answer now also covers an action held for a person (`ActionHeld`), so clients read `action` or `mission_action`.
- An extension's loop acts as an agent token too: `Host.Agents().Create` makes one (no secret kept, the gate on, at most an operator's scopes, never approving) and `Agents().Call` makes its API calls in process through the full handler, so Outflare Cloud's agents are held to exactly the rules an outside agent is. `Agents().Revoke` stops a loop by revoking its token, with the audit event. A loop's calls, and held changes replaying on approval, are the `agent` surface in `api.called` events and the request log. `Host.EnqueueJobAt` schedules a loop's next turn on the engine's queue, and `APIGuide`/`APIDocs` give its model the same API guide the built-in agent reads.
