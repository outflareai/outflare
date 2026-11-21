-- Sales, support, scores, signals, partners, revenue, lists and the record
-- catalog in Query (AGENTS.md, "Data: agents can see all of it"), so an
-- agent can follow a customer from first touch to revenue and support:
-- "which partner referrals pay back", "accounts whose health score fell
-- this month", "who changed this field and when". Like every query view,
-- each shows one workspace's rows, the brands the caller may see and, in a
-- brand's query, that brand's alone, and needs the scope the API asks for
-- the same data: customers.read for sales, the CRM, scores, revenue, lists
-- and record history; inbox.read for support conversations; partners.read
-- for partner programs; outcomes.read for recurring spend; leads.read for
-- lead lists. Sealed content, message bodies, hashes, postback addresses
-- and payout tokens are left out, and so is the full copy a merge keeps for
-- undo.

CREATE VIEW query.sales_leads WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.record_id, x.source, x.source_id, x.integration_id, x.state, x.owner_id, x.sdr_id,
    x.ae_id, x.amount_minor, x.currency, x.introduced_at, x.introduced_by, x.confirmed_at, x.confirmed_by,
    x.reply_state, x.version, x.created_at, x.updated_at, x.feed_kind, x.feed_channel_id, x.feed_source,
    x.quality
FROM sales_leads x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.sales_messages WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.record_id, x.lead_id, x.sender_id, x.handoff_to, x.draft_id, x.superseded_by,
    x.kind, x.direction, x.status, x.occurred_at, x.created_at
FROM sales_messages x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.sales_events WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.record_id, x.lead_id, x.actor_id, x.actor_role, x.action, x.schema_version,
    x.occurred_at
FROM sales_events x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.sales_members WITH (security_barrier) AS
SELECT x.brand_id, x.user_id, x.role, x.introduction, x.queue_seen_at
FROM sales_members x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.sales_quotas WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.staff_id, x.period, x.target_minor, x.created_at, x.updated_at
FROM sales_quotas x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.sales_templates WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.owner_id, x.version, x.archived, x.favorite
FROM sales_templates x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.crm_activities WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.record_id, x.kind, x.title, x.status, x.priority, x.assigned_to, x.due_at,
    x.created_by, x.version, x.created_at, x.updated_at, x.integration_id, x.source_id, x.event_state
FROM crm_activities x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.crm_calendar_events WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.owner_id, x.record_id, x.event_key, x.starts_at, x.ends_at, x.canceled, x.privacy,
    x.privacy_checked_at, x.created_at, x.updated_at
FROM crm_calendar_events x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.crm_mail_messages WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.owner_id, x.record_id, x.occurred_at, x.created_at, x.privacy, x.privacy_checked_at
FROM crm_mail_messages x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.crm_mail_sends WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.owner_id, x.record_id, x.message_id, x.status, x.error_code, x.provider_id,
    x.crm_mail_message_id, x.created_at, x.updated_at, x.sent_at
FROM crm_mail_sends x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.support_conversations WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.record_id, x.mailbox_id, x.channel, x.subject, x.status, x.assignee_id, x.team_id,
    x.tags, x.contact_email, x.first_message_at, x.first_reply_at, x.closed_at, x.last_message_at,
    x.last_from, x.unread, x.created_at, x.updated_at, x.inbound_address_id, x.reply_from, x.opened_at,
    x.reply_due_at, x.reply_breached_at, x.resolve_due_at, x.resolve_breached_at, x.sla_breaches,
    x.chat_widget_id, x.identity_verified, x.visitor_seen_at, x.knowledge_gap, x.topic_id, x.event_state,
    x.source, x.integration_id, x.external_app, x.external_id, x.external_channel, x.external_assignee_id,
    x.external_team_id, x.priority, x.reopens, x.rating, x.rating_remark, x.rated_at, x.ai_agent,
    x.ai_resolution, x.ai_answer_type, x.ai_source_type, x.ai_source_title, x.ai_rating, x.ai_rating_remark,
    x.ai_sources, x.external_updated_at
FROM support_conversations x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'inbox.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.support_messages WITH (security_barrier) AS
SELECT x.id, x.conversation_id, x.kind, x.record_id, x.author_id, x.from_email, x.from_name, x.to_email,
    x.subject, x.message_id, x.in_reply_to, x.auto_reply, x.status, x.error, x.mailbox_id, x.created_at,
    x.sent_at, x.inbound_email_id, x.email_id, x.brand_id, x.external_id, x.author_type, x.external_author_id
FROM support_messages x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'inbox.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.support_assistant_runs WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.conversation_id, x.message_id, x.channel, x.action, x.reason, x.cited, x.checks,
    x.outcome, x.trigger, x.provider, x.model, x.duration_ms, x.decided_by, x.decided_at, x.created_at,
    x.sent_message_id, x.feedback, x.feedback_at, x.send_at
