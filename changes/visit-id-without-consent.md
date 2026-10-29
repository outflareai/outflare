---
kind: improvement
area: analytics
---
Visitors who haven't consented to analytics keep one id for their whole visit, in the tab's session storage and never in a cookie, so every page counts as the same visitor and shows them the same A/B test version. It is gone when the tab closes.
