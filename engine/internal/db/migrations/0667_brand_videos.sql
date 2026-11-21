-- Talking videos a brand makes on its own AI accounts, stage by stage:
-- a voice-over from the script, the time of every word, the video
-- rendered to that voice from a picture of the person and scene, an
-- upscale, and a finish with the look of a phone camera and captions.
-- Each stage keeps what it made, so one stage can be done again, and a
-- word can be fixed or removed later without paying for the whole video.

CREATE TABLE brand_videos (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name            text NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    -- What is said, and how the video should look and move.
    script          text NOT NULL CHECK (length(script) BETWEEN 1 AND 5000),
    direction       text NOT NULL DEFAULT '' CHECK (length(direction) <= 10000),
    -- The picture of the person in the scene the video starts from.
    scene_asset_id  uuid REFERENCES brand_assets (id) ON DELETE SET NULL,
    -- The model and settings of every stage: always the asker's choice.
    settings        jsonb NOT NULL DEFAULT '{}',
    status          text NOT NULL DEFAULT 'working' CHECK (status IN ('working', 'ready', 'failed')),
    -- The stage being worked on; 'done' once ready.
    stage           text NOT NULL DEFAULT 'voice',
    -- 0 while it is first made; one more for every edit.
    round           integer NOT NULL DEFAULT 0,
    -- The edit being made: which words, and what to do with them.
    edit            jsonb,
    error_code      text NOT NULL DEFAULT '',
    error           text NOT NULL DEFAULT '',
    voice_asset_id  uuid REFERENCES brand_assets (id) ON DELETE SET NULL,
    -- The video as the model rendered it, as it stands before the finish
    -- (upscaled, edited), and finished.
    render_asset_id uuid REFERENCES brand_assets (id) ON DELETE SET NULL,
    base_asset_id   uuid REFERENCES brand_assets (id) ON DELETE SET NULL,
    final_asset_id  uuid REFERENCES brand_assets (id) ON DELETE SET NULL,
    -- [{"text", "start", "end"}]: every word and when it is said.
    words           jsonb NOT NULL DEFAULT '[]',
    -- Things a person should know: a stage that was skipped, a word the
    -- voice read differently from the script.
    notes           jsonb NOT NULL DEFAULT '[]',
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz
);
CREATE INDEX brand_videos_brand_idx ON brand_videos (workspace_id, brand_id, created_at DESC);

-- What each stage did: the generation it ran on the provider (with its
-- recipe) or the work done on the server, and the file it made.
CREATE TABLE brand_video_steps (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    video_id      uuid NOT NULL REFERENCES brand_videos (id) ON DELETE CASCADE,
    round         integer NOT NULL DEFAULT 0,
    stage         text NOT NULL,
    status        text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'failed', 'skipped')),
    generation_id uuid REFERENCES brand_generations (id) ON DELETE SET NULL,
    asset_id      uuid REFERENCES brand_assets (id) ON DELETE SET NULL,
    detail        jsonb NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz,
    UNIQUE (video_id, round, stage)
);
CREATE INDEX brand_video_steps_generation_idx ON brand_video_steps (generation_id) WHERE generation_id IS NOT NULL;
