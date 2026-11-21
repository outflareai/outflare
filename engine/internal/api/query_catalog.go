package api

import (
	"slices"
	"strings"
)

// The data catalog: what each query table holds, explained for agents and
// people (AGENTS.md, "Data: agents can see all of it"). This file is the
// written half: each table's area, where its rows come from, its time
// column, what its ids join to, which columns hold personal data, and
// example questions; and a description of every column, by the naming
// conventions below where they say enough, and in words where they don't.
// The live half (rows, dates, how often each column is filled, common
// values) is read from the data itself (query_catalog_live.go).
//
// TestEveryQueryColumnIsDocumented fails on a query column without a
// description, so a new view or column comes with its own.

// dataCatalogTable is what is written about one query table.
type dataCatalogTable struct {
	// Area groups tables for people and agents: customers, revenue,
	// outreach, email, analytics, seo, coverage, social, workspace.
	Area string
	// Source says where the rows come from: a connection, the app, a sync.
	Source string
	// Time is the column that dates a row, when the convention (the first
	// of dataCatalogTimeColumns the table has) picks the wrong one.
	Time string
	// Columns describes columns the conventions don't, or describe wrongly
	// for this table.
	Columns map[string]string
	// Joins names what a column points at, when dataCatalogJoins doesn't.
	Joins map[string]string
	// Examples are questions the table answers, each with its SQL.
	Examples []dataCatalogExample
}

type dataCatalogExample struct {
	Question string `json:"question"`
	SQL      string `json:"sql"`
}

// dataCatalogAreas are the areas, in the order the catalog lists them.
var dataCatalogAreas = []string{"customers", "revenue", "ads", "analytics", "outreach", "email", "seo", "coverage", "social", "content", "agents", "workspace"}

