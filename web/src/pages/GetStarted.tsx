import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import type { components, Connector, ConnectorCategory } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, ErrorText, field, Input, Loading, Logo, toast } from "../ui";
import { Icon, type IconName } from "../icons";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";
import { hostedMcpUrl, useAgentApps, appLabels } from "@/lib/agentApps";
import type { Tab } from "../App";
import { ConnectDialog, ConnectorCard, startConnectorOAuth, takeConnectOutcome, authOption } from "./Connectors";
import { useSiteSetupAfterConnect } from "@/components/analytics-sites";
import { setupPrompt } from "./ConnectAI";
import { CommunityLinks } from "@/components/community-links";
import { BrandDNS } from "@/components/brand-dns";
import { InstallStep } from "./BrandSetup";

type Onboarding = components["schemas"]["Onboarding"];
type StepId = components["schemas"]["OnboardingStepId"];
type Goal = components["schemas"]["OnboardingGoal"];

// The step to open Get started on, set by Home's checklist.
let startAt: StepId | undefined;
export function openGetStartedAt(step: StepId) {
  startAt = step;
}

// The Get started page: a full-page walk-through, without the app around
// it, that sets up one brand and ends with Outflare doing something: the
// brand, an AI key (and why it's needed), what to do first, the brand's
// website script and every DNS record in one go, the tools that goal
// needs, a star and a follow, then a first mission. More brands, and the
// rest of this one's checklist, come after. Inviting the team waits for
// Home. Whether a step is done comes from the workspace
// itself, so adding a brand on the Brands page or connecting a tool over
// MCP ticks it off here too. Every step, and the whole thing, can be
// skipped.
export function GetStarted({ ws, wsName, from, go }: { ws: string; wsName: string; from: string; go: (t: Tab) => void }) {
  const [o, setO] = useState<Onboarding | null>(null);
  const [error, setError] = useState("");
  // The step on screen; null shows the end.
  const [at, setAt] = useState<StepId | null | undefined>(undefined);
  const path = { params: { path: { ws } } };

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/onboarding", { params: { path: { ws } } });
    if (error) return setError(errorMessage(error));
    setO(data);
    // First load: start on the step Home asked for, else the first step
    // of the walk-through left to do.
    const asked = data.steps.find((s) => s.id === startAt && s.walkthrough)?.id;
    startAt = undefined;
    setAt((cur) => (cur === undefined ? (asked ?? data.steps.find((s) => s.walkthrough && !s.done && !s.skipped)?.id ?? null) : cur));
    return data;
  }, [ws]);

  const siteSetup = useSiteSetupAfterConnect(ws);
  useEffect(() => {
    void siteSetup.after(takeConnectOutcome());
    load();
    track("ui.onboarding_opened", { from });
  }, [load]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (o && at !== undefined) track("ui.onboarding_step_viewed", { step: at ?? "summary", done: o.done });
  }, [at]); // eslint-disable-line react-hooks/exhaustive-deps

  const steps = o?.steps.filter((s) => s.walkthrough) ?? [];
  const index = at ? steps.findIndex((s) => s.id === at) : steps.length;
  const step = at ? steps[index] : undefined;

  async function patch(body: components["schemas"]["OnboardingInput"]) {
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/onboarding", { ...path, body });
    if (error || !data || !("steps" in data)) {
      toast(errorMessage(error), "error");
      return null;
    }
    setO(data);
    return data;
  }

  // The next walk-through step after this one that is neither done nor
  // skipped, else the end.
  const after = (i: number, state: Onboarding) => state.steps.filter((s) => s.walkthrough).slice(i + 1).find((s) => !s.done && !s.skipped)?.id ?? null;

  async function skip() {
    if (!step) return;
    const next = await patch({ skip: [step.id] });
    if (next) setAt(after(index, next));
  }

  async function next() {
    const fresh = await load();
    if (fresh) setAt(after(index, fresh));
  }

  async function pickGoal(goal: Goal) {
    const next = await patch({ goal });
    if (next) setAt(after(index, next));
  }

  // Leaving at the end closes it when everything is done; otherwise Home
  // keeps a checklist until it is hidden there.
  async function finish(to: Tab = "overview") {
    if (o && o.steps.every((s) => s.done || s.skipped || !s.walkthrough) && o.done === o.total) await patch({ dismissed: true });
    go(to);
  }

  function later() {
    track("ui.onboarding_left", { step: at ?? "summary", done: o?.done ?? 0 });
    go("overview");
  }

  return (
    <div className="flex min-h-svh flex-col bg-background">
      {siteSetup.dialog}
      <header className="flex items-center justify-between gap-4 px-4 py-4 sm:px-8">
        <Logo className="text-[15px]" />
        {at !== null && (
          <button onClick={later} className="text-[13px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline">Skip setup</button>
        )}
      </header>
      <main className="flex flex-1 justify-center px-4 pt-6 pb-16 sm:pt-12">
        <div className="w-full max-w-xl space-y-8">
          {!o || at === undefined ? (
            error ? <ErrorText>{error}</ErrorText> : <Loading />
          ) : (
            <>
              <div aria-hidden="true" className="flex gap-1.5">
                {/* Done steps are green, the one on screen is solid, the rest are empty. */}
                {steps.map((s, i) => <span key={s.id} className={cn("h-1 flex-1 rounded-full", i === index ? "bg-primary" : s.done || s.skipped ? "bg-green-500/60" : "bg-border")} />)}
              </div>
              <section aria-live="polite" className="space-y-6">
                {step ? (
                  <>
                    <div className="space-y-1.5">
                      <p className="text-[12px] text-muted-foreground">Step {index + 1} of {steps.length}</p>
                      <h1 className="text-2xl font-semibold tracking-tight">{step.id === "website" && o.brand?.website ? `Connect ${o.brand.website.replace(/^https?:\/\//, "").replace(/\/$/, "")}` : headings[step.id] ?? step.title}</h1>
                      <p className="text-[15px] text-muted-foreground">{step.detail}</p>
                    </div>
                    {step.id === "brand" && <BrandStep ws={ws} wsName={wsName} done={step.done} onDone={next} go={go} />}
                    {step.id === "goal" && <GoalStep current={o.goal} onPick={pickGoal} />}
                    {step.id === "ai" && <AIStep ws={ws} onChange={load} />}
                    {step.id === "website" && <WebsiteStep ws={ws} brand={o.brand ?? null} onChange={load} onBack={() => setAt("brand")} />}
                    {step.id === "connect" && <ToolsStep ws={ws} goal={o.goal} onChange={load} go={go} />}
                    {step.id === "community" && <CommunityLinks step onClicked={load} />}
                    {step.id === "mission" && <MissionStep ws={ws} goal={o.goal} aiDone={!!o.steps.find((s) => s.id === "ai")?.done} onBack={() => setAt("ai")} onChange={load} go={finish} />}
                    <div className="flex flex-wrap items-center justify-between gap-3 pt-2">
                      <button onClick={() => setAt(index > 0 ? steps[index - 1].id : at)} disabled={index === 0} className="text-[13px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline disabled:invisible">Back</button>
                      <div className="flex items-center gap-4">
                        {!step.done && <button onClick={skip} className="text-[13px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline">Skip this step</button>}
                        {step.done && step.id !== "goal" && <Button onClick={next}>Continue</Button>}
                      </div>
                    </div>
                  </>
                ) : (
                  <Summary o={o} onOpen={setAt} onFinish={finish} />
                )}
              </section>
            </>
          )}
        </div>
      </main>
    </div>
  );
}

