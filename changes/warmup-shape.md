---
kind: improvement
area: api
---
A mailbox's `warmup.provider` is now null when no connected sequencer runs its warm-up, and `warmup.platform` is gone: the sequencer's warm-up state and score are its entry in `platforms`. To stop warm-up, set `enabled` to false; the `outflare` provider is no longer accepted.
