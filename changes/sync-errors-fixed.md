---
kind: fix
area: deliverability
---
Opt-out syncing with EmailBison, Instantly and Smartlead keeps going when reading their blocklist fails: your opt-outs still go out, their list is read every 6 hours instead of every few minutes, and a failed try waits 30 minutes. Website tracking no longer answers with an error when a company lookup for a visitor fails.
