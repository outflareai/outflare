// The app's frame: sidebar navigation, the top bar with the page path, the
// command palette (⌘K) and the API panel, which shows the call behind the
// page you are on.

import { Fragment, useEffect, useState, useSyncExternalStore } from "react";
import type { Workspace } from "@outflare/sdk";
import { Icon, isIconName, type IconName } from "./icons";
import { CopyButton, Kbd, Logo } from "./ui";
import type { Tab } from "./App";
import { appTabs } from "./lib/paths";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";
import { setBrand, useBrand } from "@/lib/brand";
import { useAgentApps } from "@/lib/agentApps";
import {
  Sidebar as UISidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarRail,
  SidebarTrigger,
  useSidebar,
} from "@/components/ui/sidebar";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuShortcut, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList, CommandShortcut } from "@/components/ui/command";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Separator } from "@/components/ui/separator";

export const tabNames: Record<Tab, string> = {
  overview: "Home",
  "get-started": "Get started",
  setup: "Brand setup",
  ai: "AI apps",
  "ai-usage": "AI usage",
  tools: "All tools",
  reports: "Sending reports",
  analytics: "Website analytics",
  links: "Campaign links",
  partners: "Partners",
  channels: "Channels",
  tracking: "Website tracking",
  flags: "Feature flags",
  "ab-tests": "A/B tests",
  seo: "Rankings & backlinks",
  "seo-overview": "SEO overview",
  audits: "Site audits",
  fixes: "Fixes",
  experiments: "Experiments",
  keywords: "Keywords",
  "ai-visibility": "AI visibility",
  backlinks: "Backlinks",
  competitors: "Competitors",
  "link-building": "Link building",
  news: "News watch",
  reddit: "Reddit",
  social: "Accounts",
  reviews: "Reviews",
  ads: "Paid ads",
  visitors: "Visitors",
  inbox: "Inbox",
  missions: "Missions",
  playbooks: "Playbooks",
  outcomes: "Outcomes",
  projects: "Projects",
  sequences: "Sequences",
  people: "People",
  leads: "Leads",
  lists: "Lists",
  "lead-finder": "Lead finder",
  "email-finder": "Email finder",
  suppression: "Do not contact",
  infrastructure: "Infrastructure",
  mailboxes: "Mailboxes",
  domains: "Domains",
  warmup: "Warm-up",
  allocation: "Mailbox allocation",
  senders: "Teams & senders",
  servers: "Sending servers",
  lifecycle: "Lifecycle overview",
  emails: "Sent emails",
  templates: "Email templates",
  topics: "Topics",
  broadcasts: "Broadcasts",
  journeys: "Journeys",
  rules: "Rules",
  providers: "Email providers",
  knowledge: "Knowledge",
  company: "About your company",
  brands: "Brands",
  "ideal-customers": "Ideal customers",
  workspace: "General",
  members: "Members",
  alerts: "Alerts",
  digests: "Digests",
  connectors: "Connectors",
  webhooks: "Webhooks",
  tokens: "API tokens",
  logs: "Logs",
  audit: "Audit log",
  instance: "This server",
  account: "Your account",
};

// What each page is for, in a line: the All tools page and the command
// palette show it.
export const tabBlurbs: Record<Tab, string> = {
  overview: "How sending is going across every mailbox and sequence, and what to set up next.",
  "get-started": "Add your brand, invite your team and connect your AI model, analytics and tools. Every step can be skipped.",
  setup: "One brand's website script, site verification and tools, in order, each in one click where it can be. Leave any time and pick up where you left off.",
  ai: "Run Outflare from Claude, ChatGPT, Cursor or any AI app: one URL, sign in, and ask.",
  "ai-usage": "Where your AI spend goes: each tool, model and day, on your own provider bill, with a monthly limit per tool.",
  tools: "Every tool in Outflare, what it does, and where to learn more.",
  inbox: "Replies from every mailbox in one place, with the ones that need an answer first.",
  playbooks: "Ready-made recipes for growth, from welcoming new sign-ups to getting named in AI answers. Copy the prompt or start one as a mission.",
  missions: "Goals your agents work toward, such as 10 meetings by a date. Approve the plan, see every call, stop it any time.",
  projects: "The brief for each motion: offer, ideal customer, tone and budget. People and agents work from it.",
  sequences: "The emails and follow-ups each lead gets, the schedule they send on, and who is in them.",
  people: "Every person and company Outflare knows: their fields, timeline, consent and duplicates.",
  leads: "Everyone you can contact, where they came from, and what has been sent to them.",
  lists: "Saved searches that build lead lists from your data providers and keep them fresh.",
  "lead-finder": "AI that searches the web for companies that fit your ideal customer, reads their websites and says which fit and why.",
  "email-finder": "Propose a likely work email for someone at a company, and check an address. Learns from your own contacts; finding is free.",
  suppression: "People and domains Outflare will never email, such as unsubscribes and bounces.",
  infrastructure: "Plan the domains and mailboxes a motion needs, and follow each through warm-up, rest and retirement.",
  mailboxes: "Connected mailboxes, their health, daily limits and sign-in state.",
  domains: "Sending domains: DNS records, blocklists and daily caps.",
  warmup: "Build each new mailbox's reputation before it sends cold email, in your connected sequencer's warm-up, and see its score.",
  servers: "Your own SMTP servers and the reputation of their IP addresses.",
  lifecycle: "Receipts, password resets and onboarding email from your own provider: set up, then see how it is going.",
  emails: "Every transactional and lifecycle email, whether it was delivered, and why not.",
  templates: "The emails your product sends, with variables, previews, test sends and versions.",
  topics: "What people can choose to get, such as product updates, and leave one at a time from the unsubscribe link.",
  broadcasts: "One lifecycle email to many people at once, such as a newsletter or a launch.",
  journeys: "Flows an event starts: send an email, wait, branch on what people did, exit.",
  rules: "Turn what people do, or don't do, into events of your own that journeys start on: signed up and didn't activate within 7 days.",
  providers: "Amazon SES, Postmark or Resend, connected step by step, and the domains you send from.",
  "link-building": "Pages that could link to your site, from every way of earning links, followed until the link is live; one website pitched at a time.",
  news: "Saved news searches on your own Geonode: follow a topic and who writes about it, and keep every article about your brand as its coverage.",
  allocation: "Which mailbox sends for which sequence, and which sequences are short of mailboxes.",
  senders: "Who your mailboxes send as, and which team owns which senders and sequences.",
  outcomes: "Meetings and deals your outreach produced, what they cost, and the cost of each.",
  reports: "Sends, bounces, replies and opt-outs by day, sequence, step, mailbox or domain.",
  analytics: "Visitors, top pages, traffic sources and conversions from the analytics tools on your website.",
  tracking: "Outflare's own snippet for your websites: the script tag to paste, and whether each site is sending visits.",
  flags: "Turn things on for some visitors, or split them between versions to see which works better.",
  "ab-tests": "See which version of a flag wins at a goal, with numbers that stay honest however often you check.",
  channels: "Visits, sign-ups, revenue and cost for every channel, credited the same way in every report.",
  links: "Tag every link with one channel, source and campaign, so each visit, lead and sale is credited to the right place.",
  partners: "Referral, affiliate and influencer programs: partners' links and codes, the sales they bring, their rewards and payouts.",
  "seo-overview": "Is each brand growing in search: branded search, visitors, domain rating and links, with their trends.",
  experiments: "Did a change help? Hold back a matched group of pages and measure the difference from Search Console.",
  audits: "OutflareBot crawls your websites, scores their health and finds what to fix, page by page.",
  fixes: "Everything worth fixing, one ticket per problem, written up by your AI model and reviewed by you.",
  keywords: "What Google shows each page for, the keyword each page targets, where it ranks, pages that compete for one search, and how Google indexed them, from your Search Console.",
  backlinks: "Who links to your sites, what you gained and lost, and which links are worth getting back.",
  "ai-visibility": "How often AI assistants name each brand against its competitors, whether they get its facts right, and which AI crawlers read its sites.",
  competitors: "Who you compete with in search, and the keywords and links they have that you don't.",
  visitors: "The companies and people on your website, scored for buying intent, from your site snippet and clicks on your emails.",
  ads: "Spend, clicks and platform-reported conversions from your own Google, Meta, Microsoft, Reddit, X and LinkedIn ad accounts, by brand.",
  social: "Confirm your brands' X and LinkedIn accounts, then follow their followers, posts and engagement every day.",
  reviews: "Every review of your brands on Trustpilot, G2, Capterra and Google, your review score and its direction, and answers drafted for you to post.",
  reddit: "Find threads where your brand can help, reply with a disclosure from its own account, and count the sign-ups.",
  seo: "Domain rating, backlinks and the keywords your sites rank for, from DataForSEO or Ahrefs.",
  connectors: "The services Outflare can use, connected once for the whole workspace.",
  knowledge: "Articles and quick answers your team writes once: products, prices, policies and stock answers. Your team and every AI in Outflare answer from them.",
  company: "What Outflare knows about your company, so people and agents never have to repeat it.",
  brands: "The businesses you market from this workspace, each with its own website, context, links and mailboxes.",
  "ideal-customers": "Who you sell to, described once for projects, lead lists and the lead finder.",
  workspace: "The workspace's name and its address in the app.",
  members: "Who is in this workspace, and what each person can do.",
  alerts: "What needs someone now, such as hot replies and hot website visitors, and who hears about it by email.",
  digests: "A daily or weekly summary of the whole workspace, kept here and emailed through your own provider.",
  tokens: "Tokens for scripts and agents, with scopes and send budgets.",
  webhooks: "Events Outflare sends to your own systems as they happen.",
  logs: "Every API call from the web app, the API, MCP agents, the CLI and missions: search, filter, follow live and export.",
  audit: "Every change in the workspace: what, who, when, and through which surface.",
  instance: "This server's version, updates and anonymous usage report.",
  account: "Your email address, password and passkeys.",
};

