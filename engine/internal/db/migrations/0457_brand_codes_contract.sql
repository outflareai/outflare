-- Brands own their data: partner and promo codes are unique per brand, so
-- two brands of a workspace can each give a partner the same code. The
-- per-brand twins came in migration 0456, and the version before this one
-- adds codes and imported clicks without naming these keys.
DROP INDEX partner_memberships_code_idx;
DROP INDEX partner_promo_codes_code_idx;
DROP INDEX partner_clicks_external_idx;
