---
kind: launch
area: analytics
---
Direct tracking for tracker campaigns: an ad can link straight to your page with the campaign's code, and the site script records the click itself, with the traffic source's click id, cost and fields. outflare.clickId() and {ofclk} in your links pass the click id on to offers, so postbacks still find the click.

Upgrade: runs migration 0533. Copies of site.js you host yourself need the new version (17) for direct mode.
