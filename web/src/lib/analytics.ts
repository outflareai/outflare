import type { PostHog } from "posthog-js";
import { ownScript } from "./own-script";
import { api } from "../api";
import { anonymousPath } from "./paths";
import { isDesktopApp } from "./desktop";

// Product analytics. The app's own events (track, and a page view per page)
// go to the server once someone is signed in and the server keeps events
// (/v1/meta has ui_events), which records them with its own events. PostHog loads only when the server operator set
// OUTFLARE_POSTHOG_KEY (/v1/meta then has product_analytics), for page
// views, autocapture and session replay, through /ingest/ on the same
// server. Self-hosted servers without it load nothing.
//
// Customer data stays out: page text, inputs and element attributes are
// masked in autocapture and session replay, and URLs keep only the page
// (/:ws/sequences/:item), never workspace names, ids, domains, tokens or query strings.

let client: Promise<PostHog | null> | undefined;
function fetchMeta() {
  return api.GET("/v1/meta");
}
let meta: ReturnType<typeof fetchMeta> | undefined;

function serverMeta() {
  meta ??= fetchMeta();
  return meta;
}

function load(): Promise<PostHog | null> {
  client ??= serverMeta()
    .then(async ({ data }) => {
      const cfg = data?.product_analytics;
      if (cfg?.provider !== "posthog") return null;
      const { default: posthog } = await import("posthog-js");
      posthog.init(cfg.key, {
        api_host: cfg.api_host,
        ui_host: cfg.ui_host,
        person_profiles: "identified_only",
        capture_pageview: false, // sent by page() with a clean URL
        capture_pageleave: true,
        mask_all_text: true,
        mask_all_element_attributes: true,
        session_recording: { maskAllInputs: true, maskTextSelector: "*" },
        before_send: (event) => {
          if (!event) return event;
          const p = event.properties;
          for (const k of ["$current_url", "$referrer", "$initial_current_url", "$initial_referrer"]) {
            if (typeof p[k] === "string") p[k] = cleanURL(p[k]);
          }
          if (typeof p.$pathname === "string") p.$pathname = cleanPath(p.$pathname);
          return event;
        },
      });
      return posthog;
    })
    .catch(() => null);
  return client;
}

// /{workspace}/{tab}/{item} keeps only the tab; other paths keep the path.
export const cleanPath = anonymousPath;

export function cleanURL(href: string): string {
  try {
    const u = new URL(href);
    if (u.origin !== location.origin) return u.origin + "/"; // someone else's site: the origin is enough
    const utm = [...u.searchParams].filter(([k]) => k.startsWith("utm_"));
    return u.origin + cleanPath(u.pathname) + (utm.length ? "?" + new URLSearchParams(utm) : "");
  } catch {
    return "";
  }
}

// Called once signed in: ties events to the person and their workspace.
export function identify(user: { id?: string; name?: string; email?: string }, ws?: { id: string; name: string }) {
  if (user.id) {
    signedIn = true;
    workspaceID = ws?.id;
  }
  load().then((ph) => {
    if (!ph || !user.id) return;
    ph.identify(user.id);
    if (ws) ph.group("workspace", ws.id, { name: ws.name });
  });
}

// The app's own events go to the server once someone is signed in
// (POST /v1/ui-events), which records them like its own: they reach wherever
// the operator keeps events, with or without PostHog. They wait a moment so
// a burst is one request, and leave with the page when it closes. Signed
// out, they go to PostHog as before, when the operator set it.
type Queued = { name: string; occurred_at: string; properties: Props };
let signedIn = false;
let workspaceID: string | undefined;
let queue: Queued[] = [];
let timer: ReturnType<typeof setTimeout> | undefined;

function record(event: string, props: Props) {
  const at = new Date().toISOString();
  // The server says in /v1/meta whether it keeps events (ui_events); one
  // that keeps none is sent none.
  serverMeta()
    .then(({ data }) => data?.ui_events === true)
    .catch(() => false)
    .then((kept) => {
      if (!signedIn || !kept) {
        load().then((ph) => ph?.capture(event, props));
        return;
      }
      const { surface: _surface, ...properties } = props; // sent once, with the batch
      queue.push({ name: event, occurred_at: at, properties });
      if (queue.length >= 20) flush();
      else timer ??= setTimeout(flush, 2000);
    });
}

// Sent with fetch, not the API client: a send that fails must not be
// recorded as a failed request, which would queue another send.
function flush() {
  clearTimeout(timer);
  timer = undefined;
  while (queue.length) {
    const events = queue.splice(0, 50);
    fetch("/v1/ui-events", {
      method: "POST",
      credentials: "same-origin",
      keepalive: true,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ workspace_id: workspaceID, surface: surface(), events }),
    }).catch(() => {});
  }
}

let currentPage = "";

// Where the person is: a browser, or the Outflare desktop app's window
// (docs/desktop-app.md). Never "desktop", which is Outflare for desktop,
// the AI usage agent.
function surface(): "web" | "desktop_app" {
  return isDesktopApp() ? "desktop_app" : "web";
}

