-- mailboxes.suggested_sender_id was allocation's guess at a mailbox's
-- sender, waiting for a person to confirm it (0011). Nothing has read or
-- written it since v0.76.0.
-- migration-safety: the release before this one (v0.76.0 on) never uses the column

ALTER TABLE mailboxes DROP COLUMN suggested_sender_id;
