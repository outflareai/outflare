-- Session recordings and heatmaps. With a brand's say-so, its website
-- script records what a visitor's page showed and did (never what they
-- typed), so a person can watch a visit back, and counts where visitors
-- click and how far they scroll. The recording itself is a file in the
-- server's object storage; these tables hold what is needed to list it,
-- find it and delete it. Heatmaps are counts per day with no visitor.

-- One row per brand: whether its site is recorded, and how.
CREATE TABLE replay_settings (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    recordings     boolean NOT NULL DEFAULT false,
    heatmaps       boolean NOT NULL DEFAULT false,
    -- The share of visits recorded, in percent.
    sample_percent integer NOT NULL DEFAULT 100 CHECK (sample_percent BETWEEN 1 AND 100),
    -- Also hide every piece of text on the page, not only what is typed.
    mask_text      boolean NOT NULL DEFAULT false,
    -- Recordings older than this are deleted.
    keep_days      integer NOT NULL DEFAULT 30 CHECK (keep_days BETWEEN 1 AND 90),
    updated_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id)
);
CREATE INDEX replay_settings_workspace_idx ON replay_settings (workspace_id);

-- One row per recorded visit. The file is kept in parts, in the order the
-- browser sent them: replay/<workspace>/<brand>/<id>/<part>.json.gz.
CREATE TABLE session_recordings (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The id the browser gave the visit, and the visitor's anonymous id,
    -- which is how the visit joins the visitor's events and their record.
    session_id   text NOT NULL CHECK (length(session_id) BETWEEN 8 AND 64),
    anonymous_id text NOT NULL DEFAULT '' CHECK (length(anonymous_id) <= 200),
    started_at   timestamptz NOT NULL DEFAULT now(),
    last_at      timestamptz NOT NULL DEFAULT now(),
    parts        integer NOT NULL DEFAULT 0,
    bytes        bigint NOT NULL DEFAULT 0,
    clicks       integer NOT NULL DEFAULT 0,
    -- The pages of the visit, in the order first seen, without their
    -- query strings.
    paths        text[] NOT NULL DEFAULT '{}',
    device       text NOT NULL DEFAULT 'desktop' CHECK (device IN ('desktop', 'tablet', 'mobile')),
    browser      text NOT NULL DEFAULT '' CHECK (length(browser) <= 40),
    os           text NOT NULL DEFAULT '' CHECK (length(os) <= 40),
    country      text NOT NULL DEFAULT '' CHECK (length(country) <= 40),
    width        integer NOT NULL DEFAULT 0,
    height       integer NOT NULL DEFAULT 0,
    viewed_at    timestamptz,
    -- Set when a person deletes it: the file goes first, then the row.
    deleted_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, session_id)
);
CREATE INDEX session_recordings_workspace_idx ON session_recordings (workspace_id);
CREATE INDEX session_recordings_recent_idx ON session_recordings (brand_id, started_at DESC);
CREATE INDEX session_recordings_paths_idx ON session_recordings USING gin (paths);
CREATE INDEX session_recordings_visitor_idx ON session_recordings (brand_id, anonymous_id) WHERE anonymous_id <> '';

-- Where visitors clicked on a page, per day: x is the click's place across
-- the page in percent, y its distance from the top of the page in steps of
-- 10 pixels.
CREATE TABLE heatmap_clicks (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    path         text NOT NULL CHECK (length(path) BETWEEN 1 AND 500),
    day          date NOT NULL,
    device       text NOT NULL DEFAULT 'desktop' CHECK (device IN ('desktop', 'tablet', 'mobile')),
    x            integer NOT NULL CHECK (x BETWEEN 0 AND 99),
    y            integer NOT NULL CHECK (y BETWEEN 0 AND 5000),
    clicks       bigint NOT NULL DEFAULT 0,
    UNIQUE (brand_id, path, day, device, x, y)
);
CREATE INDEX heatmap_clicks_workspace_idx ON heatmap_clicks (workspace_id);
CREATE INDEX heatmap_clicks_day_idx ON heatmap_clicks (brand_id, day);

-- How far down a page its visitors got, per day: depth is the deepest
-- tenth of the page a page view reached (1 to 10), and height the page's
-- height in steps of 10 pixels, so the two can be drawn together.
CREATE TABLE heatmap_scrolls (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    path         text NOT NULL CHECK (length(path) BETWEEN 1 AND 500),
    day          date NOT NULL,
    device       text NOT NULL DEFAULT 'desktop' CHECK (device IN ('desktop', 'tablet', 'mobile')),
    depth        integer NOT NULL CHECK (depth BETWEEN 1 AND 10),
    views        bigint NOT NULL DEFAULT 0,
    -- The tallest the page was that day, in steps of 10 pixels.
    height       integer NOT NULL DEFAULT 0,
    UNIQUE (brand_id, path, day, device, depth)
);
CREATE INDEX heatmap_scrolls_workspace_idx ON heatmap_scrolls (workspace_id);
CREATE INDEX heatmap_scrolls_day_idx ON heatmap_scrolls (brand_id, day);