// queryCatalog describes every table in queryTables.
var queryCatalog = map[string]dataCatalogTable{
	"brand_assets": {
		Area: "content", Source: "Files people upload to the brand (Brand kit) and every picture, video and voice-over made for it (source generated).",
		Joins: map[string]string{"consent_by": "the user who confirmed consent"},
		Columns: map[string]string{
			"kind":         "What the file is: logo, font, product_shot, screenshot, example_ad, voice, person, recording or other.",
			"note":         "What the person wrote about the file.",
			"filename":     "The file's name as uploaded or made.",
			"family":       "The file's type: image, video, audio or font.",
			"bytes":        "The file's size in bytes.",
			"width":        "Width in pixels, for a picture or video.",
			"height":       "Height in pixels, for a picture or video.",
			"resizable":    "Whether the file can be drawn at any size (an SVG).",
			"source":       "upload (by a person) or generated (by a provider).",
			"consent_at":   "When someone confirmed the person in the picture or voice agreed to its use; empty means it can't be used yet.",
			"consent_by":   "Who confirmed the consent.",
			"consent_note": "What they wrote when confirming.",
		},
		Examples: []dataCatalogExample{
			{"How many files of each kind does the brand have?", "select kind, source, count(*) from brand_assets where archived_at is null group by 1, 2 order by 3 desc"},
			{"Which voices and people can't be used yet?", "select id, kind, name from brand_assets where kind in ('voice', 'person') and consent_at is null and archived_at is null"},
		},
	},
	"brand_generations": {
		Area: "content", Source: "Requests the brand made to its own OpenAI, fal.ai and ElevenLabs connections, from the app, the API and agents.",
		Joins: map[string]string{"asset_id": "brand_assets.id", "recipe_of": "brand_generations.id", "integration_id": "the brand's connection that ran it"},
		Columns: map[string]string{
			"kind":       "What was asked for: image, video or voice.",
			"connector":  "The provider: openai, fal or elevenlabs.",
			"model":      "The provider's model id.",
			"prompt":     "The prompt as sent, or the text a voice read.",
			"voice":      "The provider's voice id, for a voice-over.",
			"options":    "The model's settings as JSON (size, seconds, and so on).",
			"asset_kind": "The kind the finished file is kept as.",
			"error":      "Why it failed, as the provider or Outflare said.",
			"asset_id":   "The file it made, once done.",
			"usage":      "What the provider said it used, as JSON; providers differ.",
			"refs":       "The brand files given as references, as JSON: each with the model input it went in.",
			"recipe_of":  "The earlier request whose recipe this one reused.",
		},
		Examples: []dataCatalogExample{
			{"How often does each model fail?", "select connector, model, count(*) filter (where status = 'failed') as failed, count(*) from brand_generations group by 1, 2 order by 4 desc"},
			{"How long does each model take?", "select model, avg(finished_at - started_at) from brand_generations where status = 'done' group by 1"},
		},
	},
	"brand_videos": {
		Area: "content", Source: "Talking videos made in the app, over the API and by agents, stage by stage on the brand's own provider connections.",
		Joins: map[string]string{"scene_asset_id": "brand_assets.id", "voice_asset_id": "brand_assets.id", "render_asset_id": "brand_assets.id", "base_asset_id": "brand_assets.id", "final_asset_id": "brand_assets.id", "variation_of": "brand_videos.id", "rated_by": "the user who rated it"},
		Columns: map[string]string{
			"script":           "What the person in the video says.",
			"direction":        "How it should look and move, as told to the video model.",
			"scene_asset_id":   "The scene picture the video starts from.",
			"settings":         "The models and settings of each stage, as JSON.",
			"stage":            "The stage it is at: voice, words, render, upscale, finish, or an edit's stage.",
			"round":            "0 for the first making; each edit is a new round.",
			"edit":             "The edit that is running or waiting, as JSON: the action, the words picked and what the person said.",
			"error":            "Why it failed, or why its last edit did.",
			"voice_asset_id":   "The voice-over.",
			"render_asset_id":  "The video as the model made it.",
			"base_asset_id":    "The video before the finish.",
			"final_asset_id":   "The finished video.",
			"words":            "Every word with its start and end time, as JSON.",
			"notes":            "What Outflare noticed while making it (a word read differently, a step left out), as JSON.",
			"waiting_version":  "The number of the fix that waits for a keep or a go back; empty when none does.",
			"variation_of":     "The video this one is a variation of.",
			"variation_change": "What was changed from the original, as JSON.",
			"rating":           "A person's opinion: liked, not_this, or empty.",
			"rating_reason":    "Why they liked it or not, in their words.",
			"rated_by":         "Who rated it.",
			"rated_at":         "When it was rated.",
		},
		Examples: []dataCatalogExample{
			{"Which videos are waiting for someone to check a fix?", "select id, name from brand_videos where waiting_version is not null"},
			{"What do people say about the videos they don't like?", "select name, rating_reason from brand_videos where rating = 'not_this' order by rated_at desc"},
		},
	},
	"brand_video_steps": {
		Area: "content", Source: "The stages of each talking video as they ran.",
		Joins: map[string]string{"video_id": "brand_videos.id", "generation_id": "brand_generations.id", "asset_id": "brand_assets.id"},
		Columns: map[string]string{
			"round":         "0 for the first making; each edit is a new round.",
			"stage":         "The stage: voice, words, render, upscale, finish, or an edit's stage.",
			"generation_id": "The provider request this stage ran, when it ran one.",
			"asset_id":      "The file this stage made.",
			"detail":        "What the stage worked out, as JSON.",
		},
		Examples: []dataCatalogExample{
			{"How long does each stage take?", "select stage, avg(finished_at - created_at) from brand_video_steps where status = 'done' group by 1"},
		},
	},
	"brand_video_versions": {
		Area: "content", Source: "Each talking video's first making and every fix made to it.",
		Joins: map[string]string{"video_id": "brand_videos.id", "base_asset_id": "brand_assets.id", "final_asset_id": "brand_assets.id"},
		Columns: map[string]string{
			"number":         "The version's number within its video, from 1.",
			"state":          "current (the video now), waiting (a fix to check), before (an earlier one) or gone_back (a fix turned down).",
			"base_asset_id":  "This version's video before the finish.",
			"final_asset_id": "This version's finished video.",
			"words":          "This version's words with their times, as JSON.",
			"notes":          "What Outflare noticed while making it, as JSON.",
			"edit":           "The edit that made this version, as JSON.",
			"reason":         "Why the fix was turned down, as picked.",
			"note":           "What the person said was wrong, in their words.",
			"decided_at":     "When someone kept it or went back.",
		},
		Examples: []dataCatalogExample{
			{"Why are fixes turned down?", "select reason, count(*) from brand_video_versions where state = 'gone_back' group by 1 order by 2 desc"},
		},
	},
	"creatives": {
		Area: "content", Source: "Creatives made from skills in the app, over the API and by agents.",
		Joins: map[string]string{"skill_id": "creative_skills.id", "generation_id": "brand_generations.id", "reviewed_by": "the user who approved or rejected it", "rated_by": "the user who rated it"},
		Columns: map[string]string{
			"number":        "The creative's number within the brand; its code is C- and this number.",
			"kind":          "image or video.",
			"skill_version": "The version of the skill it was made with.",
			"variant":       "Which of the skill's prompts made it, in a split test.",
			"persona":       "Who it is for.",
			"pain":          "The problem it speaks to.",
			"hook":          "The angle it opens with.",
			"headline":      "The headline laid over the picture.",
			"subline":       "The second line.",
			"cta":           "The button's words.",
			"prompt":        "The prompt as sent, with the skill's fill-ins and the brand's rules.",
			"review":        "waiting, approved or rejected.",
			"review_reason": "Why it was rejected.",
			"reviewed_by":   "Who approved or rejected it.",
			"reviewed_at":   "When it was approved or rejected.",
			"rating":        "A person's opinion, separate from approving: liked, not_this, or empty.",
			"rating_reason": "Why they liked it or not, in their words.",
			"rated_by":      "Who rated it.",
			"rated_at":      "When it was rated.",
		},
		Examples: []dataCatalogExample{
			{"Which skills make creatives people approve?", "select s.name, count(*) filter (where c.review = 'approved') as approved, count(*) from creatives c join creative_skills s on s.id = c.skill_id group by 1 order by 3 desc"},
			{"Which hooks are liked most?", "select hook, count(*) from creatives where rating = 'liked' group by 1 order by 2 desc"},
		},
	},
	"creative_skills": {
		Area: "content", Source: "Skills people and agents saved for the brand.",
		Columns: map[string]string{
			"kind":            "What it makes: image or video.",
			"current_version": "The version new creatives are made with.",
		},
		Examples: []dataCatalogExample{
			{"Which skills are switched on?", "select name, kind, current_version from creative_skills where enabled and archived_at is null"},
		},
	},
	"creative_skill_versions": {
		Area: "content", Source: "Every saved version of a skill; a version is never edited.",
		Joins: map[string]string{"skill_id": "creative_skills.id"},
		Columns: map[string]string{
			"note":     "What changed in this version.",
			"provider": "The provider it runs on: openai, fal or elevenlabs.",
			"model":    "The provider's model id.",
			"options":  "The model's settings, as JSON.",
			"sizes":    "The sizes it makes, such as 1080x1080.",
			"rules":    "Rules added to every prompt of this skill.",
			"variants": "Its prompts as JSON, each with the share of creatives it takes in a split test.",
		},
		Examples: []dataCatalogExample{
			{"How did a skill change over time?", "select version, note, model, created_at from creative_skill_versions where skill_id = '<skill id>' order by version"},
		},
	},
	"creative_skill_drafts": {
		Area: "content", Source: "Ads people described in words on the Creatives pages or through the API, and the draft the workspace's own AI model wrote.",
		Joins: map[string]string{"created_by": "the user who described it"},
		Columns: map[string]string{
			"description": "The ad in the person's own words.",
			"status":      "queued, running, ready or failed.",
			"error":       "Why it failed, in words.",
			"summary":     "One line on the ads the drafted skill would make.",
			"picture":     "The drafted prompt for the picture.",
			"copy":        "How the draft says to write the headline, second line and button.",
			"model":       "The chat model that wrote the draft.",
		},
		Examples: []dataCatalogExample{
			{"What ads did people describe this month?", "select description, status, name from creative_skill_drafts where created_at > now() - interval '30 days' order by created_at desc"},
		},
	},
	"creative_lessons": {
		Area: "content", Source: "Lessons people and agents saved, by hand or when keeping a fix or saying why a creative is not right.",
		Joins: map[string]string{"source_id": "brand_videos.id or creatives.id, by source_type"},
		Columns: map[string]string{
			"kind":        "say (how to pronounce a word) or rule (a line added to prompts).",
			"word":        "The word as written.",
			"say":         "How the voice should say it, spelled as it sounds.",
			"rule":        "The rule, added to every prompt it applies to.",
			"applies_to":  "What a rule applies to: all, image or video.",
			"source_type": "What it was learned on: video or creative.",
			"source_id":   "The video or creative it was learned on.",
			"removed_at":  "When it stopped being used; empty while it is in use.",
		},
		Examples: []dataCatalogExample{
			{"Which rules hold for every new picture?", "select rule from creative_lessons where kind = 'rule' and applies_to in ('all', 'image') and removed_at is null"},
		},
	},
	"creative_history": {
		Area: "content", Source: "Written whenever something happens to a creative or a talking video, in the same step as the change.",
		Joins: map[string]string{"subject_id": "brand_videos.id or creatives.id, by subject_type", "actor_id": "the user who did it; empty when Outflare did"},
		Columns: map[string]string{
			"subject_type": "video or creative.",
			"subject_id":   "The video or creative it happened to.",
			"kind":         "What happened: made, fix_asked, fix_ready, fix_kept, gone_back (a fix turned down), fix_failed, version_restored, liked, not_this, rating_cleared, variation_made, words_changed, approved, rejected.",
			"said":         "What the person said, in their words.",
			"data":         "Details as JSON, such as the version number or the words picked.",
		},
		Examples: []dataCatalogExample{
			{"What do people say when they turn a fix down?", "select said, created_at from creative_history where kind = 'gone_back' and said <> '' order by created_at desc"},
		},
	},
	"people": {
		Area: "customers", Source: "The CRM: sign-ups and identify calls from the brand's site and app, imports, connections (Stripe, a CRM), forms and people added by hand or by agents.",
		Columns: map[string]string{
			"name":       "The person's full name.",
			"email":      "Their main email address; every address they use is in record_identities.",
			"attributes": "Their fields as JSON, keyed by field key (attributes->>'plan'). Each key is a field in the brand's catalog.",
		},
		Examples: []dataCatalogExample{
			{"How many people joined each week?", "select date_trunc('week', created_at) as week, count(*) from people group by 1 order by 1"},
			{"Which people have no email?", "select id, name from people where email is null limit 100"},
		},
	},
	"companies": {
		Area: "customers", Source: "The CRM: companies people belong to (from their email domain, group calls, imports and connections) and companies added by hand or by agents.",
		Columns: map[string]string{
			"name":       "The company's name.",
			"domain":     "Its main website domain; every domain it uses is in record_identities.",
			"attributes": "Its fields as JSON, keyed by field key (attributes->>'industry').",
		},
		Examples: []dataCatalogExample{
			{"Which companies have the most people?", "select c.name, count(*) from companies c join record_relationships r on r.to_id = c.id and r.valid_to is null join people p on p.id = r.from_id group by 1 order by 2 desc limit 20"},
		},
	},
	"records": {
		Area: "customers", Source: "The CRM: every record of every type (people, companies and the brand's own types such as deal or subscription), including merged and archived ones.",
		Columns: map[string]string{
			"name":        "The record's display name.",
			"attributes":  "Its fields as JSON, keyed by field key.",
			"record_type": "Its type: person, account (a company) or one of the brand's own types.",
			"version":     "Goes up by one with every change; the change itself is in the record's history.",
		},
		Joins: map[string]string{"merged_into": "records.id"},
	},
	"record_identities": {
		Area: "customers", Source: "The CRM: every email, phone, user id and domain seen for a person or company, from every source.",
		Columns: map[string]string{
			"namespace": "What kind of identity: email, phone, user_id, domain and the like.",
			"value":     "The identity itself, such as an email address.",
			"verified":  "Whether it was proven (a verified sign-in, a confirmed email).",
		},
	},
	"record_relationships": {
		Area: "customers", Source: "The CRM: links between records, such as which company a person works at.",
		Columns: map[string]string{
			"kind":       "What the link is, such as works_at or partner_of.",
			"field_key":  "The field that holds the link, when it is one.",
			"properties": "More about the link as JSON, such as a person's title at the company.",
		},
		Joins: map[string]string{"from_id": "records.id", "to_id": "records.id"},
	},
	"customer_events": {
		Area: "customers", Source: "The brand's site snippet, app SDKs, the events API and connections (payments, product events), as track, identify, page, screen, group and alias calls.",
		Time: "occurred_at",
		Columns: map[string]string{
			"kind":       "The call: track, identify, page, screen, group or alias.",
			"name":       "The event's name, such as signed_up or payment_succeeded; page and screen calls name the page.",
			"properties": "What the event carried as JSON (properties->>'plan'); a page call's url, path and title are in context.",
			"context":    "Where it happened as JSON: page {url, host, path, search, title, referrer}, campaign (UTM tags), device, location and library.",
		},
		Joins: map[string]string{"account_id": "companies.id"},
		Examples: []dataCatalogExample{
			{"Which events happen most this month?", "select name, count(*) from customer_events where occurred_at >= date_trunc('month', now()) group by 1 order by 2 desc limit 20"},
			{"Sign-ups by first UTM campaign", "select context->'campaign'->>'name' as campaign, count(*) from customer_events where name = 'signed_up' group by 1 order by 2 desc"},
		},
	},
	"consents": {
		Area: "customers", Source: "Consent banners, unsubscribe links, forms, imports and changes by hand.",
		Columns: map[string]string{
			"topic":   "The email topic, when the consent is for one.",
			"purpose": "What it is for: marketing, analytics and the like.",
		},
	},
	"subscriptions": {
		Area: "revenue", Source: "Payment connections such as Stripe, Paddle and Chargebee, synced as they change.",
		Time: "started_at",
		Columns: map[string]string{
			"plan":             "The plan's name at the payment provider.",
			"billing_interval": "How often it bills: month, year and the like.",
			"monthly_minor":    "What it brings in per month (MRR) in minor units (cents) of currency.",
		},
	},
	"transactions": {
		Area: "revenue", Source: "Payment connections (Stripe and others) and payment events sent to the events API: payments, refunds, chargebacks and credits.",
		Time: "occurred_at",
		Columns: map[string]string{
			"kind":               "payment, refund, chargeback or credit.",
			"amount_minor":       "The amount in minor units (cents) of currency; refunds and chargebacks are their own rows.",
			"brand_amount_minor": "The same amount in the brand's currency (brand_currency), at that day's rate; empty until the rate is known.",
			"brand_currency":     "The brand's currency, which brand_amount_minor is in.",
			"source_id":          "The payment's id at its source, such as a Stripe charge id.",
			"refund_of":          "For a refund or chargeback, the source id of the payment it takes back.",
			"description":        "What the payment was for, as the source describes it.",
		},
		Examples: []dataCatalogExample{
			{"Revenue by month in the brand's currency", "select date_trunc('month', occurred_at) as month, sum(brand_amount_minor) / 100.0 as revenue from transactions where kind = 'payment' and status = 'succeeded' group by 1 order by 1"},
			{"People whose first payment was this week", "select record_id, min(occurred_at) as first_paid from transactions where kind = 'payment' group by 1 having min(occurred_at) >= now() - interval '7 days'"},
		},
	},
	"deals": {
		Area: "revenue", Source: "The CRM's pipeline: deals made by people, agents, sequences and connected CRMs.",
		Time: "opened_at",
		Columns: map[string]string{
			"name":         "The deal's name.",
			"stage":        "Its pipeline stage.",
			"state":        "open, won or lost.",
			"amount_cents": "Its value in minor units (cents) of currency.",
			"attribution":  "What brought it in, as Outflare credits it.",
		},
		Examples: []dataCatalogExample{
			{"Win rate by source", "select source, count(*) filter (where state = 'won')::float / nullif(count(*) filter (where state <> 'open'), 0) as win_rate from deals group by 1"},
		},
	},
	"meetings": {
		Area: "revenue", Source: "Booked meetings from calendar connections, booking links and sequences.",
		Time: "booked_at",
		Columns: map[string]string{
			"title":       "The meeting's title.",
			"qualified":   "Whether the meeting counted as qualified.",
			"attribution": "What brought it in, as Outflare credits it.",
		},
	},
	"spend": {
		Area: "revenue", Source: "Money spent: ad networks (synced daily per ad), tools and services, entered by hand or by connections.",
		Time: "occurred_at",
		Columns: map[string]string{
			"category":  "What kind of cost: ads, tools, data, people and the like.",
			"units":     "How many units were bought, when the cost is per unit.",
			"unit_name": "What a unit is, such as credit or mailbox.",
		},
		Examples: []dataCatalogExample{
			{"Spend by channel per month", "select date_trunc('month', occurred_at) as month, channel, currency, sum(amount_cents) / 100.0 from spend group by 1, 2, 3 order by 1"},
		},
	},
	"products": {
		Area: "revenue", Source: "The brand's product list, set up in Outflare or from payment connections.",
		Columns: map[string]string{
			"key":          "The product's key, which never changes.",
			"name":         "Its name.",
			"churn_mode":   "Whether cancelling (billing) or no use (usage) churns a customer.",
			"at_risk_days": "Days without use before a customer counts as at risk.",
			"churn_days":   "Days without use before a customer counts as churned.",
		},
	},
	"lifecycle_states": {
		Area: "revenue", Source: "Outflare's lifecycle engine, from payments, events and usage.",
		Time: "entered_at",
		Columns: map[string]string{
			"level":      "company (billing) or person (usage).",
			"stage":      "Where the customer stands with the product, such as trial, active, at_risk or churned.",
			"idle_since": "When they last stopped using it, if they have.",
			"due_at":     "When the next stage is due if nothing changes.",
		},
	},
	"lifecycle_moves": {
		Area: "revenue", Source: "Outflare's lifecycle engine: each move between stages, with what caused it.",
		Columns: map[string]string{
			"level":      "company, person, or overall for a company's status across products.",
			"move":       "The kind of move, such as activated, churned or reactivated.",
			"from_stage": "The stage before.",
			"to_stage":   "The stage after.",
		},
		Joins: map[string]string{"cause_event_id": "customer_events.id", "event_id": "customer_events.id"},
	},
	"lifecycle_overall": {
		Area: "revenue", Source: "Outflare's lifecycle engine: each company's status across all products.",
		Columns: map[string]string{
			"flags":           "Notes on the status, such as partly_churned.",
			"active_products": "How many products it pays for.",
		},
	},
	"metrics": {
		Area: "customers", Source: "The brand's metric definitions, set up in Outflare.",
		Columns: map[string]string{
			"key":           "The metric's key.",
			"levels":        "Whether it is worked out for people, companies or both.",
			"aggregate":     "How it is counted: count, sum, average, last and the like.",
			"events":        "The events it counts.",
			"usage":         "Whether it counts product usage.",
			"property":      "The event property it sums or averages.",
			"window_days":   "The window it counts over, in days.",
			"offset_days":   "How many days back the window ends.",
			"per_product":   "Whether it is also worked out per product.",
			"version":       "Goes up with each change to the definition.",
			"backfilled_at": "When its values were last worked out from all past events.",
		},
	},
	"metric_values": {
		Area: "customers", Source: "Outflare's metrics engine, kept current as events arrive.",
		Columns: map[string]string{
			"metric": "The metric's key.",
			"value":  "The current value as JSON (a number, or more for some aggregates).",
		},
	},
	"rules": {
		Area: "customers", Source: "The brand's rules, set up in Outflare.",
		Columns: map[string]string{
			"trigger":          "What starts the rule, as JSON.",
			"conditions":       "What must be true, as JSON.",
			"match":            "all or any of the conditions.",
			"emit_event":       "The event the rule sends when it fires.",
			"fire":             "When it fires again: once, every time, once per episode.",
			"cooldown_seconds": "The least time between two firings for one record.",
		},
	},
	"rule_firings": {
		Area: "customers", Source: "Outflare's rules engine: each time a rule fired.",
		Columns: map[string]string{
			"episode_key": "Groups firings that belong to one episode.",
			"depth":       "How many rules deep the chain was when this one fired.",
		},
		Joins: map[string]string{"event_id": "customer_events.id", "cause_event_id": "customer_events.id"},
	},
	"smart_list_changes": {
		Area: "customers", Source: "Outflare's lists: who joined and left each list.",
		Columns: map[string]string{"change": "enter or leave."},
	},
	"bulk_edits": {
		Area: "customers", Source: "Bulk edits owners ran in the app or through the API.",
		Columns: map[string]string{
			"source":  "Where the records came from: a list, a search, a file.",
			"changes": "What was changed, as JSON.",
			"records": "How many records it changed.",
			"counts":  "How many changed, were skipped or failed, as JSON.",
		},
		Joins: map[string]string{"undone_by": "bulk_edits.id"},
	},
	"journeys": {
		Area: "email", Source: "Lifecycle journeys set up in Outflare.",
		Columns: map[string]string{
			"trigger_event": "The event that starts a run.",
			"entry":         "Who may enter: once, every time, and the like.",
			"goal_event":    "The event that counts as reaching the goal.",
			"version":       "The live version's number.",
		},
	},
	"journey_runs": {
		Area: "email", Source: "Outflare's journey engine: each person's way through a journey.",
		Time: "entered_at",
		Columns: map[string]string{
			"step":        "The step the person is on, or ended on.",
			"exit_reason": "Why the run ended: goal reached, finished, left the audience, and the like.",
			"steps_taken": "How many steps the run has taken.",
		},
	},
	"emails": {
		Area: "email", Source: "Lifecycle and transactional email Outflare sent through the brand's email provider.",
		Time: "created_at",
		Columns: map[string]string{
			"purpose":     "marketing, transactional and the like.",
			"subject":     "The subject line.",
			"tags":        "Tags the email was sent with.",
			"bounce_type": "hard or soft, when it bounced.",
			"test":        "Whether it was a test send.",
			"opens":       "How many times it was opened.",
			"clicks":      "How many times a link in it was clicked.",
		},
	},
	"email_events": {
		Area: "email", Source: "The email provider's webhooks: delivered, opened, clicked, bounced, complained.",
		Columns: map[string]string{"type": "What happened: delivered, opened, clicked, bounced, complained and the like."},
	},
	"broadcasts": {
		Area: "email", Source: "Email broadcasts sent from Outflare.",
		Time: "created_at",
		Columns: map[string]string{
			"recipients": "How many it was sent to.",
			"skipped":    "How many were left out (no consent, suppressed, bounced before).",
		},
	},
	"leads": {
		Area: "outreach", Source: "Cold outreach leads: imports, lead finders, data providers and agents.",
		Columns: map[string]string{
			"company": "The company name as the lead source gave it.",
			"title":   "Their job title.",
			"custom":  "Other fields from the lead source, as JSON.",
		},
	},
	"sequences": {
		Area: "outreach", Source: "Cold email sequences set up in Outflare or a connected sequencer.",
		Columns: map[string]string{
			"daily_new_limit": "The most new leads it starts each day.",
			"stop_on_reply":   "Whether a reply stops the sequence for that lead.",
		},
	},
	"enrollments": {
		Area: "outreach", Source: "Leads in sequences, and where each is.",
		Columns: map[string]string{
			"status_reason": "Why it has its status, such as replied or bounced.",
			"next_step":     "The step that sends next.",
		},
	},
	"messages": {
		Area: "outreach", Source: "Cold emails sent or queued from the brand's mailboxes, without their bodies.",
		Time: "sent_at",
		Columns: map[string]string{
			"step_position": "Which step of the sequence it is.",
			"subject":       "The subject line.",
			"attempts":      "How many times sending was tried.",
		},
	},
	"replies": {
		Area: "outreach", Source: "Mail received in the brand's mailboxes, without bodies; read the text with the inbox tools.",
		Columns: map[string]string{
			"subject":  "The subject line.",
			"kind":     "reply, auto-reply, bounce and the like.",
			"category": "The AI's label for the reply: interested, not interested, out of office and the like.",
			"opt_out":  "Whether the sender asked to stop.",
			"labels":   "Labels the team put on it.",
			"done":     "Whether the team marked it done.",
			"tags":     "Inbox tags on it.",
		},
	},
	"visits": {
		Area: "analytics", Source: "The brand's Outflare site snippet: one row per visit (session), with its UTM tags, place and device.",
		Time: "started_at",
		Columns: map[string]string{
			"events":        "How many events the visit had.",
			"pageviews":     "How many pages it viewed.",
			"landing_path":  "The first page's path.",
			"referrer_host": "The site that sent the visitor.",
			"region":        "The visitor's region, from their IP address.",
			"city":          "The visitor's city, from their IP address.",
			"asn":           "The network the visit came from (autonomous system number).",
			"isp":           "The visitor's internet provider.",
		},
		Examples: []dataCatalogExample{
			{"Visits by UTM source and campaign this month", "select utm_source, utm_campaign, count(*) from visits where started_at >= date_trunc('month', now()) group by 1, 2 order by 3 desc"},
			{"Visitors who later paid, by landing page", "select v.landing_path, count(distinct v.record_id) from visits v join transactions t on t.record_id = v.record_id and t.kind = 'payment' and t.occurred_at > v.started_at group by 1 order by 2 desc limit 20"},
		},
	},
	"social_posts": {
		Area: "social", Source: "Posts on the brand's own social accounts, with numbers synced from each platform.",
		Time: "posted_at",
		Columns: map[string]string{
			"body":        "The post's text.",
			"impressions": "How many times it was seen.",
			"likes":       "Likes.",
			"replies":     "Replies.",
			"reposts":     "Reposts.",
			"quotes":      "Quotes.",
			"bookmarks":   "Bookmarks.",
			"clicks":      "Link clicks.",
		},
		Joins: map[string]string{"account_id": "the brand's social account (list_social_accounts)"},
	},
	"brand_mentions": {
		Area: "social", Source: "Found by the mention listener (Social, Mentions) in the sources the brand turned on, or added by hand.",
		Time: "found_at",
		Columns: map[string]string{
			"source":            "Where it was said: youtube, podcast, x, reddit, hacker_news, bluesky, news or web.",
			"external_id":       "The piece's id on its source, such as the video id.",
			"quote":             "The words around the name, as said or written.",
			"at_seconds":        "For a video or episode, the second the name is said.",
			"about":             "brand when the brand was named, competitor when a tracked competitor was.",
			"competitor_id":     "The competitor named, when about is competitor.",
			"matched":           "The name or alias that matched.",
			"label":             "What kind of mention it is: review, tutorial, comparison, complaint, recommendation, question, sponsored or other.",
			"sentiment":         "positive, neutral, negative or mixed.",
			"author_name":       "Who said it, as the source shows them.",
			"author_handle":     "Their handle on the source.",
			"author_url":        "Their page on the source.",
			"reach":             "Their audience: subscribers, followers or views, depending on the source.",
			"score":             "How worth a reply it is, 0 to 100.",
			"status":            "Where outreach stands: new, contacted, replied, not_a_fit or on_board.",
			"origin":            "How it was found: social_items (the brand's social listening), news, presence, video_search (a search for videos), manual (added by hand).",
			"published_at":      "When it was said or posted.",
			"found_at":          "When the listener found it.",
			"status_changed_at": "When its status last changed.",
		},
	},
	"mention_videos": {
		Area: "social", Source: "Found by the mention listener's YouTube source through the workspace's own web search connection; captions read through its own page reading connection.",
		Time: "found_at",
		Columns: map[string]string{
			"video_id":          "YouTube's id for the video; the watch page is youtube.com/watch?v= plus this id.",
			"title":             "The video's title.",
			"channel_name":      "The channel that posted it.",
			"channel_url":       "The channel's page.",
			"views":             "Views when it was read.",
			"seconds":           "How long the video is, in seconds.",
			"published_at":      "When it was posted.",
			"found_by":          "The search that found it.",
			"snippet":           "What the search result said about it.",
			"captions_status":   "Reading its captions: waiting, read, none (the video has none) or failed.",
			"captions_lang":     "The language of the captions read.",
			"captions":          "What was said, as a list of {start (seconds), text} in time order.",
			"tries":             "How many times its captions were tried.",
			"last_error_code":   "Why the last try failed, or empty.",
			"read_at":           "When its captions were read.",
			"found_at":          "When the listener found it.",
			"own_about":         "Set when the video is on the brand's own channel (brand) or a competitor's (competitor): not a mention, and skipped by later scans.",
			"own_competitor_id": "The competitor whose own channel posted it.",
		},
		Joins: map[string]string{"own_competitor_id": "seo_competitors.id"},
	},
	"mention_episodes": {
		Area: "social", Source: "Found by the mention listener's podcast source through the workspace's own podcast directory connection (Podcast Index); transcripts read from the show's own feed.",
		Time: "found_at",
		Columns: map[string]string{
			"episode_id":        "The directory's id for the episode.",
			"directory":         "The podcast directory that found it (podcastindex).",
			"title":             "The episode's title.",
			"description":       "The episode's show notes, as the show wrote them (HTML possible).",
			"link":              "The episode's web page.",
			"audio_url":         "The episode's recording.",
			"seconds":           "How long the episode is, in seconds.",
			"published_at":      "When the episode came out.",
			"show_id":           "The directory's id for the show.",
			"show_title":        "The show's name.",
			"show_link":         "The show's feed or page.",
			"show_image":        "The show's artwork.",
			"author":            "Who the show says makes it.",
			"found_by":          "The name searched for that found it.",
			"transcript_url":    "The transcript the show publishes in its feed, if any.",
			"transcript_type":   "The transcript's format (text/vtt, application/x-subrip, application/json, text/html, text/plain).",
			"transcript_status": "Reading the transcript: waiting, read, none (the show publishes none) or failed.",
			"transcript":        "What was said, as a list of {start (seconds), text} in time order; untimed transcripts start every line at 0.",
			"own_about":         "Set when the show is the brand's own (brand) or a competitor's (competitor): not a mention, and skipped by later scans.",
			"own_competitor_id": "The competitor whose own show it is.",
			"tries":             "How many times its transcript was tried.",
			"last_error_code":   "Why the last try failed, or empty.",
			"read_at":           "When its transcript was read.",
			"found_at":          "When the listener found it.",
		},
		Joins: map[string]string{"own_competitor_id": "seo_competitors.id"},
	},
	"mention_listen_rules": {
		Area: "social", Source: "Set in Social, Mentions, What we listen for.", Time: "updated_at",
		Columns: map[string]string{
			"competitor_id": "The competitor the change is for; empty for the brand's own row.",
			"listening":     "Whether scans listen for the row at all.",
			"added_terms":   "Terms added by hand, listened for as well as the automatic ones.",
			"skipped_terms": "Automatic terms (the website, the brand name) not listened for.",
		},
		Joins: map[string]string{"competitor_id": "seo_competitors.id"},
	},
	"brand_mention_settings": {
		Area: "social", Source: "Set in Social, Mentions.", Time: "updated_at",
		Columns: map[string]string{
			"enabled":          "Whether the listener is on.",
			"sources":          "The sources it reads.",
			"scan_every_hours": "How many hours between scans.",
			"last_scan_at":     "When the last scan ran.",
			"next_scan_at":     "When the next scan is due.",
			"last_error_code":  "Why the last scan failed, or empty.",
		},
	},
	"reviews": {
		Area: "social", Source: "Reviews read from the brand's review pages (Trustpilot, G2, Capterra, SourceForge, Google).",
		Time: "posted_at",
		Columns: map[string]string{
			"rating":           "Stars, 1 to 5.",
			"title":            "The review's title.",
			"body":             "The review's text.",
			"author_name":      "The reviewer's name as the platform shows it.",
			"author_country":   "The reviewer's country as the platform shows it.",
			"verified":         "Whether the platform marks it verified.",
			"sentiment":        "positive, neutral, negative or mixed.",
			"sentiment_source": "Who judged the sentiment: the platform or AI.",
			"reply_body":       "The brand's reply, if it has one.",
			"state":            "Where the review stands for the team: new, answered, ignored and the like.",
		},
		Joins: map[string]string{"page_id": "review_page_days.page_id"},
	},
	"review_page_days": {
		Area: "social", Source: "Each review page's numbers by day, read from the platform.",
		Columns: map[string]string{
			"score":          "The platform's score that day.",
			"review_count":   "How many reviews the page had.",
			"new_reviews":    "Reviews that arrived that day.",
			"stars_1":        "One-star reviews that day.",
			"stars_2":        "Two-star reviews that day.",
			"stars_3":        "Three-star reviews that day.",
			"stars_4":        "Four-star reviews that day.",
			"stars_5":        "Five-star reviews that day.",
			"outflare_score": "Outflare's review score for the page, 0 to 100.",
			"momentum":       "Whether reviews are coming in faster or slower: rising, steady, falling.",
		},
	},
	"backlinks": {
		Area: "seo", Source: "Backlink data from the brand's own SEO connection (such as Ahrefs), checked again over time.",
		Time: "first_seen_at",
		Columns: map[string]string{
			"source_url":     "The page that links.",
			"target_url":     "The brand's page it links to.",
			"source_domain":  "The linking site's domain.",
			"anchor":         "The link's text.",
			"title":          "The linking page's title.",
			"nofollow":       "Whether the link is nofollow.",
			"sponsored":      "Whether the link is marked sponsored.",
			"ugc":            "Whether the link is marked user-generated.",
			"link_type":      "text, image, redirect and the like.",
			"in_content":     "Whether the link sits in the page's main text.",
			"source_rating":  "The linking domain's strength, 0 to 100.",
			"url_rating":     "The linking page's strength, 0 to 100.",
			"domain_traffic": "The linking site's estimated monthly search traffic.",
			"page_traffic":   "The linking page's estimated monthly search traffic.",
			"review":         "The team's verdict on the link, if any.",
		},
	},
	"link_prospects": {
		Area: "seo", Source: "Link building: pages that could link to the brand, found by Outflare's methods and the brand's SEO connection.",
		Columns: map[string]string{
			"page_url":        "The page that could link.",
			"method":          "How it was found, such as competitor backlinks, broken links or mentions.",
			"target_url":      "The brand's page it should link to.",
			"evidence":        "Why it is a prospect, as JSON.",
			"domain_rating":   "The site's strength, 0 to 100.",
			"traffic":         "The site's estimated monthly search traffic.",
			"score":           "Outflare's score for the prospect.",
			"stage":           "prospect, excluded, pitched, replied, agreed, live, declined or removed.",
			"excluded_reason": "Why it was excluded.",
		},
	},
	"seo_organic_conversions_by_page": {
		Area: "seo", Source: "Organic search sessions, sign-ups and payments by landing page and day, from the brand's own PostHog connection.",
		Columns: map[string]string{
			"host":             "The landing page's host.",
			"path":             "The landing page's path.",
			"canonical_path":   "The path with its query string and trailing slash removed.",
			"organic_sessions": "Sessions that started on the page from organic search.",
			"signups":          "Sign-ups credited to the page (first organic landing page).",
			"signup_people":    "People who signed up, counted once each.",
			"payments":         "Payments credited to the page.",
			"payment_people":   "People who paid, counted once each.",
			"revenue_usd":      "Revenue credited to the page, in US dollars (not minor units); only USD payments.",
		},
	},
	"seo_cannibal_primaries": {
		Area: "seo", Source: "Chosen in the SEO overview's Cannibalisation view or through the API: the page to keep when several of a site's pages compete for one search.",
		Columns: map[string]string{
			"query":      "The search, as Search Console reports it.",
			"url":        "The page chosen to keep for it.",
			"set_by":     "The actor who chose it.",
			"created_at": "When it was first chosen.",
			"updated_at": "When it was last changed.",
		},
	},
	"seo_organic_countries": {
		Area: "seo", Source: "Organic search sessions, time on site, sign-ups and payments by the visitor's country and day, from the brand's own PostHog connection.",
		Columns: map[string]string{
			"host":             "The site's host.",
			"country":          "The visitor's country, ISO 3166-1 alpha-2 in capitals (US, IN); empty when unknown. Sign-ups and payments use the person's first country.",
			"organic_sessions": "Sessions from organic search from the country.",
			"session_seconds":  "Those sessions' time on the site added up, in seconds; divide by organic_sessions for the average.",
			"signups":          "Sign-ups of people whose first visit came from organic search.",
			"payments":         "Payments of those people, counted once each.",
			"revenue_usd":      "Their revenue in US dollars (not minor units); only USD payments.",
		},
	},
	"seo_organic_conversions_by_cluster": {
		Area: "seo", Source: "The same organic conversions, rolled up by the brand's content search groups. Groups overlap: never add group totals together.",
		Columns: map[string]string{
			"group_name":       "The search group's name, or Ungrouped.",
			"organic_sessions": "Organic search sessions that started on the group's pages.",
			"signups":          "Sign-ups credited to the group's pages.",
			"signup_people":    "People who signed up, counted once each.",
			"payments":         "Payments credited to the group's pages.",
			"payment_people":   "People who paid, counted once each.",
			"revenue_usd":      "Revenue credited to the group, in US dollars (not minor units).",
		},
	},
	"coverage_donor_profiles": {
		Area: "coverage", Source: "The brand's paid-link donor catalog: sites that sell placements, entered by the team or imported.",
		Columns: map[string]string{
			"domain":         "The donor site's domain.",
			"pbn":            "Whether the site is a private blog network.",
			"country":        "The site's main country.",
			"languages":      "The site's languages.",
			"niches":         "Topics the site covers.",
			"casino_allowed": "Whether it takes casino links.",
			"rel_offered":    "Link types it offers: dofollow, nofollow, sponsored.",
			"slot_types":     "Kinds of placement it sells: guest post, link insertion and the like.",
			"duration":       "How long a link stays: permanent, or a period.",
			"duration_days":  "That period in days, when it isn't permanent.",
			"tat_days":       "Days the site takes to publish.",
			"guarantee_days": "Days the site guarantees the link stays live.",
			"vendor_id":      "The site's id at the vendor it comes from.",
			"domain_rating":  "The site's strength, 0 to 100.",
			"traffic":        "The site's estimated monthly search traffic.",
			"metrics_source": "Where the strength and traffic numbers come from.",
		},
	},
	"coverage_link_orders": {
		Area: "coverage", Source: "Paid link orders the team placed, with prices and live checks.",
		Columns: map[string]string{
			"target_url":              "The brand's page the link points to.",
			"slot_type":               "The kind of placement bought.",
			"rel_requested":           "The link type asked for: dofollow, nofollow, sponsored.",
			"quoted_currency":         "The currency quoted_amount_minor is in.",
			"guarantee_days":          "Days the site guarantees the link stays live.",
			"guarantee_until":         "The day the guarantee ends.",
			"published_url":           "The page the link was published on.",
			"proof_url":               "A link to proof of publication.",
			"last_check_live":         "Whether the link was live at the last check.",
			"last_check_target_match": "Whether it pointed at the right page at the last check.",
			"last_check_rel":          "The link type found at the last check.",
			"last_check_anchor":       "The link text found at the last check.",
		},
		Joins: map[string]string{"replaces_order_id": "coverage_link_orders.id", "donor_profile_id": "coverage_donor_profiles.id"},
	},
	"brands": {
		Area: "workspace", Source: "The workspace's brands, set up in Settings, Brands.",
		Columns: map[string]string{
			"name":           "The brand's name.",
			"handle":         "Its short handle, used in links.",
			"website":        "Its main website.",
			"domains":        "Every domain it uses.",
			"business_model": "What the brand does, from the brand setup list.",
			"revenue_models": "How its customers pay: subscription, one_off, usage, commission and the like.",
		},
	},
}

