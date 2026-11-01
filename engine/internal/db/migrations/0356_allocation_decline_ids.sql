-- Declines become something a person can see and clear: each gets an id
-- (DELETE /v1/workspaces/{ws}/allocation/declines/{id}), when it was made,
-- and why. source is declined (a person turned a suggestion down), undone
-- (a person undid the allocator's own choice) or refused (the sequencer
-- platform would not take the mailbox for that campaign); reason is the
-- platform's words for a refusal. Older rows read as declined.
ALTER TABLE allocation_declines
    ADD COLUMN id          uuid NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN declined_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN source      text NOT NULL DEFAULT 'declined' CHECK (source IN ('declined', 'undone', 'refused')),
    ADD COLUMN reason      text NOT NULL DEFAULT '' CHECK (length(reason) <= 1000);

CREATE UNIQUE INDEX allocation_declines_id ON allocation_declines (id);
CREATE INDEX allocation_declines_workspace ON allocation_declines (workspace_id, declined_at DESC);
