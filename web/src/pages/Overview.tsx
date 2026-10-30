import { useEffect, useState } from "react";
import type { AttributionChannels, components, StoryResult, WorkspaceStats, WorkspaceStatus } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { api } from "../api";
import { Button, Card, Loading, PageHeader, Stat, Stats } from "../ui";
import { Icon } from "../icons";
import { plural, replyRate } from "../format";
import { track } from "@/lib/analytics";
import { useAgentApps } from "@/lib/agentApps";
import { AppMark } from "@/components/app-mark";
import { CommunityLinks } from "@/components/community-links";
import { openGetStartedAt } from "./GetStarted";
import { blockerTone, dotClass, type Tone } from "@/lib/status";
import { tabNames } from "../shell";
import { money, num, totals } from "./Channels";

type Go = (tab: "ai" | "get-started" | "members" | "inbox" | "sequences" | "mailboxes" | "domains" | "leads" | "company" | "servers" | "connectors" | "missions" | "allocation" | "infrastructure" | "analytics" | "fixes" | "lifecycle" | "social" | "setup" | "brands" | "channels" | "stories") => void;

// The page that resolves each area of blocker GET /status reports.
const areaTab: Record<WorkspaceStatus["blockers"][number]["area"], Parameters<Go>[0]> = {
  mailboxes: "mailboxes", domains: "domains", servers: "servers", connectors: "connectors", missions: "missions",
  sequences: "allocation", infrastructure: "infrastructure", inbox: "inbox", context: "company", analytics: "analytics", fixes: "fixes",
  // Customer data has no page of its own yet; Outflare's agent resolves it.
  lifecycle: "lifecycle", social: "social", customers: "ai", setup: "setup", brands: "brands",
};

// One row on the "Needs you" list: a status blocker, a rate that is off,
// or the next set-up step. Worst first, so red is always on top.
type Todo = { key: string; title: string; detail?: string; tone: Tone; tab: Parameters<Go>[0]; cta?: string; progress?: [number, number]; onOpen?: () => void };
const toneRank: Record<Tone, number> = { red: 0, amber: 1, blue: 2, green: 3, grey: 4 };

