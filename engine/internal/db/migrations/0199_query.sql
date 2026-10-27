-- Query: read-only SQL over the workspace's own data, for people and agents.
-- A query runs as a database role that can read only the views in the
-- query schema, never a table. Each view shows the rows of one workspace,
-- the brands the caller may see, and nothing at all without the read scope
-- the API asks for the same data. Which workspace, brands and scopes a
-- query runs with is a row in query_scopes, keyed by the backend running
-- it: the server writes it before the query and deletes it after, and the
-- query role can neither read nor change it.
--
-- Brands follow the API and the record timeline: a brand-limited caller
-- sees its brands' rows and the shared ones, with no brand. A row whose
-- brand comes from elsewhere uses the same source as the timeline: a
-- message's, meeting's or deal's sequence, a reply's mailbox, a record's
-- own brand for its identities and consents. Leads have no brand at all,
-- so they are hidden from brand-limited callers.

CREATE SCHEMA IF NOT EXISTS query;

CREATE UNLOGGED TABLE query_scopes (
    pid          int PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Null: every brand.
    brands       uuid[],
    -- The read scopes the caller holds, such as customers.read.
    scopes       text[] NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Customers (customers.read).

CREATE VIEW query.records WITH (security_barrier) AS
SELECT r.id, r.record_type, r.brand_id, r.name, r.attributes, r.origin, r.version, r.merged_into, r.archived_at, r.created_at, r.updated_at
FROM records r JOIN query_scopes s ON s.pid = pg_backend_pid() AND r.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands));

CREATE VIEW query.people WITH (security_barrier) AS
SELECT r.id, r.brand_id, r.name, r.attributes,
    (SELECT i.value FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'email' AND i.valid_to IS NULL
     ORDER BY i.verified DESC, i.created_at LIMIT 1) AS email,
    r.origin, r.created_at, r.updated_at
FROM records r JOIN query_scopes s ON s.pid = pg_backend_pid() AND r.workspace_id = s.workspace_id
WHERE r.record_type = 'person' AND r.merged_into IS NULL AND r.archived_at IS NULL
    AND 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands));

CREATE VIEW query.companies WITH (security_barrier) AS
SELECT r.id, r.brand_id, r.name, r.attributes,
    (SELECT i.value FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'domain' AND i.valid_to IS NULL
     ORDER BY i.verified DESC, i.created_at LIMIT 1) AS domain,
    r.origin, r.created_at, r.updated_at
FROM records r JOIN query_scopes s ON s.pid = pg_backend_pid() AND r.workspace_id = s.workspace_id
WHERE r.record_type = 'account' AND r.merged_into IS NULL AND r.archived_at IS NULL
    AND 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands));

CREATE VIEW query.record_identities WITH (security_barrier) AS
SELECT i.id, i.record_id, i.namespace, i.value, i.verified, i.verified_at, i.source, i.valid_from, i.valid_to, i.created_at
FROM record_identities i JOIN records r ON r.id = i.record_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND i.workspace_id = s.workspace_id AND r.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands));

CREATE VIEW query.record_relationships WITH (security_barrier) AS
SELECT l.id, l.from_id, l.to_id, l.kind, l.field_key, l.properties, l.valid_from, l.valid_to, l.created_at
FROM record_relationships l JOIN records r ON r.id = l.from_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND l.workspace_id = s.workspace_id AND r.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands));

CREATE VIEW query.customer_events WITH (security_barrier) AS
SELECT e.id, e.record_id, e.account_id, e.brand_id, e.kind, e.name, e.user_id, e.anonymous_id, e.properties, e.context, e.source,
    e.occurred_at, e.received_at
FROM customer_events e JOIN query_scopes s ON s.pid = pg_backend_pid() AND e.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR e.brand_id IS NULL OR e.brand_id = ANY (s.brands));

CREATE VIEW query.consents WITH (security_barrier) AS
SELECT c.id, c.record_id, c.channel, c.topic, c.purpose, c.status, c.source, c.occurred_at, c.created_at
FROM consents c JOIN records r ON r.id = c.record_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id AND r.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands));

