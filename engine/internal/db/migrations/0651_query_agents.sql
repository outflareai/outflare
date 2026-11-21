-- Agent work, experiments, journeys, social, content and email set-up in
-- Query (AGENTS.md, "Data: agents can see all of it"), so an agent can
-- learn from what agents and the team already did and what came of it:
-- "missions that ended without reaching their goal", "posts that drove
-- sign-ups", "A/B tests and their results". Like every query view, each
-- shows one workspace's rows, the brands the caller may see and, in a
-- brand's query, that brand's alone, and needs the scope the API asks for
-- the same data. Request bodies and results of agent actions are left out
-- (they can carry keys the action set up), and so are hashes, recipients'
-- addresses and alert dedupe keys.

CREATE VIEW query.agent_runs WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.token_id, x.name, x.goal, x.instructions, x.plan, x.inputs, x.playbook_id,
    x.playbook_version, x.automation_id, x.mode, x.max_spend_cents, x.status, x.status_reason, x.report,
    x.turns, x.next_turn_at, x.turn_started_at, x.last_turn_at, x.turn_summary, x.turn_error,
    x.turn_failures, x.created_by, x.created_at, x.updated_at, x.ended_at
FROM agent_runs x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'agents.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.agent_run_updates WITH (security_barrier) AS
SELECT x.id, x.run_id, x.brand_id, x.actor_id, x.author, x.kind, x.body, x.created_at
FROM agent_run_updates x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'agents.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.missions WITH (security_barrier) AS
SELECT x.id, x.name, x.goal, x.goal_metric, x.goal_target, x.instructions, x.plan, x.plan_updated_at, x.mode,
    x.status, x.status_reason, x.token_id, x.max_sends, x.max_spend_cents, x.ends_at, x.approved_by,
    x.approved_at, x.started_at, x.ended_at, x.report, x.created_by, x.created_at, x.updated_at, x.runner,
    x.next_turn_at, x.last_turn_at, x.turn_started_at, x.turns, x.turn_summary, x.turn_failures,
    x.turn_error, x.brand_id, x.playbook_id, x.playbook_version, x.automation_id
FROM missions x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'missions.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.mission_actions WITH (security_barrier) AS
SELECT x.id, x.mission_id, x.actor_id, x.token_id, x.method, x.path, x.query, x.content_type, x.reason,
    x.status, x.decided_by, x.decided_at, x.decision_note, x.result_status, x.request_id, x.created_at,
    x.brand_id
FROM mission_actions x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'missions.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.mission_updates WITH (security_barrier) AS
SELECT x.id, x.mission_id, x.actor_id, x.author, x.kind, x.body, x.data, x.resolved_at, x.resolved_by,
    x.created_at, x.brand_id
FROM mission_updates x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'missions.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.actions WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.actor_id, x.token_id, x.grant_id, x.method, x.path, x.query, x.content_type,
    x.reason, x.status, x.decided_by_rule, x.hold_reason, x.decided_by, x.decided_at, x.decision_note,
    x.result_status, x.request_id, x.created_at, x.expires_at, x.superseded_by
FROM actions x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'actions.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.brand_findings WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.text, x.evidence, x.confidence, x.scope, x.status, x.review_at, x.retired_reason,
    x.retired_at, x.created_by, x.created_at, x.updated_at
FROM brand_findings x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'workspace.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.memories WITH (security_barrier) AS
SELECT x.id, x.kind, x.text, x.pinned, x.created_by, x.mission_id, x.created_at, x.updated_at, x.brand_id
FROM memories x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'workspace.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ab_tests WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.flag_id, x.name, x.hypothesis, x.status, x.control, x.goals, x.min_effect,
    x.started_at, x.ended_at, x.decision, x.result, x.created_by, x.created_at, x.updated_at, x.baselines,
    x.guardrails, x.frozen_at, x.next_check_at, x.learning, x.learning_at, x.registration, x.factors, x.page
FROM ab_tests x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ab_test_checks WITH (security_barrier) AS
SELECT x.test_id, x.variant, x.status, x.note, x.checked_by, x.checked_at, x.brand_id
FROM ab_test_checks x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.insights WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.kind, x.name, x.description, x.spec, x.created_by, x.updated_by, x.created_at,
    x.updated_at
