// The toolbox: how an app reaches every Outflare tool while seeing only a
// few. Outflare has well over a thousand tools, and apps cap or choke on long
// lists (ChatGPT keeps the first 200 and drops the rest). So by default the
// server lists a small core set plus find_tools, which searches every tool,
// and call_read_tool and call_tool, which run any of them. ?tools=all on
// /mcp (or OUTFLARE_MCP_TOOLS=all) lists every tool instead, for apps that
// cope; ?tools=core,seo,analytics adds whole areas (GROUPS) to the core set.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";

/** "core", "all", or "core" and groups: "core,analytics,seo". */
export type ToolMode = string;

/**
 * toolMode reads a ?tools= value or OUTFLARE_MCP_TOOLS: "all" lists every
 * tool; a comma-separated list of GROUPS adds those areas to the core set;
 * anything else is the core set. Unknown group names are left out.
 */
export function toolMode(v: string | null | undefined): ToolMode {
  const asked = (v ?? "").toLowerCase().split(/[\s,]+/).filter(Boolean);
  if (asked.includes("all")) return "all";
  const groups = [...new Set(asked.filter((g) => Object.hasOwn(GROUPS, g)))].sort();
  return ["core", ...groups].join(",");
}

/** listed says whether a tool is listed to the app in this mode (every tool stays reachable through find_tools). */
export function listed(mode: ToolMode): (name: string) => boolean {
  if (mode === "all") return () => true;
  const groups = mode.split(",").filter((g) => Object.hasOwn(GROUPS, g)).map((g) => GROUPS[g]);
  return (name) => CORE.has(name) || groups.some((g) => inGroup(name, g));
}

// A group entry is a tool's whole name, or what it acts on: the name
// without its verb, or the start of it. "dashboard" takes get_dashboard and
// add_dashboard_tile, not create_insight_alert. So a new tool joins its
// area's group by its name alone.
function inGroup(name: string, entries: readonly string[]): boolean {
  const object = name.slice(name.indexOf("_") + 1);
  return entries.some((e) => name === e || object === e || object.startsWith(e + "_"));
}

/** groupTools is every tool name in the box that a group lists, for tests and docs. */
export function groupTools(group: string, names: Iterable<string>): string[] {
  const entries = GROUPS[group] ?? [];
  return [...names].filter((n) => inGroup(n, entries));
}

// The core set: what people ask for most, listed in every app. Keep it
// under MAX_CORE (a test checks), so it stays far below any app's cap as
// Outflare grows. Read-only tokens see the read tools among these.
export const CORE = new Set([
  // Start here.
  "get_context", "get_status", "update_context", "remember", "list_memories", "find_knowledge",
  "list_brands", "get_overview", "search_workspace", "describe_brand_data", "run_query",
  // People, companies, leads and lists.
  "list_records", "get_record", "lookup_record", "update_record", "add_partnership",
  "list_leads", "get_lead", "create_lead", "import_leads_csv", "list_lead_lists", "add_leads_to_list", "check_recipients",
  // Cold email.
  "list_sequences", "get_sequence", "create_sequence", "update_sequence", "preview_sequence", "enroll_leads",
  "list_inbox", "get_inbox_message", "reply_to_inbox_message",
  "list_mailboxes", "list_domains", "get_infrastructure",
  // Lifecycle email.
  "list_email_templates", "create_email_template", "send_email", "list_emails",
  "list_email_broadcasts", "create_email_broadcast", "send_email_broadcast", "list_journeys",
  // Results.
  "get_analytics_summary", "get_outcomes_report", "get_seo_overview",
  "list_dashboards", "get_dashboard", "get_insight", "run_trend", "run_funnel",
  // Connectors and agents.
  "list_connectors", "request_connection", "list_integrations",
  "list_playbooks", "run_playbook", "list_actions", "get_action",
  // The way to everything else.
  "find_tools", "call_read_tool", "call_tool",
]);
export const MAX_CORE = 64;