// What each step asks, as the page's heading.
const headings: Partial<Record<StepId, string>> = {
  brand: "Start with one brand",
  goal: "What should Outflare do first?",
  ai: "Add your AI key",
  website: "Connect your website",
  connect: "Connect what it needs",
  community: "Star and follow Outflare",
  mission: "Give it a goal",
};

// The goals a workspace can start with: the tools each needs and the
// first mission suggested for it.
const goals: { id: Goal; title: string; detail: string; icon: IconName; categories: ConnectorCategory[]; first: string[]; mission: string }[] = [
  { id: "outreach", title: "Find and email new customers", detail: "Build a list of people who fit, then reach them from your own mailboxes.", icon: "sequences", categories: ["crm", "data", "email_verification", "sequencer"], first: ["hubspot", "apollo"], mission: "Find 50 companies that look like our best customers and draft a three-email sequence to reach them" },
  { id: "search", title: "Grow search traffic", detail: "See which pages win and lose in search, and what to fix.", icon: "seo", categories: ["analytics", "seo"], first: ["google_search_console", "google_analytics"], mission: "Find the pages losing search traffic over the last 90 days and suggest a fix for each" },
  { id: "reports", title: "See what's working", detail: "One report across your analytics, CRM and campaigns.", icon: "reports", categories: ["analytics", "crm"], first: ["google_analytics", "posthog", "hubspot"], mission: "Report which channels brought us the most sign-ups in the last 30 days and what to do more of" },
  { id: "nurture", title: "Nurture existing customers", detail: "Emails that bring sign-ups and customers back at the right moment.", icon: "inbox", categories: ["crm", "analytics"], first: ["hubspot", "posthog"], mission: "Draft a welcome series for new sign-ups that gets them to their first win in a week" },
];