FROM support_assistant_runs x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'inbox.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.conversation_sentiments WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.record_id, x.channel, x.ref, x.label, x.quote, x.read_at
FROM conversation_sentiments x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.support_sla_policies WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.enabled, x.first_reply_minutes, x.next_reply_minutes, x.resolution_minutes,
    x.timezone, x.hours, x.created_at, x.updated_at
FROM support_sla_policies x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'inbox.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.help_centre_days WITH (security_barrier) AS
SELECT x.centre_id, x.day, x.views, x.searches, x.empty_searches, x.brand_id
FROM help_centre_days x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'workspace.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.help_article_days WITH (security_barrier) AS
SELECT x.item_id, x.day, x.views, x.helpful, x.unhelpful, x.brand_id
FROM help_article_days x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'workspace.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.score_definitions WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.key, x.label, x.description, x.record_type, x.base, x.factors, x.high_from,
    x.low_below, x.watch_fields, x.status, x.version, x.scoring_since, x.scored_at, x.created_at,
    x.updated_at, x.nature, x.parts, x.higher_is_better, x.needs
FROM score_definitions x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.score_history WITH (security_barrier) AS
SELECT x.brand_id, x.record_id, x.score_id, x.day, x.value, x.band
FROM score_history x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.signals WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.record_id, x.type, x.title, x.detail, x.status, x.outcome, x.owner_id, x.due_at,
    x.snoozed_until, x.source, x.event_id, x.data, x.escalated_at, x.created_by, x.closed_by, x.created_at,
    x.updated_at, x.closed_at
FROM signals x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.signal_types WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.key, x.label, x.description, x.events, x.due_days, x.archived_at, x.created_at,
    x.updated_at
FROM signal_types x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ideal_customer_profiles WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.name, x.description, x.filters, x.status, x.version, x.created_by, x.updated_by,
    x.created_at, x.updated_at
FROM ideal_customer_profiles x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'profiles.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.partner_programs WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.name, x.slug, x.kind, x.status, x.landing_url, x.currency, x.credit_window_days,
    x.credit_model, x.codes_win, x.hold_days, x.auto_approve, x.auto_accept, x.min_payout_cents, x.terms_url,
    x.sales_source, x.portal_enabled, x.portal_subdomain, x.portal, x.created_by, x.created_at, x.updated_at,
    x.portal_domain_id, x.default_group_id, x.switched_over_at, x.pay_browser_signups,
    x.block_self_referrals, x.payout_term_days, x.payout_methods, x.lead_expiry_days, x.url_params,
    x.block_ad_clicks, x.block_social_clicks, x.blocked_countries, x.credits_from
FROM partner_programs x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'partners.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.partner_memberships WITH (security_barrier) AS
SELECT x.id, x.program_id, x.record_id, x.code, x.status, x.group_id, x.campaign_id, x.note,
    x.source, x.application, x.terms_accepted_at, x.notify, x.created_by, x.created_at, x.updated_at,
    x.payout_type, x.accepted_terms_version, x.brand_id
FROM partner_memberships x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'partners.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.partner_groups WITH (security_barrier) AS
SELECT x.id, x.program_id, x.name, x.created_at, x.brand_id
FROM partner_groups x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'partners.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.partner_clicks WITH (security_barrier) AS
SELECT x.id, x.membership_id, x.sub_id, x.referrer_host, x.created_at, x.external_id, x.brand_id
FROM partner_clicks x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'partners.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.partner_referrals WITH (security_barrier) AS
SELECT x.id, x.program_id, x.membership_id, x.record_id, x.via, x.click_id, x.sub_id, x.status,
    x.first_sale_at, x.created_at, x.brand_id, x.session_id, x.promo_code, x.link_click_id
FROM partner_referrals x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'partners.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.partner_conversions WITH (security_barrier) AS
SELECT x.id, x.program_id, x.referral_id, x.record_id, x.type, x.event_name, x.recurring, x.external_id,
    x.refund_of, x.amount_cents, x.currency, x.product_id, x.source, x.occurred_at, x.created_at,
    x.payment_source, x.billing_interval, x.brand_id
FROM partner_conversions x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'partners.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.partner_rewards WITH (security_barrier) AS
SELECT x.id, x.program_id, x.membership_id, x.side, x.recipient_id, x.kind, x.conversion_id, x.rule_id,
    x.reverses_id, x.amount_cents, x.currency, x.action, x.action_data, x.status, x.hold_until, x.payout_id,
    x.note, x.approved_at, x.created_at, x.updated_at, x.auto_approve, x.brand_id, x.rule
