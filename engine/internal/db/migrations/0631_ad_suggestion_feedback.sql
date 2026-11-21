-- A person can say whether a paid ads finding was good or bad advice, and
-- why. The rating and reason stay on the finding, and the reason is also
-- saved as one of the brand's memories (feedback_memory_id), which agents
-- read, so the same advice isn't repeated.
--
-- Expand only (docs/migrations.md): columns with defaults the previous
-- version never reads.
ALTER TABLE ad_suggestions
    ADD COLUMN feedback text NOT NULL DEFAULT '' CHECK (feedback IN ('', 'good', 'bad')),
    ADD COLUMN feedback_note text NOT NULL DEFAULT '' CHECK (length(feedback_note) <= 400),
    ADD COLUMN feedback_by uuid REFERENCES actors(id) ON DELETE SET NULL,
    ADD COLUMN feedback_at timestamptz,
    ADD COLUMN feedback_memory_id uuid REFERENCES memories(id) ON DELETE SET NULL;
