---
kind: improvement
area: paid-ads
---
Send a conversion such as a sign-up from both the website and your server with the same event id, and Outflare sends it to ad platforms once. The website script's `outflare.track()` now takes your own id as `messageId`.
