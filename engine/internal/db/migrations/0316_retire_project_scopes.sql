-- Projects are gone, and so are their scopes' old names: projects.read and
-- projects.manage became profiles.read and profiles.manage, and the engine
-- no longer accepts the old ones. Stored grants move over so every token
-- and role keeps what it could do, and webhook subscriptions drop the
-- project events, which no longer exist. The query views lose their
-- always-null project_id column.
-- migration-safety: no code reads project_id from the query views (it has been NULL since 0280); a saved query that names it only loses that always-null column

CREATE FUNCTION pg_temp.retire_project_scopes(s text[]) RETURNS text[]
LANGUAGE sql IMMUTABLE AS $$
    SELECT coalesce(array_agg(DISTINCT x ORDER BY x), '{}')
    FROM unnest(array_replace(array_replace(s, 'projects.read', 'profiles.read'), 'projects.manage', 'profiles.manage')) x
$$;

UPDATE api_tokens SET scopes = pg_temp.retire_project_scopes(scopes)
WHERE scopes && '{projects.read,projects.manage}';

UPDATE workspace_roles SET scopes = pg_temp.retire_project_scopes(scopes)
WHERE scopes && '{projects.read,projects.manage}';

UPDATE oauth_codes SET scopes = pg_temp.retire_project_scopes(scopes)
WHERE scopes && '{projects.read,projects.manage}';

UPDATE webhook_endpoints
SET events = array_remove(array_remove(array_remove(events, 'project.created'), 'project.updated'), 'project.deleted')
WHERE events && '{project.created,project.updated,project.deleted}';

DROP VIEW query.sequences;
CREATE VIEW query.sequences WITH (security_barrier) AS
SELECT q.id, q.brand_id, q.mission_id, q.name, q.status, q.daily_new_limit, q.stop_on_reply, q.created_at, q.updated_at
FROM sequences q JOIN query_scopes s ON s.pid = pg_backend_pid() AND q.workspace_id = s.workspace_id
WHERE 'sequences.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands));

DROP VIEW query.deals;
CREATE VIEW query.deals WITH (security_barrier) AS
SELECT d.id, d.record_id, d.mission_id, d.sequence_id, d.lead_id, d.meeting_id, d.source, d.name, d.stage, d.state,
    d.amount_cents, d.currency, d.attribution, d.opened_at, d.closed_at, d.created_at, d.updated_at
FROM deals d LEFT JOIN sequences q ON q.id = d.sequence_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND d.workspace_id = s.workspace_id
WHERE 'outcomes.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands));

DROP VIEW query.meetings;
CREATE VIEW query.meetings WITH (security_barrier) AS
SELECT m.id, m.record_id, m.mission_id, m.sequence_id, m.lead_id, m.source, m.title, m.status, m.qualified, m.attribution,
    m.starts_at, m.ends_at, m.booked_at, m.created_at
FROM meetings m LEFT JOIN sequences q ON q.id = m.sequence_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND m.workspace_id = s.workspace_id
WHERE 'outcomes.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands));

DROP VIEW query.spend;
CREATE VIEW query.spend WITH (security_barrier) AS
SELECT e.id, e.brand_id, e.sequence_id, e.mission_id, e.channel, e.category, e.provider, e.units, e.unit_name,
    e.amount_cents, e.currency, e.source, e.occurred_at
FROM spend_entries e JOIN query_scopes s ON s.pid = pg_backend_pid() AND e.workspace_id = s.workspace_id
WHERE 'outcomes.read' = ANY (s.scopes) AND (s.brands IS NULL OR e.brand_id IS NULL OR e.brand_id = ANY (s.brands));