// Groups of tools an app can list on top of the core set, by area:
// ?tools=core,analytics,seo. Each stays well under any app's cap together
// with the core set (a test checks), and every tool stays reachable through
// find_tools either way. Names are part of the connect URL: add freely,
// never rename one.
export const GROUP_INFO: Record<string, string> = {
  analytics: "dashboards, insights (trends, funnels, retention, paths), saved queries and site analytics",
  events: "events and their plan, destinations, website visitors, recordings, heatmaps, errors and the consent banner",
  experiments: "A/B tests and feature flags",
  seo: "SEO sites, crawls, fixes, rankings, Search Console, page targets and SEO experiments",
  ai_visibility: "how AI answers mention the brand: prompts, runs, samples and pages",
  links: "backlinks, link prospects, link building and link orders",
  competitors: "competitors, their pricing and search gaps",
  ads: "ad accounts, campaigns, keywords, conversions, audiences and ad findings",
  creative: "the brand's files (logos, product shots, voices), generated images and videos, creative skills and creatives",
  tracking: "attribution, tracking links, offers, landers, flows, traffic sources and postbacks",
  email: "lifecycle email: templates, broadcasts, topics, email domains and providers, journeys and kits",
  outreach: "cold email: sequences, the inbox, mailboxes, domains, senders, allocation, warm-up and suppressions",
  leads: "leads, lead lists, smart lists, ideal customer profiles and email finding and verification",
  crm: "customer records, fields, imports, merges, bulk edits, deals and meetings",
  revenue: "revenue, sales team, commissions, metrics, scores, routing, signals, rules, products and spend",
  support: "knowledge, help centres, the support inbox and assistant, chat widgets and review requests",
  social: "social accounts and posts, Reddit and reviews",
  pr: "digital PR: stories, pitches, journalists and news searches",
  coverage: "coverage: publishers, placements, agreements, invoices and reports",
  partners: "partners and affiliates: programs, rewards, payouts and portals",
};
export const GROUPS: Record<string, readonly string[]> = {
  analytics: ["insight", "insights", "insights_catalog", "dashboard", "dashboards", "dashboard_tile", "funnel", "funnel_people", "funnel_correlation", "trend", "retention", "stickiness", "lifecycle", "paths", "annotation", "annotations", "analytics", "analytics_overview", "analytics_history", "analytics_tools", "analytics_changes", "analytics_catalog", "analytics_summary", "analytics_report", "analytics_site", "analytics_sites", "analytics_site_brand", "analytics_site_brands", "site_analytics", "query", "query_tables", "brand_data", "saved_query", "saved_queries", "query_endpoint", "query_endpoints", "outcomes_report", "page_speed", "site_live"],
  events: ["event", "events", "event_name", "event_definition", "event_usage", "event_counts", "event_plan_problems", "customer_events", "brand_event", "brand_events", "brand_events_summary", "destination", "destinations", "destination_sends", "heatmap", "heatmaps", "heatmap_pages", "recording", "recordings", "replay_settings", "error_issue", "error_issues", "source_map", "source_maps", "click_action", "click_actions", "seen_clicks", "internal_people", "website_visitors", "visitor_browser", "visitor_browsers", "visitor_browser_events", "visitor_settings", "visitor_link", "visitor_company", "site_key", "site_keys", "site_proxy_secret", "site_identify_secret", "consent_banner", "site_cookie", "site_cookies", "cookie_policy", "tracking_taxonomy"],
  experiments: ["ab_test", "ab_tests", "ab_test_results", "ab_test_breakdown", "ab_test_factorial", "ab_test_interactions", "ab_test_ai_run", "ab_test_versions", "ab_test_version", "ab_test_checklist", "ab_test_previews", "ab_test_learnings", "ab_test_learning", "flag", "flags", "flag_group", "flag_groups", "flag_group_holdout"],
  seo: ["seo", "seo_history", "seo_overview", "seo_report", "seo_settings", "seo_sites", "seo_site", "seo_site_dns", "seo_site_dns_approval", "seo_keyword_results", "crawl", "crawls", "crawl_pages", "crawl_page", "crawl_issues", "crawl_issue", "fix", "fixes", "fix_prompt", "fix_tickets", "fix_ticket", "fix_ticket_prompt", "fix_guidelines", "fix_guideline", "fix_autonomy", "rankings", "rank_history", "keywords", "tracked_keyword", "site_search", "site_search_history", "site_search_performance", "site_indexing", "site_indexability", "indexability_pages", "site_sitemaps", "site_cannibalisation", "site_internal_links", "site_regressions", "site_conversions", "organic_conversions", "page_targets", "page_target", "search_insights", "search_groups", "search_group", "search_migrations", "search_migration", "search_workspace", "search_report_run", "search_work", "search_work_fix", "search_report", "search_schedule", "experiments", "experiment", "experiment_pages", "deploy_options", "deploy_targets", "deploy_target", "deploys", "digest_settings", "digests", "digest"],
  ai_visibility: ["ai_visibility", "ai_visibility_questions", "ai_visibility_placements", "ai_visibility_page", "ai_visibility_pages", "ai_visibility_series", "ai_visibility_claims", "ai_visibility_claim", "ai_visibility_settings", "ai_answer_share", "ai_pages", "ai_cohorts", "ai_cohort", "ai_prompts", "ai_prompt", "ai_runs", "ai_run", "ai_samples", "ai_sample", "ai_facts", "ai_fact", "crawl_ai_access", "ai_bot_hits"],
  links: ["backlinks", "backlink", "backlink_events", "backlinks_summary", "disavow_file", "link_prospects", "link_prospect", "link_gap", "seo_link_gap_pages", "dead_pages", "top_linked_pages", "links", "link_reads", "link_read", "link_sequences", "link_sequence", "link_pitch", "link_pitches", "link_results", "link_building_settings", "link_assets", "link_asset", "link_order", "link_orders", "link_order_catalog", "link_order_catalog_table", "link_order_thread", "link_order_summary", "link_order_candidates", "link_order_equivalents", "link_order_targets", "link_order_target_anchors", "link_order_donor_emails", "link_order_donor_to_sequence", "link_order_donors_in_ahrefs", "link_order_candidate_to_catalog", "link_roi"],
  competitors: ["competitor", "competitors", "competition", "competitor_profiles", "competitor_profile", "competitor_pricing", "competitor_events", "competitor_search", "seo_competitors", "seo_competitor", "seo_competitor_gaps", "pricing_page", "pricing", "pricing_history"],
  ads: ["ad", "ads", "ads_summary", "ads_performance", "ads_setup", "all_ads", "synced_audiences", "synced_audience"],
  creative: ["brand_assets", "brand_asset", "brand_generations", "brand_generation", "generation_providers", "generation_voices", "creative_skills", "creative_skill", "creative_skill_versions", "creative_skill_draft", "creatives", "creative"],
  tracking: ["attribution", "tracking_campaigns", "tracking_campaign", "tracking_links", "tracking_link", "tracking_link_destinations", "tracking_link_paths", "tracking_link_flow", "tracking_link_weight_suggestions", "tracking_link_weights", "postback_sources", "postback_source", "tracker", "network_targets", "network_costs", "ip_lookups", "click_filters", "click_filter", "affiliate_network_templates", "offers", "offer", "landers", "lander", "lander_clicks", "flows", "flow", "flow_destinations", "flow_paths", "flow_weight_suggestions", "flow_weights", "traffic_source", "traffic_sources", "traffic_source_postbacks", "traffic_source_templates", "drilldown_report", "report_views", "report_view", "conversion_definitions", "conversion_definition", "conversion_presets", "conversion_preset"],
  email: ["email", "emails", "email_overview", "email_provider_guides", "email_providers", "email_provider", "email_events", "email_domains", "email_domain", "email_domain_dns", "email_domain_dns_approval", "email_templates", "email_template", "email_recipient", "email_topics", "email_topic", "email_broadcasts", "email_broadcast", "email_broadcast_recipients", "email_broadcast_lift", "people", "sub_sources", "sub_source", "journey_step_kinds", "journeys", "journey", "journey_versions", "journey_draft", "journey_lift", "journey_runs", "journey_run", "journey_step_secrets", "automation_map", "kit", "kit_file", "to_journey", "list_emails"],
  outreach: ["sequences", "sequence", "sequence_steps", "sequence_mailboxes", "sequence_lift", "enrollments", "enrollment", "messages", "inbox", "inbox_message", "inbox_read", "inbox_note", "inbox_reply", "inbox_tags", "inbox_tag", "inbox_views", "inbox_view", "on_inbox_message", "to_inbox_message", "saved_replies", "saved_reply", "reply_rules", "reply_referral", "mailboxes", "mailbox", "mailbox_health", "mailbox_activity", "mailbox_allocation", "mailbox_sender", "mailbox_health_settings", "domains", "domain", "domain_dns", "domain_dns_approval", "domains_bought", "tracking_domains", "tracking_domain", "tracking_domain_dns", "tracking_domain_dns_approval", "tracking_domain_fallback", "workspace_domains", "sending_servers", "sending_server", "senders", "sender", "platforms", "platform", "platform_plan", "allocation_attention", "allocation_changes", "allocation_change", "allocation_suggestion", "allocation_declines", "allocation_decline", "orphaned_follow_ups", "warmup", "infrastructure", "infra_plan", "infra_plans", "suppressions", "suppression_syncs", "suppression_sync", "guard_list_syncs", "guard_list_sync", "contact_rules", "email_guardrails", "recipients", "now", "inbound"],
  leads: ["leads", "lead", "lead_lists", "lead_list", "lead_list_members", "lead_from_list", "leads_to_list", "leads_csv", "lead_sources", "lead_search", "smart_lists", "smart_list", "smart_list_members", "smart_list_changes", "to_static_list", "from_static_list", "list_runs", "list_run", "ideal_customer_profiles", "ideal_customer_profile", "data_providers", "email_pattern", "addresses", "verifications", "verification_settings", "verification_summary", "find_emails", "verify_emails", "discovery_companies", "discovery_lanes", "discovery_lane_companies", "discovery_board", "discovery_lane", "discovery_note", "discovery_usage", "lead_finder_run"],
  crm: ["records", "record", "record_history", "record_timeline", "record_field_values", "record_field", "record_identities", "record_identity", "record_links", "record_link", "record_consents", "record_consent", "record_duplicates", "record_duplicate", "record_type", "record_summary", "record_group", "record_relationship", "transactions", "transaction", "billing_subscriptions", "billing_subscription", "crm_summary", "crm_activities", "crm_activity", "crm_views", "crm_view", "crm_imports", "crm_import", "privacy_exports", "privacy_export", "recently_deleted", "deletion", "merges", "duplicate_payments", "batches", "batch", "batch_undo", "bulk_edits", "bulk_edit", "field", "field_value", "option_set", "catalog", "developer_setup", "refund", "meetings", "meeting", "deals", "deal"],
  revenue: ["revenue", "revenue_forecast", "sales_team", "sales_quotas", "sales_reporting", "sales_reporting_leads", "sales_team_reporting", "sales_discovery_lanes", "sales_lead", "commission_plans", "commission_plan", "commissions", "commission_statement", "metrics", "metric", "record_metrics", "scores", "score", "record_scores", "score_track_record", "routing", "routing_people", "lifecycle_stages", "lifecycle_moves", "record_lifecycle", "signals", "signal", "signal_types", "signal_type", "signal_credit", "conversation_signals", "rules", "rule", "rule_firings", "products", "product", "product_names", "product_name", "spend", "spend_entry", "spend_rates", "spend_rate", "recurring_spend"],
  support: ["knowledge", "knowledge_folders", "knowledge_folder", "knowledge_versions", "knowledge_version", "knowledge_gaps", "knowledge_gap", "knowledge_suggestions", "knowledge_stats", "knowledge_from_replies", "website_to_knowledge", "help_centres", "help_centre", "help_centre_stats", "support_conversations", "support_conversation", "support_slas", "support_sla", "support_articles", "support_assistant", "support_assistant_reply", "support_stats", "support_imports", "support_import", "support_reply", "support_note", "chat_widgets", "chat_widget", "handoff_topics", "handoff_topic", "review_request_settings", "review_request_campaigns", "review_request_campaign", "review_requests", "off_support_conversation"],
  social: ["social_overview", "social_watches", "social_watch", "social_posts", "social_post", "social_reply", "social_replies", "social_sign_in_link", "social_accounts", "social_account", "social_account_days", "social_publications", "social_publication", "social_series", "series_post", "own_social_posts", "social_growth", "mentions", "mention", "mentions_overview", "mention_settings", "reddit_guide", "reddit_budget", "reddit_settings", "reddit_experiments", "reddit_experiment", "reddit_actions", "reddit_action", "reddit_report", "reddit_subreddit", "reddit_threads", "reddit_drafts", "reddit_project", "reviews_summary", "reviews", "review", "review_answer", "review_answered", "review_pages", "review_page", "review_page_days", "review_guide"],
  pr: ["stories", "story", "story_fact", "story_quote", "story_pitch", "story_pitches", "story_pitch_sequence", "story_results", "quote_approval", "quote_approval_requests", "journalists", "journalist", "journalists_csv", "pr_experts", "pr_expert", "news_searches", "news_search", "news_articles"],
  coverage: ["coverage"],
  partners: ["partner", "partners", "partner_proposals", "partner_proposal"],
};

