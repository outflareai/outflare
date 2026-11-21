-- The brand profile in Query (AGENTS.md, "Data: agents can see all of
-- it"): a brand's facts, its personas and the history of both, so an agent
-- can ask "which facts are waiting for a person", "what may be said to
-- customers about pricing" or "what changed in the profile this month".
-- Like every query view, each shows one workspace's rows, the brands the
-- caller may see and, in a brand's query, that brand's alone, and needs
-- the scope the API asks for the same data (workspace.read).

CREATE VIEW query.brand_profile_facts WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.section, x.title, x.text, x.source, x.evidence, x.confidence, x.visibility,
    x.status, x.replaces_id, x.context_field, x.review_at, x.confirmed_by, x.confirmed_at, x.retired_at,
    x.position, x.created_by, x.updated_by, x.created_at, x.updated_at
FROM brand_profile_facts x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'workspace.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.brand_personas WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.name, x.role, x.summary, x.goals, x.pains, x.triggers, x.objections, x.channels,
    x.source, x.status, x.confirmed_by, x.confirmed_at, x.position, x.created_by, x.updated_by,
    x.created_at, x.updated_at
FROM brand_personas x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'workspace.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.brand_profile_changes WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.fact_id, x.persona_id, x.action, x.before, x.after, x.actor_id, x.created_at
FROM brand_profile_changes x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'workspace.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);
