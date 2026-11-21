---
kind: improvement
area: self-hosting
---
Self-hosted servers can read connected ad platforms more often than once an hour: set `OUTFLARE_ADS_SYNC_INTERVAL` to a duration from `15m` to `24h` (such as `30m`). No platform is read more often than its floor (half an hour for most, a quarter of an hour for Zeropark), so a shorter setting reads at the floor.

Upgrade: nothing to do; without the variable, syncs stay hourly. Two new metrics, `outflare_ads_sync_overdue` and `outflare_ads_sync_oldest_seconds`, a dashboard row and the alert `OutflareAdSyncOverdue` say when a platform's connections fall behind.
