---
kind: improvement
area: analytics
---
Feature flags and A/B tests can now be decided in your own server's memory: `LocalFlags` in the TypeScript and Python libraries reads a brand's flag definitions every 30 seconds and picks each visitor's version without a request to Outflare per page, with the same answers as the website script.