// dataCatalogTimeColumns date a row when a table doesn't name its own time
// column, in this order.
var dataCatalogTimeColumns = []string{"occurred_at", "at", "day", "sent_at", "received_at", "started_at", "posted_at", "fired_at", "entered_at", "booked_at", "first_seen_at", "created_at", "updated_at"}

// dataCatalogJoins are what common id columns point at in the query tables.
var dataCatalogJoins = map[string]string{
	"brand_id":             "brands.id",
	"record_id":            "people.id, companies.id or records.id",
	"account_id":           "companies.id",
	"sequence_id":          "sequences.id",
	"lead_id":              "leads.id",
	"enrollment_id":        "enrollments.id",
	"meeting_id":           "meetings.id",
	"product_id":           "products.id",
	"email_id":             "emails.id",
	"broadcast_id":         "broadcasts.id",
	"journey_id":           "journeys.id",
	"rule_id":              "rules.id",
	"metric_id":            "metrics.id",
	"donor_profile_id":     "coverage_donor_profiles.id",
	"event_id":             "customer_events.id",
	"cause_event_id":       "customer_events.id",
	"ad_account_id":        "ad_accounts.id",
	"ad_entity_id":         "ad_entities.id",
	"ad_group_id":          "ad_entities.id (level ad_group)",
	"keyword_id":           "ad_keywords.id",
	"acquisition_id":       "ad_acquisitions.id",
	"session_id":           "visits.id",
	"list_id":              "smart_list_changes.list_id",
	"source_id":            "tracker_traffic_sources.id",
	"offer_id":             "tracker_offers.id",
	"lander_id":            "tracker_landers.id",
	"flow_id":              "tracker_flows.id",
	"link_id":              "tracking_links.id",
	"destination_id":       "tracking_destination_days.destination_id",
	"traffic_source_id":    "tracker_traffic_sources.id",
	"tracking_campaign_id": "tracking_campaigns.id",
	"definition_id":        "the conversion definition (Ads, Conversions)",
}