// Tools marked read-only that still send a POST: each one previews,
// dry-runs, queries or computes, and changes nothing. A read-only tool may
// only send GET otherwise; the server refuses anything else it tries
// (createServer), so a wrong readOnlyHint can never let call_read_tool, or
// an app's "let reads through" setting, make a change. Add a tool here only
// after checking its endpoint writes nothing, sends nothing and spends
// nothing; a test fails on stale entries.
export const READ_POSTS = new Set([
  "ask_knowledge", "try_support_assistant", "evaluate_flags", "plan_ab_test",
  "run_funnel", "run_trend", "run_retention", "run_stickiness", "run_lifecycle", "list_funnel_people", "run_funnel_correlation", "check_funnel_data", "run_paths",
  "run_query", "run_query_endpoint", "describe_query_endpoint", "run_saved_query", "export_query_csv",
  "preflight_reddit_drafts", "read_link_order_catalog_table",
  "preview_bulk_edit", "preview_smart_list", "check_recipients", "find_emails", "classify_email",
  "preview_sequence", "preview_infra_plan", "preview_email_template", "preview_ad_campaign_locations",
  "export_kit", "preview_kit_file", "test_journey", "simulate_rule",
]);

// Every area Outflare covers, so an app knows to search before it says
// Outflare can't do something. find_tools' description and get_context
// carry it.
export const AREAS = [
  "brands, context, memories and the brand's files (logos, fonts, product shots)",
  "leads, lists and email finding",
  "customer records, CRM, events and consent",
  "cold email sequences, inbox and replies",
  "mailboxes, domains, warm-up and deliverability",
  "lifecycle email: templates, send_email, broadcasts, topics, journeys and email providers",
  "analytics, insights, funnels, dashboards and A/B tests",
  "SEO, crawls, rankings, backlinks, competitors and AI visibility",
  "ads, attribution, tracking links and conversions",
  "social, reviews, Reddit and digital PR",
  "partners, affiliates and payouts",
  "support, knowledge and help centres",
  "website visitors, consent banner and feature flags",
  "connectors, integrations and AI settings",
  "agents, playbooks, automations, rules and approvals",
  "spend, deals, meetings, webhooks and logs",
];

