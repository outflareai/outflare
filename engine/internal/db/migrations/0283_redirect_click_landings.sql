-- A redirect click that reaches the site: the site's script reads ofclk
-- from the landing page and the visit it starts claims the click, once.
-- A click with no visit never reached a page with the script (or the
-- person left first), so a link can say how many of its clicks landed.
ALTER TABLE tracking_link_clicks ADD COLUMN session_id uuid REFERENCES site_sessions (id) ON DELETE SET NULL;
ALTER TABLE tracking_link_clicks ADD COLUMN landed_at timestamptz;
CREATE INDEX tracking_link_clicks_session_idx ON tracking_link_clicks (session_id) WHERE session_id IS NOT NULL;
