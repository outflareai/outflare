-- Redirect tracking links: a short link (/l/{code}, on the workspace's
-- tracking domain or this server) for places a site's script can't run,
-- such as a bio link, a partner's page or an offer on another site. A
-- click is counted here, then the person goes on to the link's tagged
-- destination, which carries the click's key (ofclk) for a later
-- conversion to name. The script stays the default; redirects are an
-- option per link, or for every new link in a campaign.
ALTER TABLE tracking_campaigns ADD COLUMN redirect_links boolean NOT NULL DEFAULT false;
ALTER TABLE tracking_links ADD COLUMN redirect_code text CHECK (redirect_code ~ '^[a-z0-9]{8}$');
CREATE UNIQUE INDEX tracking_links_redirect_code_idx ON tracking_links (redirect_code) WHERE redirect_code IS NOT NULL;

-- One row per click on a redirect link. No address or user agent is kept:
-- only whether it looked automated (a link scanner or preview).
CREATE TABLE tracking_link_clicks (
    id           bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    link_id      uuid NOT NULL REFERENCES tracking_links (id) ON DELETE CASCADE,
    -- ofclk on the destination, random, for a conversion to name later.
    click_key    text NOT NULL UNIQUE CHECK (click_key ~ '^[a-z0-9]{16}$'),
    likely_bot   boolean NOT NULL DEFAULT false,
    clicked_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tracking_link_clicks_link_idx ON tracking_link_clicks (link_id, clicked_at);
CREATE INDEX tracking_link_clicks_ws_idx ON tracking_link_clicks (workspace_id, clicked_at);