export const TOOLS_HINT =
  "Your app lists Outflare's core tools. Outflare has many more, covering: " + AREAS.join("; ") + ". " +
  "Before saying Outflare can't do something, call find_tools with what you want to do, then run the tool it finds: call_read_tool for a tool that only reads (read_only true), call_tool for one that changes things.";

type Entry = { name: string; config: ToolConfig; cb: (...a: unknown[]) => unknown; words: Words };
type ToolConfig = { title?: string; description?: string; inputSchema?: unknown; annotations?: { readOnlyHint?: boolean } };
type Words = { verb: string; name: Set<string>; title: Set<string>; text: Set<string> };

const STOP = new Set("a an the to for of and or in on at my our your with from by it its is are be me i we you how can do does please want need would like some any all this that using use via into".split(" "));
// Words people use for what Outflare calls something else.
const SYNONYMS: Record<string, string[]> = {
  make: ["create"], new: ["create"], add: ["create", "add"], build: ["create"], write: ["create", "draft", "write"], draft: ["draft", "create"],
  edit: ["update"], change: ["update"], modify: ["update"], rename: ["update"], set: ["set", "update"],
  remove: ["delete", "remove"], erase: ["delete"],
  show: ["get", "list"], see: ["get", "list"], view: ["get", "list"], read: ["get", "list", "read"], find: ["find", "list", "search"], fetch: ["get"],
  newsletter: ["broadcast"], blast: ["broadcast"], announcement: ["broadcast"],
  campaign: ["sequence", "broadcast", "campaign"], cadence: ["sequence"], outreach: ["sequence"],
  mail: ["email"], emails: ["email"], message: ["email", "inbox", "message"], transactional: ["email", "send"],
  lifecycle: ["email", "journey", "lifecycle"], drip: ["journey"], flow: ["journey", "flow"], workflow: ["journey", "playbook"], automation: ["journey", "rule", "automation"],
  contact: ["record", "lead"], person: ["record", "lead"], people: ["record", "lead"], customer: ["record", "customer"], company: ["record"], account: ["record", "account"], prospect: ["lead"],
  reply: ["reply", "inbox"], replies: ["reply", "inbox"],
  sender: ["mailbox", "sender"], inboxes: ["mailbox"], warmup: ["warmup"], deliverability: ["domain", "mailbox", "health"],
  stats: ["report", "overview", "summary"], metrics: ["report", "metric", "analytic"], traffic: ["analytic"], ranking: ["rank"],
  unsubscribe: ["suppression", "consent", "topic"], subscription: ["topic", "consent"],
  provider: ["provider"], ses: ["provider"], postmark: ["provider"], resend: ["provider"],
};

