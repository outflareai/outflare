-- Redirect clicks are written in batches a moment after the redirect
-- (plans/ad-tracker.md, T1), so a click can carry a keyed hash of the
-- visitor's address for unique-visitor rules and fraud checks later: an
-- HMAC with the server's key, per workspace, never the address. Only adds,
-- so the version before still works.
ALTER TABLE tracking_link_clicks
    ADD COLUMN ip_hash text NOT NULL DEFAULT '' CHECK (length(ip_hash) <= 32);

-- A landing page can report its click (ofclk) before the click's batch is
-- written. The landing waits here, and the batch that writes the click
-- takes it; one an hour old is dropped. The key's format is checked in
-- code (clickKeyRe); the table only caps its length, as the version before
-- this one may write any short value into it while it runs beside this one.
CREATE TABLE tracking_click_claims (
    click_key    text PRIMARY KEY CHECK (length(click_key) BETWEEN 1 AND 32),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE CASCADE,
    session_id   uuid NOT NULL,
    landed_at    timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tracking_click_claims_created_idx ON tracking_click_claims (created_at);
