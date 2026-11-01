---
kind: improvement
area: lifecycle
---
Rules, journeys, lifecycle email, alerts and webhooks now belong to one brand, with API routes under `/v1/brands/{brand}/`: a rule or journey acts only on its brand's people, an unsubscribe applies to the brand it came from (an admin can apply one to every brand), and a webhook hears only its brand's events.