function stem(w: string): string {
  if (w.length > 4 && w.endsWith("ies")) return w.slice(0, -3) + "y";
  if (w.length > 3 && w.endsWith("s") && !w.endsWith("ss")) return w.slice(0, -1);
  return w;
}

function words(s: string | undefined): string[] {
  return (s ?? "").toLowerCase().split(/[^a-z0-9]+/).filter((w) => w && !STOP.has(w)).map(stem);
}

// A tool's name is a verb and what it acts on (create_email_template). The
// verb counts for less, so "list" in "send a newsletter to a list" doesn't
// pull in every list_ tool.
function index(name: string, config: ToolConfig): Words {
  const [verb, ...rest] = name.split("_");
  return { verb: stem(verb), name: new Set(words(rest.join(" "))), title: new Set(words(config.title)), text: new Set(words(config.description)) };
}

// score ranks a tool for the query: a word in the tool's name counts most,
// then its title, then its description. A word's synonyms count a little
// less than the word itself.
function score(e: Entry, query: string[], exact: string): number {
  let total = 0;
  let inName = 0;
  for (const q of query) {
    const alts = [q, ...(SYNONYMS[q] ?? []).map(stem)];
    let best = 0;
    let named = false;
    for (const [i, w] of alts.entries()) {
      const k = i === 0 ? 1 : 0.8;
      const inN = e.words.name.has(w) || e.words.verb === w;
      const s = e.words.name.has(w) ? 6 : e.words.verb === w ? 4 : e.words.title.has(w) ? 3 : e.words.text.has(w) ? 1 : 0;
      if (s * k > best) best = s * k;
      if (inN) named = true;
    }
    if (named) inName++;
    total += best;
  }
  // A tool whose name holds every word asked for is almost surely the one.
  if (query.length > 1 && inName === query.length) total += 4;
  // Searching a tool's own name finds it first.
  if (exact === e.name) total += 10;
  return total;
}

