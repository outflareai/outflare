-- The creative studio in Query (AGENTS.md, "Data: agents can see all of
-- it"): the brand's files, what was asked of its picture, video and voice
-- providers, talking videos with their stages and versions, creatives and
-- the skills that made them, lessons, and the history of each creative, so
-- an agent can ask "which skills make creatives people like", "what do
-- people say when they turn a fix down" or "what did each video take to
-- make". Like every query view, each shows one workspace's rows, the brands
-- the caller may see and, in a brand's query, that brand's alone, and needs
-- the scope the API asks for the same data. Left out: a file's hash, a
-- provider's run id, and the request and answer as exchanged with the
-- provider (they hold short-lived links to uploaded files; the prompt,
-- options, references and usage are here).

CREATE VIEW query.brand_assets WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.kind, x.name, x.note, x.tags, x.filename, x.content_type, x.family, x.bytes,
    x.width, x.height, x.resizable, x.source, x.consent_at, x.consent_by, x.consent_note, x.archived_at,
    x.version, x.created_by, x.created_at, x.updated_at
FROM brand_assets x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'content.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.brand_generations WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.kind, x.connector, x.integration_id, x.model, x.prompt, x.voice, x.options,
    x.asset_kind, x.name, x.status, x.error_code, x.error, x.asset_id, x.usage, x.refs, x.recipe_of,
    x.created_by, x.created_at, x.started_at, x.finished_at
FROM brand_generations x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'content.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.brand_videos WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.name, x.script, x.direction, x.scene_asset_id, x.settings, x.status, x.stage,
    x.round, x.edit, x.error_code, x.error, x.voice_asset_id, x.render_asset_id, x.base_asset_id,
    x.final_asset_id, x.words, x.notes, x.waiting_version, x.variation_of, x.variation_change, x.rating,
    x.rating_reason, x.rated_by, x.rated_at, x.created_by, x.created_at, x.updated_at, x.finished_at
FROM brand_videos x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'content.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.brand_video_steps WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.video_id, x.round, x.stage, x.status, x.generation_id, x.asset_id, x.detail,
    x.created_at, x.finished_at
FROM brand_video_steps x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'content.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.brand_video_versions WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.video_id, x.number, x.state, x.base_asset_id, x.final_asset_id, x.words,
    x.notes, x.edit, x.reason, x.note, x.decided_by, x.decided_at, x.created_at
FROM brand_video_versions x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'content.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.creatives WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.number, x.name, x.kind, x.skill_id, x.skill_version, x.variant, x.persona,
    x.pain, x.hook, x.tags, x.headline, x.subline, x.cta, x.prompt, x.generation_id, x.review,
    x.review_reason, x.reviewed_by, x.reviewed_at, x.rating, x.rating_reason, x.rated_by, x.rated_at,
    x.version, x.created_by, x.created_at, x.updated_at
FROM creatives x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'content.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.creative_skills WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.name, x.description, x.kind, x.enabled, x.current_version, x.archived_at,
    x.created_by, x.created_at, x.updated_at
FROM creative_skills x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'content.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.creative_skill_versions WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.skill_id, x.version, x.note, x.provider, x.model, x.options, x.sizes, x.rules,
    x.variants, x.created_by, x.created_at
FROM creative_skill_versions x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'content.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.creative_lessons WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.kind, x.word, x.say, x.rule, x.applies_to, x.source_type, x.source_id,
    x.created_by, x.created_at, x.removed_at
FROM creative_lessons x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'content.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.creative_history WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.subject_type, x.subject_id, x.kind, x.said, x.data, x.actor_id, x.created_at
FROM creative_history x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'content.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);