CREATE VIEW query.subscriptions WITH (security_barrier) AS
SELECT p.id, p.record_id, p.provider, p.status, p.plan, p.billing_interval, p.monthly_minor, p.currency, p.started_at, p.canceled_at,
    p.created_at, p.updated_at
FROM payment_subscriptions p LEFT JOIN records r ON r.id = p.record_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND p.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands));

-- Brands and projects.

CREATE VIEW query.brands WITH (security_barrier) AS
SELECT b.id, b.name, b.handle, b.website, b.domains, b.status, b.business_model, b.revenue_models, b.created_at
FROM brands b JOIN query_scopes s ON s.pid = pg_backend_pid() AND b.workspace_id = s.workspace_id
WHERE 'workspace.read' = ANY (s.scopes) AND (s.brands IS NULL OR b.id = ANY (s.brands));

CREATE VIEW query.projects WITH (security_barrier) AS
SELECT p.id, p.brand_id, p.name, p.status, p.offer, p.audience, p.market, p.daily_send_limit, p.created_at, p.updated_at
FROM projects p JOIN query_scopes s ON s.pid = pg_backend_pid() AND p.workspace_id = s.workspace_id
WHERE 'projects.read' = ANY (s.scopes) AND (s.brands IS NULL OR p.brand_id IS NULL OR p.brand_id = ANY (s.brands));

-- Outreach (leads.read, sequences.read, inbox.read).

CREATE VIEW query.leads WITH (security_barrier) AS
SELECT l.id, l.record_id, l.email, l.domain, l.first_name, l.last_name, l.company, l.title, l.source, l.custom, l.created_at, l.updated_at
FROM leads l JOIN query_scopes s ON s.pid = pg_backend_pid() AND l.workspace_id = s.workspace_id
WHERE 'leads.read' = ANY (s.scopes) AND s.brands IS NULL;

CREATE VIEW query.sequences WITH (security_barrier) AS
SELECT q.id, q.brand_id, q.project_id, q.mission_id, q.name, q.status, q.daily_new_limit, q.stop_on_reply, q.created_at, q.updated_at
FROM sequences q JOIN query_scopes s ON s.pid = pg_backend_pid() AND q.workspace_id = s.workspace_id
WHERE 'sequences.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands));

CREATE VIEW query.enrollments WITH (security_barrier) AS
SELECT e.id, e.sequence_id, e.lead_id, e.mailbox_id, e.status, e.status_reason, e.next_step, e.next_send_at, e.created_at, e.updated_at
FROM enrollments e JOIN sequences q ON q.id = e.sequence_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND e.workspace_id = s.workspace_id AND q.workspace_id = s.workspace_id
WHERE 'sequences.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands));

-- Sent outreach messages, without their bodies.
CREATE VIEW query.messages WITH (security_barrier) AS
SELECT m.id, m.record_id, m.sequence_id, m.enrollment_id, m.step_position, m.lead_id, m.mailbox_id, m.from_email, m.to_email, m.subject,
    m.status, m.attempts, m.created_at, m.sent_at
FROM messages m LEFT JOIN sequences q ON q.id = m.sequence_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND m.workspace_id = s.workspace_id
WHERE 'sequences.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands));

-- Replies and other mail received, without their bodies.
CREATE VIEW query.replies WITH (security_barrier) AS
SELECT i.id, i.record_id, i.lead_id, i.sequence_id, i.enrollment_id, i.mailbox_id, i.from_email, i.subject, i.kind, i.category,
    i.opt_out, i.labels, i.done, i.received_at
FROM inbound_messages i LEFT JOIN mailboxes x ON x.id = i.mailbox_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND i.workspace_id = s.workspace_id
WHERE 'inbox.read' = ANY (s.scopes) AND (s.brands IS NULL OR x.brand_id IS NULL OR x.brand_id = ANY (s.brands));

-- Outcomes (outcomes.read).

CREATE VIEW query.deals WITH (security_barrier) AS
SELECT d.id, d.record_id, d.project_id, d.mission_id, d.sequence_id, d.lead_id, d.meeting_id, d.source, d.name, d.stage, d.state,
    d.amount_cents, d.currency, d.attribution, d.opened_at, d.closed_at, d.created_at, d.updated_at
