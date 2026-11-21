package api

import "maps"

// The catalog of the sales, support, score, signal, partner, revenue, list
// and record catalog query tables (migration 0650).
func init() { maps.Copy(queryCatalog, customersCatalog) }

var customersCatalog = map[string]dataCatalogTable{
	"sales_leads": {
		Area: "customers", Source: "Made in Sales by the team, sales feeds and connected sources.",
		Columns: map[string]string{
			"introduced_by": "Who introduced it.",
			"confirmed_by":  "Who confirmed it.",
			"reply_state":   "Where the reply stands.",
			"feed_kind":     "The kind of sales feed it came from.",
			"feed_source":   "The feed's source.",
			"quality":       "Its quality rating.",
			"owner_id":      "Who owns it.",
			"sdr_id":        "The SDR.",
			"ae_id":         "The account executive.",
		},
	},
	"sales_messages": {
		Area: "customers", Source: "Sales, through the team's connected mailboxes and channels.",
		Columns: map[string]string{
			"handoff_to":    "Who it was handed to.",
			"superseded_by": "The message that replaced it.",
			"direction":     "in or out.",
		},
	},
	"sales_events": {
		Area: "customers", Source: "Sales.",
		Columns: map[string]string{
			"actor_role":     "The role of who did it: SDR, AE and the like.",
			"action":         "What they did (see common values).",
			"schema_version": "The version of the event's shape.",
		},
	},
	"sales_members": {
		Area: "customers", Source: "Set up in Sales.",
		Columns: map[string]string{
			"role":         "Their role: SDR, AE, manager.",
			"introduction": "How they introduce themselves.",
		},
	},
	"sales_quotas": {
		Area: "customers", Source: "Set in Sales.",
		Columns: map[string]string{
			"period":   "The period it covers.",
			"staff_id": "The salesperson.",
		},
	},
	"sales_templates": {
		Area: "customers", Source: "Made in Sales.",
		Columns: map[string]string{
			"archived": "Whether it is archived.",
			"favorite": "Whether it is a favourite.",
		},
	},
	"sales_alert_settings": {
		Area: "customers", Source: "Set in Sales, Alerts.", Time: "updated_at",
		Columns: map[string]string{
			"integration_id": "The Slack connection the alerts post through.",
			"channel_id":     "The Slack channel's id.",
			"channel_name":   "The Slack channel's name.",
			"enabled":        "Whether alerts are on.",
			"replies":        "Whether conversations that started from an email reply are posted.",
			"enabled_at":     "When alerts were last turned on; only conversations after it are posted.",
			"error_code":     "Why the last post failed, or empty.",
		},
	},
	"crm_activities": {
		Area: "customers", Source: "Made by the team, agents and connected CRMs.",
		Columns: map[string]string{
			"assigned_to": "Who it is assigned to.",
			"due_at":      "When it is due.",
		},
	},
	"crm_calendar_events": {
		Area: "customers", Source: "The team's connected calendars.",
		Columns: map[string]string{
			"event_key": "The calendar's id for the event.",
			"canceled":  "Whether it was cancelled.",
			"starts_at": "When it starts.",
			"ends_at":   "When it ends.",
		},
	},
	"crm_mail_messages": {
		Area: "customers", Source: "The team's connected mailboxes.",
	},
	"crm_mail_sends": {
		Area: "customers", Source: "Outflare, through the team's mailboxes.",
	},
	"support_conversations": {
		Area: "customers", Source: "Support: email, chat, and connected help desks.",
		Columns: map[string]string{
			"contact_email":     "The customer's email address.",
			"last_from":         "Who wrote last: customer or team.",
			"unread":            "Whether the team has unread messages.",
			"reply_from":        "The address replies go from.",
			"sla_breaches":      "How many response targets it missed.",
			"identity_verified": "Whether the chat visitor's identity was verified.",
			"knowledge_gap":     "Whether the help centre lacked the answer.",
			"external_app":      "The connected help desk, if it came from one.",
			"external_channel":  "The help desk's channel.",
			"reopens":           "How many times it reopened.",
			"rating":            "The customer's rating.",
			"rating_remark":     "What the customer wrote with the rating.",
			"ai_agent":          "Whether the AI agent handled it.",
			"ai_resolution":     "How the AI agent resolved it.",
			"ai_answer_type":    "The kind of AI answer.",
			"ai_source_type":    "What the AI answer relied on.",
			"ai_source_title":   "The title of that source.",
			"ai_rating":         "The customer's rating of the AI answer.",
			"ai_rating_remark":  "What they wrote with it.",
			"ai_sources":        "The AI answer's sources, as JSON.",
			"tags":              "Tags on it.",
		},
	},
	"support_messages": {
		Area: "customers", Source: "Support.",
		Columns: map[string]string{
			"from_name":   "The sender's name.",
			"in_reply_to": "The message it answers.",
			"auto_reply":  "Whether it is an automatic reply.",
			"error":       "Why sending failed.",
			"author_type": "customer, team, AI agent or system.",
		},
	},
	"support_assistant_runs": {
		Area: "customers", Source: "Outflare's support agent, on the brand's own AI key.",
		Columns: map[string]string{
			"cited":      "What it cited, as JSON.",
			"checks":     "The checks it ran, as JSON.",
			"model":      "The AI model.",
			"decided_by": "Who approved or rejected it.",
			"feedback":   "The feedback on it.",
		},
	},
	"conversation_sentiments": {
		Area: "customers", Source: "Read by Outflare from conversations, on the brand's own AI key.",
		Columns: map[string]string{
			"ref":     "The conversation or message it was read from.",
			"label":   "positive, neutral or negative.",
			"quote":   "The words that show it.",
			"read_at": "When it was read.",
		},
	},
	"support_sla_policies": {
		Area: "customers", Source: "Set in Support.",
		Columns: map[string]string{
			"first_reply_minutes": "Target for the first reply.",
			"next_reply_minutes":  "Target for later replies.",
			"resolution_minutes":  "Target for resolution.",
			"timezone":            "The time zone of the hours.",
			"hours":               "Business hours, as JSON.",
		},
	},
	"help_centre_days": {
		Area: "customers", Source: "Outflare's help centre.",
		Columns: map[string]string{
			"centre_id":      "The help centre.",
			"searches":       "Searches.",
			"empty_searches": "Searches that found nothing.",
		},
	},
	"help_article_days": {
		Area: "customers", Source: "Outflare's help centre.",
		Columns: map[string]string{
			"item_id":   "The help article (knowledge item).",
			"helpful":   "Helpful votes.",
			"unhelpful": "Unhelpful votes.",
		},
	},
	"score_definitions": {
		Area: "customers", Source: "Set up in Scores.",
		Columns: map[string]string{
			"base":             "The score's starting value.",
			"factors":          "What raises or lowers it, as JSON.",
			"high_from":        "Values from here are high.",
			"low_below":        "Values below here are low.",
			"watch_fields":     "Fields that make it recompute.",
			"scoring_since":    "When scoring started.",
			"scored_at":        "When it was last computed.",
			"nature":           "What kind of score: fit, health, engagement.",
			"parts":            "Its parts, as JSON.",
			"higher_is_better": "Whether a higher value is better.",
			"needs":            "What it needs to compute.",
		},
	},
	"score_history": {
		Area: "customers", Source: "Computed daily by Outflare.",
		Columns: map[string]string{
			"score_id": "The score (score_definitions.id).",
			"band":     "high, medium or low.",
			"value":    "The score's value.",
		},
	},
	"signals": {
		Area: "customers", Source: "Raised by Outflare's rules and the brand's connections.",
		Columns: map[string]string{
			"snoozed_until": "Until when it is snoozed.",
			"data":          "More about it, as JSON.",
			"closed_by":     "Who closed it.",
		},
	},
	"signal_types": {
		Area: "customers", Source: "Set up in Signals.",
		Columns: map[string]string{
			"due_days": "Days to act on it.",
		},
	},
	"ideal_customer_profiles": {
		Area: "customers", Source: "Set up by the brand.",
	},
	"partner_programs": {
		Area: "customers", Source: "Set up in Partners.",
		Columns: map[string]string{
			"slug":                 "Its short name in links.",
			"landing_url":          "Where partner links lead.",
			"credit_window_days":   "How long after a click a sign-up is credited.",
			"credit_model":         "first or last click.",
			"codes_win":            "Whether a promo code beats a link click.",
			"hold_days":            "How long rewards are held before payout.",
			"auto_accept":          "Whether partners are accepted without a person.",
			"terms_url":            "The program's terms.",
			"sales_source":         "Where sales are read from.",
			"portal_enabled":       "Whether partners have a portal.",
			"portal_subdomain":     "The portal's subdomain.",
			"portal":               "The portal's settings, as JSON.",
			"pay_browser_signups":  "Whether sign-ups seen only in the browser earn.",
			"block_self_referrals": "Whether partners can't refer themselves.",
			"payout_term_days":     "Days after the month that payouts are due.",
			"payout_methods":       "How partners may be paid.",
			"lead_expiry_days":     "Days a lead stays credited.",
			"url_params":           "The link parameters it reads.",
			"block_ad_clicks":      "Whether clicks from ads are refused.",
			"block_social_clicks":  "Whether clicks from social sites are refused.",
			"blocked_countries":    "Countries whose clicks are refused.",
			"credits_from":         "When crediting starts.",
			"min_payout_cents":     "The least a payout may be.",
			"default_group_id":     "The group new partners join.",
		},
	},
	"partner_offer_templates": {
		Area: "customers", Source: "Saved by the team in Partnerships, Deals, when writing an offer.",
		Columns: map[string]string{
			"config": "The offer's terms, as JSON: what is asked for, what is paid and when, the link type and the message.",
		},
	},
	"partner_link_rules": {
		Area: "customers", Source: "Set in Partnerships, Payments. A brand without a row uses restore within 14 days or refund, for 12 months.",
		Columns: map[string]string{
			"removal":          "What happens when a paid link is removed: restore_or_refund, restore_or_refund_forever, shrinking_refund or warn_only.",
			"restore_days":     "Days the site owner has to put the link back before a refund is due.",
			"guarantee_months": "Months after payment that the rule applies.",
		},
	},
	"partner_memberships": {
		Area: "customers", Source: "Partners join or are added in Partners.",
		Columns: map[string]string{
			"program_id":             "The program (partner_programs.id).",
			"group_id":               "The partner group (partner_groups.id).",
			"code":                   "The partner's referral code.",
			"application":            "What the partner wrote when applying, as JSON.",
			"notify":                 "Whether the partner gets emails.",
			"payout_type":            "The kind of payout account.",
			"accepted_terms_version": "The version of the terms they accepted.",
			"terms_accepted_at":      "When they accepted the terms.",
		},
	},
	"partner_groups": {
		Area: "customers", Source: "Set up in Partners.",
	},
	"partner_clicks": {
		Area: "customers", Source: "Outflare's partner links.",
		Columns: map[string]string{
			"membership_id": "The partner (partner_memberships.id).",
			"sub_id":        "The sub id the partner passed.",
		},
	},
	"partner_referrals": {
		Area: "customers", Source: "Outflare, from partner clicks, codes and sign-ups.",
		Columns: map[string]string{
			"via":           "How it was credited: link, code or by hand.",
			"promo_code":    "The promo code used, if one.",
			"first_sale_at": "When the person first paid.",
		},
	},
	"partner_conversions": {
		Area: "customers", Source: "Outflare, from the brand's payments and events.",
		Columns: map[string]string{
			"referral_id":      "The referral (partner_referrals.id).",
			"event_name":       "The event that counted.",
			"recurring":        "Whether it is a renewal.",
			"refund_of":        "The conversion this refunds.",
			"payment_source":   "The payment connection it came from.",
			"billing_interval": "month, year and the like.",
		},
	},
	"partner_rewards": {
		Area: "customers", Source: "Outflare, by the program's reward rules.",
		Columns: map[string]string{
			"side":          "Who earns: the partner or the person referred.",
			"recipient_id":  "Who gets it.",
			"conversion_id": "The conversion it is for (partner_conversions.id).",
			"reverses_id":   "The reward it reverses.",
			"action":        "What the reward is: money, credit or a coupon.",
			"action_data":   "The reward's details, as JSON.",
			"hold_until":    "When it can be paid.",
			"payout_id":     "The payout that paid it (partner_payouts.id).",
			"rule":          "The rule that made it, as JSON.",
		},
	},
	"partner_payouts": {
		Area: "customers", Source: "Made in Partners.",
		Columns: map[string]string{
			"reference": "The payment reference.",
		},
	},
	"partner_promo_codes": {
		Area: "customers", Source: "Made in Partners.",
		Columns: map[string]string{
			"coupon":      "The coupon it applies in the payment connection.",
			"sync_status": "Whether it is synced to the payment connection.",
			"sync_error":  "Why the sync failed.",
		},
	},
	"commission_plans": {
		Area: "customers", Source: "Set up in Sales.",
		Columns: map[string]string{
			"positions":  "The sales roles it covers.",
			"staff_ids":  "The people on it.",
			"lapse_days": "Days after which a deal no longer earns.",
		},
	},
	"commission_statements": {
		Area: "customers", Source: "Computed by Outflare from the plans.",
		Columns: map[string]string{
			"staff_id":    "The salesperson.",
			"month":       "The month it covers.",
			"lines":       "Each deal and what it earned, as JSON.",
			"approved_by": "Who approved it.",
			"total_minor": "The total earned.",
		},
	},
	"revenue_days": {
		Area: "revenue", Source: "Computed daily by Outflare from subscriptions and payments.",
		Columns: map[string]string{
			"mrr_minor":         "Monthly recurring revenue.",
			"customers":         "Paying customers.",
			"at_risk_customers": "Customers at risk of churning.",
			"new_customers":     "New paying customers that day.",
			"churned_customers": "Customers lost that day.",
			"bands":             "Customers and MRR by size band, as JSON.",
		},
	},
	"revenue_customers": {
		Area: "revenue", Source: "Computed by Outflare from subscriptions.",
	},
	"revenue_forecast_history": {
		Area: "revenue", Source: "Computed weekly by Outflare from deals and subscriptions.",
		Columns: map[string]string{
			"quarter":    "The quarter, such as 2026-Q4.",
			"week":       "The week of the quarter.",
			"open_count": "Open deals.",
		},
	},
	"spend_recurring": {
		Area: "revenue", Source: "Entered by the brand in Spend.",
		Columns: map[string]string{
			"every":     "How often it is charged.",
			"starts_on": "When it starts.",
			"ends_on":   "When it ends, if it does.",
		},
	},
	"product_aliases": {
		Area: "customers", Source: "Found by Outflare in payments and events; confirmed by the brand.",
		Columns: map[string]string{
			"value":      "The name as it appears.",
			"seen_count": "How often it was seen.",
		},
	},
	"smart_lists": {
		Area: "customers", Source: "Made by the brand and its agents.",
		Columns: map[string]string{
			"members": "How many records are in it.",
			"kit_key": "The list kit it came from.",
			"match":   "all or any.",
		},
	},
	"smart_list_members": {
		Area: "customers", Source: "Computed by Outflare from each list's rules.",
	},
	"static_list_members": {
		Area: "customers", Source: "Added by the team, agents and imports.",
		Columns: map[string]string{
			"added_via": "How the record was added.",
			"added_ref": "What added it.",
		},
	},
	"lead_lists": {
		Area: "outreach", Source: "Made in Leads.",
		Columns: map[string]string{
			"enrich_integration_ids": "The connections that enrich its leads.",
			"max_leads":              "The most leads it takes.",
			"credit_cap":             "The most data credits it may spend.",
			"verify":                 "Whether emails are verified.",
			"verified_emails_only":   "Whether only verified emails are kept.",
			"skip_contacted_days":    "Leads contacted in this many days are skipped.",
			"skip_in_sequences":      "Whether leads already in sequences are skipped.",
			"min_score":              "The lowest score a lead may have.",
			"refresh_every_days":     "How often it refreshes, in days.",
		},
	},
	"field_definitions": {
		Area: "customers", Source: "The record catalog in Settings.",
		Columns: map[string]string{
			"value_type":       "What kind of value: text, number, date, option, record and the like.",
			"option_set":       "The option list it chooses from (option_sets.key).",
			"multiple":         "Whether it holds more than one value.",
			"target_type":      "For a record field, the record type it points to.",
			"write_policy":     "Who may write it: anyone, only some sources, or a ranking.",
			"precedence":       "Which sources win when they disagree, as JSON.",
			"sensitivity":      "How sensitive its values are: normal, personal or sensitive.",
			"filterable":       "Whether lists can filter on it.",
			"manual_edit_days": "Days a person's edit is kept before sources may change it again.",
		},
	},
	"option_sets": {
		Area: "customers", Source: "The record catalog in Settings.",
		Columns: map[string]string{
			"options": "The options, as JSON.",
		},
	},
	"record_types": {
		Area: "customers", Source: "The record catalog in Settings.",
		Columns: map[string]string{
			"plural": "The plural name.",
		},
	},
	"attribute_changes": {
		Area: "customers", Source: "Every change to a record, by a person, agent, import or connection.",
		Columns: map[string]string{
			"old_value":    "The value before, as JSON.",
			"new_value":    "The value after, as JSON.",
			"source":       "Where the change came from: a person, an agent, an import, a connection, a rule.",
			"actor_id":     "Who made it.",
			"batch_id":     "Changes made together.",
			"bulk_edit_id": "The bulk edit that made it, if one did (bulk_edits.id).",
			"changed_at":   "When it changed.",
		},
	},
	"record_merges": {
		Area: "customers", Source: "Merges by the team, agents and Outflare's matching.",
		Columns: map[string]string{
			"into_id":    "The record that was kept.",
			"from_id":    "The record merged into it.",
			"moved":      "What moved, as counts per table.",
			"merged_by":  "Who merged them.",
			"reversible": "Whether the merge can be undone.",
			"undone_at":  "When it was undone, if it was.",
			"undone_by":  "Who undid it.",
		},
	},
}
