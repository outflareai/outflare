-- The conversion feed (plans/paid-ads/plan.md, "Conversion feed"): Outflare
-- makes three conversion actions on a Google Ads account (sign-up, first
-- payment, repeat payment) and a Customer Match list of paying customers,
-- and sends to them through Google's Data Manager API, which replaced the
-- Ads API's click conversion uploads for new integrations in June 2026.
-- Expand only (docs/migrations.md): new columns with defaults, so the
-- version before this one still runs on this schema.

-- Which of the three Outflare conversion actions a rule feeds, and the
-- customer type it sends (Google's NEW / RETURNING flag on a conversion).
-- Rules people made by hand keep 'custom'; '' sends no customer type,
-- 'auto' works it out per event from the person's earlier payments.
ALTER TABLE ad_conversion_rules ADD COLUMN feed_role text NOT NULL DEFAULT 'custom' CHECK (feed_role IN ('custom', 'signup', 'first_payment', 'repeat_payment'));
ALTER TABLE ad_conversion_rules ADD COLUMN customer_type text NOT NULL DEFAULT '' CHECK (customer_type IN ('', 'new', 'returning', 'auto'));

-- The conversion actions and lists Outflare made on an account, by role,
-- the transport sends take (data_manager, or ads_api when Data Manager
-- refused the destination), the payers list's size and refresh, and the
-- new customer acquisition goal (engine/internal/adconv, type Feed).
ALTER TABLE ad_accounts ADD COLUMN feed jsonb NOT NULL DEFAULT '{}';

-- Each send remembers the path it took (ads_api or data_manager) and the
-- Data Manager request id, which its status can be asked by later.
ALTER TABLE ad_conversion_sends ADD COLUMN transport text NOT NULL DEFAULT 'ads_api';
ALTER TABLE ad_conversion_sends ADD COLUMN request_id text NOT NULL DEFAULT '';