function GoalStep({ current, onPick }: { current: Goal | null; onPick: (g: Goal) => void }) {
  return (
    <div role="radiogroup" aria-label="What to do first" className="grid gap-2.5">
      {goals.map((g) => (
        <button
          key={g.id}
          role="radio"
          aria-checked={current === g.id}
          onClick={() => onPick(g.id)}
          className={cn("flex items-start gap-3 rounded-lg border bg-card p-4 text-left shadow-xs transition-colors hover:border-foreground/30 hover:bg-accent/40", current === g.id && "border-primary ring-1 ring-primary")}
        >
          <span aria-hidden="true" className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-md border bg-background"><Icon name={g.icon} className="size-4" /></span>
          <span className="min-w-0 flex-1">
            <span className="block font-medium">{g.title}</span>
            <span className="block text-[13px] text-muted-foreground">{g.detail}</span>
          </span>
          {current === g.id && <Icon name="check" className="mt-1 size-4 text-primary" />}
        </button>
      ))}
      <p className="text-[13px] text-muted-foreground">You can do all of these later. This only decides what we suggest next.</p>
    </div>
  );
}

// Connect your AI: the workspace's own model key, so Outflare's agent can
// work, or an AI app (Claude, ChatGPT) that runs Outflare over MCP. Either
// one does the step.
function AIStep({ ws, onChange }: { ws: string; onChange: () => void }) {
  const apps = useAgentApps(ws);
  const [url, setUrl] = useState("");
  useEffect(() => {
    hostedMcpUrl().then(setUrl);
  }, []);
  const app = apps?.[0];
  return (
    <div className="space-y-6">
      <div className="space-y-2 rounded-lg border bg-card p-4 text-[13px]">
        <p className="font-medium">Why Outflare needs your key</p>
        <ul className="space-y-1.5 text-muted-foreground">
          <li className="flex gap-2"><Icon name="check" className="mt-0.5 size-3.5 shrink-0 text-green-600 dark:text-green-400" />It's what lets Outflare's agent work: plan missions from a goal, write emails, research companies and check how AI answers talk about you.</li>
          <li className="flex gap-2"><Icon name="check" className="mt-0.5 size-3.5 shrink-0 text-green-600 dark:text-green-400" />You pay your AI provider directly, at their prices. Outflare never adds a margin, and never lends or shares a key of its own.</li>
          <li className="flex gap-2"><Icon name="check" className="mt-0.5 size-3.5 shrink-0 text-green-600 dark:text-green-400" />It's tested before it's saved, stored encrypted and never shown again. Set a monthly limit on AI usage in Settings.</li>
        </ul>
        <p className="text-muted-foreground">Without one, reports, tracking and connections still work; anything that needs AI waits until you add it.</p>
      </div>
      <Option title="Add your AI key" detail="Claude is a good first choice. Each card links to where you get a key.">
        <ConnectStep ws={ws} pick={(c) => c.category === "model"} first={["claude"]} limit={4} onChange={onChange} />
      </Option>
      {url && (
        <Option title="Or use Outflare from Claude or ChatGPT" detail="Ask in the AI app you already use. It signs in with your Outflare account, so there's nothing to copy.">
          {app ? (
            <p className="flex items-center gap-2 text-[13px]"><Icon name="check" className="size-3.5 text-green-600 dark:text-green-400" />{app.name || appLabels[app.app]} is connected.</p>
          ) : (
            <div className="flex flex-wrap gap-2">
              {(["claude", "chatgpt"] as const).map((a) => (
                <a
                  key={a}
                  href={`${a === "claude" ? "https://claude.ai/new" : "https://chatgpt.com/"}?q=${encodeURIComponent(setupPrompt(url))}`}
                  target="_blank"
                  rel="noreferrer"
                  onClick={() => track("ui.agent_install_opened", { client: "prompt", where: `get_started_${a}` })}
                  className="inline-flex h-9 items-center gap-1.5 rounded-md border px-3 text-[13px] shadow-xs hover:bg-accent"
                >
                  Set up in {a === "claude" ? "Claude" : "ChatGPT"} <Icon name="external" className="size-3" />
                </a>
              ))}
            </div>
          )}
        </Option>
      )}
    </div>
  );
}

