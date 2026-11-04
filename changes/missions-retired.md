---
kind: improvement
area: agents
---
Missions are retired. What each mission did stays readable on the Missions page, and changing one, or sending a change with the `Outflare-Mission` header, now answers that missions are retired. Agents make changes with their own token on the action layer, and playbooks run as agent runs on the Agents page.

Upgrade: agents or scripts that still send the `Outflare-Mission` header, or use the mission tools in MCP or `--mission` in the CLI, need to drop them; their changes then go through the token's own grants.
