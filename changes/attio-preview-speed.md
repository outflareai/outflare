---
kind: fix
area: crm
---
Importing from Attio no longer gets stuck on the first screen for large workspaces. The preview reads your objects in parallel and stops counting after about 15 seconds, showing counts like "5,000+" when there are more.