// One pageview per page (tab), sent from the signed-in shell and the sign-in screens.
export function page(name: string) {
  currentPage = name;
  load().then((ph) => ph?.capture("$pageview", { page: name, surface: surface() }));
  if (signedIn) record("ui.page_viewed", { page: name });
}

// Events only the browser sees. Everything that calls the API is recorded
// by the server (api.called plus the action, such as mailbox.connected), so
// track here only what never reaches it: errors shown, crashes, and actions
// without a request. Names are object.verb in the past tense, like the
// server's; add new ones to this list (AGENTS.md, "Events").
export type UiEvent =
  | "ui.table_view_saved" // a table's Columns menu: the columns shown were saved as the viewer's default (view, shown, hidden)
  | "ui.table_view_reset" // a table's Columns menu: the saved default was dropped for the standard columns (view)
  | "ui.inbox_alerts_changed" // on: whether sound and notifications were turned on
  | "ui.sales_reporting_interacted" // bounded view, period, stage and wallet values only
  | "ui.sales_section_opened"
  | "ui.sales_ai_preview_discarded" // a generated preview discarded without replacing the saved reply
  | "ui.sales_filter_changed"
  | "ui.sales_source_opened"
  | "ui.sales_support_handoff_opened"
  | "ui.inbox_sales_conversations_opened"
  | "ui.sales_personal_settings_opened"
  | "ui.sales_dialog_opened"
  | "ui.sales_template_editor_opened"
  | "ui.sales_feed_tested" // Sales channels: a Slack channel tried (kind, outcome, previews)
  | "ui.sales_feed_item_opened" // Needs a look: a post opened to say who wrote it (status, candidates)
  | "ui.sales_slack_hit_opened" // In Slack panel: a search hit opened in Slack (position, hits)
  | "ui.sales_arrivals_shown" // Sales queue: conversations that arrived while the list was open were marked "Just in" (count)
  | "ui.sales_crm_link_opened" // conversation page: the outside CRM link from a Slack post opened (kind)
  | "ui.sales_quality_marked" // Sales queue: a conversation marked good, poor fit or junk, or the mark cleared (quality)
  | "ui.sales_feed_layout_opened" // Sales channels: the "Shown first" chooser opened for a channel (labels, chosen)
  | "ui.ads_level_opened" // paid ads narrowed to one network or account, or widened (level)
  | "ui.ads_window_changed" // Paid ads: the attribution window switched (days)
  | "ui.ads_customers_filter_changed" // Paid ads: the customers switch changed (customers: all, new, returning)
  | "ui.ads_setup_step_opened" // Paid ads: a set-up step's fix page opened (step, done)
  | "ui.ads_weekly_report_opened" // Paid ads, Set up: the weekly report's digest page opened (on)
  | "ui.ads_shadow_viewed" // Paid ads: the "What the rules would have done" section opened (rows)
  | "ui.ads_bulk_changed" // Paid ads: several ticked campaigns, ad groups, ads or keywords paused, resumed, re-budgeted or removed at once (kind, level, campaigns, items, failed)
  | "ui.ads_actions_view_opened" // Paid ads, Actions: To do, In progress, Done, Dismissed or Preview opened (view)
  | "ui.ads_action_opened" // Paid ads, Actions: an action's own page opened from the table (kind, view)
  | "ui.ads_action_agent_asked" // Paid ads, an action's page: the agent chat was asked about it (kind)
  | "ui.brand_kit_view_changed" // Brand kit: the list or gallery view was picked (view)
  | "ui.brand_kit_upload_cancelled" // Brand kit: files were chosen and then not added (files)
  | "ui.creatives_view_changed" // Creatives library: the list or gallery view was picked (view)
  | "ui.creative_size_changed" // One creative: another size of the ad was looked at (size)
  | "ui.creative_skill_edit_cancelled" // Skills and prompts: changes to a skill were thrown away (changed: how many fields)
  | "ui.ads_audience_file_downloaded" // Paid ads, Audiences: an audience's file of hashed emails and phones was downloaded (skipped: whether anyone was left out)
  | "ui.ads_alert_fix_opened" // Paid ads, Actions: an alert's button opened the page that fixes it (kind)
  | "ui.ads_compare_changed" // Paid ads Overview: what the range is compared with changed (compare: previous, year)
  | "ui.ads_numbers_saved" // Paid ads Overview: the chosen numbers and their targets saved (numbers: how many show, targets: how many changed)
  | "ui.ads_match_check_run" // Paid ads, Tracking: the dry run of matching visits by network and device was run (verdict)
  | "ui.ads_holdout_saved" // Paid ads, Experiments: a holdout test was saved (countries: how many, connector: the network or all)
  | "ui.ads_creatives_view_changed" // Paid ads, Creatives: switched between the gallery and the list (view)
  | "ui.ads_business_downloaded" // Paid ads, Whole business: the report was downloaded as CSV
  | "ui.ads_creative_paused" // Paid ads, Creatives: an ad was paused from its card (verdict)
  | "ui.ads_model_picked" // Paid ads campaigns table: an attribution model chosen to count beside the last ad click (model: its id, or custom)
  | "ui.ads_screen_opened" // Paid ads: a screen of the page opened (screen)
  | "ui.seo_board_map_measured" // measure: what the SEO board map is shaded by
  | "ui.broadcast_recipients_downloaded" // a broadcast's recipients saved as CSV (group, rows)
  | "ui.ads_status_filter_changed" // Paid ads: the Status filter over a table of campaigns, ad groups, ads or keywords changed (filter)
  | "ui.ads_report_downloaded" // Paid ads: a report table saved as CSV (report, rows)
  | "ui.report_grouped" // Tracker report: the group-by levels changed (levels)
  | "ui.report_filtered" // Tracker report: a filter added (not)
  | "ui.report_sorted" // Tracker report: sorted by a column (metric, or custom)
  | "ui.report_drilled" // Tracker report: a row drilled into (level)
  | "ui.report_downloaded" // Tracker report: saved as CSV (levels)
  | "ui.report_view_opened" // Tracker report: a saved view opened (shared, mine)
  | "ui.report_columns_changed" // Tracker report: custom columns changed (custom)
  | "ui.ads_range_changed" // Paid ads: the date range changed (days, custom)
  | "ui.ad_preview_text_opened" // A search ad's preview: every headline and description opened (where)
  | "ui.ad_draft_edit_opened" // Paid ads: a new ad's text opened for editing
  | "ui.funnel_step_moved" // a funnel step dragged or moved to another place (from, to)
  | "ui.funnel_step_optional_set" // a funnel step made optional or required (optional)
  | "ui.funnel_step_checked" // a funnel step's own numbers opened
  | "ui.funnel_opened_from_step" // a tab opened with a funnel from one of its steps on
  | "ui.funnel_view_switched" // a funnel shown as a list or as bars (view)
  | "ui.funnel_ad_clicks_switched" // the ad clicks reference bar on a funnel turned on or off (on)
  | "ui.funnel_people_opened" // the people behind a funnel step or drop-off opened (dropped, breakdown)
  | "ui.funnel_correlation_opened" // what converters did differently asked for on a funnel
  | "ui.annotation_started" // the add-a-note form opened on a trend chart
  | "ui.dashboard_tile_moved" // a dashboard tile moved or resized with the arrows (resized)
  | "ui.dashboard_tile_editor_opened" // the builder opened on a dashboard to change a tile or make a new one (kind, new)
  | "ui.ads_chart_metric_toggled" // a number tile on the Paid ads chart clicked to draw or hide its line (metric, on)
  | "ui.ads_chart_metric_picked" // another metric chosen for a tile on the Paid ads chart (metric)
  | "ui.dashboard_tile_opened_in_builder" // a dashboard report opened on its own page from the tile's menu (kind)
  | "ui.insight_panel_opened" // a built-in panel on Paid ads, Visitors or Home opened in Funnels or Trends (kind, from)
 | "ui.partner_agreement_downloaded"
  | "ui.partner_proposal_composer_opened"
  | "ui.partner_offer_template_used" // the offer form was filled from a saved template
  | "ui.partner_coupon_composer_opened"
 | "ui.crm_view_changed"
 | "ui.crm_section_opened"
 | "ui.crm_exported"
 | "ui.event_people_pasted" // rows: how many rows a paste filled on Add people from an event
 | "ui.event_people_file_read" // rows: how many people a CSV gave on Add people from an event
 | "ui.crm_people_sequenced"
  | "ui.review_view_opened" // review module subview selected
  | "ui.review_campaign_step_opened" // review campaign editor step selected
  | "ui.account_recorded"
  | "ui.content_copied" // a content item copied as Markdown or HTML (format)
  | "ui.content_downloaded" // a content item downloaded as a Markdown file
  | "ui.account_takeover_started"
  | "ui.account_takeover_handed_back"
  | "ui.accounts_offboarded"
  | "ui.coverage_view_opened"
  | "ui.partnerships_tab_opened" // a Partnerships tab opened (tab: find, deals, live, affiliates, payments, results)
  | "ui.partnership_pipeline_set_up"
  | "ui.coverage_exported"
  | "ui.link_order_picks_added" // recommended donors added to a quote request (count, top score)
  | "ui.link_order_picks_outreach_started" // recommended donors sent to find emails and join a sequence (count)
  | "ui.link_order_outreach_sequence_created" // a draft link-building sequence made from the picks dialog
  | "ui.link_order_picks_ahrefs_checked" // recommended donors sent to an Ahrefs check (count)
  | "ui.link_order_order_ahrefs_checked" // an order's donor sent to an Ahrefs check before approving or publishing (count)
  | "ui.link_order_pick_count_changed" // how many recommended donors to show changed (count)
  | "ui.link_order_picks_selected_all" // every recommended donor ticked or unticked at once (count, selected)
  | "ui.link_order_light_overridden" // an order approved or published without a green light, after ticking the override (step, tone)
  | "ui.link_order_pick_topics_changed" // topics to prefer for recommended donors changed (count)
  | "ui.link_order_bought_recorded" // already-bought links recorded from the form or a CSV of past orders (count, published)
  | "ui.coverage_filters_changed"

  | "ui.search_workspace_opened"
  | "ui.search_finding_opened"
  | "ui.search_report_run"
  | "ui.search_report_saved"
  | "ui.search_collection_queued"
  | "ui.search_schedule_changed"
  | "ui.search_workspace_deleted"
  | "ui.search_work_saved"
  | "ui.search_performance_exported" // dimension, rows and whether a period was compared
  | "ui.seo_overview_view_switched" // view: dashboard, targets, declining, cannibalization, country-pages, technical, links, performance or trends
  | "ui.seo_board_period_picked" // period: the SEO dashboard tile picked
  | "ui.seo_targets_filtered" // filter: the SEO target report tile picked
  | "ui.seo_targets_column_filtered" // column: the SEO target report column filtered (page, keyword, status, intent, type, band)
  | "ui.seo_target_opened" // secondary and tracked counts of the target row opened
  | "ui.seo_targets_imported" // rows, created, updated and skipped of a keyword map CSV import
  | "ui.seo_board_pages_downloaded" // period and rows of the SEO dashboard pages CSV
  | "ui.seo_links_filtered" // bought_only: the lost links filter on the SEO links view
  | "ui.seo_links_order_opened" // problems: how many the opened link order had
  | "ui.seo_technical_filtered" // severity: the technical SEO tile picked, or all
  | "ui.seo_country_pages_template_picked" // picked: whether a path prefix was set on the country pages view
  | "ui.seo_cannibal_primary_set" // cleared: whether the page to keep went back to Outflare's pick
  | "ui.seo_declining_period_picked" // days: 7 or 28 on the declining pages view
  | "ui.seo_money_view_switched" // view: positions, low_ctr, lost or formula on the SEO dashboard
  | "ui.search_fix_created"
  | "ui.search_insight_report_selected"
  | "ui.partner_help_composer_opened"
  | "ui.partner_help_opened"
  | "ui.partner_portal_navigated"
 | "ui.partner_opportunity_opened"
 | "ui.partner_profile_composer_opened"
  | "ui.partner_task_composer_opened"
  | "ui.app_signin_started" // in the desktop app, the person sent sign-in to their browser
  | "ui.app_external_link_opened" // in the desktop app, a link that leaves Outflare opened in the browser
  | "ui.app_update_offered" // in the desktop app, the Update button appeared (version)
  | "ui.app_update_started" // in the desktop app, the person clicked Update; the app installs it and reopens (version)
  | "ui.cookie_settings_opened" // Cookie settings in the account menu reopened the cookie banner (where the app runs the operator's own tracking tag)
  | "ui.app_update_checked" // in the desktop app, Check for updates in the account menu (result: available, none, off; version)
  | "ui.app_update_failed" // in the desktop app, installing the update failed (error_code)
  | "ui.error_shown" // an error message was shown to the person (error_code, and request_id of the call that failed)
  | "ui.page_viewed" // a page (tab) of the app was opened by someone signed in (page)
  | "ui.crashed" // a script error or a page that failed to render
  | "ui.stale_build_reloaded" // the page reloaded itself once because a script of the previous build was gone after a deploy (reason: entry or chunk)
  | "ui.dns_one_click_used" // a way to add DNS records outside Lifecycle (where: seo_site, tracking_domain, sending_domain, brand_setup, brand_dns; way: cloudflare, ai, developer, manual; how: sign_in, connected, added, approval, approved, approval_cancelled, copied, claude, chatgpt, email; groups for brand_dns)
  | "ui.request_failed" // a call never got an answer (offline, blocked)
  | "ui.command_run" // a command palette command
  | "ui.ai_account_brand_assignment_opened"
  | "ui.ai_account_brand_assignment_deferred"
  | "ui.ai_account_report_scope_changed"
  | "ui.ai_account_brand_selected" // explicit subscription brand choice
  | "ui.ai_accounts_view_opened" // a view of AI accounts opened (view: accounts, spend, people, timeline, usage)
  | "ui.ai_accounts_example_shown" // the example usage data on AI accounts' Usage view turned on
  | "ui.desktop_download_opened" // public desktop download opened (location: ai_accounts or computers)
  | "ui.ai_device_owner_opened" // linked device owner opened (user_id)
  | "ui.ai_device_link_approved" // a computer linked to Outflare for desktop from /connect/computer
  | "ui.copied" // a copy button
  | "ui.chat_exported" // an agent conversation downloaded as Markdown
  | "ui.fix_ticket_copied" // a fix ticket copied to hand on (as: issue, agent, link)
  | "ui.shared_dashboard_password_wrong" // a shared dashboard's password was wrong
  | "ui.shared_ai_visibility_password_wrong" // a shared AI visibility page's password was wrong
  | "ui.ai_visibility_csv_downloaded" // an AI visibility table downloaded as CSV (table: questions or placements)
  | "ui.data_network_previewed" // the owner opened what joining the data network would share (addresses)
  | "ui.data_network_join_cancelled" // the owner closed that preview without joining (addresses)
  | "ui.partner_install_viewed" // a tab of a partner program's install steps opened (step: script or conversions; way: html, nextjs, gtm, curl, node, postback, browser)
  | "ui.partners_filtered" // a program's partner list filtered (filter: payout_details, value: missing or all)
  | "ui.partners_searched" // a search started on a program's partner list (sort); never the search text
  | "ui.partners_sorted" // a program's partner list sorted (sort: newest, oldest, name, code, clicks, customers, sales, balance, paid)
  | "ui.partner_opened" // a partner's page opened (from: list, record, search when a whole partner id is pasted in the list's search, rewards, conversions or review from a row's partner link, proof from a proof's Open payouts)
  | "ui.partner_tab_opened" // a tab of a partner's page opened (tab: referrals, conversions, rewards, payouts, activity)
  | "ui.partner_proof_opened" // a reward's proof trail opened in its drawer (from: program or partner)
  | "ui.partner_proof_link_followed" // the proof drawer switched to another reward (to: reversed_by, reverses, or back)
  | "ui.partner_proof_export_opened" // the date range for downloading a partner's proof opened
  | "ui.partner_rewards_bulk_confirm_opened" // the bulk approve or reject confirmation opened after its dry run (action, scope: picked or all, native, imported, held_at_source counts)
  | "ui.partner_rewards_held_included" // the bulk confirmation's choice to include imported rewards the old platform still pays (action)
  | "ui.partner_rewards_searched" // a search started on a Rewards list (where: program or partner); never the search text
  | "ui.partner_rewards_filtered" // a Rewards list filtered (filter: status or source, value, where)
  | "ui.partner_conversions_searched" // a search started on a Conversions list (where: program or partner); never the search text
  | "ui.partner_conversions_filtered" // a Conversions list filtered (filter: type with value, or partner with from: code or link)
  | "ui.journey_step_picker_opened" // the journey builder's step picker opened (from: start, link, rule)
  | "ui.journey_step_added" // a step put into a journey being built, before it is saved (kind, group, at: start or the link it went on)
  | "ui.journey_step_removed" // a step taken out of a journey being built (kind, also_removed: steps only it led to)
  | "ui.journey_step_linked" // a step's link pointed at another step or the end in the side panel (kind, output, to: step or end)
  | "ui.journey_step_opened" // a step's side panel opened in the journey builder (kind, editable)
  | "ui.journey_trigger_opened" // the trigger card's side panel opened (editable)
  | "ui.journey_change_undone" // an edit in the journey builder undone or redone, by button or keyboard (redo)
  | "ui.journey_version_viewed" // an older journey version shown read-only (version, current)
  | "ui.journey_test_opened" // test mode opened in the journey builder (version: live or draft, status)
  | "ui.journey_action_preset_picked" // journey builder: a ready-made call picked for a "Do an Outflare action" step (preset: its id, or custom)
  | "ui.signals_filter_changed" // Needs attention switched between mine, all open, late, no owner, snoozed and closed (filter)
  | "ui.signal_types_opened" // the signal types editor opened from Needs attention
  | "ui.signal_flag_opened" // "Flag for attention" opened on a person or company
  | "ui.playbook_set_up" // Playbooks: a playbook's setup kit applied from its page without starting a run (playbook_id, changes, holdout_percent)
  | "ui.signal_credit_opened" // Needs attention: the credit view opened
  | "ui.signal_credit_period_changed" // Credit: the period changed (days)
  | "ui.revenue_forecast_opened" // Revenue: the forecast opened
  | "ui.revenue_forecast_quarter_changed" // Forecast: another quarter picked (ahead: 0 for this quarter)
  | "ui.revenue_forecast_target_saved" // Forecast: a quarter's target saved or removed (has_target)
  | "ui.revenue_forecast_weights_saved" // Forecast: the weights saved (stages)
  | "ui.team_org_chart_opened" // Settings, Team: the org chart opened (people)
  | "ui.sales_team_opened" // Revenue: the sales team opened
  | "ui.sales_team_quarter_changed" // Sales team: another quarter picked (ahead)
  | "ui.sales_team_quotas_saved" // Sales team: quotas saved (people)
  | "ui.commissions_opened" // Revenue: commissions opened
  | "ui.commission_month_changed" // Commissions: another month picked (back)
  | "ui.commission_statement_downloaded" // Commissions: the month's lines downloaded as a CSV file (lines)
  | "ui.commission_statement_opened" // Commissions: a person's lines shown (lines)
  | "ui.commission_statement_changed" // Commissions: a month approved, marked paid or reopened (status)
  | "ui.commission_plan_saved" // Commissions: a plan saved (rules, status, is_new)
  | "ui.commission_starter_picked" // Commissions: a new plan started from an example (rules)
  | "ui.own_playbook_builder_opened" // Playbooks: "New playbook" pressed
  | "ui.own_playbook_opened" // Playbooks: one of the brand's own playbooks opened (parts)
  | "ui.own_playbook_saved" // Playbook builder: a playbook made or changed (parts, is_new)
  | "ui.own_playbook_removed" // A brand's own playbook removed (parts)
  | "ui.account_group_opened" // Account page: another company in the group opened from the Company group card (depth)
  | "ui.conversation_signals_opened" // Needs attention: the conversations setting opened
  | "ui.conversation_signals_saved" // Conversations: reading switched on or off, or a channel changed (enabled, channels)
  | "ui.routing_saved" // Routing: the brand's routing rules saved (rules)
  | "ui.routing_people_saved" // Routing: teammates' hours, capacity and away saved (people)
  | "ui.revenue_period_changed" // Revenue: the period switched (days)
  | "ui.score_starter_picked" // Scores: a starter (health, lead) opened in the editor (starter)
  | "ui.signal_assigned" // Needs attention: a signal given to a teammate from the Assign menu
  | "ui.record_owner_assigned" // A record's page or the CRM list: an owner picked (record_type; to: me, teammate, nobody)
  | "ui.record_routed" // A company's or person's page: Route pressed for a record with no owner (outcome)
  | "ui.automation_map_node_picked" // a node picked on the automation map, lighting up what it sets off and what sets it off (kind)
  | "ui.automation_map_flag_opened" // a flag on the automation map shown on the map (flag: loop, overlap, unheard or never_fires)
  | "ui.automation_map_filtered" // the automation map switched between everything and only what's live (show), or narrowed to what touches a sequence, list or tag (touches: its kind)
  | "ui.automation_map_person_picked" // a person picked on the automation map, to light up what they reached
  | "ui.journey_test_path_switched" // a test sent a branch the other way (to: yes or no)
  | "ui.kit_export_opened" // the export panel opened on a journey (status)
  | "ui.kit_downloaded" // an exported kit file downloaded (journeys, rules, inputs)
  | "ui.kit_file_chosen" // a kit file picked from disk to apply on the Playbooks page
  | "ui.rule_trigger_picked" // the rule card's sentence builder switched to another kind of trigger (kind)
  | "ui.rule_followup_picked" // "When this fires, do…" picked a first step for a new journey (kind)
  | "ui.knowledge_formatted" // formatting used in the knowledge editor (command: h2, bold, ul, table, ...; how: toolbar, slash, key)
  | "ui.knowledge_pasted" // formatted text pasted into the knowledge editor and kept as Markdown
  | "ui.knowledge_audience_picked" // the Ask box switched who the answer is for (audience: team, customers)
  | "ui.help_centre_template_picked" // a template applied in the help centre's Configure and style (template: classic, minimal, bold)
  | "ui.help_centre_brand_imported" // Import your brand in the help centre editor (found_logo, found_color)
  | "ui.support_chat_made" // a website chat made in Support settings (require_email, show_articles)
  | "ui.support_chat_saved" // a website chat's settings saved (require_email, show_articles)
  | "ui.support_sla_saved" // response times saved or turned off in Support settings (enabled, business_hours)
  | "ui.support_assistant_tried" // a made-up question asked in Support settings (channel, action)
  | "ui.support_assistant_reply_stopped" // a held Auto email reply stopped in the conversation
  | "ui.knowledge_reply_learning_started" // Learn from past replies started in Knowledge → Review (days)
  | "ui.support_assistant_draft_used" // the assistant's draft put in the composer (action: answer, clarify)
  | "ui.knowledge_website_import_started" // Import website started in Knowledge (max_pages, publish)
  | "ui.support_article_linked" // a help centre article's link put in a support reply (from: suggested, search, suggestion)
  | "ui.support_address_made" // a support forwarding address made from Support settings (brand: whether it is a brand's)
  | "ui.support_address_renamed" // a support forwarding address renamed (brand)
  | "ui.support_mailbox_picked" // a mailbox chosen for support from the Support settings (first: whether it was the first)
  | "ui.knowledge_suggestion_approved" // a suggested answer approved in Knowledge, Review (source, edited)
  | "ui.knowledge_conflict_settled" // a conflict settled by keeping one item (items)
  | "ui.knowledge_started" // the knowledge editor opened for something new (kind: article, snippet; template: none or a starter's id)
  | "ui.playbook_opened" // a playbook opened on the Playbooks page (playbook_id, ready)
  | "ui.agents_view_opened" // a view of the Agents page opened (view: waiting, runs, all, grants)
  | "ui.team_activity_opened" // a view of Settings, Team activity opened (view: waiting, changes, runs, logs)
  | "ui.action_decided" // a person approved or rejected agents' held changes (decision: approve or reject; how: single, batch or remember; count; from: run when decided on a run's page)
  | "ui.agent_run_started" // a playbook started as an agent run from the Playbooks page (playbook_id, mode, has_budget)
  | "ui.agent_run_acted" // a person paused, resumed, stopped or wrote to an agent run on its page (action: pause, resume, stop or write; mode)
  | "ui.action_grant_created" // a grant set on the Agents page (mode, has_daily_limit, template)
  | "ui.action_grant_revoked" // a grant revoked on the Agents page
  | "ui.ad_removal_permission_set" // an agent's permission to remove ad campaigns, ads or keywords set (permission, mode: off, hold or run, conditions: how many)
  | "ui.playbook_prompt_copied" // a playbook's prompt copied (playbook_id)
  | "ui.dialog_answered" // a confirm or prompt dialog: the choice, or cancelled
  | "ui.docs_opened" // a link to a guide on outflare.ai (tab, from)
  | "ui.nav_pinned" // a page, or a thing inside one (kind: item), pinned to the menu (tab, from)
  | "ui.nav_unpinned" // a page or thing taken off the menu's pins (tab, from, kind)
  | "ui.nav_recent_opened" // something opened from the menu's Recents (tab)
  | "ui.nav_recents_cleared" // the menu's Recents cleared (count)
  | "ui.nav_module_toggled" // a sidebar module opened or closed by hand (module, open)
  | "ui.nav_section_toggled" // a sidebar heading folded or unfolded (section, folded)
  | "ui.nav_collapsed" // every open sidebar module closed at once (modules: how many were open)
  | "ui.nav_version_switched" // the menu switched between the single sidebar and the rail of areas (version: v1 or v2, from)
  | "ui.nav_area_opened" // an area picked on the rail of the new menu (area)
  | "ui.brand_switched" // the brand switcher, or Back to a page on another brand: all brands or one (scope, brand_id, from: switcher, link)
  | "ui.onboarding_opened" // the Get started page: how it opened (auto, overview, palette, link)
  | "ui.onboarding_step_viewed" // a Get started step shown (step, done)
  | "ui.onboarding_left" // Get started closed with "Skip setup" (step, done)
  | "ui.setup_opened" // a brand's setup checklist shown (done, total)
  | "ui.setup_step_opened" // a setup step expanded (step, status)
  | "ui.tracking_opened" // Settings → Website tracking shown (brands, receiving: whether the picked brand's script has sent a visit; state, with one brand: none, waiting, live or quiet)
  | "ui.tracking_verified" // the guided install saw the brand's first visit while the page was open (brand_id)
  | "ui.site_key_duplicates_shown" // Website tracking listed spare scripts for a brand (extra: how many)
  | "ui.site_key_second_asked" // Add a site when the brand already has a script, and what they chose (choice: edit, second, cancel)
  | "ui.consent_banner_opened" // a site's consent banner settings opened on Website tracking (site_key_id)
  | "ui.site_cookies_opened" // a site's cookies and scripts opened on Website tracking (site_key_id)
  | "ui.site_own_domain_opened" // the "serve from your own domain" steps opened on Website tracking (site_key_id, status)
  | "ui.site_own_domain_host_picked" // a host picked in those steps (host: nextjs, vercel, netlify, cloudflare, nginx)
  | "ui.visitor_profile_opened" // a visitor's profile opened from Visitors, Everyone (known: true or false)
  | "ui.setup_method_used" // a way to do an install step picked (step, method: gtm, dns, developer, ai, manual; how: copied, email, claude, chatgpt)
  | "ui.indexability_filtered" // a site's indexability report filtered by verdict or issue (filter, value: the verdict or issue id, or all)
  | "ui.logs_filtered" // a filter on the Logs page changed (filter: which one)
  | "ui.brand_events_view_changed" // the view on a brand's Events page changed (view: dictionary for the event list, all for the latest events, api for the events API's inbox, destinations)
  | "ui.event_list_range_changed" // the range the event list counts over changed (days: 7, 30, 90, or 0 for all time)
  | "ui.brand_events_filtered" // a filter on a brand's Events page changed (filter: status, code or source; value: the status or code, or set/all for source)
  | "ui.brand_event_opened" // one received event opened on a brand's Events page (status, code)
  | "ui.brand_events_replayed" // rejected or held events replayed from a brand's Events page (how: selected, code, held_name or one; replayed)
  | "ui.logs_live_toggled" // Live on the Logs page turned on or off (on)
  | "ui.logs_exported" // logs downloaded (format: csv or ndjson)
  | "ui.logs_link_copied" // a link to a filtered Logs view copied (filters: how many)
  | "ui.agent_client_picked" // Connect your AI: which app's steps were opened (client)
  | "ui.agent_install_opened" // Connect your AI: a one-click install or the app's settings opened (client, where)
  | "ui.agent_connect_prompted" // a Connect your AI prompt clicked outside its page (from: sidebar, home)
  | "ui.agent_panel_toggled" // the brand agent's panel beside every page opened or closed (open)
  | "ui.agent_panel_resized" // the brand agent's panel beside every page was dragged wider or narrower (width, in pixels)
  | "ui.home_todo_opened" // a row on Home's Needs you list opened (code: the blocker code or setup row, tone)
  | "ui.email_guide_opened" // Lifecycle: a provider's setup guide opened (connector, from)
  | "ui.email_domain_dns_way_used" // Lifecycle: a way to add a sending domain's records used (way: cloudflare, ai, developer, manual; how: connected, added, approval, approved, approval_cancelled, copied, email, claude, chatgpt)
  | "ui.email_agent_prompt_used" // Lifecycle: a provider's agent prompt copied or opened in an AI app (connector, where)
  | "ui.email_template_previewed" // Lifecycle: the template preview switched between desktop, phone and text (view)
  | "ui.analytics_sites_opened" // the step choosing an analytics tool's sites and brands opened (from: connect, analytics, brand; connector)
  | "ui.analytics_sites_skipped" // that step closed with Later, nothing saved (from, connector)
  | "ui.ai_cost_notice_accepted" // connecting an AI model: the person accepted that their provider bills them (connector, via)
  | "ui.analytics_site_brand_created" // a new brand made from a site no brand fitted, in that step (connector)
  | "ui.connect_for_opened" // a page's Connect button for a service it needs, such as Geonode on Social (connector, from)
  | "ui.connect_for_returned" // back on that page after Connectors (connector, from, connected)
  | "ui.query_table_inserted" // Query Studio: a table, column or record type inserted into the SQL from the side panel (kind: table, column, type)
  | "ui.query_chart_shown" // Query Studio: results switched to a chart (type: bar, line, area or none; series; rows)
  | "ui.query_saved_opened" // Query Studio: a saved query opened from the list (shared, mine)
  | "ui.query_versions_opened" // Query Studio: a saved query's version history opened (versions: how many)
  | "ui.query_version_viewed" // Query Studio: an older version shown in the editor (version, current)
  | "ui.query_described" // Queries: a plain-English description answered (outcome: builder, sql, none, failed or queued; refines; seconds)
  | "ui.query_sql_edited" // Queries: the builder turned off to edit its SQL by hand (from_builder)
  | "ui.record_dialog_closed" // People: the add or edit dialog closed without saving, after typing (mode: add, edit; type; changed: fields changed)
  | "ui.cloud_feature_shown" // an Outflare Cloud feature's page on a server that doesn't run it (capability)
  | "ui.cloud_feature_clicked" // its link to Outflare Cloud (capability)
  | "ui.feedback_opened" // the Feedback box opened (from: top_bar, event)
  | "ui.community_link_opened" // a link to Outflare on GitHub, X, its changelog or support (target: issue_draft, discussion_draft, x_post, builder_follow, good_first_issue, codespace, discussions, support_email, changelog; from: feedback, feedback_step2, feedback_step3, whats_new, palette; kind for drafts: bug, idea, question)
  | "ui.whats_new_opened"; // What's new opened (unseen: releases new since they last looked)

