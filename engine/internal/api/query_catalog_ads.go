package api

import "maps"

// The catalog of the paid ads, attribution and click tracker query tables
// (migration 0648): what each holds, where it comes from and how to read it.
func init() { maps.Copy(queryCatalog, adsCatalog) }

const adsNetworks = "Synced from the brand's connected ad accounts (Google Ads, Meta Ads, LinkedIn Ads, Reddit Ads and others), with the brand's own key or sign-in."

var adsCatalog = map[string]dataCatalogTable{
	"ad_accounts": {
		Area: "ads", Source: adsNetworks + " One row per ad account Outflare reads.",
		Time: "created_at",
		Columns: map[string]string{
			"integration_id":                    "The connection that reads it.",
			"time_zone":                         "The account's own time zone: its daily numbers are days in this zone.",
			"sync_on":                           "Whether Outflare syncs it.",
			"first_day":                         "The first day with numbers synced.",
			"last_day":                          "The last day with numbers synced.",
			"last_synced_at":                    "When it last synced.",
			"last_error":                        "Why the last sync failed, empty when it worked.",
			"target_cost_per_conversion_micros": "The brand's target cost per conversion for the account, if set.",
		},
		Examples: []dataCatalogExample{
			{"Which ad accounts are connected and how fresh are they?", "select connector, name, currency, status, last_day, last_error from ad_accounts order by connector, name"},
		},
	},
	"ad_entities": {
		Area: "ads", Source: adsNetworks + " Campaigns, ad groups (ad sets) and ads, as the network has them now.",
		Time: "created_at",
		Columns: map[string]string{
			"level":                             "campaign, ad_group or ad.",
			"parent_id":                         "The campaign of an ad group, or the ad group of an ad (ad_entities.id).",
			"provider_status":                   "The status as the network words it.",
			"objective":                         "What the campaign optimises for, as the network words it.",
			"daily_budget_micros":               "The daily budget.",
			"bidding_strategy":                  "How the network bids, such as maximize_conversions or target_cpa.",
			"platform_target_cpa_micros":        "The target cost per action set in the network.",
			"platform_target_roas":              "The target return on ad spend set in the network (2.5 is 250%).",
			"target_cost_per_conversion_micros": "The brand's own target cost per conversion in Outflare, if set.",
			"details":                           "More settings from the network, as JSON (targeting, ad copy and the like).",
		},
		Joins: map[string]string{"parent_id": "ad_entities.id"},
		Examples: []dataCatalogExample{
			{"Live campaigns and their daily budgets", "select name, connector, daily_budget_micros / 1e6 as daily_budget, e.currency from ad_entities e join ad_accounts a on a.id = e.ad_account_id where level = 'campaign' and e.status = 'active' order by 3 desc"},
		},
	},
	"ad_metrics_daily": {
		Area: "ads", Source: adsNetworks + " Each campaign's, ad group's and ad's numbers per day, as the network reports them; recent days are re-synced while the network still changes them.",
		Time: "day",
		Columns: map[string]string{
			"level":                       "campaign, ad_group or ad: which level ad_entity_id is. Sum one level only, or the spend counts twice.",
			"day":                         "The day, in the ad account's time zone.",
			"all_conversions":             "Every conversion the network counted, including ones not used for bidding.",
			"all_conversion_value_micros": "The value of all_conversions.",
			"reach":                       "People reached, when the network reports it.",
			"link_clicks":                 "Clicks on the ad's link (Meta and others that tell clicks apart).",
			"landing_page_views":          "Landing page views the network counted.",
			"video_views":                 "Video views.",
		},
		Examples: []dataCatalogExample{
			{"Spend, clicks and the network's conversions by campaign, last 30 days", "select e.name, sum(m.spend_micros) / 1e6 as spend, m.currency, sum(m.clicks) as clicks, sum(m.conversions) as conversions from ad_metrics_daily m join ad_entities e on e.id = m.ad_entity_id where m.level = 'campaign' and m.day >= current_date - 30 group by 1, 3 order by 2 desc"},
			{"Daily spend against sign-ups", "select m.day, sum(m.spend_micros) / 1e6 as spend, (select count(*) from people p where p.created_at::date = m.day) as signups from ad_metrics_daily m where m.level = 'campaign' group by 1 order by 1"},
		},
	},
	"ad_conversion_actions_daily": {
		Area: "ads", Source: adsNetworks + " The network's conversions per day split by conversion action (such as a purchase or sign-up action).",
		Time: "day",
		Columns: map[string]string{
			"action": "The conversion action's name in the network.",
		},
		Examples: []dataCatalogExample{
			{"Conversions by action, last 30 days", "select action, sum(conversions) from ad_conversion_actions_daily where day >= current_date - 30 group by 1 order by 2 desc"},
		},
	},
	"ad_keywords": {
		Area: "ads", Source: adsNetworks + " Search keywords of the ad groups, with the network's quality score and bid estimates.",
		Time: "updated_at",
		Columns: map[string]string{
			"text":                      "The keyword.",
			"quality_score":             "The network's quality score, 1 to 10.",
			"first_page_cpc_micros":     "The bid the network estimates for the first page.",
			"top_of_page_cpc_micros":    "The bid the network estimates for the top of the page.",
			"first_position_cpc_micros": "The bid the network estimates for the first position.",
		},
		Examples: []dataCatalogExample{
			{"Keywords with a low quality score", "select text, match_type, quality_score from ad_keywords where quality_score < 5 order by quality_score"},
		},
	},
	"ad_keyword_metrics_daily": {
		Area: "ads", Source: adsNetworks + " Each keyword's numbers per day.",
		Time: "day",
		Examples: []dataCatalogExample{
			{"Keywords by spend, last 30 days", "select k.text, sum(m.spend_micros) / 1e6 as spend, sum(m.clicks) as clicks, sum(m.conversions) as conversions from ad_keyword_metrics_daily m join ad_keywords k on k.id = m.keyword_id where m.day >= current_date - 30 group by 1 order by 2 desc"},
		},
	},
	"ad_search_terms_daily": {
		Area: "ads", Source: adsNetworks + " What people actually searched when a search ad showed, per day.",
		Time: "day",
		Columns: map[string]string{
			"term":                   "What the person searched.",
			"search_term_match_type": "How the search matched the keyword, as the network says.",
			"keyword_text":           "The keyword it matched.",
			"keyword_match_type":     "That keyword's match type.",
		},
		Examples: []dataCatalogExample{
			{"Search terms that spent but never converted", "select term, sum(spend_micros) / 1e6 as spend from ad_search_terms_daily where day >= current_date - 30 group by 1 having sum(conversions) = 0 order by 2 desc limit 50"},
		},
	},
	"ad_clicks": {
		Area: "ads", Source: adsNetworks + " Individual ad clicks with their click id, where the network reports them (Google Ads click view); they tie a click id on a visit to its campaign, ad group, ad and keyword.",
		Time: "day",
		Columns: map[string]string{
			"kind": "What kind of click id: gclid, gbraid or wbraid.",
		},
		Examples: []dataCatalogExample{
			{"Clicks by keyword, last 7 days", "select keyword, count(*) from ad_clicks where day >= current_date - 7 group by 1 order by 2 desc"},
		},
	},
	"ad_acquisitions": {
		Area: "ads", Source: "Outflare: the ad click that brought each person in, found from their visits' click ids and ad tags within the brand's acquisition window, with when they signed up and first paid. One row per person.",
		Time: "clicked_at",
		Columns: map[string]string{
			"campaign_entity_id":   "The campaign (ad_entities.id), when Outflare knows it.",
			"ad_group_entity_id":   "The ad group (ad_entities.id), when known.",
			"campaign_external_id": "The campaign's id at the network, as the click carried it.",
			"ad_group_external_id": "The ad group's id at the network, as the click carried it.",
			"ad_external_id":       "The ad's id at the network, as the click carried it.",
			"click_id_kind":        "The kind of click id: gclid, gbraid, wbraid, fbclid, msclkid and the like.",
			"matched_by":           "How the click was tied to the campaign: click_id, template, campaign_param or utm.",
			"window_days":          "The acquisition window that applied, in days.",
			"signed_up_at":         "When the person signed up.",
			"first_paid_at":        "When the person first paid, empty until they do.",
			"first_payment_minor":  "Their first payment.",
			"ad_consent":           "Whether the person agreed to ad measurement, so their conversion may be sent back to the network.",
		},
		Joins: map[string]string{"campaign_entity_id": "ad_entities.id", "ad_group_entity_id": "ad_entities.id"},
		Examples: []dataCatalogExample{
			{"Payers and cost per payer by campaign, last 30 days", "with s as (select ad_entity_id, sum(spend_micros) / 1e6 as spend from ad_metrics_daily where level = 'campaign' and day >= current_date - 30 group by 1) select e.name, count(a.first_paid_at) as payers, max(s.spend) as spend, max(s.spend) / nullif(count(a.first_paid_at), 0) as cost_per_payer from ad_entities e join s on s.ad_entity_id = e.id left join ad_acquisitions a on a.campaign_entity_id = e.id and a.clicked_at >= current_date - 30 group by 1 order by 3 desc"},
			{"Time from ad click to first payment", "select percentile_cont(0.5) within group (order by first_paid_at - clicked_at) from ad_acquisitions where first_paid_at is not null"},
		},
	},
	"ad_revenue": {
		Area: "ads", Source: "Outflare: every payment and refund event of a person, credited to the ad acquisition that brought them in (or to the last ad touch), so revenue can be compared to spend by campaign.",
		Time: "occurred_at",
		Columns: map[string]string{
			"touch_campaign_entity_id": "The campaign of the last ad touch before the payment (ad_entities.id), when there was one.",
			"touch_connector":          "The ad network of that touch.",
			"bucket":                   "new (from a customer the ads brought in) or returning.",
			"value_minor":              "The payment; negative for a refund.",
			"occurred_at":              "When the payment happened.",
		},
		Joins: map[string]string{"touch_campaign_entity_id": "ad_entities.id"},
		Examples: []dataCatalogExample{
			{"Revenue by campaign of acquisition", "select e.name, r.currency, sum(r.value_minor) / 100.0 as revenue from ad_revenue r join ad_acquisitions a on a.id = r.acquisition_id join ad_entities e on e.id = a.campaign_entity_id group by 1, 2 order by 3 desc"},
		},
	},
	"ad_changes": {
		Area: "ads", Source: "Outflare: every pause, resume and budget change Outflare made to a campaign, by a person or an agent, and whether the network shows it.",
		Time: "created_at",
		Columns: map[string]string{
			"kind":        "pause, resume or budget.",
			"before":      "The campaign's status and daily budget before, as JSON.",
			"after":       "What the change asked for, as JSON.",
			"status":      "queued, applied (the network took it) or failed.",
			"error":       "Why it failed, if it did.",
			"verified_at": "When the campaign was read back from the network and showed the change.",
			"verify_note": "What the network showed instead, when it did not show the change.",
			"actor_id":    "Who asked for it: a person, a token or an agent.",
			"action_id":   "The agent's action that asked for it, if one did.",
			"batch_id":    "Groups the changes of one brand-wide pause.",
		},
		Examples: []dataCatalogExample{
			{"Budget changes and the spend after them", "select c.created_at, e.name, c.before, c.after, c.status from ad_changes c join ad_entities e on e.id = c.ad_entity_id where c.kind = 'budget' order by c.created_at desc"},
		},
	},
	"ad_conversion_sends": {
		Area: "ads", Source: "Outflare: conversions sent back to the ad networks by the brand's conversion rules (Ads, Conversions), with what happened to each.",
		Time: "occurred_at",
		Columns: map[string]string{
			"occurred_at":    "When the conversion happened.",
			"status":         "Where the send stands: pending, sent, skipped or failed (see common values).",
			"reason":         "Why it was skipped or failed.",
			"value_micros":   "The value sent with it.",
			"country_source": "Where the country came from.",
			"transport":      "How it was sent to the network.",
			"attempts":       "How many times Outflare tried.",
			"sent_at":        "When the network took it.",
		},
		Examples: []dataCatalogExample{
			{"Conversion sends by status, last 7 days", "select status, reason, count(*) from ad_conversion_sends where occurred_at >= now() - interval '7 days' group by 1, 2 order by 3 desc"},
		},
	},
	"ad_audiences": {
		Area: "ads", Source: "Outflare: lists the brand syncs to its ad networks as audiences, to target or exclude.",
		Time: "created_at",
		Columns: map[string]string{
			"list_id": "The list it syncs.",
			"exclude": "Whether the audience is excluded from ads rather than targeted.",
		},
		Examples: []dataCatalogExample{
			{"Audiences and whether they are on", "select name, enabled, exclude from ad_audiences order by name"},
		},
	},
	"ad_match_quality": {
		Area: "ads", Source: "Read each day from the ad network (Meta's event match quality) for every pixel the brand's conversion rules send to. One row per pixel, event and day.",
		Time: "day",
		Columns: map[string]string{
			"target_id":  "The pixel (dataset) on the ad network, as on the conversion rule.",
			"event":      "The network's event name, such as Purchase.",
			"day":        "The day the score is for.",
			"score":      "The network's match quality score out of 10; empty when it gave none.",
			"keys":       "The share of events that carried each identifier, as JSON: [{\"key\", \"percent\"}].",
			"issues":     "What the network says is wrong and how to fix it, as JSON: [{\"name\", \"description\", \"solution\", \"percent\"}].",
			"fetched_at": "When Outflare last read it from the network.",
		},
		Examples: []dataCatalogExample{
			{"Latest match quality score per event", "select distinct on (target_id, event) target_id, event, day, score from ad_match_quality order by target_id, event, day desc"},
		},
	},
	"ad_holdouts": {
		Area: "ads", Source: "Set by the brand in Ads, Experiments: a holdout test, where ads are kept out of some countries for a while to see what they really add. One row per test; the results are counted from visits and events when read through the API.",
		Time: "starts_on",
		Columns: map[string]string{
			"id":         "The holdout test's id.",
			"name":       "The name the brand gave the test.",
			"connector":  "The ad network whose ads are held out, such as google_ads; empty when every network's are.",
			"countries":  "The two-letter codes of the countries the ads are kept out of.",
			"starts_on":  "The first day ads are held out.",
			"ends_on":    "The last day ads are held out.",
			"created_at": "When the test was added.",
			"updated_at": "When the test was last changed.",
		},
		Examples: []dataCatalogExample{
			{"Holdout tests, newest first", "select name, connector, countries, starts_on, ends_on from ad_holdouts order by starts_on desc"},
		},
	},
	"ad_brand_economics": {
		Area: "ads", Source: "Set by the brand in Ads: what a customer is worth and what it may pay to get one. One row per brand.",
		Time: "updated_at",
		Columns: map[string]string{
			"target_cost_per_payer_micros":  "The most the brand wants to pay for a new paying customer.",
			"target_cost_per_signup_micros": "The most the brand wants to pay for a sign-up.",
			"acquisition_window_days":       "How long after an ad click a sign-up still counts as from the ad.",
			"payback_months":                "How many months a customer may take to pay back what they cost.",
			"gross_margin_percent":          "The brand's gross margin.",
			"daily_budget_ceiling_micros":   "The most the brand will spend on ads in a day.",
			"shadow_mode":                   "Whether Outflare only suggests ad changes instead of making them.",
		},
		Examples: []dataCatalogExample{
			{"The brand's ad targets", "select * from ad_brand_economics"},
		},
	},
	"attribution_conversions": {
		Area: "analytics", Source: "Outflare: each conversion (an event a conversion definition counts, such as a sign-up or payment) with the visits that led to it.",
		Time: "occurred_at",
		Columns: map[string]string{
			"definition_id": "The conversion definition that counted it.",
			"name":          "The conversion's name.",
			"occurred_at":   "When the conversion happened.",
			"value_minor":   "Its value.",
			"touches":       "How many touches (visits and clicks) came before it.",
			"matched_by":    "How its touches were found.",
			"credited_at":   "When Outflare shared out its credit.",
		},
		Examples: []dataCatalogExample{
			{"Conversions by name, last 30 days", "select name, count(*), sum(value_minor) / 100.0 from attribution_conversions where occurred_at >= now() - interval '30 days' group by 1"},
		},
	},
	"attribution_credits": {
		Area: "analytics", Source: "Outflare: how each conversion's credit is shared over the touches before it, once per attribution model.",
		Time: "touch_at",
		Columns: map[string]string{
			"event_id":   "The conversion (attribution_conversions.event_id).",
			"model":      "The attribution model: first_touch, last_touch, linear and the like (see attribution_models.key).",
			"position":   "The touch's place in the path, 1 for the first.",
			"touch_at":   "When the touch happened.",
			"source":     "The touch's source.",
			"campaign":   "The touch's campaign.",
			"tag_method": "What decided the touch's channel: ofc, ref, click_id, utm, referrer or none.",
			"weight":     "The touch's share of the conversion, 0 to 1; a conversion's shares add up to 1 per model.",
		},
		Joins: map[string]string{"event_id": "attribution_conversions.event_id, customer_events.id"},
		Examples: []dataCatalogExample{
			{"Conversions by channel, linear model", "select c.channel, sum(c.weight) as conversions, sum(c.weight * v.value_minor) / 100.0 as value from attribution_credits c join attribution_conversions v on v.event_id = c.event_id where c.model = 'linear' group by 1 order by 2 desc"},
		},
	},
	"attribution_models": {
		Area: "analytics", Source: "Outflare's built-in attribution models (no brand) and the brand's own.",
		Time: "created_at",
		Columns: map[string]string{
			"brand_id":    "The brand that made it; empty for a built-in model.",
			"spec":        "How it shares credit, as JSON.",
			"archived_at": "When it was archived, if it was.",
		},
		Examples: []dataCatalogExample{
			{"The models", "select key, name, version from attribution_models where archived_at is null"},
		},
	},
	"tracking_campaigns": {
		Area: "analytics", Source: "Made by the brand in Tracking: campaigns whose links carry tags (and redirect links), so visits and clicks are credited to them.",
		Time: "created_at",
		Columns: map[string]string{
			"slug":               "The utm_campaign the links carry.",
			"code":               "A short code for the campaign.",
			"source":             "The utm_source the links carry.",
			"object_kind":        "What the campaign is for in Outflare, such as a sequence or a post.",
			"object_id":          "The id of that object.",
			"landing_url":        "The page the links lead to.",
			"redirect_links":     "Whether its links go through Outflare's redirect.",
			"redirect_mode":      "How the redirect sends people on.",
			"cost_model":         "How its traffic is paid for, such as cpc or a revenue share.",
			"cost_amount_micros": "The cost per unit of cost_model.",
			"cost_share_bp":      "The revenue share paid, for a revenue share.",
			"cost_currency":      "The currency of the cost.",
		},
		Examples: []dataCatalogExample{
			{"Visits by tracking campaign", "select t.name, count(v.id) from tracking_campaigns t left join visits v on v.utm_campaign = t.slug group by 1 order by 2 desc"},
		},
	},
	"tracking_links": {
		Area: "analytics", Source: "Made by the brand in Tracking: the redirect links of each campaign.",
		Time: "created_at",
		Columns: map[string]string{
			"campaign_id":   "The tracking campaign (tracking_campaigns.id).",
			"url":           "The page the link leads to.",
			"content":       "The utm_content it carries.",
			"term":          "The utm_term it carries.",
			"redirect_code": "The short code in the link.",
		},
		Joins: map[string]string{"campaign_id": "tracking_campaigns.id"},
		Examples: []dataCatalogExample{
			{"Clicks per link", "select l.redirect_code, l.url, count(c.id) from tracking_links l left join tracking_link_clicks c on c.link_id = l.id group by 1, 2 order by 3 desc"},
		},
	},
	"tracking_link_clicks": {
		Area: "analytics", Source: "Outflare's redirect: one row per click on a tracking or partner link, with the device and place (no address kept).",
		Time: "clicked_at",
		Columns: map[string]string{
			"click_key":           "Outflare's id of the click, carried to the site so the visit can be tied to it.",
			"landed_at":           "When the click reached the site, if it did.",
			"direct":              "Whether the link sent people straight on with no page of Outflare's in between.",
			"sub_id":              "The sub id the traffic source passed.",
			"source_click_id":     "The traffic source's own click id.",
			"referrer_host":       "The site the click came from.",
			"cost_micros":         "What the traffic source said the click cost.",
			"network_cost_micros": "The click's share of the network's reported cost, when Outflare spreads it over clicks.",
			"destination_id":      "Which of the link's destinations it went to.",
			"path_id":             "Which of the link's rule paths it took; empty for the default.",
			"region":              "The region the click came from.",
			"city":                "The city the click came from.",
			"asn":                 "The network's number.",
			"isp":                 "The internet provider.",
		},
		Examples: []dataCatalogExample{
			{"Clicks and bots by country, last 7 days", "select country, count(*), count(*) filter (where likely_bot) as bots from tracking_link_clicks where clicked_at >= now() - interval '7 days' group by 1 order by 2 desc"},
		},
	},
	"tracking_destination_days": {
		Area: "analytics", Source: "Outflare: clicks per link destination per day.",
		Time: "day",
		Columns: map[string]string{
			"destination_id": "The destination.",
		},
		Joins: map[string]string{"destination_id": "tracking_link_clicks.destination_id"},
		Examples: []dataCatalogExample{
			{"Clicks per destination, last 30 days", "select destination_id, sum(clicks) from tracking_destination_days where day >= current_date - 30 group by 1"},
		},
	},
	"tracker_traffic_sources": {
		Area: "ads", Source: "Made by the brand in the click tracker: the traffic sources (ad networks it buys clicks from) and how conversions are sent back to them.",
		Time: "created_at",
		Columns: map[string]string{
			"template":          "The network template it was made from.",
			"postback_share":    "The share of the payout sent back to the source, in percent.",
			"postback_types":    "The conversion types sent back; empty for all.",
			"postback_statuses": "The conversion statuses sent back.",
		},
		Examples: []dataCatalogExample{
			{"Traffic sources", "select name, template, currency, status from tracker_traffic_sources"},
		},
	},
	"tracker_offers": {
		Area: "ads", Source: "Made by the brand in the click tracker: the offers clicks are sent to, and what each pays.",
		Time: "created_at",
		Columns: map[string]string{
			"payout_mode":       "postback (what the postback says, else payout_micros) or fixed (always payout_micros).",
			"payout_micros":     "What a conversion pays.",
			"country_payouts":   "Payouts by the click's country, as JSON in micros, before payout_micros.",
			"conversion_type":   "The conversion type it counts.",
			"daily_cap":         "How many a day it takes; empty for no cap, 0 stops it.",
			"cap_kind":          "Whether daily_cap counts clicks or conversions.",
			"fallback_offer_id": "Where clicks go once it is capped.",
			"check_status":      "Whether its page answered at the last check.",
			"checked_at":        "When it was last checked.",
			"broken_since":      "Since when its page has been broken, if it is.",
		},
		Joins: map[string]string{"fallback_offer_id": "tracker_offers.id"},
		Examples: []dataCatalogExample{
			{"Offers by conversions, last 30 days", "select o.name, sum(d.clicks) as clicks, sum(d.conversions) as conversions from tracker_offers o join tracker_offer_days d on d.offer_id = o.id where d.day >= current_date - 30 group by 1 order by 3 desc"},
		},
	},
	"tracker_offer_days": {
		Area: "ads", Source: "Outflare's click tracker: clicks and conversions per offer per day.",
		Time: "day",
		Columns: map[string]string{
			"conversions": "Conversions.",
		},
		Examples: []dataCatalogExample{
			{"Conversion rate by offer", "select offer_id, sum(conversions)::numeric / nullif(sum(clicks), 0) from tracker_offer_days group by 1"},
		},
	},
	"tracker_landers": {
		Area: "ads", Source: "Made by the brand in the click tracker: landing pages shown before an offer.",
		Time: "created_at",
		Columns: map[string]string{
			"ctas":         "How many calls to action the page has.",
			"check_status": "Whether the page answered at the last check.",
			"checked_at":   "When it was last checked.",
			"broken_since": "Since when the page has been broken, if it is.",
		},
		Examples: []dataCatalogExample{
			{"Lander click-through by lander", "select l.name, count(c.id) from tracker_landers l left join tracker_lander_clicks c on c.lander_id = l.id group by 1"},
		},
	},
	"tracker_lander_clicks": {
		Area: "ads", Source: "Outflare's click tracker: each click on a lander's call to action.",
		Time: "clicked_at",
		Columns: map[string]string{
			"click_id":  "The tracker click it continues (tracking_link_clicks.id).",
			"click_key": "Outflare's id of that click.",
			"cta":       "Which call to action, 1 for the first.",
		},
		Joins: map[string]string{"click_id": "tracking_link_clicks.id"},
		Examples: []dataCatalogExample{
			{"Clicks by call to action", "select lander_id, cta, count(*) from tracker_lander_clicks group by 1, 2"},
		},
	},
	"tracker_flows": {
		Area: "ads", Source: "Made by the brand in the click tracker: flows that route clicks to landers and offers by rules.",
		Time: "created_at",
		Examples: []dataCatalogExample{
			{"Flows", "select name, status from tracker_flows"},
		},
	},
	"tracker_conversions": {
		Area: "ads", Source: "Outflare's click tracker: conversions offers reported by postback, tied to the click that led to them.",
		Time: "converted_at",
		Columns: map[string]string{
			"click_id":       "The tracker click (tracking_link_clicks.id).",
			"campaign_id":    "The tracking campaign (tracking_campaigns.id).",
			"converted_at":   "When the conversion happened.",
			"transaction_id": "The offer's id for the conversion.",
			"type":           "The conversion type the offer reported.",
			"status":         "approved, pending or rejected and the like, as the offer reported it.",
			"postbacks":      "How many postbacks arrived for it.",
		},
		Joins: map[string]string{"click_id": "tracking_link_clicks.id", "campaign_id": "tracking_campaigns.id"},
		Examples: []dataCatalogExample{
			{"Payouts by traffic source, last 30 days", "select s.name, c.currency, sum(c.payout_micros) / 1e6 from tracker_conversions c join tracker_traffic_sources s on s.id = c.source_id where c.converted_at >= now() - interval '30 days' and c.status = 'approved' group by 1, 2"},
		},
	},
	"tracker_network_costs": {
		Area: "ads", Source: "Synced from the traffic sources' networks with the brand's own key: what each network campaign spent per day, and how Outflare shared it over clicks.",
		Time: "day",
		Columns: map[string]string{
			"network":             "The network, named by its traffic source template.",
			"campaign_ref":        "The network's campaign id, as the clicks carry it.",
			"spend_micros":        "What the network reported spending.",
			"matched_micros":      "Spend put on the clicks of the target that spent it.",
			"spread_micros":       "Spend shared evenly over the campaign's clicks that named no reported target.",
			"unattributed_micros": "Spend put on no click.",
			"clicks":              "Clicks the network reported.",
			"targets":             "Targets (placements) the network reported.",
			"synced_at":           "When it was last synced.",
		},
		Examples: []dataCatalogExample{
			{"Network spend by day", "select day, network, sum(spend_micros) / 1e6 from tracker_network_costs group by 1, 2 order by 1"},
		},
	},
}
