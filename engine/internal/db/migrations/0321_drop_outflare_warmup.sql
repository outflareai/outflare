-- Outflare's own warm-up is gone (0091 turned it off): a connected
-- sequencer runs warm-up. Nothing has written warm-up messages since, and
-- since v0.46.0 nothing reads them or the ramp settings either, so they go.
-- migration-safety: the release before this one (v0.46.0 on) no longer reads warmup_messages, warmup_daily_target, warmup_reply_rate or warmup_checked_at

DROP TABLE warmup_messages;

ALTER TABLE mailboxes
    DROP COLUMN warmup_daily_target,
    DROP COLUMN warmup_reply_rate,
    DROP COLUMN warmup_checked_at;