function Option({ title, detail, children }: { title: string; detail: string; children: ReactNode }) {
  return (
    <div className="space-y-3">
      <div>
        <h2 className="text-[14px] font-medium">{title}</h2>
        <p className="text-[13px] text-muted-foreground">{detail}</p>
      </div>
      {children}
    </div>
  );
}

// The brand's website, in one step: the script on its pages, then every
// DNS record the brand needs, added in one go. Either part ticks off on its
// own once Outflare sees it (a first visit, the records in DNS).
function WebsiteStep({ ws, brand, onChange, onBack }: { ws: string; brand: Onboarding["brand"]; onChange: () => void; onBack: () => void }) {
  const [setup, setSetup] = useState<components["schemas"]["BrandSetup"] | null>(null);
  const [catalog, setCatalog] = useState<Connector[]>([]);
  const handle = brand?.handle;
  const load = useCallback(async () => {
    if (!handle) return;
    const { data } = await api.GET("/v1/workspaces/{ws}/brands/{brand}/setup", { params: { path: { ws, brand: handle } } });
    if (data) setSetup(data);
  }, [ws, handle]);
  useEffect(() => {
    void load();
    api.GET("/v1/workspaces/{ws}/connectors", { params: { path: { ws } } }).then(({ data }) => setCatalog(data?.data ?? []));
  }, [load, ws]);
  // While waiting for a first visit, look again so the step ticks off.
  const script = setup?.steps.find((s) => s.id === "site_script");
  useEffect(() => {
    if (script?.status !== "waiting") return;
    const t = setInterval(() => {
      void load();
      onChange();
    }, 8000);
    return () => clearInterval(t);
  }, [script?.status, load]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!brand || !brand.website) {
    return <p className="text-[14px] text-muted-foreground">This step needs your brand's website. <button className="underline underline-offset-2" onClick={onBack}>Add it</button> first.</p>;
  }
  if (!setup || !script?.install) return <Loading />;
  const part = (n: number, title: string, why: string, done: boolean, waiting: string | undefined) => (
    <div className="flex items-start gap-3">
      <span className={cn("mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full border font-mono text-[11px]", done && "border-transparent bg-brand text-brand-foreground")}>{done ? <Icon name="check" className="size-3.5" /> : n}</span>
      <div className="min-w-0">
        <h2 className="text-[14px] font-medium">{title}</h2>
        <p className="text-[13px] text-muted-foreground">{why}</p>
        {waiting && !done && <p className="mt-1 text-[12px] text-blue-600 dark:text-blue-400">{waiting}</p>}
      </div>
    </div>
  );
  return (
    <div className="space-y-8">
      <section className="space-y-3">
        {part(1, "Add the script to your pages", "One line on every page counts visits and sign-ups and where they came from.", script.done, script.waiting)}
        {!script.done && <div className="sm:pl-9"><InstallStep ws={ws} brand={brand.handle} step={script} install={script.install} canManage catalog={catalog} back="get-started" onChange={(d) => (d ? setSetup(d) : void load())} /></div>}
      </section>
      <section className="space-y-3">
        {part(2, "Add your DNS records, all at once", "Proves the site is yours so Outflare can audit it. Records for sending email and tracking links join this list when you set those up.", !!setup.steps.find((s) => s.id === "verify_site")?.done, setup.steps.find((s) => s.id === "verify_site")?.waiting)}
        <div className="sm:pl-9"><BrandDNS ws={ws} brand={brand.handle} website={brand.website} canManage page="get-started" onChange={() => { void load(); onChange(); }} /></div>
      </section>
    </div>
  );
}

