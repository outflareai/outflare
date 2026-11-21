---
kind: improvement
area: paid-ads
---
Conversions sent to ChatGPT Ads now carry the person's IP address (when your server passes it on as `context.ip`), their browser and their country, so OpenAI can match more of them to an ad click. The browser comes from the event, or from the person's latest visit recorded by the Outflare site script, so your server doesn't need to send it.
