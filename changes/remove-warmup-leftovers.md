---
kind: improvement
area: api
---
The last numbers from Outflare's old built-in warm-up are gone: `GET /mailboxes/{id}/warmup`, the 7-day warm-up counts and inbox rates on mailboxes, domains, stats, reports, mailbox health and the infrastructure overview, and the ignored `daily_target` and `reply_rate` settings. Warm-up runs in a connected sequencer, and its score is on each mailbox's `warmup.platform`.
