# 8. The built-in agent is just another agent on the mission gate

Status: Accepted (23 October 2026)

## Decision

Outflare ships its own agent so a person can type a goal and have it carried out without running an agent of their own. Planning (`POST /missions/plan`) and running (`runner: builtin` on approve) both use the workspace's own AI model key: originally Claude only, now the model chosen under AI settings (see [ADR 9](0009-ai-models.md)). Outflare never pays for or resells model usage; each plan and turn is recorded in the outcomes ledger against the mission.

The built-in agent gets an API token of its own when a mission is handed to it. The secret is never kept, so only the engine can act as it. Its calls are made in-process through the full HTTP handler with the `Outflare-Mission` header, exactly like an outside agent's: scopes, the mission gate (dry runs, held writes, limits), policy checks, the call log and audit all apply. The planner and the agent are told nothing the API would not tell them, and the planner only gets counts and names, never leads or credentials.

The agent works in turns rather than as a long-running process. A turn starts fresh from the mission check-in and ends with `finish_turn`, which says when to look again. Turns are claimed with a lease column (`turn_started_at`) so two engines never run the same mission, and are brought forward when a person writes or decides on a held change.

## Why

One gate for every agent means the built-in agent cannot do anything an outside agent with the same token could not, and the person sees its work in the same call log. Turns keep each model call small and cheap, survive restarts, and make "the agent remembers only what is in the mission" true by construction.

## Consequences

- The built-in agent is only as capable as the API and its guide (generated from the OpenAPI document), so every new endpoint is usable by it without extra work.
- Problems the person must fix (no key, spend limit, a refusal, repeated failures) pause the mission with a reason instead of retrying forever.
- A turn's cost is known only afterwards; the spend check before a turn assumes 25 cents.
