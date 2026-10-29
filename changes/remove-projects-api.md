---
kind: improvement
area: api
---
Projects are gone from the API and the MCP server: brands and sequences replace them. A brand's mailboxes now send for any of its sequences, a project's reply rules became its sequences' rules, and outcome reports group by sequence or brand. Older clients that still send `project_id` keep working; it is ignored, except on new API tokens and reply rules, where it is refused.

Upgrade: API tokens limited to one project are now workspace tokens with the same scopes, so each can see and change the whole workspace. Review them on the API tokens page and revoke any that should stay narrow.
