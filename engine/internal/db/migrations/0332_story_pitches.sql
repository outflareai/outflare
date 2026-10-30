-- Digital PR P4: pitching an approved story to journalists through one of
-- the workspace's Outreach sequences, and its results. A pitch is one
-- journalist (a person record), the sequence that writes to them, and the
-- opener the workspace's AI wrote from the story and their recent article
-- (the step's {{custom.opener}}). A person approves each one; approving
-- enrolls the journalist's lead, held until the story's embargo. Replies
-- are read as covering it, wants the data, wants an interview, not my
-- beat, declined or other.

CREATE TABLE story_pitches (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    story_id     uuid NOT NULL REFERENCES stories (id) ON DELETE CASCADE,
    sequence_id  uuid NOT NULL REFERENCES sequences (id) ON DELETE CASCADE,
    -- The journalist, and their lead once one is made.
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    lead_id      uuid REFERENCES leads (id) ON DELETE SET NULL,
    -- Their recent article the opener mentions, if any.
    article_id   uuid REFERENCES news_articles (id) ON DELETE SET NULL,
    -- writing (the AI opener is being written), draft (waits for a
    -- person), enrolled, rejected.
    status       text NOT NULL DEFAULT 'writing' CHECK (status IN ('writing', 'draft', 'enrolled', 'rejected')),
    opener       text NOT NULL DEFAULT '' CHECK (length(opener) <= 2000),
    -- Why there is no opener, or why it was rejected.
    reason       text NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    reply_kind   text CHECK (reply_kind IN ('covering', 'wants_data', 'wants_interview', 'not_my_beat', 'declined', 'other')),
    replied_at   timestamptz,
    approved_by  uuid REFERENCES actors (id) ON DELETE SET NULL,
    approved_at  timestamptz,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX story_pitches_story_idx ON story_pitches (workspace_id, story_id, created_at DESC);
CREATE INDEX story_pitches_lead_idx ON story_pitches (workspace_id, lead_id) WHERE lead_id IS NOT NULL;
CREATE INDEX story_pitches_record_idx ON story_pitches (workspace_id, record_id, approved_at);
-- One open pitch per journalist per story.
CREATE UNIQUE INDEX story_pitches_open_idx ON story_pitches (story_id, record_id) WHERE status IN ('writing', 'draft', 'enrolled');

-- Coverage a person put on a story. Left null, a story's results count
-- coverage by the journalists it pitched, after the pitch.
ALTER TABLE coverage ADD COLUMN story_id uuid REFERENCES stories (id) ON DELETE SET NULL;
CREATE INDEX coverage_story_idx ON coverage (story_id) WHERE story_id IS NOT NULL;
