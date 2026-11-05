---
kind: improvement
area: paid-ads
---
Conversions sent from both the website and your server count once even when the two copies don't share a `messageId`: they are matched on the event name, the person and the time (within 15 minutes), and the server's copy is sent. Paid ads → Tracking health warns when this happens, with the one-line fix.