// Where to read more about each page on outflare.ai.
export const tabDocs: Partial<Record<Tab, string>> = {
  brands: "/docs/guides/brands/",
  "ideal-customers": "/docs/guides/ideal-customers/",
  ai: "/docs/agents/mcp/",
  "ai-usage": "/docs/guides/ai-usage/",
  overview: "/docs/quickstart/",
  "get-started": "/docs/guides/getting-started/",
  setup: "/docs/guides/brand-setup/",
  inbox: "/docs/guides/inbox/",
  missions: "/docs/agents/missions/",
  playbooks: "/playbooks/",
  projects: "/docs/guides/projects/",
  sequences: "/docs/guides/sequences/",
  people: "/docs/guides/customer-records/",
  leads: "/docs/guides/import-leads/",
  lists: "/docs/guides/lead-lists/",
  suppression: "/docs/concepts/policy-check/",
  infrastructure: "/docs/guides/infrastructure/",
  mailboxes: "/docs/guides/import-mailboxes/",
  domains: "/docs/guides/sending-domains/",
  warmup: "/docs/guides/warmup/",
  servers: "/docs/guides/sending-servers/",
  lifecycle: "/docs/guides/lifecycle-email/",
  emails: "/docs/guides/lifecycle-email/",
  templates: "/docs/guides/email-templates/",
  topics: "/docs/guides/lifecycle-email/#topics-and-the-preference-page",
  broadcasts: "/docs/guides/lifecycle-email/#broadcasts",
  journeys: "/docs/guides/lifecycle-email/#journeys",
  rules: "/docs/guides/rules/",
  providers: "/docs/integrations/email-providers/",
  allocation: "/docs/guides/mailbox-allocation/",
  senders: "/docs/guides/mailbox-allocation/",
  outcomes: "/docs/guides/outcomes/",
  reports: "/docs/guides/reports/",
  analytics: "/docs/integrations/analytics/",
  experiments: "/docs/guides/seo-experiments/",
  links: "/docs/guides/campaign-links/",
  partners: "/docs/guides/partners/",
  channels: "/docs/guides/attribution/",
  tracking: "/docs/guides/website-tracking/",
  flags: "/docs/guides/feature-flags/",
  "ab-tests": "/docs/guides/ab-tests/",
  seo: "/docs/integrations/seo/",
  "seo-overview": "/docs/integrations/seo/",
  audits: "/docs/guides/site-audits/",
  fixes: "/docs/guides/site-audits/#the-fixes-list",
  backlinks: "/docs/guides/backlinks/",
  competitors: "/docs/guides/competitors/",
  keywords: "/docs/guides/keywords/",
  reddit: "/docs/guides/reddit/",
  "ai-visibility": "/docs/guides/ai-visibility/",
  social: "/docs/guides/social/",
  reviews: "/docs/guides/reviews/",
  ads: "/docs/integrations/paid-media/",
  visitors: "/docs/guides/website-visitors/",
  connectors: "/docs/integrations/connectors/",
  company: "/docs/agents/memory/",
  workspace: "/docs/guides/workspaces-and-access/",
  members: "/docs/guides/workspaces-and-access/",
  alerts: "/docs/guides/alerts/",
  digests: "/docs/guides/digests/",
  tokens: "/docs/api/authentication/",
  webhooks: "/docs/api/webhooks/",
  logs: "/docs/api/logs/",
  audit: "/docs/guides/workspaces-and-access/",
  instance: "/docs/self-hosting/telemetry/",
};

// "g" then this key opens the page.
export const shortcuts: Partial<Record<Tab, string>> = { overview: "o", tools: "t", reports: "r", analytics: "w", inbox: "i", missions: "a", outcomes: "c", projects: "p", sequences: "s", people: "h", leads: "l", suppression: "x", mailboxes: "m", domains: "d", infrastructure: "f", warmup: "u", seo: "e", fixes: "k", connectors: "n" };

export type NavGroup = { label?: string; tabs: Tab[] };

// The sidebar: the everyday pages, then one item per module, then the
// company hub. A module's pages show beneath it while you are in it. A new
// tool joins its module; a new module is one more entry here, in this
// order: CRM, Visitors, Outreach, Deliverability, Lifecycle, SEO, Social, Reviews, Paid ads,
// Partners, Analytics, Playbooks. A module with no pages yet is left out.
export const everyday: Tab[] = ["overview", "inbox", "missions", "projects"];

export type Module = { id: string; label: string; icon: IconName; tabs: Tab[] };
export const modules: Module[] = [
  { id: "crm", label: "CRM", icon: "leads", tabs: ["people", "leads", "lists", "lead-finder", "email-finder", "suppression"] },
  { id: "visitors", label: "Visitors", icon: "visitors", tabs: ["visitors"] },
  { id: "outreach", label: "Outreach", icon: "sequences", tabs: ["sequences", "link-building", "news", "allocation", "reports"] },
  { id: "deliverability", label: "Deliverability", icon: "mailboxes", tabs: ["infrastructure", "mailboxes", "domains", "warmup", "servers", "senders"] },
  { id: "lifecycle", label: "Lifecycle", icon: "lifecycle", tabs: ["lifecycle", "broadcasts", "journeys", "rules", "templates", "topics", "emails", "providers"] },
  { id: "seo", label: "SEO", icon: "seo", tabs: ["seo-overview", "audits", "fixes", "experiments", "keywords", "seo", "backlinks", "ai-visibility", "competitors"] },
  { id: "social", label: "Social", icon: "social", tabs: ["social", "reddit"] },
  { id: "reviews", label: "Reviews", icon: "reviews", tabs: ["reviews"] },
  { id: "ads", label: "Paid ads", icon: "ads", tabs: ["ads"] },
  { id: "partners", label: "Partners", icon: "partners", tabs: ["partners"] },
  { id: "analytics", label: "Analytics", icon: "reports", tabs: ["channels", "outcomes", "analytics", "tracking", "flags", "ab-tests", "links"] },
  // Playbooks: recipes that use every module above, from playbooks/ in the repository.
  { id: "playbooks", label: "Playbooks", icon: "playbooks", tabs: ["playbooks"] },
  // What the business knows: knowledge, company context and memories, which
  // every module and agent reads.
  { id: "knowledge", label: "Knowledge", icon: "book", tabs: ["knowledge", "company"] },
  // The company hub: its brands, their setup and who it sells to.
  { id: "company", label: "Company", icon: "company", tabs: ["brands", "setup", "ideal-customers"] },
];
export const moduleOf = (t: Tab) => modules.find((m) => m.tabs.includes(t));
const allModules = modules;

// Pages that work for someone kept to some brands. The others need every
// brand (the API answers 403), so they are hidden from them.
const brandTabs: readonly Tab[] = ["overview", "seo-overview", "audits", "fixes", "experiments", "keywords", "ai-visibility", "backlinks", "competitors", "link-building", "news", "reddit", "social", "reviews", "ads", "visitors", "tracking", "flags", "ab-tests", "inbox", "projects", "sequences", "people", "leads", "lists", "rules", "lead-finder", "suppression", "mailboxes", "domains", "knowledge", "company", "brands", "setup", "ideal-customers", "members", "account", "instance"];
export const canOpen = (ws: Workspace | undefined, t: Tab) => !ws?.brand_ids?.length || brandTabs.includes(t);

// The sidebar keeps two things. Pins are kept in this browser for the
// person who made them, and dropped if someone else signs in. Which module
// is open lasts only while signed in: signing out starts again at Home
// with every module closed.
type NavPrefs = { pins: Tab[]; open: string };
const prefsKey = "nav";
const isAppTab = (t: unknown): t is Tab => typeof t === "string" && (appTabs as readonly string[]).includes(t);

function loadPins(user: string): Tab[] {
  try {
    const p = JSON.parse(localStorage.getItem(prefsKey) ?? "null");
    if (p && typeof p === "object" && p.user === user && Array.isArray(p.pins)) return p.pins.filter(isAppTab);
  } catch {
    // Storage can be refused or hold something old; start fresh.
  }
  return [];
}

let navUser = "";
let prefs: NavPrefs = { pins: [], open: "" };
const prefListeners = new Set<() => void>();

function setPrefs(next: NavPrefs) {
  if (next.pins !== prefs.pins) {
    try {
      localStorage.setItem(prefsKey, JSON.stringify({ user: navUser, pins: next.pins }));
    } catch {
      // Not remembered after a reload; fine.
    }
  }
  prefs = next;
  prefListeners.forEach((l) => l());
}

// Called once the signed-in person is known: loads their pins.
export function startNav(user: string) {
  if (user === navUser) return;
  navUser = user;
  prefs = { pins: loadPins(user), open: "" };
  prefListeners.forEach((l) => l());
}

