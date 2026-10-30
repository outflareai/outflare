-- Privacy export (plans/brands/README.md): everything a brand holds about
-- one person or company, as a file. POST /v1/brands/{brand}/records/{record}/
-- privacy-exports makes a pending row and queues the privacy_export.build
-- job, which gathers the record and every row that points at it (the
-- tables in customers.Follows and customers.Owned) in the record's brand,
-- and stores the result as gzipped JSON in content. The file can be
-- downloaded for 7 days (expires_at); then the sweep marks the row expired
-- and deletes the file.
--
-- Postgres is Outflare's only dependency, so the file is kept here rather
-- than in optional object storage. The row itself holds nothing personal
-- beyond the file: no name, email or copy of the record. Erasing the
-- record deletes its exports (customers.Erased).
CREATE TABLE privacy_exports (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    record_id     uuid REFERENCES records (id) ON DELETE SET NULL,
    status        text NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'ready', 'failed', 'expired')),
    -- Who asked, so the job records the result as theirs.
    actor_id      uuid REFERENCES actors (id) ON DELETE SET NULL,
    actor_type    text NOT NULL DEFAULT 'system',
    user_id       uuid REFERENCES users (id) ON DELETE SET NULL,
    token_id      uuid REFERENCES api_tokens (id) ON DELETE SET NULL,
    request_id    text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz,
    -- When the file stops being downloadable: 7 days after it is ready.
    expires_at    timestamptz,
    -- The file's size (gzipped) and the uncompressed JSON's.
    size_bytes    bigint,
    raw_bytes     bigint,
    -- Rows exported, by table.
    counts        jsonb NOT NULL DEFAULT '{}',
    error_code    text,
    -- The gzipped JSON file, deleted when the export expires.
    content       bytea,
    downloads     integer NOT NULL DEFAULT 0
);

-- A brand's exports, newest first, and one record's.
CREATE INDEX privacy_exports_brand_idx ON privacy_exports (brand_id, created_at DESC, id DESC);
CREATE INDEX privacy_exports_record_idx ON privacy_exports (record_id, created_at DESC) WHERE record_id IS NOT NULL;
-- The expiry sweep.
CREATE INDEX privacy_exports_expiry_idx ON privacy_exports (expires_at) WHERE status = 'ready';
-- Builds that never finished.
CREATE INDEX privacy_exports_pending_idx ON privacy_exports (created_at) WHERE status = 'pending';
