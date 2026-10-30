-- Drafting a story's release with the workspace's own AI: whether a draft
-- is being written, and why the last one failed.
ALTER TABLE stories
    ADD COLUMN ai_status text NOT NULL DEFAULT '' CHECK (ai_status IN ('', 'drafting', 'failed')),
    ADD COLUMN ai_error  text NOT NULL DEFAULT '' CHECK (length(ai_error) <= 500);