// On sign-out: forget where this person was, so the next sign-in, theirs
// or anyone's, starts at Home.
export function resetNav() {
  navUser = "";
  prefs = { pins: [], open: "" };
  prefListeners.forEach((l) => l());
  history.replaceState(null, "", "/");
}

export function useNavPrefs(): NavPrefs {
  return useSyncExternalStore(
    (l) => {
      prefListeners.add(l);
      return () => prefListeners.delete(l);
    },
    () => prefs,
  );
}

export function togglePin(t: Tab, from: string) {
  const on = prefs.pins.includes(t);
  setPrefs({ ...prefs, pins: on ? prefs.pins.filter((x) => x !== t) : [...prefs.pins, t] });
  track(on ? "ui.nav_unpinned" : "ui.nav_pinned", { tab: t, from });
}

// One module is open at a time, like an accordion: opening one closes the
// one before, so the sidebar never grows past the screen.
function toggleModule(id: string) {
  const open = prefs.open !== id;
  setPrefs({ ...prefs, open: open ? id : "" });
  track("ui.nav_module_toggled", { module: id, open });
}

// Opening a page opens its module and closes any other.
function visited(t: Tab) {
  const open = moduleOf(t)?.id ?? "";
  if (prefs.open !== open) setPrefs({ ...prefs, open });
}

// A small pin beside a page's name: shows on hover (always when pinned),
// hidden on phones, where All tools does the pinning.
export function PinButton({ tab, from, className }: { tab: Tab; from: string; className?: string }) {
  const { pins } = useNavPrefs();
  const on = pins.includes(tab);
  return (
    <button
      type="button"
      onClick={(e) => { e.stopPropagation(); togglePin(tab, from); }}
      aria-pressed={on}
      aria-label={on ? `Unpin ${tabNames[tab]}` : `Pin ${tabNames[tab]} to the sidebar`}
      title={on ? "Unpin" : "Pin to the sidebar"}
      className={cn(
        "hidden size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-sidebar-accent hover:text-foreground focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:outline-none md:flex",
        on ? "text-brand-strong dark:text-brand" : "opacity-0",
        className,
      )}
    >
      <Icon name="pin" className="size-3.5" />
    </button>
  );
}

// Settings pages sit behind one Settings entry, with their own menu:
// the workspace, its AI, and what developers and agents use.
export function settingsGroups(isAdmin: boolean, ws?: Workspace): NavGroup[] {
  const groups: NavGroup[] = [
    { label: "Workspace", tabs: ["workspace", "members"] },
    { label: "AI", tabs: ["ai", "ai-usage"] },
    { label: "Notifications", tabs: ["alerts", "digests"] },
    { label: "Developers", tabs: isAdmin ? ["tokens", "webhooks", "logs", "audit", "tools"] : ["webhooks", "tools"] },
    { label: "You", tabs: ["account"] },
    { label: "Server", tabs: ["instance"] },
  ];
  return groups.map((g) => ({ ...g, tabs: g.tabs.filter((t) => canOpen(ws, t)) })).filter((g) => g.tabs.length > 0);
}

export const settingsTabs: readonly Tab[] = ["workspace", "members", "ai", "ai-usage", "alerts", "digests", "tokens", "webhooks", "logs", "audit", "tools", "account", "instance"];
export const isSettings = (t: Tab) => settingsTabs.includes(t);

// ---- Theme ----

export type Theme = "dark" | "light";

export function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useState<Theme>(() => (document.documentElement.dataset.theme === "light" ? "light" : "dark"));
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    try {
      localStorage.setItem("theme", theme);
    } catch {
      // Private windows may refuse storage; the theme still applies now.
    }
  }, [theme]);
  return [theme, () => setTheme((t) => (t === "dark" ? "light" : "dark"))];
}

// ---- Sidebar ----

/** An extra sidebar item a server extension adds for this person only (/v1/me menu_links). */
export type MenuLink = { id: string; label: string; href: string; icon: string };

type SidebarProps = {
  me: { email?: string };
  links?: MenuLink[];
  workspaces: Workspace[];
  ws?: Workspace;
  onWorkspace: (id: string) => void;
  tab: Tab;
  onTab: (t: Tab) => void;
  unread: number;
  theme: Theme;
  onTheme: () => void;
  onSearch: () => void;
  onSignOut: () => void;
};

export function Sidebar(p: SidebarProps) {
  const { setOpenMobile, state } = useSidebar();
  const close = () => setOpenMobile(false);
  const prefs = useNavPrefs();
  const open = prefs.open;
  const shown = (t: Tab) => canOpen(p.ws, t);
  const pins = prefs.pins.filter(shown);
  const modules = allModules.map((m) => ({ ...m, tabs: m.tabs.filter(shown) })).filter((m) => m.tabs.length > 0);
  useEffect(() => visited(p.tab), [p.tab]);
  return (
    <UISidebar collapsible="icon">
      <SidebarHeader className="gap-2 pt-3">
        <div className="grid gap-px">
          <WorkspaceSwitcher workspaces={p.workspaces} ws={p.ws} onChange={(id) => { p.onWorkspace(id); close(); }} />
          {p.ws && <BrandSwitcher ws={p.ws} onManage={() => p.onTab("brands")} onPicked={close} />}
        </div>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton tooltip="Search  ⌘K" onClick={() => { close(); p.onSearch(); }} className="h-8 border bg-background text-muted-foreground shadow-xs hover:text-foreground dark:bg-input/30">
              <Icon name="search" className="size-3.5" />
              <span className="flex-1">Search or jump to…</span>
              <Kbd>⌘K</Kbd>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
        {p.ws && shown("ai") && <ConnectAIPrompt ws={p.ws.id} active={p.tab === "ai"} onOpen={() => { p.onTab("ai"); close(); }} />}
      </SidebarHeader>

      <SidebarContent className="gap-0">
        <SidebarGroup className="py-1">
          <SidebarMenu className="gap-px">
            {everyday.filter(shown).map((t) => <NavItem key={t} tab={t} active={p.tab === t} unread={t === "inbox" ? p.unread : 0} onClick={() => { p.onTab(t); close(); }} />)}
          </SidebarMenu>
        </SidebarGroup>
        {pins.length > 0 && (
          <SidebarGroup className="py-1">
            <SidebarGroupLabel className="h-5 font-mono text-[10px] tracking-wider uppercase">Pinned</SidebarGroupLabel>
            <SidebarMenu className="gap-px">
              {pins.map((t) => <NavItem key={t} tab={t} active={p.tab === t} pin="pinned" onClick={() => { p.onTab(t); close(); }} />)}
            </SidebarMenu>
          </SidebarGroup>
        )}
        <SidebarGroup className="py-1">
          <SidebarGroupLabel className="h-5 font-mono text-[10px] tracking-wider uppercase">Modules</SidebarGroupLabel>
          <SidebarMenu className="gap-px">
            {modules.map((m) => {
              const inside = m.tabs.includes(p.tab);
              const expanded = open === m.id;
              return (
                <SidebarMenuItem key={m.id}>
                  <SidebarMenuButton
                    isActive={inside && !expanded}
                    tooltip={m.label}
                    onClick={() => { if (state === "collapsed") p.onTab(m.tabs[0]); else toggleModule(m.id); }}
                    aria-expanded={expanded}
                    className="h-10 text-[13px] md:h-7"
                  >
                    <Icon name={m.icon} className={cn("size-4", inside && "text-brand-strong dark:text-brand")} />
                    <span className="flex-1 font-medium">{m.label}</span>
                    <Icon name="chevron" className={cn("size-3.5 text-muted-foreground transition-transform duration-200", expanded && "rotate-90")} />
                  </SidebarMenuButton>
                  {/* Opens and closes with a short slide; closed pages can't be tabbed to. */}
                  <div className={cn("grid transition-[grid-template-rows] duration-200 ease-out motion-reduce:transition-none group-data-[collapsible=icon]:hidden", expanded ? "grid-rows-[1fr]" : "grid-rows-[0fr]")} inert={!expanded}>
                    <div className="min-h-0 overflow-hidden">
                      <SidebarMenuSub className="mr-0 gap-px py-0.5 pr-0">
                        {m.tabs.map((t) => (
                          <SidebarMenuSubItem key={t} className="flex items-center">
                            <SidebarMenuSubButton asChild isActive={p.tab === t}>
                              <button onClick={() => { p.onTab(t); close(); }} aria-current={p.tab === t ? "page" : undefined} className="group/nav h-9 w-full flex-1 text-[13px] md:h-7">
                                <span className="flex-1 text-left">{tabNames[t]}</span>
                                {shortcuts[t] && <span className="hidden font-mono text-[10px] text-muted-foreground md:group-hover/nav:inline">G {shortcuts[t]!.toUpperCase()}</span>}
                              </button>
                            </SidebarMenuSubButton>
                            <PinButton tab={t} from="sidebar" className="ml-0.5 group-hover/menu-sub-item:opacity-100" />
                          </SidebarMenuSubItem>
                        ))}
                      </SidebarMenuSub>
                    </div>
                  </div>
                </SidebarMenuItem>
              );
            })}
          </SidebarMenu>
        </SidebarGroup>
      </SidebarContent>

      <SidebarFooter className="gap-1 border-t pt-1.5 pb-3">
        <SidebarMenu className="gap-px">
          {shown("connectors") && <NavItem tab="connectors" pin="sidebar" active={p.tab === "connectors"} onClick={() => { p.onTab("connectors"); close(); }} />}
          <NavItem tab="workspace" label="Settings" icon="settings" active={isSettings(p.tab)} onClick={() => { p.onTab(isSettings(p.tab) ? p.tab : "workspace"); close(); }} />
          {(p.links ?? []).map((l) => (
            <SidebarMenuItem key={l.id}>
              <SidebarMenuButton asChild tooltip={l.label} className="h-10 text-[13px] md:h-7">
                <a href={l.href}>
                  <Icon name={isIconName(l.icon) ? l.icon : "external"} className="size-4" />
                  <span className="flex-1">{l.label}</span>
                </a>
              </SidebarMenuButton>
            </SidebarMenuItem>
          ))}
        </SidebarMenu>
        <Account email={p.me.email} theme={p.theme} onTheme={p.onTheme} onSignOut={p.onSignOut} onAccount={() => p.onTab("account")} />
      </SidebarFooter>
      <SidebarRail />
    </UISidebar>
  );
}

