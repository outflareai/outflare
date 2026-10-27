# 7. Agent missions gate ordinary API calls with a header

Status: Accepted (23 October 2026)

## Decision

An agent acts inside a mission by sending `Outflare-Mission: <id>` on its ordinary API calls, not through a separate mission API. The gate sits in `inWorkspace`, after authentication and the scope check, so it covers every workspace endpoint, including ones added later (connectors, projects, infrastructure).

For each call the gate checks that the caller may act in the mission (the mission's token, or any holder of `missions.run` when it has none), logs the call in `mission_calls`, and then, by status and mode, runs the write, records it as a dry run, or holds it in `mission_actions` for a person. Approving a held write replays the stored request through the full handler as the agent that made it, so every scope, limit, policy check and audit rule applies at the moment it runs. Reads always run.

An agent never approves its own mission or writes: approval needs `missions.approve` and is refused to the mission's own token and to the actor that made the write. The mission's limits sit on top of the token's: `max_sends` in the sending gate (check 3c), `max_spend_cents` through `missions.CheckSpend` before paid connector calls, against the outcomes ledger.

## Why

Agents already know the API and the MCP tools. A header lets the same calls be rehearsed, approved or run without a second set of endpoints to keep in step, and the dashboard can show exactly what an agent did because the record is the calls themselves. Replaying approved writes, instead of trusting a pre-computed result, means an approval can never bypass a rule that changed in the meantime.

## Consequences

- A dry run records the request but does not validate it; validation happens when it is approved and runs.
- Writes that run are logged after their own transaction; a failure to write the log is logged and never undoes the call.
- Missions read the project brief from `projects` and progress from sequences, `meetings` and `spend_entries`, so they need no copy of either.
