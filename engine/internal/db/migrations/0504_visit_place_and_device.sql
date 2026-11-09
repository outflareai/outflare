-- Where visits and redirect clicks come from, in more detail, and what they
-- come on. The region, city and network (the autonomous system's number and
-- who runs it: an internet provider, a mobile carrier or a host) come from
-- DB-IP's City Lite and ASN Lite files, looked up when the request arrives;
-- the address itself is never stored. The device's maker and model and the
-- system's and browser's versions come from the user agent, and the
-- language from the browser's locale. '' and 0 are unknown, as on rows from
-- before this. Only adds columns with defaults, so the version before
-- still works.
ALTER TABLE site_sessions
    ADD COLUMN region          text   NOT NULL DEFAULT '' CHECK (length(region) <= 200),
    ADD COLUMN city            text   NOT NULL DEFAULT '' CHECK (length(city) <= 200),
    ADD COLUMN asn             bigint NOT NULL DEFAULT 0 CHECK (asn BETWEEN 0 AND 4294967295),
    ADD COLUMN isp             text   NOT NULL DEFAULT '' CHECK (length(isp) <= 200),
    ADD COLUMN device_brand    text   NOT NULL DEFAULT '' CHECK (length(device_brand) <= 40),
    ADD COLUMN device_model    text   NOT NULL DEFAULT '' CHECK (length(device_model) <= 60),
    ADD COLUMN os_version      text   NOT NULL DEFAULT '' CHECK (length(os_version) <= 20),
    ADD COLUMN browser_version text   NOT NULL DEFAULT '' CHECK (length(browser_version) <= 20),
    ADD COLUMN language        text   NOT NULL DEFAULT '' CHECK (language ~ '^([a-z]{2,3})?$');

-- The inbox keeps a batch's place until a worker stores its events, as it
-- keeps the country.
ALTER TABLE site_event_inbox
    ADD COLUMN region text   NOT NULL DEFAULT '' CHECK (length(region) <= 200),
    ADD COLUMN city   text   NOT NULL DEFAULT '' CHECK (length(city) <= 200),
    ADD COLUMN asn    bigint NOT NULL DEFAULT 0 CHECK (asn BETWEEN 0 AND 4294967295),
    ADD COLUMN isp    text   NOT NULL DEFAULT '' CHECK (length(isp) <= 200);

-- A redirect click keeps the same details as a visit. Still no address and
-- no user agent.
ALTER TABLE tracking_link_clicks
    ADD COLUMN country         text   NOT NULL DEFAULT '' CHECK (country ~ '^([A-Z]{2})?$'),
    ADD COLUMN region          text   NOT NULL DEFAULT '' CHECK (length(region) <= 200),
    ADD COLUMN city            text   NOT NULL DEFAULT '' CHECK (length(city) <= 200),
    ADD COLUMN asn             bigint NOT NULL DEFAULT 0 CHECK (asn BETWEEN 0 AND 4294967295),
    ADD COLUMN isp             text   NOT NULL DEFAULT '' CHECK (length(isp) <= 200),
    ADD COLUMN device          text   NOT NULL DEFAULT '' CHECK (device IN ('', 'desktop', 'mobile', 'tablet')),
    ADD COLUMN device_brand    text   NOT NULL DEFAULT '' CHECK (length(device_brand) <= 40),
    ADD COLUMN device_model    text   NOT NULL DEFAULT '' CHECK (length(device_model) <= 60),
    ADD COLUMN os              text   NOT NULL DEFAULT '' CHECK (length(os) <= 40),
    ADD COLUMN os_version      text   NOT NULL DEFAULT '' CHECK (length(os_version) <= 20),
    ADD COLUMN browser         text   NOT NULL DEFAULT '' CHECK (length(browser) <= 40),
    ADD COLUMN browser_version text   NOT NULL DEFAULT '' CHECK (length(browser_version) <= 20),
    ADD COLUMN language        text   NOT NULL DEFAULT '' CHECK (language ~ '^([a-z]{2,3})?$');

-- Query reads the new visit columns too, added at the end.
CREATE OR REPLACE VIEW query.visits WITH (security_barrier) AS
SELECT v.id, v.brand_id, v.record_id, v.anonymous_id, v.started_at, v.last_seen_at, v.events, v.pageviews, v.landing_path, v.referrer_host,
    v.utm_source, v.utm_medium, v.utm_campaign, v.utm_term, v.utm_content, v.country, v.device, v.browser, v.os,
    v.region, v.city, v.asn, v.isp, v.device_brand, v.device_model, v.os_version, v.browser_version, v.language
FROM site_sessions v JOIN query_scopes s ON s.pid = pg_backend_pid() AND v.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes) AND (s.brands IS NULL OR v.brand_id IS NULL OR v.brand_id = ANY (s.brands));