// dataCatalogCommon describes columns that mean the same in every table.
var dataCatalogCommon = map[string]string{
	"goal":                    "What it is meant to achieve.",
	"instructions":            "The instructions it was given.",
	"plan":                    "Its plan.",
	"mode":                    "How it acts: ask first, act within limits, or only suggest.",
	"status_reason":           "Why it is in that status.",
	"turns":                   "How many turns it took.",
	"turn_summary":            "What its last turn did.",
	"turn_error":              "Why its last turn failed.",
	"turn_failures":           "Failed turns in a row.",
	"playbook_version":        "The playbook version it follows.",
	"method":                  "The HTTP method, or how it was found.",
	"query":                   "The URL query string.",
	"content_type":            "The request's content type.",
	"decided_by":              "Who approved or refused it.",
	"decision_note":           "What they said when deciding.",
	"result_status":           "The HTTP status the call returned.",
	"author":                  "Who wrote it: the agent or a person.",
	"approved_by":             "Who approved it.",
	"evidence":                "What it is based on.",
	"confidence":              "How sure, as a share or label.",
	"followers":               "Followers.",
	"following":               "Accounts it follows.",
	"posts":                   "Posts.",
	"handle":                  "The account's handle.",
	"last_error":              "Why the last sync or check failed.",
	"last_error_code":         "The last error as a code.",
	"failures":                "Failures in a row.",
	"data":                    "More about it, as JSON.",
	"hypothesis":              "What the change should do.",
	"result":                  "The result, as JSON.",
	"published_by":            "Who published it.",
	"rules":                   "Its rules, as JSON.",
	"referrer_host":           "The site the click or visit came from.",
	"id":                      "This row's id.",
	"brand_id":                "The brand the row belongs to.",
	"record_id":               "The person or company it is about.",
	"name":                    "Its name.",
	"status":                  "Where it stands now (see common values).",
	"source":                  "Where it came from (see common values).",
	"origin":                  "How the record was first made: a sign-up, an import, a connection, by hand.",
	"currency":                "The ISO currency code the amounts are in.",
	"channel":                 "The channel, such as email, ads, organic or social.",
	"kind":                    "What kind of row it is (see common values).",
	"title":                   "Its title.",
	"description":             "Its description.",
	"version":                 "Goes up by one with every change.",
	"domain":                  "A website domain.",
	"email":                   "An email address.",
	"from_email":              "The address it was sent from.",
	"to_email":                "The address it was sent to.",
	"first_name":              "First name.",
	"last_name":               "Last name.",
	"subject":                 "The subject line.",
	"spam_score":              "The spam score of the site, 0 to 100 (higher is worse).",
	"domain_rating":           "The site's strength, 0 to 100.",
	"traffic":                 "Estimated monthly search traffic.",
	"stage":                   "Its stage (see common values).",
	"state":                   "Its state (see common values).",
	"category":                "Its category (see common values).",
	"provider":                "The connection or provider it came from.",
	"platform":                "The platform, such as a review site or social network.",
	"country":                 "The country, as an ISO code.",
	"language":                "The language, as an ISO code.",
	"url":                     "Its web address.",
	"day":                     "The day the numbers are for.",
	"at":                      "When it happened (UTC).",
	"valid_from":              "When it started to hold (UTC).",
	"valid_to":                "When it stopped holding (UTC); empty while it still holds.",
	"score":                   "Its score.",
	"tags":                    "Tags on it.",
	"verified":                "Whether it was verified.",
	"value":                   "The value.",
	"key":                     "Its key, which never changes.",
	"level":                   "The level it applies at (see common values).",
	"purpose":                 "What it is for.",
	"topic":                   "The email topic.",
	"events":                  "Events counted.",
	"clicks":                  "Clicks.",
	"body":                    "The text.",
	"attributes":              "Its fields as JSON, keyed by field key.",
	"properties":              "More about it as JSON.",
	"type":                    "Its type (see common values).",
	"record_type":             "The record's type: person, account (a company) or one of the brand's own types.",
	"trigger_kind":            "What kind of thing starts it.",
	"target_url":              "The page the link points to.",
	"steps":                   "How many steps it has.",
	"step":                    "The step.",
	"move":                    "The kind of move.",
	"change":                  "The change.",
	"changes":                 "The changes, as JSON.",
	"counts":                  "Counts, as JSON.",
	"records":                 "How many records.",
	"labels":                  "Labels on it.",
	"done":                    "Whether it is done.",
	"test":                    "Whether it is a test.",
	"opens":                   "Opens.",
	"replies":                 "Replies.",
	"likes":                   "Likes.",
	"impressions":             "Impressions.",
	"rating":                  "The rating.",
	"sentiment":               "positive, neutral, negative or mixed.",
	"user_id":                 "The user id the brand's app sent.",
	"anonymous_id":            "The browser's or device's anonymous id before sign-up.",
	"merged_into":             "The record this one was merged into, if it was.",
	"undone_by":               "The bulk edit that undid this one, if one did.",
	"entry":                   "Who may enter.",
	"device":                  "desktop, mobile or tablet.",
	"browser":                 "The browser.",
	"browser_version":         "The browser's version.",
	"os":                      "The operating system.",
	"os_version":              "The operating system's version.",
	"device_brand":            "The device's maker.",
	"device_model":            "The device's model.",
	"utm_source":              "The utm_source tag of the visit's first page.",
	"utm_medium":              "The utm_medium tag.",
	"utm_campaign":            "The utm_campaign tag.",
	"utm_term":                "The utm_term tag.",
	"utm_content":             "The utm_content tag.",
	"namespace":               "What kind of identity.",
	"field_key":               "The field it belongs to.",
	"journey_version":         "The journey version the run follows.",
	"rule_version":            "The rule version that fired.",
	"usage":                   "Whether it counts usage.",
	"trigger":                 "What starts it.",
	"conditions":              "What must be true.",
	"match":                   "all or any.",
	"emit_event":              "The event it sends.",
	"fire":                    "When it fires again.",
	"cooldown_seconds":        "The least time between two firings, in seconds.",
	"recipients":              "How many it reached.",
	"skipped":                 "How many were left out.",
	"stars_1":                 "One-star reviews.",
	"stars_2":                 "Two-star reviews.",
	"stars_3":                 "Three-star reviews.",
	"stars_4":                 "Four-star reviews.",
	"stars_5":                 "Five-star reviews.",
	"organic_sessions":        "Organic search sessions.",
	"signups":                 "Sign-ups.",
	"signup_people":           "People who signed up.",
	"payments":                "Payments.",
	"payment_people":          "People who paid.",
	"revenue_usd":             "Revenue in US dollars (not minor units).",
	"guarantee_days":          "Days the link is guaranteed to stay live.",
	"attribution":             "What brought it in, as Outflare credits it.",
	"window_days":             "The window in days.",
	"connector":               "The ad network or connection it came from (see common values).",
	"external_id":             "Its id at the ad network or provider.",
	"spend_micros":            "Money spent.",
	"conversions":             "Conversions the ad network counted (can be fractional with its own attribution).",
	"conversion_value_micros": "The value the ad network gave those conversions.",
	"keyword":                 "The keyword that matched, when the network says.",
	"match_type":              "The keyword's match type: exact, phrase or broad.",
	"click_id":                "The ad network's click id (such as gclid, fbclid, msclkid).",
	"click_key":               "Outflare's own id of the click, carried on the link.",
	"likely_bot":              "Whether Outflare thinks a bot made it.",
	"fraud_reasons":           "Why it looks like fraud, if it does.",
	"payout_micros":           "What a conversion pays.",
	"medium":                  "The utm_medium the links carry.",
	"matched_by":              "How the click or touch was tied to the person (see common values).",
	"enabled":                 "Whether it is on.",
	"spec":                    "Its settings, as JSON.",
	"label":                   "Its label as people see it.",
	"views":                   "Views.",
	"note":                    "A note the team wrote.",
	"filters":                 "Its filters, as JSON.",
	"priority":                "Its priority.",
	"code":                    "The code.",
	"action":                  "What was done (see common values).",
	"outcome":                 "How it ended.",
	"updated_by":              "Who last changed it.",
	"auto_approve":            "Whether it is approved without a person.",
	"privacy":                 "Whether the team marked it private (private ones are still listed, without content).",
	"event_state":             "Where its sync with the connection stands.",
	"host":                    "The website's host name, such as www.example.com.",
	"created_by":              "Who made it: a person, a token or an agent.",
	"site":                    "The site as the connection names it (a Search Console property or analytics site).",
	"position":                "The position, 1 for the top.",
	"path":                    "The page's path on the site.",
	"reason":                  "Why, in a few words.",
	"sessions":                "Sessions (visits).",
	"token":                   "The crawler's user-agent token, such as GPTBot or ClaudeBot.",
	"pages":                   "How many pages.",
	"page":                    "The page's full address.",
	"error_code":              "What went wrong, as an error code, if anything did.",
	"entity":                  "Who is named: the brand itself (self), a competitor or another brand.",
	"engine":                  "The search engine, such as google or bing.",
	"depth":                   "How many clicks from the start page, or how deep the results were read.",
	"bytes":                   "Size in bytes.",
	"visitors":                "Unique visitors.",
	"verdict":                 "The verdict (see common values).",
	"text":                    "The text.",
	"schedule":                "How often it runs.",
	"requests":                "Requests made.",
	"report":                  "Which report of the connection the numbers come from.",
	"registered_on":           "When the domain was registered, from its public record.",
	"quote":                   "The words quoted from the answer.",
	"pageviews":               "Page views.",
	"obey_robots":             "Whether the crawler obeys robots.txt.",
	"nofollow":                "Whether the link or page is nofollow.",
	"metric":                  "Which metric (see common values).",
	"max_pages":               "The most pages a crawl reads.",
	"indexed":                 "Pages indexed.",
	"in_sitemap":              "Whether the page is in a sitemap.",
	"height":                  "Height in pixels.",
	"duration_ms":             "How long it took.",
	"detail":                  "More detail, in words.",
	"ctr":                     "Click rate: clicks divided by impressions, 0 to 1.",
	"cost_usd":                "What the data call cost on the brand's own account, in US dollars.",
	"competitor":              "The competitor it is about, if one.",
	"aliases":                 "Other names it goes by.",
}

