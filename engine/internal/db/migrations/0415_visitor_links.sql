-- Website visitors across devices and browsers (docs/website-visitors.md).

-- Visitor links: a link to the brand's own site that the workspace puts in
-- a message it sends itself (its own product's email, a text message, a
-- QR code). The first browser that opens it joins the person, so a visit
-- on a phone where they never sign in still counts as theirs. One link
-- names one browser, and a sign-in on it as someone else takes it back,
-- the way an email click does.
CREATE TABLE visitor_links (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    -- The browser that opened it first, and when.
    anonymous_id text CHECK (length(anonymous_id) BETWEEN 1 AND 200),
    claimed_at   timestamptz
);

CREATE INDEX visitor_links_record_idx ON visitor_links (record_id);
CREATE INDEX visitor_links_workspace_idx ON visitor_links (workspace_id, created_at DESC);

-- Events a site key took that came through the site's own domain (a path
-- the site forwards to Outflare), and events from Safari and iPhone
-- browsers that came straight to Outflare instead: those browsers forget
-- such a visitor after seven days, so the page can say what serving the
-- script from the site's own domain would fix.
ALTER TABLE site_key_days ADD COLUMN own_domain integer NOT NULL DEFAULT 0;
ALTER TABLE site_key_days ADD COLUMN safari_direct integer NOT NULL DEFAULT 0;

-- A visitor known from a visitor link.
ALTER TABLE site_visitors DROP CONSTRAINT site_visitors_identified_by_check;
ALTER TABLE site_visitors ADD CONSTRAINT site_visitors_identified_by_check
    CHECK (identified_by IN ('', 'product', 'email_click', 'link', 'company_ip', 'provider')) NOT VALID;
ALTER TABLE site_visitors VALIDATE CONSTRAINT site_visitors_identified_by_check;