FROM partner_rewards x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'partners.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.partner_payouts WITH (security_barrier) AS
SELECT x.id, x.membership_id, x.amount_cents, x.currency, x.status, x.note, x.reference, x.paid_at,
    x.created_by, x.created_at, x.updated_at, x.brand_id
FROM partner_payouts x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'partners.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.partner_promo_codes WITH (security_barrier) AS
SELECT x.id, x.membership_id, x.code, x.coupon, x.integration_id, x.external_id, x.sync_status, x.sync_error,
    x.created_at, x.rules, x.brand_id
FROM partner_promo_codes x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'partners.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.commission_plans WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.name, x.status, x.positions, x.staff_ids, x.lapse_days, x.rules, x.created_at,
    x.updated_at
FROM commission_plans x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.commission_statements WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.staff_id, x.month, x.status, x.total_minor, x.currency, x.lines, x.approved_by,
    x.approved_at, x.paid_at
FROM commission_statements x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.revenue_days WITH (security_barrier) AS
SELECT x.brand_id, x.day, x.currency, x.mrr_minor, x.customers, x.at_risk_minor, x.at_risk_customers,
    x.new_minor, x.expansion_minor, x.reactivation_minor, x.contraction_minor, x.churned_minor,
    x.new_customers, x.churned_customers, x.bands, x.updated_at
FROM revenue_days x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.revenue_customers WITH (security_barrier) AS
SELECT x.record_id, x.brand_id, x.mrr_minor, x.updated_at
FROM revenue_customers x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.revenue_forecast_history WITH (security_barrier) AS
SELECT x.brand_id, x.quarter, x.week, x.currency, x.target_minor, x.won_minor, x.renewals_expected_minor,
    x.deals_expected_minor, x.forecast_minor, x.best_case_minor, x.open_count, x.taken_at
FROM revenue_forecast_history x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.spend_recurring WITH (security_barrier) AS
SELECT x.id, x.sequence_id, x.category, x.provider, x.name, x.amount_cents, x.currency, x.every, x.starts_on,
    x.ends_on, x.note, x.created_by, x.created_at, x.updated_at, x.channel, x.brand_id
FROM spend_recurring x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'outcomes.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.product_aliases WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.product_id, x.source, x.value, x.status, x.seen_count, x.first_seen_at,
    x.last_seen_at, x.created_at, x.updated_at
FROM product_aliases x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.smart_lists WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.name, x.description, x.rules, x.match, x.members, x.refreshed_at, x.error_code,
    x.created_by, x.created_at, x.updated_at, x.kind, x.kit_id, x.kit_key, x.record_type, x.saved_query_id
FROM smart_lists x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.smart_list_members WITH (security_barrier) AS
SELECT x.list_id, x.record_id, x.entered_at, x.brand_id
FROM smart_list_members x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.static_list_members WITH (security_barrier) AS
SELECT x.list_id, x.record_id, x.entered_at, x.added_via, x.added_ref, x.brand_id, x.batch_id
FROM static_list_members x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.lead_lists WITH (security_barrier) AS
SELECT x.id, x.name, x.created_at, x.description, x.kind, x.search_integration_id, x.filters,
    x.enrich_integration_ids, x.max_leads, x.credit_cap, x.verify, x.verified_emails_only,
    x.skip_contacted_days, x.skip_in_sequences, x.min_score, x.refresh_every_days, x.next_refresh_at,
    x.created_by, x.updated_at, x.brand_id, x.profile_id
FROM lead_lists x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'leads.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.field_definitions WITH (security_barrier) AS
SELECT x.id, x.record_type, x.key, x.label, x.description, x.value_type, x.option_set, x.multiple,
    x.target_type, x.write_policy, x.precedence, x.sensitivity, x.filterable, x.archived_at, x.created_by,
    x.created_at, x.updated_at, x.brand_id, x.manual_edit_days
FROM field_definitions x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.option_sets WITH (security_barrier) AS
SELECT x.id, x.key, x.name, x.options, x.created_by, x.created_at, x.updated_at, x.brand_id
FROM option_sets x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.record_types WITH (security_barrier) AS
SELECT x.id, x.key, x.name, x.plural, x.description, x.archived_at, x.created_by, x.created_at, x.updated_at,
    x.brand_id
FROM record_types x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.attribute_changes WITH (security_barrier) AS
SELECT x.id, x.record_id, x.field_key, x.old_value, x.new_value, x.source, x.actor_id, x.request_id,
    x.changed_at, x.brand_id, x.batch_id, x.bulk_edit_id
FROM attribute_changes x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.record_merges WITH (security_barrier) AS
SELECT x.id, x.into_id, x.from_id, x.moved, x.merged_by, x.request_id, x.merged_at, x.brand_id, x.reversible,
    x.undone_at, x.undone_by
FROM record_merges x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);