// dataCatalogPersonal are columns that hold personal data: who someone is, how
// to reach them, where they are. Agents treat these with care: never copy
// them anywhere the brand hasn't asked for.
var dataCatalogPersonal = map[string]bool{
	"email": true, "from_email": true, "to_email": true, "first_name": true, "last_name": true,
	"author_name": true, "anonymous_id": true, "user_id": true, "city": true, "region": true,
	"isp": true, "asn": true, "context": true,
}

// dataCatalogPersonalIn are columns personal only in some tables.
var dataCatalogPersonalIn = map[string][]string{
	"people":                  {"name", "attributes"},
	"records":                 {"name", "attributes"},
	"leads":                   {"custom"},
	"record_identities":       {"value"},
	"customer_events":         {"properties"},
	"support_conversations":   {"contact_email", "rating_remark", "ai_rating_remark"},
	"support_messages":        {"from_name"},
	"conversation_sentiments": {"quote"},
	"mention_episodes":        {"author"},
	"attribute_changes":       {"old_value", "new_value"},
	"partner_memberships":     {"application", "note"},
	"suppressions":            {"value", "note"},
	"crm_activities":          {"title"},
	"signals":                 {"title"},
}

// dataCatalogValueColumns are columns whose common values the catalog lists:
// the few values a status, kind or source takes say more than a type.
var dataCatalogValueColumns = map[string]bool{
	"status": true, "source": true, "origin": true, "kind": true, "channel": true, "stage": true, "state": true,
	"category": true, "type": true, "provider": true, "platform": true, "currency": true, "level": true, "move": true,
	"method": true, "purpose": true, "sentiment": true, "record_type": true, "namespace": true, "country": true,
	"device": true, "browser": true, "os": true, "utm_source": true, "utm_medium": true, "utm_campaign": true,
	"trigger_kind": true, "change": true, "name": true, "slot_type": true, "link_type": true, "bounce_type": true,
	"brand_currency": true, "quoted_currency": true, "billing_interval": true, "plan": true, "unit_name": true,
	"exit_reason": true, "step": true, "from_stage": true, "to_stage": true, "fire": true, "momentum": true,
	"churn_mode": true, "connector": true, "mode": true, "author": true, "decision": true, "added_by_type": true, "drafted_via": true, "result_status": true, "direction": true, "via": true, "side": true, "author_type": true, "band": true, "role": true, "value_type": true, "sensitivity": true, "added_via": true, "reply_state": true, "verdict": true, "severity": true, "rule": true, "engine": true, "intent": true, "owner": true, "stance": true, "source_type": true, "surface": true, "trigger": true, "entity": true, "grp": true, "design": true, "coverage": true, "token": true, "match_type": true, "click_id_kind": true, "matched_by": true,
	"bucket": true, "objective": true, "bidding_strategy": true, "model": true, "tag_method": true, "network": true,
	"payout_mode": true, "conversion_type": true, "cap_kind": true, "check_status": true, "cost_model": true,
	"redirect_mode": true, "medium": true, "transport": true, "search_term_match_type": true,
	"keyword_match_type": true, "provider_status": true, "action": true, "touch_connector": true, "reason": true, "landing_path": true, "referrer_host": true, "aggregate": true, "metric": true,
	"emit_event": true, "trigger_event": true, "goal_event": true, "status_reason": true, "review": true,
	"rel_requested": true, "sentiment_source": true, "field_key": true, "category_name": true,
}