// The tools the chosen goal needs, a few at most; every other one is a
// click away.
function ToolsStep({ ws, goal, onChange, go }: { ws: string; goal: Goal | null; onChange: () => void; go: (t: Tab) => void }) {
  const g = goals.find((x) => x.id === goal);
  const categories = g?.categories ?? toolCategories;
  return (
    <div className="space-y-6">
      <ConnectStep ws={ws} pick={(c) => categories.includes(c.category)} first={g?.first ?? ["google_analytics", "hubspot"]} limit={4} onChange={onChange} more={() => go("connectors")} />
      {goal === "outreach" && <DomainResearch ws={ws} onChange={onChange} go={go} />}
      {goal === "nurture" && (
        <p className="text-[13px] text-muted-foreground">
          Lifecycle emails go through your own email provider. <button className="underline underline-offset-2 hover:text-foreground" onClick={() => go("lifecycle")}>Set up lifecycle email</button> when you're ready.
        </p>
      )}
    </div>
  );
}

// The other tools worth connecting early, most useful first.
const toolCategories: ConnectorCategory[] = ["analytics", "crm", "data", "email_verification", "sequencer", "calendar", "seo", "notification"];
const rank = (c: Connector) => (toolCategories.indexOf(c.category) + 1 || 99);

// The first mission: a goal in plain words, already written for the goal
// picked, that the workspace's AI turns into a plan to approve. With no AI
// key but an AI app connected, the app is asked instead.
function MissionStep({ ws, goal, aiDone, onBack, onChange, go }: { ws: string; goal: Goal | null; aiDone: boolean; onBack: () => void; onChange: () => void; go: (t: Tab) => void }) {
  const [text, setText] = useState(goals.find((x) => x.id === goal)?.mission ?? goals[0].mission);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // Without an AI key it says so before anything is typed or pressed.
  const [needsKey, setNeedsKey] = useState(!aiDone);
  const [planned, setPlanned] = useState(false);
  const apps = useAgentApps(ws);
  const app = apps?.[0];

  async function plan(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error, response } = await api.POST("/v1/workspaces/{ws}/missions/plan", { params: { path: { ws } }, body: { goal: text.trim() } });
    setBusy(false);
    const code = (error as { error?: { code?: string } } | undefined)?.error?.code;
    if (response.status === 409 && code === "connector_needed") return setNeedsKey(true);
    if (error || !data) return setError(errorMessage(error));
    setPlanned(true);
    onChange();
  }

  if (planned) {
    return (
      <div className="space-y-4 rounded-lg border bg-card p-4 shadow-xs">
        <p className="flex items-center gap-2 font-medium"><Icon name="check" className="size-4 text-green-600 dark:text-green-400" />The plan is drafted.</p>
        <p className="text-[13px] text-muted-foreground">Review its steps, limits and the tools it asks for. Nothing runs until you approve it.</p>
        <Button onClick={() => go("missions")}>Review the plan</Button>
      </div>
    );
  }

  return (
    <form onSubmit={plan} className="space-y-3">
      <textarea className={`${field} min-h-24 w-full text-[15px]`} value={text} onChange={(e) => setText(e.target.value)} maxLength={2000} aria-label="Goal" disabled={busy} />
      {busy && <p className="text-[13px] text-muted-foreground">Your AI is drafting the plan. This takes up to a minute.</p>}
      {needsKey && (
        <div className="space-y-2 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2.5 text-[13px]">
          {app ? (
            <>
              <p>Outflare's own agent needs your AI key to plan this. Or ask {app.name || appLabels[app.app]}, which is already connected:</p>
              <CopyGoal text={text} />
            </>
          ) : (
            <p>Outflare's own agent needs your AI key to plan this. <button type="button" className="underline underline-offset-2" onClick={onBack}>Connect your AI</button> first.</p>
          )}
        </div>
      )}
      <ErrorText>{error}</ErrorText>
      <div className="flex flex-wrap items-center gap-3">
        <Button disabled={busy || !text.trim() || (!aiDone && needsKey)}>{busy ? "Planning…" : "Draft the plan"}</Button>
        <span className="text-[12px] text-muted-foreground">Nothing is sent until you approve it.</span>
      </div>
    </form>
  );
}

