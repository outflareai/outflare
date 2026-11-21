-- Working on a creative after it is made, without losing anything:
-- a fix to a talking video waits for a person's "keep" or "go back"
-- before it replaces anything, every version is kept and can be gone
-- back to, a video can be liked or marked "not this" with a reason,
-- variations point at the video they were made from, lessons a person
-- gave are kept for every later creative, and one history per creative
-- holds what happened to it, who did it and what they said.

-- Every state a talking video has been in. One is current; a fix that
-- waits for the person's answer is 'waiting'.
CREATE TABLE brand_video_versions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    video_id       uuid NOT NULL REFERENCES brand_videos (id) ON DELETE CASCADE,
    number         integer NOT NULL,
    -- current: what the video is now. waiting: a fix nobody has answered.
    -- before: an earlier version. gone_back: a fix the person turned down.
    state          text NOT NULL CHECK (state IN ('current', 'waiting', 'before', 'gone_back')),
    base_asset_id  uuid REFERENCES brand_assets (id) ON DELETE SET NULL,
    final_asset_id uuid REFERENCES brand_assets (id) ON DELETE SET NULL,
    words          jsonb NOT NULL DEFAULT '[]',
    notes          jsonb NOT NULL DEFAULT '[]',
    -- The edit that made this version; null for the first.
    edit           jsonb,
    -- Why the person went back, in their words.
    reason         text NOT NULL DEFAULT '' CHECK (length(reason) <= 200),
    note           text NOT NULL DEFAULT '' CHECK (length(note) <= 1000),
    decided_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    decided_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (video_id, number)
);
CREATE INDEX brand_video_versions_brand_idx ON brand_video_versions (workspace_id, brand_id);

ALTER TABLE brand_videos
    -- The number of the version that waits for keep or go back.
    ADD COLUMN waiting_version integer,
    -- The video this one is a variation of, and the one thing changed.
    ADD COLUMN variation_of uuid REFERENCES brand_videos (id) ON DELETE SET NULL,
    ADD COLUMN variation_change jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN rating text NOT NULL DEFAULT '' CHECK (rating IN ('', 'liked', 'not_this')),
    ADD COLUMN rating_reason text NOT NULL DEFAULT '' CHECK (length(rating_reason) <= 500),
    ADD COLUMN rated_by uuid REFERENCES actors (id) ON DELETE SET NULL,
    ADD COLUMN rated_at timestamptz;
CREATE INDEX brand_videos_variation_idx ON brand_videos (variation_of) WHERE variation_of IS NOT NULL;

ALTER TABLE creatives
    ADD COLUMN rating text NOT NULL DEFAULT '' CHECK (rating IN ('', 'liked', 'not_this')),
    ADD COLUMN rating_reason text NOT NULL DEFAULT '' CHECK (length(rating_reason) <= 500),
    ADD COLUMN rated_by uuid REFERENCES actors (id) ON DELETE SET NULL,
    ADD COLUMN rated_at timestamptz;

-- What a person taught the brand's creatives: how to say a word (given
-- to every voice), or a rule (added to every prompt).
CREATE TABLE creative_lessons (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('say', 'rule')),
    -- For 'say': the word as written, and how to spell it for a voice.
    word         text NOT NULL DEFAULT '' CHECK (length(word) <= 100),
    say          text NOT NULL DEFAULT '' CHECK (length(say) <= 200),
    -- For 'rule': the rule in the person's words.
    rule         text NOT NULL DEFAULT '' CHECK (length(rule) <= 500),
    -- Which creatives a rule is for: image, video or all.
    applies_to   text NOT NULL DEFAULT 'all' CHECK (applies_to IN ('all', 'image', 'video')),
    -- Where it came from: the video or creative being fixed or rated.
    source_type  text NOT NULL DEFAULT '' CHECK (source_type IN ('', 'video', 'creative')),
    source_id    uuid,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    removed_at   timestamptz
);
CREATE INDEX creative_lessons_brand_idx ON creative_lessons (workspace_id, brand_id) WHERE removed_at IS NULL;

-- One history per creative: what happened, who did it, the words they
-- said. Never changed or deleted while the creative exists.
CREATE TABLE creative_history (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    subject_type text NOT NULL CHECK (subject_type IN ('video', 'creative')),
    subject_id   uuid NOT NULL,
    -- made, fix_asked, fix_ready, fix_failed, fix_kept, gone_back,
    -- version_restored, liked, not_this, rating_cleared, variation_made,
    -- words_changed, approved, rejected.
    kind         text NOT NULL CHECK (length(kind) BETWEEN 1 AND 40),
    -- The person's own words: what was wrong, why not.
    said         text NOT NULL DEFAULT '' CHECK (length(said) <= 2000),
    -- The version it is about, and anything else worth showing.
    data         jsonb NOT NULL DEFAULT '{}',
    actor_id     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX creative_history_subject_idx ON creative_history (workspace_id, brand_id, subject_type, subject_id, created_at);
