---
kind: improvement
area: deliverability
---
Tracking domains can get their HTTPS certificate automatically on servers that support it, and each tracking domain now shows whether HTTPS is set up (`https_status`). Links only switch to a tracking domain once its certificate is ready.

Upgrade: migration 0227 adds two columns to tracking domains. `OUTFLARE_TRACKING_TARGET` optionally sets the host customers point their CNAME at, and `OUTFLARE_EDGE_SECRET` lets an edge proxy in front of tracking domains pass on the domain and visitor address.