// Until an AI app is connected, the sidebar's most visible spot asks for
// one: running Outflare by asking an agent is the point of it. After that
// it lives in Settings → AI apps.
function ConnectAIPrompt({ ws, active, onOpen }: { ws: string; active: boolean; onOpen: () => void }) {
  const apps = useAgentApps(ws);
  if (apps === null || apps.length > 0) return null;
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <SidebarMenuButton
          tooltip="Connect your AI"
          isActive={active}
          onClick={() => { track("ui.agent_connect_prompted", { from: "sidebar" }); onOpen(); }}
          className="h-auto min-h-10 items-start gap-2.5 border border-brand-strong/40 bg-brand-strong/5 py-2 hover:bg-brand-strong/10 dark:border-brand/40 dark:bg-brand/5 dark:hover:bg-brand/10"
        >
          <Icon name="ai" className="mt-0.5 size-4 text-brand-strong dark:text-brand" />
          <span className="min-w-0 flex-1 group-data-[collapsible=icon]:hidden">
            <span className="block text-[13px] font-medium">Connect your AI</span>
            <span className="block text-[11px] leading-snug text-muted-foreground">From Claude, ChatGPT or Cursor</span>
          </span>
        </SidebarMenuButton>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}

// One page in the sidebar: 28px rows on a desktop, 40px in the phone sheet.
function NavItem({ tab, label = tabNames[tab], icon = tab as IconName, active, unread = 0, pin, onClick }: { tab: Tab; label?: string; icon?: IconName; active: boolean; unread?: number; pin?: "pinned" | "sidebar"; onClick: () => void }) {
  return (
    <SidebarMenuItem className={cn(pin && "flex items-center")}>
      <SidebarMenuButton
        isActive={active}
        tooltip={label}
        onClick={onClick}
        aria-current={active ? "page" : undefined}
        className="group/nav h-10 text-[13px] md:h-7 data-[active=true]:shadow-xs data-[active=true]:ring-1 data-[active=true]:ring-sidebar-border"
      >
        <Icon name={icon} className={cn("size-4", active && "text-brand-strong dark:text-brand")} />
        <span className="flex-1">{label}</span>
        {shortcuts[tab] && label === tabNames[tab] && unread === 0 && !pin && <span className="hidden font-mono text-[10px] text-muted-foreground md:group-hover/nav:inline">G {shortcuts[tab]!.toUpperCase()}</span>}
      </SidebarMenuButton>
      {pin && <PinButton tab={tab} from={pin} className={cn("ml-0.5 group-hover/menu-item:opacity-100", pin === "pinned" && "opacity-0")} />}
      {unread > 0 && (
        <SidebarMenuBadge className="rounded bg-brand-strong px-1.5 font-mono text-[11px] text-white peer-hover/menu-button:text-white md:top-1 dark:bg-brand dark:text-neutral-950 dark:peer-hover/menu-button:text-neutral-950" aria-label={`${unread} unread`}>
          {unread}
        </SidebarMenuBadge>
      )}
    </SidebarMenuItem>
  );
}

function Account({ email, theme, onTheme, onSignOut, onAccount }: { email?: string; theme: Theme; onTheme: () => void; onSignOut: () => void; onAccount: () => void }) {
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton className="h-9 data-[state=open]:bg-sidebar-accent" tooltip={email}>
              <span aria-hidden="true" className="flex size-6 shrink-0 items-center justify-center rounded-full bg-sidebar-accent font-mono text-[11px] font-medium uppercase ring-1 ring-sidebar-border">{email?.[0] ?? "?"}</span>
              <span className="min-w-0 flex-1 truncate text-[12px] text-muted-foreground" title={email}>{email}</span>
              <Icon name="updown" className="size-3.5 text-muted-foreground" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent side="top" align="start" className="w-(--radix-dropdown-menu-trigger-width) min-w-52">
            <DropdownMenuLabel className="truncate font-normal text-muted-foreground">{email}</DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem className="text-[13px]" onSelect={onAccount}>
              <Icon name="account" className="size-3.5" />
              Your account
            </DropdownMenuItem>
            <DropdownMenuItem className="text-[13px]" onSelect={onTheme}>
              <Icon name={theme === "dark" ? "sun" : "moon"} className="size-3.5" />
              {theme === "dark" ? "Light theme" : "Dark theme"}
            </DropdownMenuItem>
            <DropdownMenuItem className="text-[13px]" onSelect={onSignOut}>
              <Icon name="logout" className="size-3.5" />
              Sign out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}

// BrandSwitcher picks the one brand every page shows, like a project in
// PostHog. It sits under the workspace at the top of the sidebar, so the
// scope is above everything it scopes, as in Cloudflare; on a phone, where
// the sidebar is behind the menu button, the top bar shows it as a chip.
// Hidden until the workspace has a brand. "All brands" is a deliberate
// pick, for comparing them side by side.
function BrandSwitcher({ ws, onManage, chip, onPicked }: { ws: Workspace; onManage: () => void; chip?: boolean; onPicked?: () => void }) {
  const { brand, active } = useBrand(ws.id);
  if (active.length === 0) return null;
  const limited = (ws.brand_ids?.length ?? 0) > 0;
  const all = limited ? "All your brands" : "All brands";
  const label = `Brand: ${brand ? brand.name : all.toLowerCase()}. Switch brand`;
  const pick = (handle: string) => { setBrand(handle); onPicked?.(); };
  const menu = (
    <DropdownMenuContent align="start" className={cn("min-w-60", !chip && "w-(--radix-dropdown-menu-trigger-width)")}>
      <DropdownMenuLabel className="truncate font-mono text-[11px] text-muted-foreground uppercase">Brand in {ws.name}</DropdownMenuLabel>
      {active.map((b) => (
        <DropdownMenuItem key={b.id} className="text-[13px]" onSelect={() => pick(b.handle)}>
          <span className="min-w-0 flex-1 truncate">{b.name}</span>
          {b.id === brand?.id ? <Icon name="check" className="size-3.5" /> : <span className="truncate font-mono text-[11px] text-muted-foreground">{b.website}</span>}
        </DropdownMenuItem>
      ))}
      {active.length > 1 && (
        <>
          <DropdownMenuSeparator />
          <DropdownMenuItem className="text-[13px]" onSelect={() => pick("")}>
            <span className="flex-1">{all}</span>
            {!brand && <Icon name="check" className="size-3.5" />}
          </DropdownMenuItem>
        </>
      )}
      <DropdownMenuSeparator />
      <DropdownMenuItem className="text-[13px]" onSelect={() => { onManage(); onPicked?.(); }}>
        <Icon name="workspace" className="size-3.5" />
        Manage brands
      </DropdownMenuItem>
    </DropdownMenuContent>
  );
  if (chip) {
    return (
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button aria-label={label} className="inline-flex h-7 min-w-0 max-w-40 shrink-0 items-center gap-1.5 rounded-md border bg-background px-2 text-[13px] font-medium shadow-xs hover:bg-accent data-[state=open]:bg-accent md:hidden dark:bg-input/30">
            <span className="min-w-0 truncate">{brand ? brand.name : all}</span>
            <Icon name="updown" className="size-3.5 shrink-0 text-muted-foreground" />
          </button>
        </DropdownMenuTrigger>
        {menu}
      </DropdownMenu>
    );
  }
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton aria-label={label} tooltip={brand ? brand.name : all} className="h-9 data-[state=open]:bg-sidebar-accent">
              {/* Collapsed, only the initial shows; hovering names the brand. */}
              <span aria-hidden="true" className="flex size-5 shrink-0 items-center justify-center rounded border bg-background font-mono text-[10px] font-medium uppercase dark:bg-input/30">{brand ? brand.name[0] : "∗"}</span>
              <span className="min-w-0 flex-1 truncate text-left font-medium">{brand ? brand.name : all}</span>
              <Icon name="updown" className="size-3.5 text-muted-foreground" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          {menu}
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}

