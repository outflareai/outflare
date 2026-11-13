-- Paid IP reputation lookups for tracker clicks (plans/ad-tracker.md, B7c;
-- the owner's decision 12): each brand chooses whether its clicks are
-- looked up on its workspace's own IPQualityScore (or other ip.score)
-- connection, and which ones: clicks a fraud signal flagged, clicks that
-- convert, and a sample of the rest, up to a monthly cap.
CREATE TABLE tracker_ip_lookup_settings (
    brand_id      uuid PRIMARY KEY REFERENCES brands (id) ON DELETE CASCADE,
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    enabled       boolean NOT NULL DEFAULT false,
    flagged       boolean NOT NULL DEFAULT true,
    converting    boolean NOT NULL DEFAULT true,
    -- The share of the other clicks looked up, in basis points (500 = 5%).
    sample_bp     integer NOT NULL DEFAULT 500 CHECK (sample_bp BETWEEN 0 AND 10000),
    monthly_cap   integer NOT NULL DEFAULT 5000 CHECK (monthly_cap BETWEEN 0 AND 10000000),
    -- The provider's risk score (0 to 100) at which a click is flagged.
    threshold     integer NOT NULL DEFAULT 75 CHECK (threshold BETWEEN 1 AND 100),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tracker_ip_lookup_settings_ws_idx ON tracker_ip_lookup_settings (workspace_id);

-- The exact address of a tracker click that may be looked up, kept only
-- while its brand's lookups are on and for 30 days at most: the owner's
-- fraud exception (ideas/fraud-signals.md, "The full IP, for fraud only";
-- plan decision 5). Never on a visit, a person or anywhere else, and only
-- ever sent to the workspace's own lookup provider.
CREATE TABLE tracker_click_addresses (
    click_key    text PRIMARY KEY CHECK (length(click_key) <= 32),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    ip           inet NOT NULL,
    user_agent   text NOT NULL DEFAULT '' CHECK (length(user_agent) <= 1000),
    -- Look it up now (flagged or sampled), rather than only if it converts.
    look_up      boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tracker_click_addresses_created_idx ON tracker_click_addresses (created_at);
CREATE INDEX tracker_click_addresses_due_idx ON tracker_click_addresses (workspace_id, created_at) WHERE look_up;
