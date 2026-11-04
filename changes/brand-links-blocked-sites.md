---
kind: fix
area: social
---
Find links no longer gives up silently on a brand website that shows a bot check or loads slowly: Outflare tries once more with JavaScript on, then tells you under Links why it couldn't read the site, without retrying and spending more Geonode tokens.

Upgrade: runs migration 0412.
