-- The files a brand keeps: logos, fonts, product shots, screenshots,
-- example ads, voices and pictures of people. The bytes live in the
-- server's object storage (OUTFLARE_S3_*); this table says what each file
-- is. Ads and other content are built from these, so a brand's facts stay
-- in one place.

CREATE TABLE brand_assets (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- What the file is for. It decides which types of file fit.
    kind           text NOT NULL CHECK (kind IN ('logo', 'font', 'product_shot', 'screenshot', 'example_ad', 'voice', 'person', 'recording', 'other')),
    name           text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    note           text NOT NULL DEFAULT '' CHECK (length(note) <= 2000),
    tags           text[] NOT NULL DEFAULT '{}',
    -- The name the file was uploaded with, for downloads.
    filename       text NOT NULL CHECK (length(filename) BETWEEN 1 AND 255),
    -- Worked out from the file's bytes, never taken from the uploader.
    content_type   text NOT NULL,
    family         text NOT NULL CHECK (family IN ('image', 'video', 'audio', 'font')),
    bytes          bigint NOT NULL CHECK (bytes > 0),
    sha256         text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    -- Pictures only; null when unknown.
    width          integer CHECK (width > 0),
    height         integer CHECK (height > 0),
    -- Whether smaller copies of the picture can be made.
    resizable      boolean NOT NULL DEFAULT false,
    -- How it got here: a person or agent uploaded it, or Outflare made it.
    source         text NOT NULL DEFAULT 'upload' CHECK (source IN ('upload', 'generated')),
    -- A voice or a person's picture may be used only once someone has
    -- said the person agreed, and how (a signed release, an email).
    consent_at     timestamptz,
    consent_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    consent_note   text NOT NULL DEFAULT '' CHECK (length(consent_note) <= 2000),
    archived_at    timestamptz,
    version        integer NOT NULL DEFAULT 1,
    created_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX brand_assets_brand_idx ON brand_assets (workspace_id, brand_id, kind, created_at DESC);
-- The same file twice in one brand is one asset.
CREATE UNIQUE INDEX brand_assets_file_idx ON brand_assets (brand_id, sha256);
