---
kind: improvement
area: analytics
---
Each brand has a reporting time zone. Attribution and paid ads reports count days in it (with `?timezone=` for one report), and tracker caps and path rules by day or hour follow the brand's midnight. Ad platforms' own numbers stay in their accounts' time zones.

Upgrade: runs migration 0538.