FROM deals d LEFT JOIN sequences q ON q.id = d.sequence_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND d.workspace_id = s.workspace_id
WHERE 'outcomes.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands));

CREATE VIEW query.meetings WITH (security_barrier) AS
SELECT m.id, m.record_id, m.project_id, m.mission_id, m.sequence_id, m.lead_id, m.source, m.title, m.status, m.qualified, m.attribution,
    m.starts_at, m.ends_at, m.booked_at, m.created_at
FROM meetings m LEFT JOIN sequences q ON q.id = m.sequence_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND m.workspace_id = s.workspace_id
WHERE 'outcomes.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands));

CREATE VIEW query.spend WITH (security_barrier) AS
SELECT e.id, e.brand_id, e.project_id, e.sequence_id, e.mission_id, e.channel, e.category, e.provider, e.units, e.unit_name,
    e.amount_cents, e.currency, e.source, e.occurred_at
FROM spend_entries e JOIN query_scopes s ON s.pid = pg_backend_pid() AND e.workspace_id = s.workspace_id
WHERE 'outcomes.read' = ANY (s.scopes) AND (s.brands IS NULL OR e.brand_id IS NULL OR e.brand_id = ANY (s.brands));

-- Lifecycle email (emails.read), without bodies or other recipients.

CREATE VIEW query.emails WITH (security_barrier) AS
SELECT e.id, e.record_id, e.brand_id, e.broadcast_id, e.template_id, e.channel, e.purpose, e.subject, e.tags, e.status, e.bounce_type,
    e.test, e.opens, e.clicks, e.send_at, e.sent_at, e.delivered_at, e.opened_at, e.clicked_at, e.created_at
FROM emails e JOIN query_scopes s ON s.pid = pg_backend_pid() AND e.workspace_id = s.workspace_id
WHERE 'emails.read' = ANY (s.scopes) AND (s.brands IS NULL OR e.brand_id IS NULL OR e.brand_id = ANY (s.brands));

CREATE VIEW query.email_events WITH (security_barrier) AS
SELECT v.id, v.email_id, v.type, v.occurred_at
FROM email_events v JOIN emails e ON e.id = v.email_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND v.workspace_id = s.workspace_id AND e.workspace_id = s.workspace_id
WHERE 'emails.read' = ANY (s.scopes) AND (s.brands IS NULL OR e.brand_id IS NULL OR e.brand_id = ANY (s.brands));

CREATE VIEW query.broadcasts WITH (security_barrier) AS
SELECT b.id, b.brand_id, b.name, b.template_id, b.topic_id, b.status, b.recipients, b.skipped, b.send_at, b.started_at, b.finished_at,
    b.created_at
FROM email_broadcasts b JOIN query_scopes s ON s.pid = pg_backend_pid() AND b.workspace_id = s.workspace_id
WHERE 'emails.read' = ANY (s.scopes) AND (s.brands IS NULL OR b.brand_id IS NULL OR b.brand_id = ANY (s.brands));

-- Website visits (analytics.read), without the full landing URL.

CREATE VIEW query.visits WITH (security_barrier) AS
SELECT v.id, v.brand_id, v.record_id, v.anonymous_id, v.started_at, v.last_seen_at, v.events, v.pageviews, v.landing_path, v.referrer_host,
    v.utm_source, v.utm_medium, v.utm_campaign, v.utm_term, v.utm_content, v.country, v.device, v.browser, v.os
FROM site_sessions v JOIN query_scopes s ON s.pid = pg_backend_pid() AND v.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes) AND (s.brands IS NULL OR v.brand_id IS NULL OR v.brand_id = ANY (s.brands));

-- Social posts (social.read).

CREATE VIEW query.social_posts WITH (security_barrier) AS
SELECT p.id, p.brand_id, p.account_id, p.channel, p.url, p.body, p.posted_at, p.impressions, p.likes, p.replies, p.reposts, p.quotes,
    p.bookmarks, p.clicks, p.metrics_at
FROM social_posts p JOIN query_scopes s ON s.pid = pg_backend_pid() AND p.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes) AND (s.brands IS NULL OR p.brand_id IS NULL OR p.brand_id = ANY (s.brands));