type Props = Record<string, string | number | boolean | undefined>;

// A named product event. Properties are ids, codes, counts and fixed
// labels only: never names, emails, domains, typed text or error text
// that could quote them. The page is added for you.
export function track(event: UiEvent, props?: Props) {
  record(event, { page: currentPage, surface: surface(), ...props });
}

// Script errors and promise rejections nobody caught. The message is a
// code bug's, not customer data, but it is cut short all the same.
export function trackCrash(error: unknown, where: string) {
  const e = error instanceof Error ? error : new Error(String(error));
  if (!ownScript(error instanceof Error ? error.stack : undefined, location.origin)) return;
  // The same crash again on this page load says nothing new.
  const key = `${where}:${e.name}:${e.message}`;
  if (crashesSeen.has(key)) return;
  crashesSeen.add(key);
  track("ui.crashed", { outcome: "error", error_code: "crashed", where, name: e.name, message: e.message.slice(0, 200) });
}

const crashesSeen = new Set<string>();

// web/stale-build.js reloads the page once and leaves this note for the reloaded page to report.
export function trackStaleBuildReload() {
  try {
    const key = "outflare:stale-build-reload";
    const last = JSON.parse(sessionStorage.getItem(key) || "null") as { at: number; reason: string; tracked?: boolean } | null;
    if (!last || last.tracked) return;
    sessionStorage.setItem(key, JSON.stringify({ ...last, tracked: true }));
    track("ui.stale_build_reloaded", { reason: last.reason });
  } catch {}
}

if (typeof window !== "undefined") {
  window.addEventListener("error", (e) => trackCrash(e.error ?? e.message, "window"));
  window.addEventListener("unhandledrejection", (e) => trackCrash(e.reason, "promise"));
  window.addEventListener("pagehide", flush);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") flush();
  });
}

// On sign-out, so the next person on this browser starts fresh.
export function resetAnalytics() {
  queue = []; // the session that would have sent them is over
  signedIn = false;
  workspaceID = undefined;
  load().then((ph) => ph?.reset());
}
