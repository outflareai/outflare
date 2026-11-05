-- How many inbox syncs of a mailbox have failed in a row. A mailbox that
-- keeps failing is tried less often, and one whose sign-in the server keeps
-- refusing turns to error so the person connects it again
-- (engine/internal/inbox/sync.go). A sync that works sets it back to 0.
ALTER TABLE mailboxes ADD COLUMN sync_failures int NOT NULL DEFAULT 0;
