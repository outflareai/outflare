---
kind: improvement
area: api
---
The API calls a reply's tags `tags`: inbox messages return `tags` instead of `labels`, triaging a reply takes `tags`, the `inbox.updated` event reports changed `tags`, and the MCP tool `triage_inbox_message` takes `tags`. "Label" now always means a reply's category, such as interested or not_now.
