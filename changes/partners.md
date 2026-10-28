---
kind: launch
area: partners
---
Partners: run referral, affiliate and influencer programs. Partners get ref codes, short links, promo codes and a portal (or a widget in your app); your backend reports sign-ups, sales and refunds by API or postback URL (each also counted in attribution); rewards (cash, fees, or actions your own system grants, for one or both sides) wait out a hold, then are approved and marked paid.

Upgrade: runs migration 0235. Set `OUTFLARE_PARTNERS_DOMAIN` (with wildcard DNS and a wildcard certificate) to serve partner portals at `<subdomain>.<that domain>`; without it, portals are at `/p/<program>/` and on programs' own domains.
