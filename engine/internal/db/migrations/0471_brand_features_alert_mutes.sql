-- Brands are fully separate (plans/brands/README.md): which switched
-- features are on, and which kinds of alert a person has muted, are each
-- brand's own, the owner's decision of 2026-11-05. Each brand starts with
-- a copy of what its workspace had.
--
-- Expand only (docs/migrations.md): workspace_features and alert_mutes
-- stay for the previous version, which still reads and writes them; a
-- later release drops them.

-- A feature switched on for one brand (engine/internal/features).
CREATE TABLE brand_features (
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- One of features.Catalog, checked in Go before it is saved.
    feature      text NOT NULL,
    enabled_at   timestamptz NOT NULL DEFAULT now(),
    -- Who switched it on through an extension, or null for the
    -- operator's command line.
    enabled_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (brand_id, feature)
);

CREATE INDEX brand_features_ws_idx ON brand_features (workspace_id);

INSERT INTO brand_features (brand_id, workspace_id, feature, enabled_at, enabled_by)
SELECT b.id, f.workspace_id, f.feature, f.enabled_at, f.enabled_by
FROM workspace_features f JOIN brands b ON b.workspace_id = f.workspace_id
ON CONFLICT DO NOTHING;

-- A person who doesn't want a kind of alert by email from one brand.
CREATE TABLE brand_alert_mutes (
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind         text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_id, user_id, kind)
);

CREATE INDEX brand_alert_mutes_ws_idx ON brand_alert_mutes (workspace_id, user_id);

INSERT INTO brand_alert_mutes (brand_id, workspace_id, user_id, kind, created_at)
SELECT b.id, m.workspace_id, m.user_id, m.kind, m.created_at
FROM alert_mutes m JOIN brands b ON b.workspace_id = m.workspace_id
ON CONFLICT DO NOTHING;
