-- Paid link orders in Query: the brand's donor catalog and each order,
-- for questions such as "spent by currency this quarter" or "published
-- orders whose live check failed". Like every query view, each shows one
-- workspace's rows and the brands the caller may see, and needs the
-- coverage.read scope the API asks for the same data. Free text a person
-- wrote (notes, lost_reason) is left out. Domain is taken from the
-- Coverage publisher; donor attributes stay on the profile so Presence
-- publishers are unchanged.
CREATE VIEW query.coverage_donor_profiles WITH (security_barrier) AS
SELECT p.id, p.brand_id, p.publisher_id, pub.domain, p.pbn, p.country, p.languages, p.niches, p.casino_allowed,
    p.rel_offered, p.slot_types, p.duration, p.duration_days, p.tat_days, p.guarantee_days, p.vendor_id, p.source,
    p.domain_rating, p.traffic, p.spam_score, p.metrics_at, p.metrics_source, p.archived_at, p.created_at, p.updated_at
FROM coverage_donor_profiles p
JOIN coverage_publishers pub ON pub.id = p.publisher_id AND pub.workspace_id = p.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND p.workspace_id = s.workspace_id
WHERE 'coverage.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR p.brand_id = ANY (s.brands));

CREATE VIEW query.coverage_link_orders WITH (security_barrier) AS
SELECT o.id, o.brand_id, o.publisher_id, o.donor_profile_id, o.target_url, o.slot_type, o.rel_requested, o.status,
    o.quoted_amount_minor, o.quoted_currency, o.donor_amount_minor, o.currency, o.guarantee_days, o.guarantee_until,
    o.published_url, o.proof_url, o.published_at, o.placement_id, o.agreement_id, o.invoice_id, o.payment_id,
    o.credit_payment_id, o.replaces_order_id, o.owner_user_id, o.last_check_at, o.last_check_live,
    o.last_check_target_match, o.last_check_rel, o.last_check_anchor, o.approved_at, o.paid_at, o.lost_at,
    o.created_at, o.updated_at
FROM coverage_link_orders o JOIN query_scopes s ON s.pid = pg_backend_pid() AND o.workspace_id = s.workspace_id
WHERE 'coverage.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR o.brand_id = ANY (s.brands));
