package webhooks

// Event is one kind of event a webhook endpoint can subscribe to. Every
// event comes from the audit log, written in the same transaction as the
// change it describes, so a webhook is only sent for a change that
// happened, and never lost for one that did.
type Event struct {
	Type        string `json:"type"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

// TestEvent is what POST .../webhooks/{id}/test sends.
const TestEvent = "webhook.test"

// Catalog lists the events webhooks deliver. An audit action not listed
// here is not delivered: listing one is a promise to keep its type and the
// meaning of its data. Add to it freely; never rename or remove.
var Catalog = []Event{
	// Replies and opt-outs
	{"lead.replied", "replies", "A lead replied. data has inbound_id (GET .../inbox/{id}), sequence_id, mailbox_id, from_email, subject, enrollments_stopped, and category when rules recognised one (out_of_office), else null."},
	{"lead.unsubscribed", "replies", "A lead opted out (unsubscribe link, header or a reply asking to stop) and was suppressed."},
	{"inbox.replied", "replies", "Someone in the workspace (or an agent) replied to an inbound message."},
	{"inbox.updated", "replies", "An inbound message was marked read or done, or its category was set (interested, meeting_booked, not_now, ...). data holds what changed."},
	{"inbox.classified", "replies", "A reply got a label, or a different one, from the built-in rules, the AI, a person or an agent. data has inbound_id, lead_id, sequence_id, mailbox_id, source (mailbox or the sequencer's connector id), category, group (hot, later, closed, automatic), previous_category, category_source, confidence (0 to 1) and needs_review."},
	// Deliverability
	{"lead.bounced", "deliverability", "An email to a lead bounced permanently and the address was suppressed."},
	{"mailbox.auto_paused", "deliverability", "A mailbox was paused automatically because its bounce rate crossed the limit."},
	// Leads
	{"lead.created", "leads", "A lead was created."},
	{"lead.updated", "leads", "A lead's fields were changed."},
	{"lead.deleted", "leads", "A lead was deleted."},
	{"leads.imported", "leads", "Leads were imported from a CSV."},
	{"suppressions.added", "leads", "Addresses or domains were added to the suppression list."},
	{"suppression.removed", "leads", "An entry was removed from the suppression list."},
	{"lead_list.created", "leads", "A lead list was created."},
	{"lead_list.leads_added", "leads", "Leads were added to a lead list."},
	{"lead_list.run_started", "leads", "A lead list build, refresh or enrichment from a data provider started."},
	{"lead_list.build_done", "leads", "A lead list build finished."},
	{"lead_list.build_failed", "leads", "A lead list build failed. data.error says why."},
	{"lead_list.refresh_done", "leads", "A lead list refresh finished."},
	{"lead_list.refresh_failed", "leads", "A lead list refresh failed."},
	{"lead_list.enrich_done", "leads", "A lead list enrichment finished."},
	{"lead_list.enrich_failed", "leads", "A lead list enrichment failed."},
	{"import.done", "leads", "An import from a connected sequencer finished."},
	{"import.failed", "leads", "An import from a connected sequencer failed."},
	// Sequences
	{"sequence.created", "sequences", "A sequence was created."},
	{"sequence.updated", "sequences", "A sequence's settings changed."},
	{"sequence.activated", "sequences", "A sequence started sending."},
	{"sequence.paused", "sequences", "A sequence was paused."},
	{"sequence.archived", "sequences", "A sequence was archived."},
	{"sequence.deleted", "sequences", "A sequence was deleted."},
	{"sequence.steps_updated", "sequences", "A sequence's steps were replaced."},
	{"sequence.leads_enrolled", "sequences", "Leads were enrolled in a sequence."},
	{"enrollment.stopped", "sequences", "An enrollment was stopped by hand."},
	// Mailboxes and domains
	{"mailbox.imported", "mailboxes", "Mailboxes were added or imported."},
	{"mailbox.updated", "mailboxes", "A mailbox's settings changed."},
	{"mailbox.paused", "mailboxes", "A mailbox was paused by hand or by its sequencer."},
	{"mailbox.resumed", "mailboxes", "A paused mailbox was resumed."},
	{"mailbox.stage_changed", "mailboxes", "A mailbox moved between warming, ready, resting and retired."},
	{"mailbox.deleted", "mailboxes", "A mailbox was removed."},
	{"domain.added", "mailboxes", "A sending domain was added."},
	{"domain.paused", "mailboxes", "A sending domain was paused, which pauses its mailboxes."},
	{"domain.resumed", "mailboxes", "A paused sending domain was resumed."},
	{"domain.removed", "mailboxes", "A sending domain was removed."},
	// Lifecycle email
	{"email.queued", "lifecycle_email", "A lifecycle or transactional email was queued. GET .../emails/{id} has it; data has purpose, recipients and template_id."},
	{"email.sent", "lifecycle_email", "The email provider accepted an email. data has provider_id, connector and purpose."},
	{"email.delivered", "lifecycle_email", "The receiving mail server accepted an email."},
	{"email.bounced", "lifecycle_email", "An email bounced permanently and the address was suppressed. data.bounce_type is hard."},
	{"email.complained", "lifecycle_email", "A recipient marked an email as spam and the address was suppressed."},
	{"email.failed", "lifecycle_email", "An email could not be sent. data.error_code says why; GET the email for the provider's answer."},
	{"email.suppressed", "lifecycle_email", "An email was not sent because every recipient is on the suppression list, or the provider refused the address."},
	{"email.cancelled", "lifecycle_email", "A queued or scheduled email was cancelled."},
	{"email.unsubscribed", "lifecycle_email", "A recipient used a lifecycle email's unsubscribe link and was suppressed."},
	{"email.opened", "lifecycle_email", "An email was opened for the first time: its tracking pixel loaded (track_opens), or the provider reported it."},
	{"email.clicked", "lifecycle_email", "A link in an email was clicked for the first time: a tracked link (track_clicks), or the provider reported it."},
	{"email.preferences_changed", "lifecycle_email", "Someone changed their topics or turned off all marketing email on the preference page behind an email's unsubscribe link. The choices are consents on their customer record."},
	{"email_domain.verified", "lifecycle_email", "A sending domain was verified by its email provider and can send."},
	{"email_domain.verification_failed", "lifecycle_email", "A sending domain was still not verified after three days of checks."},
	{"email_provider.events_failed", "lifecycle_email", "Outflare could not set up delivery events with an email provider. GET the provider for events_error."},
	{"email_template.published", "lifecycle_email", "A template's draft was published as a new version. data.version is its number."},
	// Outcomes
	{"meeting.created", "outcomes", "A meeting was booked or logged."},
	{"meeting.updated", "outcomes", "A meeting changed (held, no-show, cancelled, rescheduled)."},
	{"meeting.deleted", "outcomes", "A meeting was deleted."},
	{"deal.created", "outcomes", "A deal was created."},
	{"deal.updated", "outcomes", "A deal changed stage or value."},
	{"deal.deleted", "outcomes", "A deal was deleted."},
	{"spend.recorded", "outcomes", "Spend was recorded in the ledger."},
	// Connectors
	{"integration.connected", "connectors", "A connector was connected."},
	{"integration.updated", "connectors", "A connector's settings changed."},
	{"integration.disconnected", "connectors", "A connector was disconnected."},
	{"connect_request.created", "connectors", "A one-time connect link was created for a person to finish connecting a service."},
	// Deploys
	{"deploy.detected", "deploys", "A watched repository or project went live in production. data has target_id, site_id (the SEO site it serves, or null), kind (deployment or push), commit, ref and deployed_at; GET .../deploys lists them."},
	{"deploy.failed", "deploys", "A production deploy of a watched repository or project failed. data has target_id, site_id, kind, commit and ref."},
	// Digests
	{"digest.ready", "digests", "A daily or weekly digest of the workspace is ready. data has frequency, trigger (scheduled or manual), period_start, period_end, sections (their ids) and emailed; GET .../digests/{id} has the numbers."},
	// AI spend
	{"ai_budget.warned", "ai", "AI calls this month reached 80% of the workspace's monthly AI limit, or of one tool's limit. data has task (empty for the workspace's limit, else the tool id), level (80), limit_cents, spent_cents and month (YYYY-MM); GET .../ai-usage has the detail."},
	{"ai_budget.reached", "ai", "AI calls this month reached the workspace's monthly AI limit, or one tool's limit, so Outflare makes no more of those calls until next month or a higher limit. data has task, level (100), limit_cents, spent_cents and month."},
	{"connection_limit.warned", "connectors", "A paid connection (SEO data, lead data, email verification, page reads) used 80% of its daily or monthly usage limit. data has integration_id, connector, period (day or month), period_start, level (80), unit (USD, credit, API unit, token), limit, used and hard_stop; GET .../integrations/{id}/usage has the detail."},
	{"connection_limit.reached", "connectors", "A paid connection reached its daily or monthly usage limit, so with hard_stop Outflare makes no more calls through it until the next day or month, or a higher limit. data has the same fields as connection_limit.warned, with level 100."},
	// Experiments
	{"experiment.started", "experiments", "An SEO experiment started measuring: its site deployed, or a person said when the change went live. data has site_id, by (deploy or person), deploy_id and changed_at."},
	{"experiment.concluded", "experiments", "An SEO experiment ended with a result. data has site_id, design, metric, verdict (better, worse, no_clear_change or too_early), lift, p_value, days_measured and treatment_pages."},
	// Projects
	{"project.created", "projects", "A project was created."},
	{"project.updated", "projects", "A project's brief or settings changed."},
	{"project.deleted", "projects", "A project was deleted."},
	// Missions
	{"mission.created", "missions", "An agent mission was created."},
	{"mission.updated", "missions", "A mission's goal, limits or settings changed."},
	{"mission.submitted", "missions", "A mission was submitted for a person's approval."},
	{"mission.approved", "missions", "A mission was approved and is running."},
	{"mission.paused", "missions", "A mission was paused."},
	{"mission.resumed", "missions", "A paused mission was resumed."},
	{"mission.stopped", "missions", "A mission was stopped."},
	{"mission.completed", "missions", "A mission was marked complete."},
	{"mission.action_held", "missions", "A write in a mission is waiting for a person to approve it."},
	{"mission.action_approved", "missions", "A held mission write was approved and run."},
	{"mission.action_rejected", "missions", "A held mission write was rejected."},
	// Infrastructure
	{"infra.plan_created", "infrastructure", "An infrastructure plan was drafted."},
	{"infra.plan_approved", "infrastructure", "An infrastructure plan was approved, which may buy domains and mailboxes."},
	{"infra.ordered", "infrastructure", "Mailboxes in a plan were ordered from the supplier."},
	{"infra.order_failed", "infrastructure", "Ordering a plan's mailboxes failed. data.error says why."},
	{"infra.domains_bought", "infrastructure", "The domains in a plan were bought."},
	{"infra.mailbox_joined", "infrastructure", "A mailbox a plan ordered arrived and was added to the workspace."},
	{"infra.plan_warming", "infrastructure", "A plan's mailboxes are all in and warming up."},
	{"infra.plan_ready", "infrastructure", "A plan's mailboxes finished warming up and are ready to send."},
	{"infra.plan_cancelled", "infrastructure", "An infrastructure plan was cancelled."},
	// Fixes list
	{"fix.found", "fixes", "A new fix was added to the fixes list (a site audit found an issue). data has source, rule, subject_type, subject_id and run_id. GET .../fixes/{id}/prompt turns it into a prompt for a coding agent."},
	{"fix.updated", "fixes", "Someone or an agent took, finished, ignored or reopened a fix, or changed its note. data holds what changed."},
	{"fix.started", "fixes", "Someone or an agent took a fix before changing the site (POST .../fixes/{id}/start). data.autonomy says what fix autonomy decided."},
	{"fix.verify_requested", "fixes", "Someone or an agent said a fix is out and asked for a check (POST .../fixes/{id}/verify). data.run_id is the crawl that checks it."},
	{"fix.confirmed", "fixes", "A complete crawl no longer found a fix's issue, so it is done. data.run_id is the crawl."},
	{"fix.reopened", "fixes", "A fix marked done was found again by a crawl, so it is open again. data.run_id is the crawl."},
	// Customer records
	{"record.created", "customers", "A person, company or other record was created: by hand, an import, a lead, a sign-up or product events. data has type and origin."},
	{"record.bulk_created", "customers", "Many records were created at once (a lead import or unsubscribes from another sequencer). data has type, origin, via and count."},
	{"record.bulk_updated", "customers", "Many people were updated at once from their leads (an import, an enrichment run). data has type, via and count."},
	{"record.updated", "customers", "A record's name, origin, brand or attributes changed. data.type is its record type; GET .../records/{id}/history has what changed."},
	{"record.merged", "customers", "Two records were merged into one. The target is the record kept; data.from_id is the merged one and data has counts of what moved."},
	{"identity.verified", "customers", "Someone proved they own an email or phone, so it now claims that address for their record."},
	{"identity.closed", "customers", "An email, phone or id stopped pointing at a record (replaced or removed). It stays in the record's history."},
	{"relationship.added", "customers", "Two records were linked, such as a person to their company."},
	{"relationship.ended", "customers", "A link between two records ended."},
	{"consent.granted", "customers", "A person gave consent. data has channel, purpose, source, and topic (true when it is for one topic rather than the whole channel)."},
	{"consent.withdrawn", "customers", "A person withdrew consent, such as by unsubscribing from any email. data has channel, purpose, source and topic, as for consent.granted."},
	{"consent.bulk_withdrawn", "customers", "Many people withdrew consent at once, such as unsubscribes imported from another sequencer. data.count says how many."},
	// Brands
	{"brand.created", "brands", "A brand was added to the workspace."},
	{"brand.updated", "brands", "A brand's name, website or settings changed. data.fields lists what changed."},
	// Broadcasts and journeys
	{"email_broadcast.created", "lifecycle_email", "A broadcast was drafted or scheduled."},
	{"email_broadcast.started", "lifecycle_email", "A broadcast started sending."},
	{"email_broadcast.sent", "lifecycle_email", "A broadcast finished sending. data has recipients and skipped."},
	{"email_broadcast.failed", "lifecycle_email", "A broadcast stopped before reaching everyone. GET the broadcast for status_reason."},
	{"email_broadcast.cancelled", "lifecycle_email", "A broadcast was cancelled. data.emails_cancelled counts emails it had queued."},
	{"journey.created", "lifecycle_email", "A journey was created."},
	{"journey.updated", "lifecycle_email", "A journey's settings or steps changed."},
	{"journey_run.entered", "lifecycle_email", "A person entered a journey. The target is the journey; data.via is event or manual."},
	{"journey_run.exited", "lifecycle_email", "A person left a journey before its end. The target is the journey; data.reason says why (goal, manual, ...)."},
	// Rules
	{"rule.created", "rules", "A rule was created in the rules engine (draft until activated). data has trigger, conditions, fire, record_type and emit_event."},
	{"rule.updated", "rules", "A rule's name, description or definition changed. data.definition_changed says whether its version went up."},
	{"rule.activated", "rules", "A rule went live. It acts only on what happens from now on."},
	{"rule.paused", "rules", "A rule was paused: it fires for no one until activated again."},
	{"rule.archived", "rules", "A rule was archived for good. Its firings stay."},
	{"rule.fired", "rules", "A rule fired for a record and emitted its event on the record's timeline. data has record_id, event (the emitted event's name), event_id, trigger, depth and rule_version. Fires once per record and episode."},
	// Setup and install health
	{"brand_setup.installed", "setup", "A one-click install on a brand's setup checklist finished: the website script through Tag Manager, or a DNS record through the DNS host. data has brand_id, step, method and integration_id."},
	{"brand_setup.install_failed", "setup", "A one-click install on a brand's setup checklist failed. data has brand_id, step, method and integration_id; GET .../brands/{brand}/setup says why."},
	{"install.issue_opened", "setup", "Something installed stopped working or fell behind, such as a website script with no visits or an old SDK. data has kind, severity, subject and brand_id; GET .../install-health says what to do."},
	{"install.issue_resolved", "setup", "An install problem cleared. data has kind and brand_id."},
	// SEO
	{"seo_site.created", "seo", "A website was added for site audits."},
	{"seo_site.verified", "seo", "Ownership of a site was proved. data.method is how."},
	{"seo_site.unverified", "seo", "A site's ownership proof was gone at a later check, so it is crawled as anyone else's would be."},
	{"seo_site.deleted", "seo", "A site was removed from site audits."},
	{"crawl.started", "seo", "A crawl of a site started. data has site_id and trigger."},
	{"crawl.stopped", "seo", "A crawl was stopped by hand."},
	{"crawl.done", "seo", "A crawl finished. data has site_id, status (done, stopped or failed), reason and page counts; GET .../crawls/{id} has its audit."},
	{"link_pitch.drafted", "link_building", "A link pitch's opener was written (or couldn't be, data.reason) and it waits for a person to approve it. data has prospect_id, sequence_id and opener_written."},
	{"link_pitch.approved", "link_building", "A link pitch was approved (data.approved, or sent on its own when its sequence skips approval) and its contact enrolled. data has prospect_id, sequence_id, lead_id, record_id and edited."},
	{"link_pitch.rejected", "link_building", "A draft link pitch was rejected. data.prospect_id."},
	{"link_prospect.overdue", "link_building", "An agreed link isn't on the page 14 days after the site agreed; time to nudge them. data has site_id, record_id, method and days_since_agreed."},
	{"backlink.new", "seo", "A new link to one of the SEO sites was found. data has site_id, brand_id, source_url, target_url, source_domain, source_rating, nofollow, at, connector and run_id."},
	{"backlink.lost", "seo", "A link to one of the SEO sites is gone. data has the link as for backlink.new, plus reason when the provider says why (page removed, link removed, redirect)."},
	{"backlink.regained", "seo", "A lost link to one of the SEO sites is back. data has the link as for backlink.new."},
	{"competitor.page_removed", "seo", "A page is gone from a tracked competitor's sitemaps (at most 50 a look). Sites still linking to it may link to yours instead. data has site_id, competitor_id, url and removed (how many went in this look)."},
	// Link building
	{"link_prospect.found", "link_building", "A page that could link to an SEO site was added to the link prospects list. data has site_id, brand_id, record_id (the website's company), method, score, stage (prospect, or excluded with excluded_reason) and source."},
	{"link_prospect.checked", "link_building", "OutflareBot checked a link prospect's page. data has site_id, brand_id, record_id, status (up, gone, redirected, blocked, opted_out, unreachable, failed), links_to_us, broken (how many broken outbound links), fit (0 to 100, or null without AI), score, contacts (how many new) and error_code; GET the prospect for the check."},
	{"link_prospect.excluded", "link_building", "A person or agent took a link prospect out of outreach. data has excluded_reason and was."},
	{"link_prospect.pitched", "link_building", "A link prospect was pitched. No other prospect on the same website can be pitched until this one ends."},
	{"link_prospect.replied", "link_building", "The website answered the pitch for a link prospect."},
	{"link_prospect.agreed", "link_building", "The website agreed to link."},
	{"link_prospect.live", "link_building", "The link is live on the prospect's page."},
	{"link_prospect.declined", "link_building", "The website said no, or stopped answering. Its site can be pitched again after the cool-down."},
	{"link_prospect.removed", "link_building", "A live link from a prospect's page went away."},
	{"link_prospect.reopened", "link_building", "A link prospect went back to the list to pitch. data.was is the stage it left."},
	{"link_prospect.updated", "link_building", "A link prospect's page to offer (target_url) or note changed. data says which."},
	{"link_prospect.deleted", "link_building", "A link prospect was deleted."},
	// Social
	{"social_action.drafted", "social", "A social reply was drafted and waits for a person to approve it."},
	{"social_action.approved", "social", "A person approved a social reply, so it will post."},
	{"social_action.rejected", "social", "A person rejected a drafted social reply."},
	{"social_action.done", "social", "An approved social reply was posted from the brand's own account. data has channel and type; GET it for its link."},
	{"social_action.failed", "social", "An approved social reply could not be posted. GET it for status_reason."},
	// Reviews
	{"review.posted", "reviews", "A new review appeared on one of the brands' review pages. data has page_id, platform, rating (1 to 5), sentiment and verified; GET /reviews/{id} for its text."},
	{"review.replied", "reviews", "A review got the business's reply, seen on the platform (via platform) or marked in Outflare (via outflare)."},
	{"review.reply_drafted", "reviews", "The workspace's AI drafted an answer to a review, waiting for a person to edit and post it."},
	{"review_momentum.changed", "reviews", "A review page's momentum changed, such as from steady to slipping. data has from, to, score and score_change."},
	// Alerts
	{"alert.raised", "alerts", "Something needs a person now: a hot reply (reply.hot), a hot website visitor (visitor.hot) or another kind a module raises, at most once per thing. data has alert_id, kind, target_type, target_id, record_id, owner_id (the user it is for, or null), brand_id and the kind's own ids; GET .../alerts has what it says."},
	// Website visitors
	{"visitor.became_hot", "visitors", "A known person, or their company, reached the workspace's hot score on its website (at most once a week each). data has record_id, kind (person or company), score, hot_score, pageviews, key_pageviews, sessions and people."},
	// Partners: referral, affiliate and influencer programs
	{"partner_program.created", "partners", "A partner program (referral, affiliate or influencer) was created. data has kind, brand_id and rules (how many)."},
	{"partner_program.updated", "partners", "A partner program's settings changed, or it was paused, archived or restored. data has kind, status and changed (the settings sent)."},
	{"partner_program.rules_updated", "partners", "A partner program's reward rules were replaced. Rewards already in the ledger keep what they earned. data has kind and rules (how many)."},
	{"partner.created", "partners", "Someone joined a partner program. data has program_id, program_kind, record_id (the partner's customer record), status (pending waits for approval) and source."},
	{"partner.updated", "partners", "A partner was approved, suspended or rejected, moved to another group, or their payout method or note changed. data has program_id, record_id, status, previous_status and group_id."},
	{"partner_referral.created", "partners", "A partner was credited with a customer, bound by a lead or sale from your backend. data has program_id, partner_id, record_id (the customer), via (ref, click, promo_code or visit) and source."},
	{"partner_conversion.created", "partners", "A sign-up, sale, refund or custom conversion was recorded for a referred customer. data has program_id, partner_id, referral_id, record_id, type, event_name (custom only), recurring, amount_cents, currency, product_id, source and, for a refund, refund_of."},
	{"partner_reward.created", "partners", "A conversion earned a reward, or a fee or adjustment was logged; it waits out the program's hold. data has program_id, program_kind, partner_id, side (partner or referred), kind (cash, fee, action or adjustment), recipient_id, amount_cents, currency, conversion_id and, for an action, action and action_data."},
	{"partner_reward.approved", "partners", "A reward was approved: cash now counts toward the partner's next payout; an action is for your system to carry out (data has action, action_data, recipient_id and recipient_customer_id, your own user id for them), then call POST .../partner-rewards/{id}/fulfil. data.by is auto, manual or refund."},
	{"partner_reward.rejected", "partners", "A held reward was rejected, by a person or because its sale was refunded. data.reason says why."},
	{"partner_reward.reversed", "partners", "A refund took back part or all of a reward: a new adjustment entry with a negative amount for cash, or, for an action, data.action and action_data for your system to take back. data has reverses_id and reason."},
	{"partner_reward.fulfilled", "partners", "Your system reported that it carried out an approved action reward."},
	{"partner_payout.created", "partners", "A payout is due: a partner's approved cash, fees and adjustments were gathered. Pay it from your own account or payout tool, then mark it paid. data has partner_id, program_id, amount_cents, currency and rewards."},
	{"partner_payout.paid", "partners", "A payout was marked paid. data has partner_id, program_id, amount_cents, currency and reference."},
	{"partner_payout.canceled", "partners", "A due payout was canceled and its rewards went back into the partner's balance."},
	// Workspace
	{"workspace.updated", "workspace", "The workspace was renamed or its slug (its name in URLs) changed. data has the new and previous values."},
	// Webhooks
	{TestEvent, "webhooks", "Sent by the test endpoint to check that a webhook works."},
}

var catalogTypes = func() map[string]bool {
	m := map[string]bool{}
	for _, e := range Catalog {
		m[e.Type] = true
	}
	return m
}()

// Delivered reports whether webhooks deliver events of this type.
func Delivered(eventType string) bool { return catalogTypes[eventType] }

// ValidPattern reports whether p is a type in the catalog, a category
// wildcard such as "lead.*", or "*" for every event.
func ValidPattern(p string) bool {
	if p == "*" || catalogTypes[p] {
		return true
	}
	if len(p) > 2 && p[len(p)-2:] == ".*" {
		prefix := p[:len(p)-1]
		for t := range catalogTypes {
			if len(t) > len(prefix) && t[:len(prefix)] == prefix {
				return true
			}
		}
	}
	return false
}
