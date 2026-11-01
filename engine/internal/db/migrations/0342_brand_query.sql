-- Query for one brand (plans/brands/README.md, "Data pages"): a query run
-- under /v1/brands/{brand}/query sees that brand's customer data and
-- nothing else. The server writes the brand into the query's query_scopes
-- row, which the query role can neither read nor change, and every
-- customer view keeps to it on each row's own brand_id. A row with no brand
-- is not that brand's, so it is left out too.
--
-- Rollback-safe: brand is a new nullable column, and null keeps every view
-- exactly as before, so the previous version (which never sets it) reads
-- what it read. The views change only their WHERE clauses; their columns
-- are the same.

ALTER TABLE query_scopes ADD COLUMN brand uuid;

CREATE OR REPLACE VIEW query.records WITH (security_barrier) AS
SELECT r.id, r.record_type, r.brand_id, r.name, r.attributes, r.origin, r.version, r.merged_into, r.archived_at, r.created_at, r.updated_at
FROM records r JOIN query_scopes s ON s.pid = pg_backend_pid() AND r.workspace_id = s.workspace_id
WHERE r.deleted_at IS NULL
    AND 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR r.brand_id = s.brand);

CREATE OR REPLACE VIEW query.people WITH (security_barrier) AS
SELECT r.id, r.brand_id, r.name, r.attributes,
    (SELECT i.value FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'email' AND i.valid_to IS NULL
     ORDER BY i.verified DESC, i.created_at LIMIT 1) AS email,
    r.origin, r.created_at, r.updated_at
FROM records r JOIN query_scopes s ON s.pid = pg_backend_pid() AND r.workspace_id = s.workspace_id
WHERE r.record_type = 'person' AND r.merged_into IS NULL AND r.archived_at IS NULL
    AND 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR r.brand_id = s.brand);

CREATE OR REPLACE VIEW query.companies WITH (security_barrier) AS
SELECT r.id, r.brand_id, r.name, r.attributes,
    (SELECT i.value FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'domain' AND i.valid_to IS NULL
     ORDER BY i.verified DESC, i.created_at LIMIT 1) AS domain,
    r.origin, r.created_at, r.updated_at
FROM records r JOIN query_scopes s ON s.pid = pg_backend_pid() AND r.workspace_id = s.workspace_id
WHERE r.record_type = 'account' AND r.merged_into IS NULL AND r.archived_at IS NULL
    AND 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR r.brand_id = s.brand);

CREATE OR REPLACE VIEW query.record_identities WITH (security_barrier) AS
SELECT i.id, i.record_id, i.namespace, i.value, i.verified, i.verified_at, i.source, i.valid_from, i.valid_to, i.created_at
FROM record_identities i JOIN records r ON r.id = i.record_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND i.workspace_id = s.workspace_id AND r.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR (i.brand_id = s.brand AND r.brand_id = s.brand));

CREATE OR REPLACE VIEW query.record_relationships WITH (security_barrier) AS
SELECT l.id, l.from_id, l.to_id, l.kind, l.field_key, l.properties, l.valid_from, l.valid_to, l.created_at
FROM record_relationships l JOIN records r ON r.id = l.from_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND l.workspace_id = s.workspace_id AND r.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR (l.brand_id = s.brand AND r.brand_id = s.brand));

CREATE OR REPLACE VIEW query.customer_events WITH (security_barrier) AS
SELECT e.id, e.record_id, e.account_id, e.brand_id, e.kind, e.name, e.user_id, e.anonymous_id, e.properties, e.context, e.source,
    e.occurred_at, e.received_at
FROM customer_events e JOIN query_scopes s ON s.pid = pg_backend_pid() AND e.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR e.brand_id IS NULL OR e.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR e.brand_id = s.brand);

CREATE OR REPLACE VIEW query.consents WITH (security_barrier) AS
SELECT c.id, c.record_id, c.channel, c.topic, c.purpose, c.status, c.source, c.occurred_at, c.created_at
FROM consents c JOIN records r ON r.id = c.record_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id AND r.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR (c.brand_id = s.brand AND r.brand_id = s.brand));

CREATE OR REPLACE VIEW query.subscriptions WITH (security_barrier) AS
SELECT p.id, p.record_id, p.provider, p.status, p.plan, p.billing_interval, p.monthly_minor, p.currency, p.started_at, p.canceled_at,
    p.created_at, p.updated_at
FROM payment_subscriptions p LEFT JOIN records r ON r.id = p.record_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND p.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR (p.brand_id = s.brand AND (r.id IS NULL OR r.brand_id = s.brand)));

CREATE OR REPLACE VIEW query.brands WITH (security_barrier) AS
SELECT b.id, b.name, b.handle, b.website, b.domains, b.status, b.business_model, b.revenue_models, b.created_at
FROM brands b JOIN query_scopes s ON s.pid = pg_backend_pid() AND b.workspace_id = s.workspace_id
WHERE 'workspace.read' = ANY (s.scopes) AND (s.brands IS NULL OR b.id = ANY (s.brands))
    AND (s.brand IS NULL OR b.id = s.brand);