// dataCatalogValuesIn says where name is worth listing: a customer's name is
// not, an event's or metric's is.
var dataCatalogValuesIn = map[string][]string{"name": {"customer_events", "metrics", "products", "rules", "journeys", "sequences"}}

// dataCatalogColumn is what the catalog says about one column.
type dataCatalogColumn struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	JoinsTo     string `json:"joins_to,omitempty"`
	Unit        string `json:"unit,omitempty"`
	Personal    bool   `json:"personal"`
}

// describeDataColumn writes up one column of a table: the table's own words
// first, then the common ones, then what its name says.
func describeDataColumn(table string, col queryColumn) dataCatalogColumn {
	t := queryCatalog[table]
	out := dataCatalogColumn{Name: col.Name, Type: col.Type}
	out.Description = t.Columns[col.Name]
	if out.Description == "" {
		out.Description = dataCatalogCommon[col.Name]
	}
	if out.Description == "" {
		out.Description = dataColumnConvention(col.Name)
	}
	out.JoinsTo = t.Joins[col.Name]
	if out.JoinsTo == "" {
		out.JoinsTo = dataCatalogJoins[col.Name]
	}
	switch {
	case strings.HasSuffix(col.Name, "_minor") || strings.HasSuffix(col.Name, "_cents"):
		cur := "currency"
		switch {
		case col.Name == "brand_amount_minor":
			cur = "brand_currency"
		case col.Name == "quoted_amount_minor":
			cur = "quoted_currency"
		}
		out.Unit = "minor units (cents) of the currency in " + cur + "; divide by 100 for most currencies"
	case strings.HasSuffix(col.Name, "_at") && strings.Contains(col.Type, "timestamp"):
		out.Unit = "UTC timestamp"
	case strings.HasSuffix(col.Name, "_micros"):
		cur := "currency"
		if col.Name == "cost_amount_micros" {
			cur = "cost_currency"
		}
		out.Unit = "micros (millionths) of the currency in " + cur + "; divide by 1,000,000"
	case strings.HasSuffix(col.Name, "_bp"):
		out.Unit = "basis points (1/100 of a percent)"
	case strings.HasSuffix(col.Name, "_percent"):
		out.Unit = "percent"
	case strings.HasSuffix(col.Name, "_ms"):
		out.Unit = "milliseconds"
	case strings.HasSuffix(col.Name, "_usd"):
		out.Unit = "US dollars"
	case strings.HasSuffix(col.Name, "_days"):
		out.Unit = "days"
	case strings.HasSuffix(col.Name, "_seconds"):
		out.Unit = "seconds"
	}
	out.Personal = dataCatalogPersonal[col.Name] || slices.Contains(dataCatalogPersonalIn[table], col.Name)
	return out
}