export class Toolbox {
  readonly tools = new Map<string, Entry>();

  add(name: string, config: ToolConfig, cb: (...a: unknown[]) => unknown) {
    this.tools.set(name, { name, config, cb, words: index(name, config) });
  }

  search(query: string, limit: number): Entry[] {
    const q = words(query);
    if (!q.length) return [];
    const exact = query.trim().toLowerCase().replace(/[\s-]+/g, "_");
    return [...this.tools.values()]
      .map((e) => ({ e, s: score(e, q, exact) }))
      .filter((r) => r.s > 0)
      .sort((a, b) => b.s - a.s || a.e.name.length - b.e.name.length || a.e.name.localeCompare(b.e.name))
      .slice(0, limit)
      .map((r) => r.e);
  }

  describe(e: Entry) {
    return {
      name: e.name,
      title: e.config.title,
      description: e.config.description,
      read_only: e.config.annotations?.readOnlyHint === true,
      input_schema: jsonSchema(e.config.inputSchema),
    };
  }
}

function schemaOf(input: unknown): z.ZodType | undefined {
  if (input === undefined) return undefined;
  if (input && typeof (input as z.ZodType).safeParse === "function") return input as z.ZodType;
  return z.object(input as z.ZodRawShape);
}

function jsonSchema(input: unknown): unknown {
  const s = schemaOf(input);
  if (!s) return { type: "object", properties: {} };
  try {
    const j = z.toJSONSchema(s, { io: "input", unrepresentable: "any" }) as Record<string, unknown>;
    delete j.$schema;
    return j;
  } catch {
    return { type: "object" };
  }
}

