---
kind: improvement
area: analytics
---
Tracker conversions now say whether the traffic source was told, and if not, why (suspect, filtered by the postback rules, no traffic source and so on). A postback that carries both the network's own click id as cid and Outflare's ofclk credits the click, and a send that went out without the network's click id is marked.
