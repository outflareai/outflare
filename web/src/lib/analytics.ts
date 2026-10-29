import type { PostHog } from "posthog-js";
import { api } from "../api";
import { anonymousPath } from "./paths";

// Product analytics, only when the server operator set OUTFLARE_POSTHOG_KEY
// (/v1/meta then has product_analytics). Self-hosted servers without it load
// nothing. Events go to the operator's own PostHog through /ingest/ on the
// same server.
//
// Customer data stays out: page text, inputs and element attributes are
// masked in autocapture and session replay, and URLs keep only the page
// (/:ws/sequences/:item), never workspace names, ids, domains, tokens or query strings.

let client: Promise<PostHog | null> | undefined;

function load(): Promise<PostHog | null> {
  client ??= api
    .GET("/v1/meta")
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
  load().then((ph) => {
    if (!ph || !user.id) return;
    ph.identify(user.id, { email: user.email, name: user.name });
    if (ws) ph.group("workspace", ws.id, { name: ws.name });
  });
}

let currentPage = "";

// One pageview per page (tab), sent from the signed-in shell and the sign-in screens.
export function page(name: string) {
  currentPage = name;
  load().then((ph) => ph?.capture("$pageview", { page: name }));
}

// Events only the browser sees. Everything that calls the API is recorded
// by the server (api.called plus the action, such as mailbox.connected), so
// track here only what never reaches it: errors shown, crashes, and actions
// without a request. Names are object.verb in the past tense, like the
// server's; add new ones to this list (AGENTS.md, "Events").
export type UiEvent =
  | "ui.error_shown" // an error message was shown to the person (error_code)
  | "ui.crashed" // a script error or a page that failed to render
  | "ui.dns_one_click_used" // a way to add DNS records outside Lifecycle (where: seo_site, tracking_domain, sending_domain, brand_setup, brand_dns; way: cloudflare, ai, developer, manual; how: sign_in, connected, added, approval, approved, approval_cancelled, copied, claude, chatgpt, email; groups for brand_dns)
  | "ui.request_failed" // a call never got an answer (offline, blocked)
  | "ui.command_run" // a command palette command
  | "ui.copied" // a copy button
  | "ui.partner_install_viewed" // a tab of a partner program's install steps opened (step: script or conversions; way: html, nextjs, gtm, curl, node, postback)
  | "ui.journey_step_picker_opened" // the journey builder's step picker opened (from: start, link, rule)
  | "ui.journey_step_added" // a step put into a journey being built, before it is saved (kind, group, at: start or the link it went on)
  | "ui.journey_step_removed" // a step taken out of a journey being built (kind, also_removed: steps only it led to)
  | "ui.journey_step_linked" // a step's link pointed at another step or the end in the side panel (kind, output, to: step or end)
  | "ui.journey_step_opened" // a step's side panel opened in the journey builder (kind, editable)
  | "ui.journey_trigger_opened" // the trigger card's side panel opened (editable)
  | "ui.journey_change_undone" // an edit in the journey builder undone or redone, by button or keyboard (redo)
  | "ui.journey_version_viewed" // an older journey version shown read-only (version, current)
  | "ui.journey_test_opened" // test mode opened in the journey builder (version: live or draft, status)
  | "ui.journey_test_path_switched" // a test sent a branch the other way (to: yes or no)
  | "ui.rule_trigger_picked" // the rule card's sentence builder switched to another kind of trigger (kind)
  | "ui.rule_followup_picked" // "When this fires, do…" picked a first step for a new journey (kind)
  | "ui.knowledge_formatted" // formatting used in the knowledge editor (command: h2, bold, ul, table, ...; how: toolbar, slash, key)
  | "ui.knowledge_pasted" // formatted text pasted into the knowledge editor and kept as Markdown
  | "ui.knowledge_started" // the knowledge editor opened for something new (kind: article, snippet; template: none or a starter's id)
  | "ui.playbook_opened" // a playbook opened on the Playbooks page (playbook_id, ready)
  | "ui.playbook_prompt_copied" // a playbook's prompt copied (playbook_id)
  | "ui.dialog_answered" // a confirm or prompt dialog: the choice, or cancelled
  | "ui.docs_opened" // a link to a guide on outflare.ai (tab, from)
  | "ui.nav_pinned" // a page pinned to the sidebar (tab, from)
  | "ui.nav_unpinned" // a page taken off the sidebar's pins (tab, from)
  | "ui.nav_module_toggled" // a sidebar module opened or closed by hand (module, open)
  | "ui.brand_switched" // the brand switcher, or Back to a page on another brand: all brands or one (scope, brand_id, from: switcher, link)
  | "ui.onboarding_opened" // the Get started page: how it opened (auto, overview, palette, link)
  | "ui.onboarding_step_viewed" // a Get started step shown (step, done)
  | "ui.onboarding_left" // Get started closed with "Skip setup" (step, done)
  | "ui.setup_opened" // a brand's setup checklist shown (done, total)
  | "ui.setup_step_opened" // a setup step expanded (step, status)
  | "ui.tracking_opened" // Analytics → Website tracking shown (brands, receiving: whether the picked brand's script has sent a visit)
  | "ui.tracking_verified" // the guided install saw the brand's first visit while the page was open (brand_id)
  | "ui.site_key_duplicates_shown" // Website tracking listed spare scripts for a brand (extra: how many)
  | "ui.site_key_second_asked" // Add a site when the brand already has a script, and what they chose (choice: edit, second, cancel)
  | "ui.consent_banner_opened" // a site's consent banner settings opened on Website tracking (site_key_id)
  | "ui.site_cookies_opened" // a site's cookies and scripts opened on Website tracking (site_key_id)
  | "ui.setup_method_used" // a way to do an install step picked (step, method: gtm, dns, developer, ai, manual; how: copied, email, claude, chatgpt)
  | "ui.indexability_filtered" // a site's indexability report filtered by verdict or issue (filter, value: the verdict or issue id, or all)
  | "ui.logs_filtered" // a filter on the Logs page changed (filter: which one)
  | "ui.logs_live_toggled" // Live on the Logs page turned on or off (on)
  | "ui.logs_exported" // logs downloaded (format: csv or ndjson)
  | "ui.logs_link_copied" // a link to a filtered Logs view copied (filters: how many)
  | "ui.agent_client_picked" // Connect your AI: which app's steps were opened (client)
  | "ui.agent_install_opened" // Connect your AI: a one-click install or the app's settings opened (client, where)
  | "ui.agent_connect_prompted" // a Connect your AI prompt clicked outside its page (from: sidebar, home)
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
  | "ui.record_dialog_closed" // People: the add or edit dialog closed without saving, after typing (mode: add, edit; type; changed: fields changed)
  | "ui.cloud_feature_shown" // an Outflare Cloud feature's page on a server that doesn't run it (capability)
  | "ui.cloud_feature_clicked"; // its link to Outflare Cloud (capability)

type Props = Record<string, string | number | boolean | undefined>;

// A named product event. Properties are ids, codes, counts and fixed
// labels only: never names, emails, domains, typed text or error text
// that could quote them. The page is added for you.
export function track(event: UiEvent, props?: Props) {
  load().then((ph) => ph?.capture(event, { page: currentPage, surface: "web", ...props }));
}

// Script errors and promise rejections nobody caught. The message is a
// code bug's, not customer data, but it is cut short all the same.
export function trackCrash(error: unknown, where: string) {
  const e = error instanceof Error ? error : new Error(String(error));
  track("ui.crashed", { outcome: "error", error_code: "crashed", where, name: e.name, message: e.message.slice(0, 200) });
}

if (typeof window !== "undefined") {
  window.addEventListener("error", (e) => trackCrash(e.error ?? e.message, "window"));
  window.addEventListener("unhandledrejection", (e) => trackCrash(e.reason, "promise"));
}

// On sign-out, so the next person on this browser starts fresh.
export function resetAnalytics() {
  load().then((ph) => ph?.reset());
}
