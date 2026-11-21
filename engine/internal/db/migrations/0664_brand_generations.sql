-- Pictures, video and voice a brand asked one of its own AI accounts to
-- make (OpenAI, fal.ai, ElevenLabs). Each row is one request: what was
-- asked, which connection made it, where it stands, and the brand file
-- it became. A request that takes minutes keeps the provider's run id,
-- so it is asked about, never started (and paid for) twice.

CREATE TABLE brand_generations (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    kind           text NOT NULL CHECK (kind IN ('image', 'video', 'voice')),
    -- The connector that makes it, and the connection it runs on.
    connector      text NOT NULL,
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- The provider's own id for the model: always the asker's choice.
    model          text NOT NULL CHECK (length(model) BETWEEN 1 AND 200),
    prompt         text NOT NULL CHECK (length(prompt) BETWEEN 1 AND 100000),
    voice          text NOT NULL DEFAULT '',
    -- The model's own settings, sent to the provider as they are.
    options        jsonb NOT NULL DEFAULT '{}',
    -- What the finished file is kept as among the brand's files.
    asset_kind     text NOT NULL DEFAULT 'other',
    name           text NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    status         text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    -- The provider's id for work still going on.
    run_id         text NOT NULL DEFAULT '',
    error_code     text NOT NULL DEFAULT '',
    error          text NOT NULL DEFAULT '',
    asset_id       uuid REFERENCES brand_assets (id) ON DELETE SET NULL,
    -- What the provider reported it used: numbers only.
    usage          jsonb NOT NULL DEFAULT '{}',
    created_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    started_at     timestamptz,
    finished_at    timestamptz
);
CREATE INDEX brand_generations_brand_idx ON brand_generations (workspace_id, brand_id, created_at DESC);
