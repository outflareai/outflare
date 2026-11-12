---
kind: fix
area: agents
---
Every Outflare tool now works in ChatGPT and other apps that cap how many tools they show. Apps see about 50 core tools, including email templates, send email and broadcasts, plus `find_tools` to search all of Outflare's tools and `call_tool` to run any of them. Connect to `/mcp?tools=all` to list every tool instead. The MCP server now reports the Outflare version it runs.