function CopyGoal({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      type="button"
      onClick={async () => {
        await navigator.clipboard?.writeText(`Using Outflare, plan a mission for this goal and show me the plan before anything runs: ${text}`);
        track("ui.copied");
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
    >
      <Icon name={copied ? "check" : "copy"} className="size-3.5" /> {copied ? "Copied" : "Copy the request"}
    </Button>
  );
}

function Summary({ o, onOpen, onFinish }: { o: Onboarding; onOpen: (s: StepId) => void; onFinish: (to?: Tab) => void }) {
  const brand = o.brand?.name;
  const left = o.steps.filter((s) => s.walkthrough && !s.done);
  const team = o.steps.find((s) => s.id === "team");
  return (
    <div className="space-y-6">
      <div className="space-y-1.5">
        <h1 className="text-2xl font-semibold tracking-tight">{left.length === 0 ? "You're set up" : "That's enough to start"}</h1>
        <p className="text-[15px] text-muted-foreground">
          {left.length === 0 ? "Outflare knows your business, has an AI and the tools your goal needs, and your first plan is waiting for you." : "Pick up the rest from Home whenever you're ready."}
        </p>
      </div>
      {left.length > 0 && (
        <ul className="divide-y rounded-lg border bg-card">
          {left.map((s) => (
            <li key={s.id} className="flex items-center justify-between gap-3 px-4 py-2.5 text-[14px]">
              <span>{s.title}{s.skipped && <span className="text-muted-foreground"> · skipped</span>}</span>
              <button onClick={() => onOpen(s.id)} className="text-[13px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline">Do it now</button>
            </li>
          ))}
        </ul>
      )}
      {brand && (
        <div className="space-y-2 rounded-lg border bg-card p-4">
          <p className="font-medium">Next: the rest of {brand}</p>
          <p className="text-[13px] text-muted-foreground">Analytics, Search Console, email, payments, ads, social and your CRM, one at a time and each skippable. Home keeps your place.</p>
          <Button onClick={() => onFinish("setup")}>Continue with {brand}</Button>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-4">
        <Button variant={brand ? "ghost" : undefined} onClick={() => onFinish()}>Go to Home</Button>
        {team && !team.done && <button onClick={() => onFinish("members")} className="text-[13px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline">Invite your team</button>}
      </div>
    </div>
  );
}

// The brand step: the website first, then the name (taken from the domain
// until it is typed) and a line on what it does. Sign-up made the
// workspace's first brand, so this fills that one in rather than adding
// another. It also fills the company's name and website when those are
// still empty, so agents know them from the first chat.
function BrandStep({ ws, wsName, done, onDone, go }: { ws: string; wsName: string; done: boolean; onDone: () => void; go: (t: Tab) => void }) {
  const [name, setName] = useState(wsName);
  const [named, setNamed] = useState(false);
  const [website, setWebsite] = useState("");
  const [about, setAbout] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [brands, setBrands] = useState<components["schemas"]["Brand"][] | null>(null);

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/brands", { params: { path: { ws } } }).then(({ data }) => setBrands(data?.data ?? []));
  }, [ws, done]);
  // The brand sign-up made, waiting for its website.
  const starter = brands?.find((b) => !b.website);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const path = { params: { path: { ws } } };
    const body = { name: name.trim(), website: website.trim() || undefined };
    const { data, error } = starter
      ? await api.PATCH("/v1/workspaces/{ws}/brands/{brand}", { params: { path: { ws, brand: starter.id } }, body })
      : await api.POST("/v1/workspaces/{ws}/brands", { ...path, body });
    if (error || !data || !("id" in data)) {
      setBusy(false);
      return setError(errorMessage(error));
    }
    if (about.trim()) await api.PATCH("/v1/workspaces/{ws}/brands/{brand}/context", { params: { path: { ws, brand: data.id } }, body: { about: about.trim() } });
    const ctx = await api.GET("/v1/workspaces/{ws}/context", path);
    const c = ctx.data?.context;
    if (c && (!c.company || !c.website)) {
      await api.PATCH("/v1/workspaces/{ws}/context", { ...path, body: { ...(!c.company ? { company: data.name } : {}), ...(!c.website && data.website ? { website: data.website } : {}) } });
    }
    setBusy(false);
    toast(`${data.name} added.`);
    onDone();
  }

  if (done) {
    return (
      <div className="space-y-3 text-sm">
        <ul className="space-y-1.5">
          {(brands ?? []).map((b) => (
            <li key={b.id} className="flex items-center gap-2"><Icon name="check" className="size-3.5 text-green-600 dark:text-green-400" />{b.name}{b.website && <span className="text-muted-foreground">· {b.website}</span>}</li>
          ))}
        </ul>
        <p className="text-muted-foreground">Market more than one? Add the rest under <button className="underline underline-offset-2 hover:text-foreground" onClick={() => go("brands")}>Brands</button> later.</p>
      </div>
    );
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <Input label="Website" hint="Analytics and SEO for this domain count as the brand's." value={website} onChange={(e) => { setWebsite(e.target.value); if (!named) setName(nameFrom(e.target.value) || wsName); }} maxLength={253} required autoFocus placeholder="acme-com.example" />
      <Input label="Brand name" value={name} onChange={(e) => { setName(e.target.value); setNamed(true); }} maxLength={100} required placeholder="Acme" />
      <label className="block text-sm">
        <span className="mb-1.5 block text-[13px] font-medium">What does it do? <span className="font-normal text-muted-foreground">(optional)</span></span>
        <textarea className={`${field} min-h-20 w-full`} value={about} onChange={(e) => setAbout(e.target.value)} maxLength={4000} placeholder="Billing analytics for B2B SaaS teams. Customers cut churn by spotting failed payments early." />
        <span className="mt-1 block text-xs text-muted-foreground">A sentence or two. Every agent reads it before writing for this brand.</span>
      </label>
      <ErrorText>{error}</ErrorText>
      <Button className="w-full sm:w-auto" disabled={busy || !name.trim() || !website.trim()}>{busy ? "Saving…" : "Continue"}</Button>
      <p className="text-[12px] text-muted-foreground">Market more than one product or site? Set this one up first; add the others under Brands later.</p>
    </form>
  );
}

// A brand name from a domain: acme-labs.co.uk is Acme Labs.
function nameFrom(site: string) {
  const host = site.trim().replace(/^https?:\/\//, "").replace(/^www\./, "").split(/[/?#]/)[0];
  const word = host.split(".")[0] ?? "";
  return word.split(/[-_]/).filter(Boolean).map((w) => w[0].toUpperCase() + w.slice(1)).join(" ");
}

// A step that connects tools from the catalog: the connectors it picks,
// connected right here, coming back to this page after a sign-in.
function ConnectStep({ ws, pick, first = [], note, limit, more, onChange }: { ws: string; pick: (c: Connector) => boolean; first?: string[]; note?: string; limit?: number; more?: () => void; onChange: () => void }) {
  const [catalog, setCatalog] = useState<Connector[] | null>(null);
  const [connecting, setConnecting] = useState<Connector | null>(null);
  const siteSetup = useSiteSetupAfterConnect(ws);

  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/connectors", { params: { path: { ws } } });
    setCatalog(data?.data ?? []);
  }, [ws]);
  useEffect(() => {
    load();
  }, [load]);

  if (!catalog) return <Loading />;
  // Ready-to-connect ones first, then the ones this server can't connect yet.
  const ready = (c: Connector) => c.status === "available" && c.configured;
  // One of each kind before a second of any, so a short list covers the most ground.
  const all = catalog.filter((c) => c.auth !== "none" && c.status === "available" && pick(c)).sort((a, b) => Number(ready(b)) - Number(ready(a)) || b.connected - a.connected || rank(a) - rank(b));
  const nth = new Map(all.map((c) => [c.id, all.filter((x) => x.category === c.category).indexOf(c)]));
  const lead = (c: Connector) => (first.indexOf(c.id) + 1 || 99);
  let shown = all.sort((a, b) => Number(ready(b)) - Number(ready(a)) || lead(a) - lead(b) || nth.get(a.id)! - nth.get(b.id)! || rank(a) - rank(b));
  const hidden = limit && shown.length > limit ? shown.length - limit : 0;
  if (limit) shown = shown.slice(0, limit);

  function connect(def: Connector) {
    if (authOption(def, "oauth2")?.available && !authOption(def, "api_key")) startConnectorOAuth(ws, def, "get-started");
    else setConnecting(def);
  }

  return (
    <div className="space-y-3">
      {shown.length === 0 ? (
        <p className="text-sm text-muted-foreground">None of these can be connected on this server yet. Skip this step for now.</p>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2">
          {shown.map((d) => <ConnectorCard key={d.id} def={d} canManage onConnect={() => connect(d)} />)}
        </div>
      )}
      {note && <p className="text-xs text-muted-foreground">{note}</p>}
      {more && (
        <button onClick={more} className="text-[13px] text-muted-foreground underline underline-offset-2 hover:text-foreground">
          {hidden > 0 ? `See all connectors (${hidden} more here)` : "See all connectors"}
        </button>
      )}
      {connecting && (
        <ConnectDialog
          ws={ws}
          def={connecting}
          onSignIn={() => startConnectorOAuth(ws, connecting, "get-started")}
          onClose={() => setConnecting(null)}
          onDone={(i) => {
            load();
            onChange();
            void siteSetup.after(i?.id);
          }}
        />
      )}
      {siteSetup.dialog}
    </div>
  );
}

