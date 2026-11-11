-- Redirect modes and tracking domain fallbacks (plans/ad-tracker.md, T4c).
--
-- A campaign's redirect links send people on with a 302 (the default), a
-- 307, a meta refresh or a double meta refresh (both leave the referrer
-- out). Tracker features (destinations, rule-based paths and the modes
-- other than 302) work only on the workspace's own verified tracking
-- domains, the owner's decision 8; on the shared domain a click goes to the
-- link's own page with a 302. Only adds, so the version before still works
-- (it sends every click with a 302).
ALTER TABLE tracking_campaigns
    ADD COLUMN redirect_mode text NOT NULL DEFAULT '302' CHECK (redirect_mode IN ('302', '307', 'meta', 'double_meta'));

-- Where a redirect code that doesn't exist on the domain goes, instead of a
-- not found page; '' for none.
ALTER TABLE tracking_domains
    ADD COLUMN fallback_url text NOT NULL DEFAULT '' CHECK (length(fallback_url) <= 2000);
