-- IP lookups run (plans/ad-tracker.md, B7c): a worker looks up each kept
-- address that is due, on the brand's own connection, and keeps only the
-- result. A failed lookup is tried again a few times, then given up.
ALTER TABLE tracker_click_addresses
    ADD COLUMN attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN next_at  timestamptz NOT NULL DEFAULT now();
CREATE INDEX tracker_click_addresses_next_idx ON tracker_click_addresses (next_at) WHERE look_up;

-- One row per lookup a brand's provider answered, with no address: the
-- score and flags, for the click's detail and the monthly cap.
CREATE TABLE tracker_ip_lookups (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    click_key    text NOT NULL CHECK (length(click_key) <= 32),
    -- The provider's risk score, 0 to 100, when it gave one.
    score        smallint CHECK (score BETWEEN 0 AND 100),
    proxy        boolean NOT NULL DEFAULT false,
    vpn          boolean NOT NULL DEFAULT false,
    tor          boolean NOT NULL DEFAULT false,
    hosting      boolean NOT NULL DEFAULT false,
    crawler      boolean NOT NULL DEFAULT false,
    -- The fraud reasons the lookup added to the click.
    flagged      text[] NOT NULL DEFAULT '{}',
    looked_up_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tracker_ip_lookups_brand_idx ON tracker_ip_lookups (brand_id, looked_up_at);
CREATE INDEX tracker_ip_lookups_click_idx ON tracker_ip_lookups (workspace_id, click_key);
CREATE INDEX tracker_ip_lookups_ws_idx ON tracker_ip_lookups (workspace_id);