// Domain research: an optional card on the tools step. Researching unknown
// email domains reads their homepage through Geonode and sums it up with
// the workspace's own Anthropic key. It shows only once this server has the
// Geonode connector, and is never required.
function DomainResearch({ ws, onChange, go }: { ws: string; onChange: () => void; go: (t: Tab) => void }) {
  const [catalog, setCatalog] = useState<Connector[] | null>(null);
  const [connecting, setConnecting] = useState<Connector | null>(null);
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/connectors", { params: { path: { ws } } });
    setCatalog(data?.data ?? []);
  }, [ws]);
  useEffect(() => {
    load();
  }, [load]);

  const geonode = catalog?.find((c) => c.id === "geonode" && c.status === "available");
  const claude = catalog?.find((c) => c.id === "claude" && c.status === "available");
  if (!geonode) return null;

  function connect(def: Connector) {
    if (authOption(def, "oauth2")?.available && !authOption(def, "api_key")) startConnectorOAuth(ws, def, "get-started");
    else setConnecting(def);
  }
  const parts: [Connector | undefined, string][] = [[geonode, "Connect Geonode"], [claude, "Add your Anthropic key"]];

  return (
    <div className="space-y-3 rounded-md border p-4">
      <div>
        <h3 className="text-[13px] font-medium">Domain research <span className="font-normal text-muted-foreground">(optional)</span></h3>
        <p className="text-[13px] text-muted-foreground">To research unknown email domains, connect Geonode (reads their homepage) and add your Anthropic key.</p>
      </div>
      <ul className="space-y-2">
        {parts.map(([def, label]) =>
          def ? (
            <li key={def.id} className="flex items-center justify-between gap-3 text-[13px]">
              <span className="flex items-center gap-2">
                {def.connected > 0 ? <Icon name="check" className="size-3.5 text-green-600 dark:text-green-400" /> : <span aria-hidden="true" className="size-3.5 rounded-full border" />}
                {def.name}
                <span className="sr-only">{def.connected > 0 ? " (connected)" : ""}</span>
              </span>
              {def.connected > 0 ? <span className="text-muted-foreground">Connected</span> : <Button variant="ghost" onClick={() => connect(def)}>{label}</Button>}
            </li>
          ) : null,
        )}
      </ul>
      <p className="text-xs text-muted-foreground">
        Your own keys, billed to your accounts. Choose the model in <button className="underline underline-offset-2 hover:text-foreground" onClick={() => go("workspace")}>AI settings</button>.
      </p>
      {connecting && (
        <ConnectDialog
          ws={ws}
          def={connecting}
          onSignIn={() => startConnectorOAuth(ws, connecting, "get-started")}
          onClose={() => setConnecting(null)}
          onDone={() => {
            load();
            onChange();
          }}
        />
      )}
    </div>
  );
}
