-- Link building in Query: the sites that link to the brand's websites
-- (backlinks) and the pages that could (link prospects), for questions
-- such as "live links by method this quarter". Like every query view, each
-- shows one workspace's rows and the brands the caller may see, and needs
-- the seo.read scope the API asks for the same data. Free text a person
-- wrote (notes) and the providers' raw readings are left out.
CREATE VIEW query.backlinks WITH (security_barrier) AS
SELECT b.id, b.brand_id, b.site_id, b.source_url, b.target_url, b.source_domain, b.anchor, b.title, b.nofollow, b.sponsored, b.ugc,
    b.link_type, b.in_content, b.status, b.source_rating, b.url_rating, b.domain_traffic, b.page_traffic, b.spam_score, b.review,
    b.first_seen_at, b.last_seen_at, b.lost_at, b.regained_at, b.checked_at
FROM backlinks b JOIN query_scopes s ON s.pid = pg_backend_pid() AND b.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes) AND (s.brands IS NULL OR b.brand_id IS NULL OR b.brand_id = ANY (s.brands));

CREATE VIEW query.link_prospects WITH (security_barrier) AS
SELECT p.id, p.brand_id, p.site_id, p.record_id, p.page_url, p.method, p.target_url, p.evidence, p.domain_rating, p.traffic,
    p.spam_score, p.score, p.stage, p.excluded_reason, p.stage_changed_at, p.pitched_at, p.agreed_at, p.live_at, p.removed_at, p.overdue_at,
    p.ended_at, p.source, p.created_at
FROM link_prospects p JOIN query_scopes s ON s.pid = pg_backend_pid() AND p.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes) AND (s.brands IS NULL OR p.brand_id IS NULL OR p.brand_id = ANY (s.brands));