// dataColumnConvention describes a column by what its name says, or "" when it
// says too little: an id names what it points at, a time says what
// happened, an amount its unit.
func dataColumnConvention(name string) string {
	words := func(s string) string { return strings.ReplaceAll(s, "_", " ") }
	switch {
	case name == "created_at":
		return "When Outflare first stored the row."
	case name == "updated_at":
		return "When the row last changed."
	case strings.HasSuffix(name, "_id"):
		return "The id of the " + words(strings.TrimSuffix(name, "_id")) + " it belongs to."
	case strings.HasSuffix(name, "_at"):
		return "When it was " + words(strings.TrimSuffix(name, "_at")) + "."
	case strings.HasSuffix(name, "_minor"), strings.HasSuffix(name, "_cents"):
		w := strings.TrimSuffix(strings.TrimSuffix(name, "_minor"), "_cents")
		return "The " + words(w) + " in minor units (cents) of its currency."
	case strings.HasSuffix(name, "_micros"):
		return "The " + words(strings.TrimSuffix(name, "_micros")) + " in micros of its currency."
	}
	return ""
}

// dataCatalogTime is the column that dates a table's rows, or "".
func dataCatalogTime(table string, cols []queryColumn) string {
	if t := queryCatalog[table].Time; t != "" {
		return t
	}
	for _, name := range dataCatalogTimeColumns {
		for _, c := range cols {
			if c.Name == name {
				return name
			}
		}
	}
	return ""
}

// dataCatalogLists says whether the catalog lists a column's common values.
func dataCatalogLists(table string, col queryColumn) bool {
	if !dataCatalogValueColumns[col.Name] {
		return false
	}
	if only, ok := dataCatalogValuesIn[col.Name]; ok && !slices.Contains(only, table) {
		return false
	}
	return col.Type == "text" || col.Type == "boolean"
}
