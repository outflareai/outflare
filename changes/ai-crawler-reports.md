---
kind: improvement
area: analytics
---
AI crawler visits without Cloudflare: your server can now report them. Call `crawlerVisit` (`crawler_visit` in Python) in the server library for every request, and AI visibility shows how often GPTBot, ClaudeBot, PerplexityBot and the others read each site, per day. Only the counts are kept.
