-- Moving a partner program over from another platform (Tolt), with the
-- workspace's own connection. An import can run as often as wanted until
-- switch-over day: each run reads everything and stores only what is new
-- or changed, matched by the platform's own ids.

CREATE TABLE partner_imports (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    program_id      uuid NOT NULL REFERENCES partner_programs (id) ON DELETE CASCADE,
    integration_id  uuid REFERENCES integrations (id) ON DELETE SET NULL,
    connector       text NOT NULL,
    -- The program's id on the platform.
    -- '' takes the account's only program.
    source_program  text NOT NULL DEFAULT '' CHECK (length(source_program) <= 200),
    status          text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    -- Where the next step starts: the kind being read and the page cursor.
    progress        jsonb NOT NULL DEFAULT '{}',
    -- Counts per kind, and what couldn't be brought over and why.
    summary         jsonb NOT NULL DEFAULT '{}',
    error           text,
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_by_type text NOT NULL DEFAULT 'human',
    created_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    finished_at     timestamptz
);
CREATE INDEX partner_imports_program_idx ON partner_imports (program_id, created_at DESC);
CREATE INDEX partner_imports_ws_idx ON partner_imports (workspace_id, created_at DESC);
-- One import at a time per program.
CREATE UNIQUE INDEX partner_imports_running_idx ON partner_imports (program_id) WHERE status IN ('queued', 'running');

-- What each object on the platform became here, so a second run updates
-- instead of adding: a partner is a membership, a customer a referral, a
-- commission a reward and a payout a payout. Transactions are conversions,
-- found by their external_id.
CREATE TABLE partner_import_refs (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    program_id   uuid NOT NULL REFERENCES partner_programs (id) ON DELETE CASCADE,
    connector    text NOT NULL,
    kind         text NOT NULL CHECK (kind IN ('partner', 'customer', 'commission', 'payout', 'promo_code')),
    external_id  text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 200),
    local_id     uuid NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (program_id, connector, kind, external_id)
);
CREATE INDEX partner_import_refs_local_idx ON partner_import_refs (local_id);
