---
kind: improvement
area: workspace
---
Each brand now has its own senders, inbox tags, saved inbox views, alert mutes and switched-on features. Every brand starts with a copy of what the workspace had, and changing one brand's no longer changes another's. New routes under `/v1/brands/{brand}/` (`senders`, `inbox-tags`, `inbox-views`, `alerts/mutes`); the workspace routes act on `?brand=`, else the first brand.
