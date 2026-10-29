---
kind: improvement
area: workspace
---
Agency accounts are gone for now, to keep Outflare simple: a workspace is one company, with its brands inside it. The Agency page, the `/v1/agencies` API, agency tokens (`ofa_`), the MCP tools `list_clients`, `use_client` and `create_client`, and agency white label are removed. Client workspaces stay, with their direct members and data.

Upgrade: agency tokens stop working, and people who reached a workspace only through an agency lose access to it; add them as members of that workspace first. No migration runs; the agency tables stay until a later release.
