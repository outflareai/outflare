---
kind: fix
area: agents
---
AI apps that ask before changes, such as Claude, can now read with any Outflare tool without an approval prompt: the new `call_read_tool` runs only tools that read, and `call_tool` stays the way to change things. Reading dashboards and their tiles is now in the core tool set.