function text(t: string, isError = false): CallToolResult {
  return { content: [{ type: "text", text: t }], ...(isError ? { isError: true } : {}) };
}

/** registerToolbox adds find_tools, call_read_tool and call_tool, which reach every tool in the box. */
export function registerToolbox(server: McpServer, box: Toolbox, readOnly: boolean) {
  server.registerTool(
    "find_tools",
    {
      title: "Find any Outflare tool",
      description:
        "Search all of Outflare's tools (over a thousand) by what you want to do, such as \"create an email template\", \"send a broadcast\" or \"check rankings\". " +
        "Your app lists only the core tools; this finds the rest. Outflare covers: " + AREAS.join("; ") + ". " +
        "Always search here before telling the person Outflare can't do something. Returns each match's name, description, input_schema and read_only; run a read_only one with call_read_tool and any other with call_tool.",
      inputSchema: {
        query: z.string().min(1).describe("What you want to do, in plain words."),
        limit: z.number().int().min(1).max(25).optional().describe("How many matches to return. Default 8."),
      },
      annotations: { readOnlyHint: true, openWorldHint: false },
    },
    ({ query, limit }: { query: string; limit?: number }) => {
      const found = box.search(query, limit ?? 8).map((e) => box.describe(e));
      const data = {
        tools: found,
        next_step: found.length
          ? "Run the tool that fits, passing its name and arguments that match its input_schema: call_read_tool when it is read_only, otherwise call_tool."
          : "Nothing matched. Try other words, such as the thing (template, broadcast, sequence, record) and the action (create, list, send).",
      };
      return { content: [{ type: "text", text: JSON.stringify(data) }], structuredContent: data };
    },
  );

  // run finds a tool in the box, checks its arguments and runs it. With
  // readsOnly it refuses tools that change things, so call_read_tool can
  // carry a read-only annotation: apps such as Claude then let reads through
  // without asking each time, while call_tool keeps asking for writes.
  const run = async (name: string, args: Record<string, unknown> | undefined, extra: unknown, readsOnly: boolean): Promise<CallToolResult> => {
    const e = box.tools.get(name.trim());
    if (!e || e.name === "call_tool" || e.name === "call_read_tool") {
      const near = box.search(name.replace(/_/g, " "), 5).map((t) => t.name).filter((n) => n !== "call_tool" && n !== "call_read_tool");
      const why = readOnly ? " This connection can only read, so tools that change things are left out." : "";
      return text(`No tool is called ${JSON.stringify(name)}.${why}` + (near.length ? ` Did you mean: ${near.join(", ")}? ` : " ") + "Search with find_tools.", true);
    }
    if (readsOnly && e.config.annotations?.readOnlyHint !== true) {
      return text(`${e.name} changes things, so call_read_tool can't run it. Run it with call_tool instead.`, true);
    }
    const schema = schemaOf(e.config.inputSchema);
    if (!schema) return (await e.cb(extra)) as CallToolResult;
    const parsed = schema.safeParse(args ?? {});
    if (!parsed.success) {
      const problems = parsed.error.issues.map((i) => `${i.path.join(".") || "arguments"}: ${i.message}`).join("; ");
      return text(`The arguments for ${e.name} don't fit: ${problems}\nIts input_schema: ${JSON.stringify(jsonSchema(e.config.inputSchema))}`, true);
    }
    return (await e.cb(parsed.data, extra)) as CallToolResult;
  };
  const input = {
    name: z.string().min(1).describe("The tool's name, from find_tools."),
    arguments: z.record(z.string(), z.unknown()).optional().describe("The tool's arguments, matching its input_schema."),
  };

  server.registerTool(
    "call_read_tool",
    {
      title: "Read with any Outflare tool",
      description:
        "Run any Outflare tool that only reads (read_only true in find_tools), including the ones your app doesn't list: dashboards, reports, rankings, lists. " +
        "It refuses tools that change things; run those with call_tool. The tool runs exactly as if it were called directly: same permissions and audit log.",
      inputSchema: input,
      // Some reads reach outside services (a Reddit rule check, a shared
      // Google Sheet), so it says so; it still changes nothing.
      annotations: { readOnlyHint: true, openWorldHint: true },
    },
    ({ name, arguments: args }: { name: string; arguments?: Record<string, unknown> }, extra: unknown) => run(name, args, extra, true),
  );

  server.registerTool(
    "call_tool",
    {
      title: "Run any Outflare tool",
      description:
        "Run any Outflare tool by name, including the ones your app doesn't list. Find the name and its input_schema with find_tools first. " +
        "For a tool that only reads, use call_read_tool instead. " +
        "The tool runs exactly as if it were called directly: same permissions, send policy, approvals and audit log.",
      inputSchema: input,
      annotations: readOnly ? { readOnlyHint: true, openWorldHint: false } : { readOnlyHint: false, destructiveHint: true, openWorldHint: true },
    },
    ({ name, arguments: args }: { name: string; arguments?: Record<string, unknown> }, extra: unknown) => run(name, args, extra, false),
  );
}
