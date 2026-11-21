package api

import "maps"

// The catalog of the agent work, experiment, journey, social, content and
// email set-up query tables (migration 0651).
func init() { maps.Copy(queryCatalog, agentsCatalog) }

var agentsCatalog = map[string]dataCatalogTable{
	"agent_runs": {
		Area: "agents", Source: "Started by the team, playbooks and automations.",
		Columns: map[string]string{
			"inputs":          "What it was started with, as JSON.",
			"report":          "What it reported at the end.",
			"max_spend_cents": "The most it may spend, in cents.",
			"token_id":        "The API token it acts with.",
		},
	},
	"agent_run_updates": {
		Area: "agents", Source: "Written by agents and the team during runs.",
	},
	"missions": {
		Area: "agents", Source: "Started by the team and agents.",
		Columns: map[string]string{
			"goal_metric":     "The metric the goal is measured by.",
			"goal_target":     "The value to reach.",
			"max_sends":       "The most emails it may send.",
			"runner":          "What runs it.",
			"max_spend_cents": "The most it may spend, in cents.",
		},
	},
	"mission_actions": {
		Area: "agents", Source: "Agents in missions, through the API.",
	},
	"mission_updates": {
		Area: "agents", Source: "Written by agents and the team during missions.",
		Columns: map[string]string{
			"resolved_by": "Who resolved it.",
			"resolved_at": "When it was resolved.",
		},
	},
	"actions": {
		Area: "agents", Source: "The action log: agents calling the API.",
		Columns: map[string]string{
			"decided_by_rule": "The approval rule that decided it, if one did.",
			"hold_reason":     "Why it was held for approval.",
			"superseded_by":   "The action that replaced it.",
			"grant_id":        "The standing approval it ran under, if any.",
			"expires_at":      "When the request lapses if nobody decides.",
		},
	},
	"brand_findings": {
		Area: "agents", Source: "Written by agents; reviewed by the team.",
		Columns: map[string]string{
			"text":           "The finding.",
			"scope":          "What it applies to.",
			"retired_reason": "Why it no longer holds.",
		},
	},
	"brand_profile_facts": {
		Area: "agents", Source: "The brand profile: typed or confirmed by the team, and suggested by agents, the interview and the brand's website.",
		Columns: map[string]string{
			"section":       "The part of the profile it belongs to: company, offer, customers, positioning, proof, competitors, objections, voice, goals, channels or rules.",
			"title":         "A short name for the fact.",
			"text":          "The fact.",
			"source":        "Where it came from: person, interview, website, conversation, agent, import or context (the brand's older context fields).",
			"visibility":    "customers: may be said to customers once confirmed. internal: for the team and its agents only.",
			"status":        "suggested (waiting for a person), confirmed or retired. Only a confirmed fact with visibility customers may be said to customers.",
			"replaces_id":   "For a suggestion: the fact it would replace once confirmed.",
			"context_field": "Set when the fact mirrors one of the brand's older context fields (about, audience, goals, voice, rules); the two change together.",
			"confirmed_by":  "Who confirmed it.",
			"confirmed_at":  "When it was confirmed.",
			"position":      "Its place in its section.",
		},
		Joins: map[string]string{"replaces_id": "brand_profile_facts.id"},
		Examples: []dataCatalogExample{
			{"Which facts are waiting for a person to confirm?", "select section, title, text, source from brand_profile_facts where status = 'suggested' order by created_at"},
			{"What may be said to customers about the offer?", "select title, text from brand_profile_facts where section = 'offer' and status = 'confirmed' and visibility = 'customers' order by position"},
		},
	},
	"brand_personas": {
		Area: "agents", Source: "The brand profile: written or confirmed by the team, and suggested by agents and the interview.",
		Columns: map[string]string{
			"name":         "The persona's name, such as Operations manager.",
			"role":         "Who they are in a line: their job and the kind of company.",
			"summary":      "A short description of the persona.",
			"goals":        "What they want, as a JSON list of short sentences.",
			"pains":        "What gets in their way, as a JSON list of short sentences.",
			"triggers":     "What makes them start looking, as a JSON list of short sentences.",
			"objections":   "Why they hesitate, as a JSON list of short sentences.",
			"channels":     "Where they can be reached, as a JSON list.",
			"source":       "Where it came from: person, interview, website, conversation, agent or import.",
			"status":       "suggested (waiting for a person), confirmed or archived.",
			"confirmed_by": "Who confirmed it.",
			"confirmed_at": "When it was confirmed.",
			"position":     "Its place in the list.",
		},
		Examples: []dataCatalogExample{
			{"Who buys from this brand?", "select name, role, summary from brand_personas where status = 'confirmed' order by position"},
		},
	},
	"brand_profile_changes": {
		Area: "agents", Source: "Written with every change to a brand profile fact or persona.",
		Columns: map[string]string{
			"fact_id":    "The fact that changed, when the row is about a fact.",
			"persona_id": "The persona that changed, when the row is about a persona.",
			"action":     "created, updated, confirmed, retired, restored or imported.",
			"before":     "The fields that changed, as they were, as JSON.",
			"after":      "The fields that changed, as they are now, as JSON.",
			"actor_id":   "Who made the change: a person, a token or an agent.",
		},
		Joins: map[string]string{"fact_id": "brand_profile_facts.id", "persona_id": "brand_personas.id"},
		Examples: []dataCatalogExample{
			{"What changed in the profile in the last 30 days?", "select created_at, action, fact_id, persona_id, after from brand_profile_changes where created_at > now() - interval '30 days' order by created_at desc"},
		},
	},
	"memories": {
		Area: "agents", Source: "Written by the team and agents.",
		Columns: map[string]string{
			"pinned": "Whether it is always given to agents.",
		},
	},
	"ab_tests": {
		Area: "analytics", Source: "Set up by the team or agents in A/B tests.",
		Columns: map[string]string{
			"control":      "The control variant.",
			"goals":        "The goal events and metrics, as JSON.",
			"min_effect":   "The smallest effect worth finding.",
			"decision":     "What was decided: ship, revert or keep testing.",
			"baselines":    "The metrics before, as JSON.",
			"guardrails":   "Metrics that must not get worse, as JSON.",
			"learning":     "What was learned, in words.",
			"registration": "The test as registered before it started, as JSON.",
			"factors":      "What varies, as JSON.",
			"page":         "The page tested.",
			"flag_id":      "The feature flag that splits visitors.",
		},
	},
	"ab_test_checks": {
		Area: "analytics", Source: "Run by Outflare and the team.",
		Columns: map[string]string{
			"variant":    "The variant checked.",
			"checked_by": "Who checked it.",
		},
	},
	"insights": {
		Area: "analytics", Source: "Made by the team and agents.",
	},
	"alerts": {
		Area: "analytics", Source: "Raised by Outflare's alert rules.",
		Columns: map[string]string{
			"target_type": "What the alert is about: a record, a metric and the like.",
		},
	},
	"journey_versions": {
		Area: "customers", Source: "Made in Journeys.",
		Columns: map[string]string{
			"trigger_event":         "The event that starts it.",
			"trigger_filter":        "Which of those events count, as JSON.",
			"goal_event":            "The event that means it worked.",
			"exit_events":           "Events that take people out.",
			"exit_conditions":       "Conditions that take people out, as JSON.",
			"exclude":               "Who may never enter, as JSON.",
			"entry_limit":           "How many times a person may enter.",
			"entry_limit_per":       "The period of that limit.",
			"reentry_after_seconds": "How long before a person may enter again.",
		},
	},
	"journey_run_steps": {
		Area: "customers", Source: "Outflare's journeys.",
	},
	"lifecycle_stages": {
		Area: "customers", Source: "Set up in Lifecycle.",
		Columns: map[string]string{
			"rank": "Its order, lowest first.",
		},
	},
	"social_accounts": {
		Area: "social", Source: "Synced from the brand's connected social accounts.",
		Columns: map[string]string{
			"avatar_url":    "Its picture.",
			"competitor_id": "The competitor it belongs to, for a competitor's account (seo_competitors.id).",
		},
	},
	"social_account_days": {
		Area: "social", Source: "Synced daily.",
	},
	"social_publications": {
		Area: "social", Source: "Drafted by the team and agents in Social.",
		Columns: map[string]string{
			"link_url":      "The link it carries.",
			"prompt":        "What the post was drafted from.",
			"scheduled_for": "When it is set to go out.",
			"drafted_via":   "Who or what drafted it.",
			"series_id":     "The series it belongs to.",
		},
	},
	"social_post_snapshots": {
		Area: "social", Source: "Synced from the brand's social connections.",
		Columns: map[string]string{
			"reposts":   "Reposts.",
			"quotes":    "Quotes.",
			"bookmarks": "Bookmarks.",
		},
	},
	"content_items": {
		Area: "content", Source: "Written by the team and agents in Content.",
		Columns: map[string]string{
			"slug":          "Its address on the site.",
			"target":        "The keyword or question it targets.",
			"excerpt":       "A short summary.",
			"canonical_url": "Its canonical address.",
			"cms_status":    "Its status in the CMS.",
			"notes":         "The team's notes.",
			"placement":     "Where it is published.",
			"seo":           "Its SEO settings, as JSON.",
			"links":         "Links in it, as JSON.",
			"media":         "Its images and media, as JSON.",
			"brief":         "The brief it was written from.",
			"revision":      "Goes up with every edit.",
			"body":          "The content.",
		},
	},
	"content_publishes": {
		Area: "content", Source: "Outflare, through the brand's CMS connections.",
		Columns: map[string]string{
			"live":         "Whether it is live.",
			"error":        "Why it failed.",
			"cms_status":   "Its status in the CMS.",
			"requested_by": "Who asked for it.",
			"verified":     "Whether Outflare checked it went live.",
			"item_id":      "The content item (content_items.id).",
		},
	},
	"email_templates": {
		Area: "email", Source: "Made in Email.",
		Columns: map[string]string{
			"handle":            "Its handle in code.",
			"from_address":      "The address it is sent from.",
			"reply_to":          "Where replies go.",
			"html":              "The HTML.",
			"text_body":         "The plain text.",
			"variables":         "The variables it uses.",
			"published_version": "The version that is live.",
			"track_opens":       "Whether opens are tracked.",
			"track_clicks":      "Whether clicks are tracked.",
		},
	},
	"email_topics": {
		Area: "email", Source: "Set up in Email.",
		Columns: map[string]string{
			"subscribed_by_default": "Whether people get it until they leave.",
			"visibility":            "Whether people see it on the preferences page.",
		},
	},
	"email_broadcast_recipients": {
		Area: "email", Source: "Outflare's broadcasts.",
	},
	"suppressions": {
		Area: "email", Source: "Bounces, complaints, unsubscribes, the team and connections.",
		Columns: map[string]string{
			"value":         "The address or domain.",
			"method":        "How it was found.",
			"added_by":      "Who added it.",
			"added_by_type": "person, agent or system.",
		},
	},
}