FROM insights x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.alerts WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.kind, x.target_type, x.target_id, x.record_id, x.owner_id, x.data, x.created_at
FROM alerts x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'workspace.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.journey_versions WITH (security_barrier) AS
SELECT x.id, x.journey_id, x.status, x.version, x.trigger_kind, x.trigger_event, x.trigger_filter, x.entry,
    x.goal_event, x.steps, x.created_by, x.published_by, x.created_at, x.updated_at, x.published_at,
    x.exit_events, x.exit_conditions, x.exclude, x.entry_limit, x.entry_limit_per, x.reentry_after_seconds,
    x.brand_id
FROM journey_versions x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'emails.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.journey_run_steps WITH (security_barrier) AS
SELECT x.id, x.run_id, x.journey_id, x.step, x.kind, x.outcome, x.email_id, x.detail, x.at, x.brand_id
FROM journey_run_steps x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'emails.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.lifecycle_stages WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.level, x.key, x.name, x.category, x.rank, x.created_at, x.updated_at
FROM lifecycle_stages x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.social_accounts WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.channel, x.kind, x.integration_id, x.external_id, x.handle, x.name, x.url,
    x.avatar_url, x.status, x.followers, x.following, x.posts, x.last_synced_at, x.next_sync_at,
    x.last_error, x.created_at, x.updated_at, x.failures, x.last_error_code, x.last_failed_at, x.competitor_id
FROM social_accounts x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.social_account_days WITH (security_barrier) AS
SELECT x.account_id, x.day, x.followers, x.following, x.posts, x.updated_at, x.brand_id
FROM social_account_days x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.social_publications WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.account_id, x.integration_id, x.channel, x.body, x.link_url, x.utm_content,
    x.prompt, x.scheduled_for, x.status, x.status_reason, x.drafted_via, x.series_id, x.mission_id,
    x.created_by, x.approved_by, x.approved_at, x.external_id, x.url, x.post_id, x.published_at,
    x.request_id, x.created_at, x.updated_at
FROM social_publications x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.social_post_snapshots WITH (security_barrier) AS
SELECT x.post_id, x.taken_at, x.impressions, x.likes, x.replies, x.reposts, x.quotes, x.bookmarks, x.clicks,
    x.brand_id
FROM social_post_snapshots x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.content_items WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.title, x.slug, x.target, x.body, x.excerpt, x.tags, x.canonical_url, x.status,
    x.integration_id, x.external_id, x.url, x.cms_status, x.published_at, x.notes, x.created_by,
    x.created_at, x.updated_at, x.language, x.placement, x.seo, x.links, x.media, x.brief, x.revision,
    x.approved_by, x.approved_at
FROM content_items x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'content.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.content_publishes WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.item_id, x.integration_id, x.kind, x.live, x.status, x.error, x.external_id,
    x.url, x.cms_status, x.verified, x.requested_by, x.created_at, x.applied_at
FROM content_publishes x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'content.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.email_templates WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.handle, x.name, x.channel, x.purpose, x.status, x.from_address, x.reply_to,
    x.subject, x.html, x.text_body, x.variables, x.published_version, x.created_at, x.updated_at,
    x.track_opens, x.track_clicks, x.topic_id
FROM email_templates x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'emails.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.email_topics WITH (security_barrier) AS
SELECT x.id, x.key, x.name, x.description, x.subscribed_by_default, x.visibility, x.status, x.created_at,
    x.updated_at, x.brand_id
FROM email_topics x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'emails.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.email_broadcast_recipients WITH (security_barrier) AS
SELECT x.broadcast_id, x.record_id, x.email_id, x.skipped, x.created_at, x.brand_id
FROM email_broadcast_recipients x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'emails.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.suppressions WITH (security_barrier) AS
SELECT x.id, x.kind, x.value, x.reason, x.note, x.source, x.created_at, x.record_id, x.method, x.added_by,
    x.added_by_type, x.integration_id, x.confidence, x.evidence, x.brand_id
FROM suppressions x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'leads.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);