function WorkspaceSwitcher({ workspaces, ws, onChange }: { workspaces: Workspace[]; ws?: Workspace; onChange: (id: string) => void }) {
  const inner = (
    <>
      {/* One row: the Outflare mark, then the workspace. Collapsed, only the mark shows. */}
      <Logo markOnly className="shrink-0" />
      <span aria-hidden="true" className="text-muted-foreground/40 group-data-[collapsible=icon]:hidden">/</span>
      <span className="min-w-0 flex-1 truncate text-left font-medium group-data-[collapsible=icon]:hidden">{ws?.name}</span>
    </>
  );
  if (workspaces.length < 2) {
    return (
      <SidebarMenu>
        <SidebarMenuItem>
          <SidebarMenuButton className="pointer-events-none h-9" tabIndex={-1}>{inner}</SidebarMenuButton>
        </SidebarMenuItem>
      </SidebarMenu>
    );
  }
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton aria-label="Switch workspace" className="h-9 data-[state=open]:bg-sidebar-accent">
              {inner}
              <Icon name="updown" className="size-3.5 text-muted-foreground group-data-[collapsible=icon]:hidden" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-(--radix-dropdown-menu-trigger-width) min-w-56">
            <DropdownMenuLabel className="font-mono text-[11px] text-muted-foreground uppercase">Workspaces</DropdownMenuLabel>
            {workspaces.map((w, i) => (
              <DropdownMenuItem key={w.id} className="text-[13px]" onSelect={() => onChange(w.id)}>
                <span className="flex size-5 items-center justify-center rounded border font-mono text-[10px] uppercase">{w.name[0]}</span>
                <span className="flex-1 truncate">{w.name}</span>
                {w.id === ws?.id ? <Icon name="check" className="size-3.5" /> : i < 9 && <DropdownMenuShortcut className="font-mono">{w.role}</DropdownMenuShortcut>}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}

// ---- Settings ----

// The settings pages share one frame: their own menu beside the page on a
// desktop, a row of tabs above it on a phone.
export function SettingsFrame({ groups, tab, onTab, children }: { groups: NavGroup[]; tab: Tab; onTab: (t: Tab) => void; children: React.ReactNode }) {
  // On a phone the tabs scroll sideways; keep the open one in view.
  useEffect(() => {
    document.querySelector('nav[aria-label="Settings"] [aria-current="page"]')?.scrollIntoView({ block: "nearest", inline: "center" });
  }, [tab]);
  return (
    <div className="grid gap-6 md:grid-cols-[11rem_minmax(0,1fr)] lg:gap-10">
      <nav aria-label="Settings" className="-mx-4 min-w-0 self-start sm:-mx-6 md:sticky md:top-18 md:mx-0">
        <div className="mb-2 hidden px-2 text-[15px] font-semibold md:block">Settings</div>
        <div className="flex gap-1 overflow-x-auto px-4 pb-1 sm:px-6 md:flex-col md:gap-px md:overflow-visible md:p-0">
          {groups.map((g) => (
            <Fragment key={g.label}>
              <div className="mt-3 hidden h-6 items-center px-2 font-mono text-[10px] tracking-wider text-muted-foreground uppercase first:mt-0 md:flex">{g.label}</div>
              {g.tabs.map((t) => (
                <div key={t} className="group/set flex shrink-0 items-center md:w-full">
                <button
                  onClick={() => onTab(t)}
                  aria-current={tab === t ? "page" : undefined}
                  className={cn(
                    "flex h-9 shrink-0 items-center gap-2 rounded-md border border-transparent px-2.5 md:flex-1 text-[13px] whitespace-nowrap text-muted-foreground transition-colors hover:bg-accent hover:text-foreground md:h-8",
                    tab === t && "border-border bg-accent text-foreground shadow-xs",
                  )}
                >
                  <Icon name={t as IconName} className={cn("size-3.5", tab === t && "text-brand-strong dark:text-brand")} />
                  <span className="flex-1 text-left">{tabNames[t]}</span>
                </button>
                <PinButton tab={t} from="settings" className="ml-0.5 group-hover/set:opacity-100" />
                </div>
              ))}
            </Fragment>
          ))}
        </div>
      </nav>
      <div className="min-w-0">{children}</div>
    </div>
  );
}

// ---- Top bar ----

// BrandSlash separates the phone's brand chip from the page, when it shows.
function BrandSlash({ ws }: { ws: string }) {
  const { active } = useBrand(ws);
  return active.length > 0 ? <span aria-hidden="true" className="text-muted-foreground/50 md:hidden">/</span> : null;
}

const uuidish = /^[0-9a-f]{8}-[0-9a-f]{4}-/i;

export function TopBar({ ws, tab, item, itemName, onTab, onApi, apiOpen }: { ws?: Workspace; tab: Tab; item?: string; itemName?: string; onTab: (t: Tab) => void; onApi: () => void; apiOpen: boolean }) {
  // Pages can name the open item (a mission's name) with an
  // "outflare:item-name" event; otherwise IDs are shown shortened.
  const [named, setNamed] = useState<{ id: string; name: string } | null>(null);
  useEffect(() => {
    const on = (e: Event) => setNamed((e as CustomEvent<{ id: string; name: string }>).detail);
    window.addEventListener("outflare:item-name", on);
    return () => window.removeEventListener("outflare:item-name", on);
  }, []);
  const itemLabel = item && (itemName ?? (tab === "domains" ? item : named?.id === item ? named.name : uuidish.test(item) ? "…" : item));
  return (
    <header className="sticky top-0 z-20 flex h-12 shrink-0 items-center gap-2 border-b bg-background/80 px-3 backdrop-blur">
      <SidebarTrigger className="-ml-1 size-7 text-muted-foreground" />
      <Separator orientation="vertical" className="mr-1 data-[orientation=vertical]:h-4" />
      <nav aria-label="Breadcrumb" className="flex min-w-0 flex-1 items-center gap-1.5 text-[13px]">
        {ws && <BrandSwitcher ws={ws} chip onManage={() => onTab("brands")} />}
        {ws && <BrandSlash ws={ws.id} />}
        {moduleOf(tab) && (
          <>
            <span className="hidden text-muted-foreground sm:inline">{moduleOf(tab)!.label}</span>
            <span aria-hidden="true" className="hidden text-muted-foreground/50 sm:inline">/</span>
          </>
        )}
        {isSettings(tab) && (
          <>
            <button onClick={() => onTab("workspace")} className="hidden text-muted-foreground hover:text-foreground sm:inline">Settings</button>
            <span aria-hidden="true" className="hidden text-muted-foreground/50 sm:inline">/</span>
          </>
        )}
        {item ? (
          <button onClick={() => onTab(tab)} className="truncate text-muted-foreground hover:text-foreground">{tabNames[tab]}</button>
        ) : (
          <span aria-current="page" className="truncate text-foreground">{tabNames[tab]}</span>
        )}
        {item && (
          <>
            <span aria-hidden="true" className="text-muted-foreground/50">/</span>
            <span aria-current="page" className={cn("truncate text-foreground", itemLabel === item && "font-mono text-[12px]")} title={item}>{itemLabel}</span>
            {item !== "new" && <CopyButton text={item} label={`Copy id ${item}`} />}
          </>
        )}
      </nav>
      <button onClick={onApi} aria-pressed={apiOpen} className={cn("inline-flex h-7 items-center gap-1.5 rounded-md border px-2 font-mono text-[12px] shadow-xs transition-colors", apiOpen ? "border-brand/50 bg-brand/10 text-brand-strong dark:text-brand" : "text-muted-foreground hover:bg-accent hover:text-foreground")}>
        <Icon name="code" className="size-3.5" />
        API
      </button>
    </header>
  );
}

// ---- API panel ----

type Call = { title: string; path: string; params: Record<string, string>; query?: Record<string, string | number | boolean>; mcp?: string; ask?: string; scope: string };

function callFor(tab: Tab, ws: string, item?: string): Call {
  const base = { ws };
  switch (tab) {
    case "overview":
      return { title: "Workspace stats", path: "/v1/workspaces/{ws}/stats", params: base, mcp: "get_overview", ask: "How is sending going this week?", scope: "workspace.read" };
    case "get-started":
      return { title: "Getting-started steps", path: "/v1/workspaces/{ws}/onboarding", params: base, mcp: "get_onboarding", ask: "Help me finish setting up Outflare: what is left, and can you do it for me?", scope: "workspace.read" };
    case "setup":
      return { title: "A brand's setup checklist, and GET .../install-health for what is out of date", path: "/v1/workspaces/{ws}/brands/{brand}/setup", params: { ...base, brand: item ?? "{brand}" }, mcp: "get_brand_setup", ask: "What is left to set up for this brand, and can you do the next step for me?", scope: "workspace.read" };
    case "ai":
      return { title: "AI apps connected with OAuth", path: "/v1/workspaces/{ws}/agent-apps", params: base, ask: "What can you do for me in Outflare?", scope: "workspace.read" };
    case "ai-usage":
      return { title: "Where AI spend goes", path: "/v1/workspaces/{ws}/ai-usage", params: base, mcp: "get_ai_usage", ask: "Which Outflare tools spent the most AI money this month, and where could a cheaper model do the job?", scope: "integrations.read" };
    case "tools":
      return { title: "What this server can do", path: "/v1/meta", params: {}, ask: "What can Outflare do for my outreach, and what should I set up first?", scope: "none (public)" };
    case "seo":
      return { title: "SEO tools", path: "/v1/workspaces/{ws}/seo", params: base, mcp: "list_seo", ask: "How did my domain rating and rankings move this month, and which keywords dropped?", scope: "analytics.read" };
    case "seo-overview":
      return { title: "Is each brand growing in search", path: "/v1/workspaces/{ws}/seo/overview", params: base, mcp: "get_seo_overview", ask: "Is our branded search growing, and how did domain rating and backlinks move this month?", scope: "analytics.read" };
    case "experiments":
      return { title: "SEO experiments", path: "/v1/workspaces/{ws}/experiments", params: base, mcp: "list_experiments", ask: "Did shortening the blog titles help, or is it too early to tell?", scope: "seo.read" };
    case "audits":
      return item
        ? { title: "One site and its latest crawl", path: "/v1/workspaces/{ws}/seo-sites/{site}", params: { ...base, site: item }, ask: "What are the worst issues on this site, and which URL templates cause them?", scope: "seo.read" }
        : { title: "Sites OutflareBot audits", path: "/v1/workspaces/{ws}/seo-sites", params: base, mcp: "list_seo_sites", ask: "Which of my sites has the lowest health score, and what should I fix first?", scope: "seo.read" };
    case "keywords":
      return { title: "Sites, then GET .../seo-sites/{site}/search for each one's pages and queries", path: "/v1/workspaces/{ws}/seo-sites", params: base, mcp: "get_site_search", ask: "Which pages get the most search impressions but few clicks, and what should I change on them?", scope: "seo.read" };
    case "ai-visibility":
      return { title: "AI visibility for each cohort", path: "/v1/workspaces/{ws}/ai-visibility", params: base, mcp: "get_ai_visibility", ask: "How often do AI answers name us against our competitors, and what do they get wrong about us?", scope: "seo.read" };
    case "fixes":
      return { title: "Fix tickets", path: "/v1/workspaces/{ws}/fix-tickets", params: base, query: { status: "todo" }, mcp: "list_fix_tickets", ask: "Take the three worst fix tickets, read each with get_fix_ticket, and tell me which to approve and why.", scope: "seo.read" };
    case "competitors":
      return { title: "Sites you compete with", path: "/v1/workspaces/{ws}/seo-competitors", params: base, query: { status: "tracked" }, mcp: "list_seo_competitors", ask: "What did my tracked competitors publish this month, and whose domain rating is rising?", scope: "seo.read" };
    case "link-building":
      return { title: "Pages that could link to you", path: "/v1/workspaces/{ws}/link-prospects", params: base, mcp: "list_link_prospects", ask: "Which competitor pages died this month, who links to them, and which of those sites should we ask first?", scope: "seo.read" };
    case "news":
      return { title: "Coverage of your brands", path: "/v1/workspaces/{ws}/coverage", params: base, mcp: "list_coverage", ask: "What was written about us this week, which outlets covered us without a link, and which topics should we comment on?", scope: "seo.read" };
    case "backlinks":
      return { title: "Links to your sites", path: "/v1/workspaces/{ws}/backlinks", params: base, query: { status: "live" }, mcp: "list_backlinks", ask: "Which strong links did we lose this month, and where did our new links come from?", scope: "seo.read" };
    case "reddit":
      return { title: "Reddit drafts waiting for approval", path: "/v1/workspaces/{ws}/social/actions", params: base, query: { channel: "reddit", status: "draft" }, mcp: "list_social_replies", ask: "Find Reddit threads this week where we can genuinely help, and draft replies that disclose we work here.", scope: "social.read" };
    case "social":
      return { title: "Social growth by channel", path: "/v1/workspaces/{ws}/social/growth", params: base, mcp: "get_social_growth", ask: "How did our followers and engagement on X and LinkedIn move this month, and which posts did best?", scope: "social.read" };
    case "reviews":
      return { title: "Your review score and its direction", path: "/v1/workspaces/{ws}/reviews/summary", params: base, mcp: "get_reviews_summary", ask: "How are our reviews trending this month, and which unhappy reviews still need an answer?", scope: "social.read" };
    case "visitors":
      return { title: "Companies on your website", path: "/v1/workspaces/{ws}/visitors", params: base, query: { view: "companies" }, mcp: "list_website_visitors", ask: "Which companies looked at our pricing this week, and who there should we follow up with?", scope: "customers.read" };
    case "ads":
      return { title: "Ad spend and results by platform", path: "/v1/workspaces/{ws}/ads/summary", params: base, query: { group: "connector" }, mcp: "get_ads_summary", ask: "What did we spend on ads this month per brand, and which campaigns have the worst cost per conversion?", scope: "analytics.read" };
    case "warmup":
      return { title: "Mailboxes and their warm-up", path: "/v1/workspaces/{ws}/mailboxes", params: base, mcp: "list_mailboxes", ask: "Which mailboxes are still warming, and which have a low warm-up score?", scope: "mailboxes.read" };
    case "reports":
      return { title: "Report by day", path: "/v1/workspaces/{ws}/reports", params: base, query: { group_by: "day" }, mcp: "get_report", ask: "Which sequence and step get the most replies this month, and which mailboxes bounce most?", scope: "sequences.read" };
    case "analytics":
      return { title: "Traffic from every analytics tool", path: "/v1/workspaces/{ws}/analytics/summary", params: base, mcp: "get_analytics_summary", ask: "How did the website do over the last 28 days, and which pages and searches bring people in?", scope: "analytics.read" };
    case "tracking":
      return { title: "Website tracking keys and their snippets", path: "/v1/workspaces/{ws}/site-keys", params: base, mcp: "list_site_keys", ask: "Is our website snippet installed and sending visits on every site?", scope: "customers.read" };
    case "flags":
      return { title: "Feature flags", path: "/v1/workspaces/{ws}/flags", params: base, mcp: "list_flags", ask: "Set up a test of a new pricing headline on half of our visitors, shown only on the pricing page.", scope: "analytics.read" };
    case "ab-tests":
      return { title: "A/B tests", path: "/v1/workspaces/{ws}/ab-tests", params: base, mcp: "list_ab_tests", ask: "How is our pricing headline test doing, and is it safe to stop?", scope: "analytics.read" };
    case "channels":
      return { title: "Visits, conversions, revenue and cost per channel", path: "/v1/workspaces/{ws}/attribution/channels", params: base, mcp: "get_attribution_channels", ask: "Which channel brought the most revenue this month, and what did each sign-up cost us per channel?", scope: "analytics.read" };
    case "partners":
      return { title: "Partners and what they earned", path: "/v1/workspaces/{ws}/partners", params: base, mcp: "list_partners", ask: "Which partners brought the most sales this month, and whose payouts are due?", scope: "partners.read" };
    case "links":
      return { title: "Campaign links", path: "/v1/workspaces/{ws}/tracking-campaigns", params: base, query: { status: "active" }, mcp: "list_tracking_campaigns", ask: "Make a tagged link for our spring launch on LinkedIn ads, pointing at the pricing page.", scope: "analytics.read" };
    case "inbox":
      return item
        ? { title: "One conversation", path: "/v1/workspaces/{ws}/inbox/{id}", params: { ...base, id: item }, mcp: "get_inbox_message", ask: "Summarise this reply and draft an answer.", scope: "inbox.read" }
        : { title: "Unread replies", path: "/v1/workspaces/{ws}/inbox", params: base, query: { unread: true }, mcp: "list_inbox", ask: "Which replies are interested and need an answer?", scope: "inbox.read" };
    case "playbooks":
      return item
        ? { title: "One playbook", path: "/v1/workspaces/{ws}/playbooks/{id}", params: { ...base, id: item }, mcp: "get_playbook", ask: "Run this playbook for my brand.", scope: "missions.read" }
        : { title: "Playbooks", path: "/v1/workspaces/{ws}/playbooks", params: base, mcp: "list_playbooks", ask: "Which playbooks am I ready to run, and what should I connect for the rest?", scope: "missions.read" };
    case "missions":
      return item
        ? { title: "One mission", path: "/v1/workspaces/{ws}/missions/{id}", params: { ...base, id: item }, mcp: "get_mission", ask: "What has this mission done so far, and what is it waiting on?", scope: "missions.read" }
        : { title: "Missions", path: "/v1/workspaces/{ws}/missions", params: base, mcp: "list_missions", ask: "Start a mission to book 10 meetings with fintech CFOs in Australia by 30 November.", scope: "missions.read" };
    case "outcomes":
      return { title: "Cost per meeting", path: "/v1/workspaces/{ws}/outcomes/report", params: base, query: { group_by: "sequence" }, mcp: "get_outcomes_report", ask: "Which sequences have the lowest cost per meeting this month?", scope: "outcomes.read" };
    case "projects":
      return item
        ? { title: "A project's brief", path: "/v1/workspaces/{ws}/projects/{id}/brief", params: { ...base, id: item }, mcp: "get_project_brief", ask: "Read this project's brief and draft a first email for it.", scope: "projects.read" }
        : { title: "All projects", path: "/v1/workspaces/{ws}/projects", params: base, mcp: "list_projects", ask: "Which projects are sending, and which briefs are incomplete?", scope: "projects.read" };
    case "sequences":
      return item
        ? { title: "One sequence", path: "/v1/workspaces/{ws}/sequences/{id}", params: { ...base, id: item }, mcp: "get_sequence", ask: "How is this sequence performing by step?", scope: "sequences.read" }
        : { title: "All sequences", path: "/v1/workspaces/{ws}/sequences", params: base, mcp: "list_sequences", ask: "List my active sequences and their reply rates.", scope: "sequences.read" };
    case "people":
      return item && /^[0-9a-f-]{36}$/.test(item)
        ? { title: "One record", path: "/v1/workspaces/{ws}/records/{record}", params: { ...base, record: item }, mcp: "get_record", ask: "Summarise this person's history with us and what to do next.", scope: "customers.read" }
        : item === "duplicates"
          ? { title: "Possible duplicates", path: "/v1/workspaces/{ws}/records/duplicates", params: base, mcp: "list_record_duplicates", ask: "Which possible duplicates are safe to merge, and which look like two different people?", scope: "customers.read" }
          : { title: item === "companies" ? "Companies" : "People", path: "/v1/workspaces/{ws}/records", params: base, query: { type: item === "companies" ? "account" : "person", limit: 50 }, mcp: "list_records", ask: "Which customers at fintech companies haven't heard from us this month?", scope: "customers.read" };
    case "leads":
      return { title: "Leads", path: "/v1/workspaces/{ws}/leads", params: base, query: { limit: 50 }, mcp: "list_leads", ask: "Find leads at companies in fintech.", scope: "leads.read" };
    case "lists":
      return item
        ? { title: "One lead list", path: "/v1/workspaces/{ws}/lead-lists/{id}", params: { ...base, id: item }, mcp: "get_lead_list", ask: "How many leads did the last refresh add, and what did it skip?", scope: "leads.read" }
        : { title: "Lead lists", path: "/v1/workspaces/{ws}/lead-lists", params: base, mcp: "list_lead_lists", ask: "Build a list of 200 heads of growth at 50-200 person SaaS companies.", scope: "leads.read" };
    case "lead-finder":
      return { title: "Lead finder runs", path: "/v1/workspaces/{ws}/cloud/lead-finder/runs", params: base, ask: "Find 25 companies that fit our ideal customer profile.", scope: "leads.read" };
    case "email-finder":
      return { title: "Find a work email", path: "/v1/workspaces/{ws}/email-finder/find", params: base, mcp: "find_emails", ask: "Find the work email for Alice Nguyen at acme.example.", scope: "leads.read" };
    case "suppression":
      return { title: "Suppression list", path: "/v1/workspaces/{ws}/suppressions", params: base, mcp: "list_suppressions", ask: "Is anyone at acme-com.example suppressed?", scope: "leads.read" };
    case "mailboxes":
      return { title: "Mailboxes", path: "/v1/workspaces/{ws}/mailboxes", params: base, mcp: "list_mailboxes", ask: "Which mailboxes are failing to sign in?", scope: "mailboxes.read" };
    case "domains":
      return item
        ? { title: "One domain", path: "/v1/workspaces/{ws}/sending-domains/{domain}", params: { ...base, domain: item }, mcp: "get_domain", ask: `What do I need to fix on ${item}?`, scope: "mailboxes.read" }
        : { title: "Sending domains", path: "/v1/workspaces/{ws}/sending-domains", params: base, mcp: "list_domains", ask: "Which of my sending domains need attention?", scope: "mailboxes.read" };
    case "infrastructure":
      return item && item !== "new"
        ? { title: "One plan", path: "/v1/workspaces/{ws}/infra-plans/{id}", params: { ...base, id: item }, mcp: "get_infra_plan", ask: "What is left to do on this plan, and when will it be ready?", scope: "infrastructure.read" }
        : { title: "Infrastructure", path: "/v1/workspaces/{ws}/infrastructure", params: base, mcp: "get_infrastructure", ask: "Plan 10 domains and 30 mailboxes for this project, ready by the 14th.", scope: "infrastructure.read" };
    case "allocation":
      return { title: "Allocation needing a person", path: "/v1/workspaces/{ws}/allocation/attention", params: base, mcp: "get_allocation_attention", ask: "Which sequences are short of mailboxes, and why?", scope: "sequences.read" };
    case "senders":
      return { title: "Senders", path: "/v1/workspaces/{ws}/senders", params: base, mcp: "list_senders", ask: "Which mailboxes have no confirmed sender?", scope: "mailboxes.read" };
    case "servers":
      return item
        ? { title: "One server", path: "/v1/workspaces/{ws}/sending-servers/{id}", params: { ...base, id: item }, mcp: "get_sending_server", ask: "Is this server's IP on any blocklist, and what should I do about it?", scope: "mailboxes.read" }
        : { title: "Sending servers", path: "/v1/workspaces/{ws}/sending-servers", params: base, mcp: "list_sending_servers", ask: "Are any of my sending servers' IPs blocklisted?", scope: "mailboxes.read" };
    case "lifecycle":
      return { title: "Lifecycle email setup and sending", path: "/v1/workspaces/{ws}/email-overview", params: base, mcp: "get_email_overview", ask: "Set up Amazon SES so my app can send password resets and receipts from example.com.", scope: "emails.read" };
    case "emails":
      return item
        ? { title: "One email and its delivery", path: "/v1/workspaces/{ws}/emails/{id}", params: { ...base, id: item }, mcp: "get_email", ask: "Why did this email not reach the person?", scope: "emails.read" }
        : { title: "Sent emails", path: "/v1/workspaces/{ws}/emails", params: base, query: { limit: 50 }, mcp: "list_emails", ask: "Which receipts bounced this week, and why?", scope: "emails.read" };
    case "templates":
      return item
        ? { title: "One template", path: "/v1/workspaces/{ws}/email-templates/{id}", params: { ...base, id: item }, mcp: "get_email_template", ask: "Rewrite this template to be shorter and friendlier, keeping its variables.", scope: "emails.read" }
        : { title: "Email templates", path: "/v1/workspaces/{ws}/email-templates", params: base, mcp: "list_email_templates", ask: "Write a welcome email template for new sign-ups with a first_name variable.", scope: "emails.read" };
    case "broadcasts":
      return { title: "Email broadcasts", path: "/v1/workspaces/{ws}/email-broadcasts", params: base, mcp: "list_email_broadcasts", ask: "Draft our October newsletter to everyone in the newsletter topic, using the monthly template, and tell me how many people it will reach.", scope: "emails.read" };
    case "journeys":
      return item
        ? { title: "One journey", path: "/v1/workspaces/{ws}/journeys/{id}", params: { ...base, id: item }, mcp: "get_journey", ask: "How is this journey doing, and where do people drop out?", scope: "emails.read" }
        : { title: "Journeys", path: "/v1/workspaces/{ws}/journeys", params: base, mcp: "list_journeys", ask: "Build an onboarding journey: when someone signs up, send the welcome email, wait two days, and send tips unless they upgraded.", scope: "emails.read" };
    case "rules":
      return item
        ? { title: "One rule", path: "/v1/workspaces/{ws}/rules/{id}", params: { ...base, id: item }, mcp: "get_rule", ask: "What would this rule have done over the last 30 days, and which journeys start on its event?", scope: "customers.read" }
        : { title: "Rules", path: "/v1/workspaces/{ws}/rules", params: base, mcp: "list_rules", ask: "Make a rule that emits customer.stalled when someone signs up and doesn't activate within 7 days, unless they're on enterprise, and dry-run it.", scope: "customers.read" };
    case "topics":
      return { title: "Email topics", path: "/v1/workspaces/{ws}/email-topics", params: base, mcp: "list_email_topics", ask: "Add topics for product updates and our monthly newsletter, and put my lifecycle templates in the right one.", scope: "emails.read" };
    case "providers":
      return { title: "Email providers", path: "/v1/workspaces/{ws}/email-providers", params: base, mcp: "list_email_providers", ask: "Is my email provider still in the SES sandbox, and are my sending domains verified?", scope: "emails.read" };
    case "knowledge":
      return { title: "Knowledge", path: "/v1/workspaces/{ws}/knowledge/items", params: base, mcp: "search_knowledge", ask: "Write down our refund policy: full refunds within 30 days, no questions asked.", scope: "workspace.read" };
    case "company":
      return { title: "What Outflare knows about the company", path: "/v1/workspaces/{ws}/context", params: base, mcp: "get_context", ask: "What do you know about my company, and what is blocking my outreach?", scope: "workspace.read" };
    case "brands":
      return item
        ? { title: "A brand's context", path: "/v1/workspaces/{ws}/brands/{brand}/context", params: { ...base, brand: item }, mcp: "get_brand_context", ask: "Read this brand's website and fill in what it is, who it's for and how it sounds.", scope: "workspace.read" }
        : { title: "Brands", path: "/v1/workspaces/{ws}/brands", params: base, mcp: "list_brands", ask: "Which brands do we market, and what belongs to each?", scope: "workspace.read" };
    case "ideal-customers":
      return { title: "Ideal customer profiles", path: "/v1/workspaces/{ws}/ideal-customer-profiles", params: base, mcp: "list_ideal_customer_profiles", ask: "Write an ideal customer profile from our best customers and link it to the AU project.", scope: "projects.read" };
    case "workspace":
      return { title: "The workspace", path: "/v1/workspaces/{ws}", params: base, scope: "workspace.read" };
    case "members":
      return { title: "Members", path: "/v1/workspaces/{ws}/members", params: base, scope: "members.read" };
    case "alerts":
      return { title: "Alerts", path: "/v1/workspaces/{ws}/alerts", params: base, mcp: "list_alerts", ask: "What needs me right now?", scope: "workspace.read" };
    case "digests":
      return { title: "Digests", path: "/v1/workspaces/{ws}/digests", params: base, mcp: "preview_digest", ask: "Summarise how the whole workspace did this week, and what needs me first.", scope: "workspace.read" };
    case "connectors":
      if (item) return { title: "A supplier's mailboxes", path: "/v1/workspaces/{ws}/integrations/{id}/mailboxes", params: { ...base, id: item }, mcp: "list_supplier_mailboxes", ask: "Import every active mailbox from my supplier that isn't imported yet, and tell me how long the Google ones will take.", scope: "mailboxes.read" };
      return { title: "Connected services", path: "/v1/workspaces/{ws}/integrations", params: base, mcp: "list_integrations", ask: "Which of my connected services need attention, and how many verification credits are left?", scope: "integrations.read" };
    case "webhooks":
      return item
        ? { title: "A webhook's deliveries", path: "/v1/workspaces/{ws}/webhooks/{id}/deliveries", params: { ...base, id: item }, mcp: "list_webhook_deliveries", ask: "Which deliveries to this webhook failed this week, and why?", scope: "webhooks.read" }
        : { title: "Webhooks", path: "/v1/workspaces/{ws}/webhooks", params: base, mcp: "list_webhooks", ask: "Are any of my webhooks failing?", scope: "webhooks.read" };
    case "tokens":
      return { title: "API tokens", path: "/v1/workspaces/{ws}/api-tokens", params: base, scope: "tokens.manage" };
    case "logs":
      return { title: "Request logs", path: "/v1/workspaces/{ws}/logs", params: { ...base, level: "warning,error", since: "1h" }, mcp: "list_logs", ask: "Which of my calls failed in the last hour, and why?", scope: "audit.read" };
    case "audit":
      return { title: "Audit log", path: "/v1/workspaces/{ws}/audit-events", params: base, scope: "audit.read" };
    case "instance":
      return { title: "Usage stats and updates", path: "/v1/instance/telemetry", params: {}, scope: "signed-in user (not API tokens)" };
    case "account":
      return { title: "Your passkeys", path: "/v1/me/passkeys", params: {}, scope: "signed-in user (not API tokens)" };
  }
}

type Lang = "curl" | "ts" | "mcp";

export function ApiPanel({ ws, tab, item, onClose }: { ws: string; tab: Tab; item?: string; onClose: () => void }) {
  const [lang, setLang] = useState<Lang>(() => {
    try {
      return (localStorage.getItem("api-lang") as Lang) || "curl";
    } catch {
      return "curl";
    }
  });
  useEffect(() => {
    try {
      localStorage.setItem("api-lang", lang);
    } catch {
      // Not remembered; fine.
    }
  }, [lang]);

  const call = callFor(tab, ws, item);
  const origin = location.origin;
  const qs = call.query ? new URLSearchParams(Object.entries(call.query).map(([k, v]) => [k, String(v)])).toString() : "";
  const url = call.path.replace(/\{(\w+)\}/g, (_, k) => encodeURIComponent(call.params[k])) + (qs ? `?${qs}` : "");
  const tsQuery = call.query ? `, query: { ${Object.entries(call.query).map(([k, v]) => `${k}: ${JSON.stringify(v)}`).join(", ")} }` : "";
  const pathParams = Object.entries(call.params).map(([k, v]) => `${k}: "${v}"`).join(", ");
  const snippets: Record<Lang, string> = {
    curl: `curl "${origin}${url}" \\\n  -H "Authorization: Bearer $OUTFLARE_TOKEN"`,
    ts: `import { createClient, ok } from "@outflare/sdk";\n\nconst gtm = createClient({\n  baseUrl: "${origin}",\n  token: process.env.OUTFLARE_TOKEN,\n});\n\nconst data = ok(await gtm.GET("${call.path}", {\n  params: { path: { ${pathParams} }${tsQuery} },\n}));`,
    mcp: call.mcp
      ? `claude mcp add --transport http outflare ${origin}/mcp\n\n# Tool: ${call.mcp}\n# Ask: "${call.ask}"`
      : "# No MCP tool for this page yet.\n# Use the REST call instead.",
  };

  return (
    <aside aria-label="API for this page" className="flex h-full flex-col border-l bg-sidebar">
      <div className="flex h-12 items-center justify-between border-b px-4">
        <div className="flex items-center gap-2 font-medium">
          <Icon name="terminal" className="size-4 text-brand-strong dark:text-brand" />
          API for this page
        </div>
        <button onClick={onClose} aria-label="Close API panel" className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground">
          <Icon name="x" />
        </button>
      </div>
      <div className="flex-1 space-y-4 overflow-y-auto p-4">
        <div>
          <div className="text-[13px] font-medium">{call.title}</div>
          <div className="mt-1.5 flex items-center gap-2 font-mono text-[12px]">
            <span className="rounded bg-green-500/15 px-1.5 py-0.5 font-semibold text-green-700 dark:text-green-400">GET</span>
            <span className="truncate text-muted-foreground" title={call.path}>{call.path.replace("/v1/workspaces/{ws}", "…")}</span>
          </div>
          <div className="mt-1.5 text-[12px] text-muted-foreground">Token scope: <span className="font-mono text-foreground">{call.scope}</span></div>
        </div>

        <Tabs value={lang} onValueChange={(v) => setLang(v as Lang)} className="gap-0 overflow-hidden rounded-lg border bg-card">
          <div className="flex items-center border-b pr-1">
            <TabsList className="h-9 rounded-none bg-transparent p-0">
              {(["curl", "ts", "mcp"] as Lang[]).map((l) => (
                <TabsTrigger key={l} value={l} className="h-9 rounded-none border-0 border-b-2 border-transparent px-3 font-mono text-[12px] data-[state=active]:border-brand data-[state=active]:bg-transparent data-[state=active]:shadow-none dark:data-[state=active]:border-brand dark:data-[state=active]:bg-transparent">
                  {{ curl: "cURL", ts: "TypeScript", mcp: "MCP" }[l]}
                </TabsTrigger>
              ))}
            </TabsList>
            <span className="flex-1" />
            <CopyButton text={snippets[lang]} label="Copy snippet" />
          </div>
          {(["curl", "ts", "mcp"] as Lang[]).map((l) => (
            <TabsContent key={l} value={l}>
              <pre className="overflow-x-auto p-3 text-[12px] leading-5 text-foreground/90"><code>{snippets[l]}</code></pre>
            </TabsContent>
          ))}
        </Tabs>

        <dl className="space-y-2 text-[12px]">
          <div className="flex items-center justify-between gap-2">
            <dt className="text-muted-foreground">Workspace ID</dt>
            <dd className="flex items-center font-mono">{ws.slice(0, 8)}…<CopyButton text={ws} label="Copy workspace ID" /></dd>
          </div>
          <div className="flex items-center justify-between gap-2">
            <dt className="text-muted-foreground">Base URL</dt>
            <dd className="flex items-center font-mono">{origin}/v1<CopyButton text={`${origin}/v1`} label="Copy base URL" /></dd>
          </div>
        </dl>

        <ul className="space-y-1 border-t pt-3 text-[12px]">
          <li><a className="text-muted-foreground underline decoration-border underline-offset-2 hover:text-foreground" href="/v1/openapi.yaml" target="_blank" rel="noreferrer">OpenAPI spec for this instance</a></li>
          <li className="text-muted-foreground">Create a token under <span className="font-medium text-foreground">Settings → API tokens</span>.</li>
        </ul>
      </div>
    </aside>
  );
}

// ---- Command palette ----

export type Command = { id: string; label: string; group: string; icon: IconName; hint?: string; run: () => void };

export function CommandPalette({ open, onClose, commands }: { open: boolean; onClose: () => void; commands: Command[] }) {
  const groups = [...new Set(commands.map((c) => c.group))];
  return (
    <CommandDialog open={open} onOpenChange={(o) => !o && onClose()} title="Command palette" description="Jump to a page or run a command" showCloseButton={false} className="top-[12vh] translate-y-0 sm:max-w-xl">
      <CommandInput placeholder="Type a command or page…" className="text-[14px]" />
      <CommandList className="max-h-[50vh]">
        <CommandEmpty>No matches.</CommandEmpty>
        {groups.map((g) => (
          <CommandGroup key={g} heading={g} className="[&_[cmdk-group-heading]]:font-mono [&_[cmdk-group-heading]]:text-[10px] [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:uppercase">
            {commands.filter((c) => c.group === g).map((c) => (
              <CommandItem key={c.id} value={`${c.label} ${c.group}`} onSelect={() => { onClose(); track("ui.command_run", { command: c.id.replace(/-[0-9a-f-]{36}$/, "") }); c.run(); }} className="gap-2.5 py-2! text-[13px] [&_svg]:size-4!">
                <Icon name={c.icon} className="size-4 text-muted-foreground" />
                <span className="flex-1 truncate">{c.label}</span>
                {c.hint && <CommandShortcut className="font-mono text-[11px]">{c.hint}</CommandShortcut>}
              </CommandItem>
            ))}
          </CommandGroup>
        ))}
      </CommandList>
      <div className="flex items-center gap-3 border-t px-3 py-2 font-mono text-[11px] text-muted-foreground">
        <span className="flex items-center gap-1"><Kbd>↑</Kbd><Kbd>↓</Kbd> move</span>
        <span className="flex items-center gap-1"><Kbd>↵</Kbd> open</span>
        <span className="hidden items-center gap-1 sm:flex"><Kbd>G</Kbd> then a letter jumps to a page</span>
      </div>
    </CommandDialog>
  );
}

