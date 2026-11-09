---
kind: fix
area: agents
---
A web search an agent asks for now belongs to the brand it searches for: `POST /v1/brands/{brand}/search-results` (MCP `search_web` takes `brand`) replaces the workspace route, so the approval waits under that brand alone on the Agents page, and its cost and event carry the brand. A change that names no brand says "Every brand" on its card when a brand is picked.
