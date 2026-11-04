---
kind: fix
area: self-hosting
---
Background jobs no longer stall for up to five minutes when the server restarts or deploys: running jobs are stopped and handed straight back to the queue, and a restart no longer uses up one of a job's retries.

Upgrade: `OUTFLARE_SHUTDOWN_GRACE` (default `10s`) sets how long running jobs get to stop on `SIGTERM`. Keep it under your platform's stop timeout (docs/scaling.md, "Restarts and deploys").