// limited: the person is kept to some brands, so the workspace's blockers
// (which need every brand) are left out.
export function Overview({ ws, name, isAdmin, go, limited = false }: { ws: string; name: string; isAdmin: boolean; go: Go; limited?: boolean }) {
  const [s, setS] = useState<WorkspaceStats | null>(null);
  const [status, setStatus] = useState<WorkspaceStatus | null>(null);
  const apps = useAgentApps(limited ? "" : ws);
  const { brand } = useBrand(ws);
  const handle = brand?.handle;
  const [onboarding, setOnboarding] = useState<components["schemas"]["Onboarding"] | null>(null);

  // Owners and admins see how far Get started got until it is finished or closed.
  useEffect(() => {
    if (isAdmin) api.GET("/v1/workspaces/{ws}/onboarding", { params: { path: { ws } } }).then(({ data }) => data && setOnboarding(data));
  }, [ws, isAdmin]);

  // The selected brand's setup checklist, until it is finished or closed.
  const [brandSetup, setBrandSetup] = useState<components["schemas"]["BrandSetup"] | null>(null);
  useEffect(() => {
    setBrandSetup(null);
    if (handle) api.GET("/v1/workspaces/{ws}/brands/{brand}/setup", { params: { path: { ws, brand: handle } } }).then(({ data }) => data && setBrandSetup(data));
  }, [ws, handle]);

  function open(step: components["schemas"]["OnboardingStep"]) {
    if (!step.walkthrough) return go("members");
    openGetStartedAt(step.id);
    go("get-started");
  }

  async function closeOnboarding() {
    setOnboarding(null);
    await api.PATCH("/v1/workspaces/{ws}/onboarding", { params: { path: { ws } }, body: { dismissed: true } });
  }

  useEffect(() => {
    const load = async () => {
      const [stats, st] = await Promise.all([
        api.GET("/v1/workspaces/{ws}/stats", { params: { path: { ws }, query: handle ? { brand: handle } : {} } }),
        limited ? Promise.resolve({ data: undefined }) : api.GET("/v1/workspaces/{ws}/status", { params: { path: { ws }, query: handle ? { brand: handle } : {} } }),
      ]);
      if (stats.data) setS(stats.data);
      if (st.data) setStatus(st.data);
    };
    load();
    const t = setInterval(load, 30000);
    return () => clearInterval(t);
  }, [ws, limited, handle]);

  if (!s) return <Loading />;
  const m = s.mailboxes;
  // Replies this week can answer emails sent earlier, so a rate is only shown
  // once there is enough sending for it to mean something.
  const pct = (n: number) => `${Math.round(n * 1000) / 10}%`;
  const rate = replyRate(s.replied_leads_7d, s.contacted_leads_7d) ?? null;
  const bounceRate = s.sent_7d > 0 ? s.bounces_7d / s.sent_7d : 0;

  // What a new workspace needs, in order, until all are done.
  const contextDone = !status?.blockers.some((b) => b.code === "context_missing");
  const setup: { done: boolean; title: string; detail: string; tab: Parameters<Go>[0]; cta: string }[] = [
    { done: contextDone, title: "Tell Outflare about your company", detail: "What you do, your goals and how emails should sound. Every agent reads it, so you never explain it twice.", tab: "company", cta: "Add company details" },
    { done: m.active > 0, title: "Connect your mailboxes", detail: "Import the CSV from your inbox provider, or sign in with Google or Microsoft.", tab: "mailboxes", cta: "Connect mailboxes" },
    { done: s.leads > 0, title: "Import your leads", detail: "Upload a CSV from Apollo, Clay, your CRM or a spreadsheet. Only an email column is needed.", tab: "leads", cta: "Import leads" },
    { done: s.active_sequences > 0, title: "Start a sequence", detail: "Write your steps, preview them for a real lead, add a lead list and activate it.", tab: "sequences", cta: "Create a sequence" },
  ];
  const nextStep = setup.find((x) => !x.done);

  // While Get started has steps left, it is the set-up row on Home: it
  // covers connecting an AI, so the other set-up steps wait.
  // Once every Get started step is done or skipped, the brand's own
  // checklist takes over.
  const checklist = onboarding && !onboarding.dismissed && onboarding.steps.some((x) => x.walkthrough && !x.done && !x.skipped) ? onboarding : null;

  // Everything that needs a person, in one list: the blockers agents read
  // from GET /status (the set-up row covers the empty-workspace ones and
  // includes open errors on the fixes list), the rates, and set-up.
  const todos: Todo[] = [];
  for (const b of status?.blockers ?? []) {
    if (b.code === "no_mailboxes" || b.code === "context_missing") continue;
    todos.push({ key: b.code, title: b.title, tone: blockerTone[b.severity], tab: areaTab[b.area] });
  }
  if (!status) {
    if (s.unread_replies > 0) todos.push({ key: "unread", title: `${plural(s.unread_replies, "reply is", "replies are")} unread`, tone: "grey", tab: "inbox" });
    if (m.error > 0) todos.push({ key: "mailbox_error", title: `${plural(m.error, "mailbox is", "mailboxes are")} not sending`, tone: "red", tab: "mailboxes" });
  }
  if (s.domains.need_attention > 0 && !status?.blockers.some((b) => b.code === "domain_critical"))
    todos.push({ key: "domains", title: `${plural(s.domains.need_attention, "sending domain needs", "sending domains need")} attention`, tone: "amber", tab: "domains", cta: "See what to fix" });
  if (bounceRate > 0.02) todos.push({ key: "bounces", title: `Bounce rate is ${pct(bounceRate)} this week, keep it under 2%`, tone: "red", tab: "leads", cta: "Check your list" });

  if (checklist) {
    // Counted like Get started: its steps, not the team invite after it.
    const steps = checklist.steps.filter((x) => x.walkthrough);
    const next = checklist.steps.find((x) => x.id === checklist.next_step) ?? checklist.steps.find((x) => !x.done);
    todos.push({
      key: "onboarding", title: `Finish setting up ${name}`, detail: next ? `Next: ${next.title}` : undefined, tone: "blue", tab: "get-started",
      cta: "Continue", progress: [steps.filter((x) => x.done).length, steps.length], onOpen: next ? () => open(next) : undefined,
    });
  } else if (brandSetup && !brandSetup.complete && !brandSetup.dismissed) {
    // One set-up row at a time: Get started first, then the brand's own
    // checklist, then what outreach needs.
    const next = brandSetup.steps.find((x) => x.id === brandSetup.next_step);
    todos.push({
      key: "brand_setup", title: `Continue setting up ${brandSetup.brand}`, detail: next ? `Next: ${next.title}` : undefined, tone: "blue", tab: "setup",
      cta: "Continue", progress: [brandSetup.done, brandSetup.total],
    });
  } else if (nextStep) {
    todos.push({ key: "setup", title: nextStep.title, detail: nextStep.detail, tone: "blue", tab: nextStep.tab, cta: nextStep.cta, progress: [setup.filter((x) => x.done).length, setup.length] });
  }
  todos.sort((a, b) => toneRank[a.tone] - toneRank[b.tone]);

  return (
    <div className="space-y-6">
      <PageHeader title="Home" description={`What needs you in ${name}, and how outreach is going. Updates every 30 seconds.`} />

      {!checklist && !limited && apps !== null && apps.length === 0 && (
        <section aria-labelledby="home-ai" className="flex flex-col gap-4 rounded-lg border border-brand-strong/40 bg-brand-strong/5 p-5 sm:flex-row sm:items-center dark:border-brand/40 dark:bg-brand/5">
          <div className="flex shrink-0 -space-x-2" aria-hidden>
            <AppMark app="claude" className="ring-2 ring-background" />
            <AppMark app="chatgpt" className="ring-2 ring-background" />
            <AppMark app="cursor" className="ring-2 ring-background" />
          </div>
          <div className="min-w-0 flex-1">
            <h2 id="home-ai" className="text-[15px] font-semibold">Connect your AI and just ask</h2>
            <p className="text-[13px] text-muted-foreground">Add Outflare to Claude, ChatGPT or Cursor with one URL and a sign-in. Then tell it what you want: it sets up mailboxes, builds lists, writes sequences and answers replies, within the limits you set.</p>
          </div>
          <Button onClick={() => { track("ui.agent_connect_prompted", { from: "home" }); go("ai"); }} className="shrink-0">Connect your AI</Button>
        </section>
      )}

      <section aria-labelledby="home-todo" className="overflow-hidden rounded-lg border bg-card shadow-xs">
        <div className="flex items-center gap-3 border-b px-4 py-3">
          <h2 id="home-todo" className="flex-1 font-medium">Needs you{todos.length > 0 && <span className="ml-2 font-mono text-[12px] text-muted-foreground">{todos.length}</span>}</h2>
          {checklist && (
            <button aria-label="Hide set-up steps" title="Hide set-up. Open Get started from the command palette (⌘K) any time." onClick={closeOnboarding} className="-m-1 rounded-md p-2 text-[12px] text-muted-foreground hover:bg-accent hover:text-foreground">
              Hide set-up
            </button>
          )}
        </div>
        {todos.length === 0 ? (
          <p className="flex items-center gap-3 px-4 py-3 text-[14px] text-muted-foreground">
            <span aria-hidden="true" className="flex size-5 items-center justify-center rounded-full bg-green-500/15 text-green-600 dark:text-green-400"><Icon name="check" className="size-3" /></span>
            Nothing needs you right now.
          </p>
        ) : (
          <ul className="divide-y">
            {todos.map((x) => (
              <li key={x.key}>
                <button onClick={() => { track("ui.home_todo_opened", { code: x.key, tone: x.tone }); (x.onOpen ?? (() => go(x.tab)))(); }} className="group flex w-full items-center gap-3 px-4 py-2.5 text-left hover:bg-accent/50">
                  <span aria-hidden="true" className={`size-2 shrink-0 rounded-full ${dotClass[x.tone]}`} />
                  <span className="sr-only">{x.tone === "red" ? "Urgent: " : x.tone === "amber" ? "Should fix: " : ""}</span>
                  <span className="min-w-0 flex-1">
                    <span className="block text-[14px]">{x.title}</span>
                    {x.detail && <span className="block truncate text-[13px] text-muted-foreground">{x.detail}</span>}
                  </span>
                  {x.progress && (
                    <span className="hidden shrink-0 items-center gap-2 sm:flex" aria-label={`${x.progress[0]} of ${x.progress[1]} done`}>
                      <span className="h-1.5 w-16 overflow-hidden rounded-full bg-border"><span className="block h-full bg-primary" style={{ width: `${(100 * x.progress[0]) / x.progress[1]}%` }} /></span>
                      <span className="font-mono text-[11px] text-muted-foreground">{x.progress[0]}/{x.progress[1]}</span>
                    </span>
                  )}
                  <span className="hidden shrink-0 text-[12px] text-muted-foreground group-hover:text-foreground sm:inline">{x.cta ?? tabNames[x.tab]}</span>
                  <Icon name="arrow" className="size-3.5 shrink-0 text-muted-foreground/70 group-hover:text-foreground" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <ChannelTiles ws={ws} brand={handle} go={go} />
      <PRTiles ws={ws} brand={handle} go={go} />

      <CommunityLinks />

      <Stats>
        <Stat label="Sent · 24h" value={s.sent_24h.toLocaleString()} sub={m.daily_capacity > 0 ? `of ${m.daily_capacity.toLocaleString()} daily capacity` : "no active mailboxes yet"} />
        <Stat label="Sent · 7d" value={s.sent_7d.toLocaleString()} />
        <Stat label="Replies · 7d" value={s.replies_7d.toLocaleString()} sub={rate === null ? undefined : `${rate} reply rate`} />
        <Stat label="Bounces · 7d" value={s.bounces_7d.toLocaleString()} tone={bounceRate > 0.02 ? "red" : undefined} sub={s.sent_7d > 0 ? `${pct(bounceRate)} bounce rate, keep under 2%` : undefined} />
      </Stats>

      <div className="grid gap-4 md:grid-cols-2">
        <Card title="Mailboxes" action={<button onClick={() => go("mailboxes")} className="font-mono text-[12px] text-muted-foreground hover:text-foreground">View all →</button>}>
          <Rows rows={[["Sending", m.active], ["Not sending", m.error, m.error > 0], ["Paused", m.paused], ["Checking", m.verifying], ["Warming up", m.warming]]} />
          <button onClick={() => go("domains")} className="mt-2 text-[12px] text-muted-foreground hover:text-foreground">
            {plural(s.domains.total, "sending domain")}
            {s.domains.need_attention > 0 && <span className="text-amber-700 dark:text-amber-400">, {s.domains.need_attention} need attention</span>}
            {s.domains.paused > 0 && <>, {s.domains.paused} paused</>} →
          </button>
        </Card>
        <Card title="Outreach" action={<button onClick={() => go("sequences")} className="font-mono text-[12px] text-muted-foreground hover:text-foreground">View sequences →</button>}>
          <Rows rows={[["Active sequences", s.active_sequences], ["Leads being emailed", s.active_enrollments], ["Leads in total", s.leads]]} />
        </Card>
      </div>
    </div>
  );
}

function Rows({ rows }: { rows: [string, number, boolean?][] }) {
  return (
    <dl className="divide-y divide-border">
      {rows.map(([label, value, bad]) => (
        <div key={label} className="flex justify-between py-1.5">
          <dt className="text-muted-foreground">{label}</dt>
          <dd className={`font-mono tabular-nums ${bad ? "text-red-600 dark:text-red-400" : ""}`}>{value.toLocaleString()}</dd>
        </div>
      ))}
    </dl>
  );
}

// ChannelTiles is the CMO view's headline on Home: the last 28 days across
// every channel, under the workspace's default model. It stays hidden until
// the site has visits or conversions, and for anyone who can't see Channels.
function ChannelTiles({ ws, brand, go }: { ws: string; brand?: string; go: Go }) {
  const [report, setReport] = useState<AttributionChannels | null>(null);
  useEffect(() => {
    const to = new Date();
    const day = (d: Date) => d.toISOString().slice(0, 10);
    const query = { from: day(new Date(to.getTime() - 27 * 86_400_000)), to: day(to), ...(brand ? { brand } : {}) };
    api.GET("/v1/workspaces/{ws}/attribution/channels", { params: { path: { ws }, query } }).then(({ data }) => setReport(data ?? null));
  }, [ws, brand]);
  if (!report) return null;
  const t = totals(report);
  if (t.visits === 0 && t.conversions === 0) return null;
  return (
    <section aria-labelledby="home-channels" className="space-y-2">
      <div className="flex items-center">
        <h2 id="home-channels" className="flex-1 text-[13px] font-medium text-muted-foreground">Channels · 28 days</h2>
        <button onClick={() => go("channels")} className="font-mono text-[12px] text-muted-foreground hover:text-foreground">By channel →</button>
      </div>
      <Stats>
        <Stat label="Visits" value={num(t.visits)} />
        <Stat label="Conversions" value={num(t.conversions)} />
        <Stat label="Revenue" value={money(t.revenue)} sub="net of refunds" />
        <Stat label="Cost" value={money(t.cost)} />
      </Stats>
    </section>
  );
}

// PRTiles is digital PR's line in the CMO view: the last 28 days of story
// pitches, replies and the coverage that followed. It stays hidden until a
// story was pitched or covered, and for anyone who can't read stories.
function PRTiles({ ws, brand, go }: { ws: string; brand?: string; go: Go }) {
  const [total, setTotal] = useState<Omit<StoryResult, "story_id" | "brand_id" | "title" | "status"> | null>(null);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/stories/results", { params: { path: { ws }, query: brand ? { brand } : {} } }).then(({ data }) => setTotal(data?.total ?? null));
  }, [ws, brand]);
  if (!total || (total.pitched === 0 && total.coverage === 0)) return null;
  const wanted = (total.reply_kinds.wants_data ?? 0) + (total.reply_kinds.wants_interview ?? 0);
  return (
    <section aria-labelledby="home-pr" className="space-y-2">
      <div className="flex items-center">
        <h2 id="home-pr" className="flex-1 text-[13px] font-medium text-muted-foreground">Digital PR · 28 days</h2>
        <button onClick={() => go("stories")} className="font-mono text-[12px] text-muted-foreground hover:text-foreground">By story →</button>
      </div>
      <Stats>
        <Stat label="Journalists pitched" value={num(total.pitched)} />
        <Stat label="Replies" value={num(total.replied)} sub={wanted ? `${num(wanted)} want data or an interview` : undefined} />
        <Stat label="Articles" value={num(total.coverage)} />
        <Stat label="Linked articles" value={num(total.linked)} />
      </Stats>
    </section>
  );
}
