---
kind: fix
area: crm
---
A connected sequencer's draft campaign can now be linked to a sequence before anyone starts it there: name it in `link_campaigns` on `POST /v1/workspaces/{ws}/platforms/{id}/sync` (or the `sync_platform` MCP tool) and it comes in as a draft sequence, so it can be chosen as a campaign. Syncs still link live campaigns by themselves and leave other drafts out.
