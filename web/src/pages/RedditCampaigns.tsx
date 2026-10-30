import { useCallback, useEffect, useState } from "react";
import type { RedditAction, RedditBudget, RedditExperiment, RedditReport, RedditReportRow, RedditSettings } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { DataTable } from "@/components/data-table";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Input, Loading, PageHeader, Stat, Stats, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

// Reddit campaigns through Engain: what each brand placed on Reddit, what
// is still live, what it cost and how long it lasted. Agents place most
// actions over MCP; this page is where a person sets each brand's limits,
// reads the results and looks at any single action in detail.

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };
type Tone = keyof typeof tones;

const statusTone: Record<RedditAction["status"], Tone> = {
  draft: "amber", approved: "blue", sending: "blue", submitted: "blue", live: "green", frozen: "green",
  removed: "red", hidden: "red", gone: "red", failed: "red", cancelled: "grey", done: "green",
};
const statusLabel: Record<RedditAction["status"], string> = {
  draft: "Draft", approved: "Queued", sending: "Sending", submitted: "With Engain", live: "Live", frozen: "Live, locked",
  removed: "Removed", hidden: "Hidden", gone: "Deleted", failed: "Failed", cancelled: "Cancelled", done: "Done",
};
const kindLabel: Record<RedditAction["kind"], string> = { comment: "Comment", reply: "Reply", post: "Post", thread: "Thread group", upvotes: "Upvotes" };
const groupings = ["subreddit", "strategy", "kind", "link_type", "experiment", "account_tier", "author"] as const;
type Grouping = (typeof groupings)[number];

const pct = (v: number | null | undefined) => (v == null ? "–" : `${Math.round(v * 100)}%`);
const days = (v: number | null | undefined) => (v == null ? "–" : v < 1 ? `${Math.round(v * 24)}h` : `${v.toFixed(1)}d`);
const credits = (v: number) => v.toLocaleString(undefined, { maximumFractionDigits: 1 });
const money = (cents: number, currency: string) => (cents > 0 ? (cents / 100).toLocaleString(undefined, { style: "currency", currency }) : "");

export function RedditCampaigns({ ws, canManage, go }: Props) {
  const brand = useBrand(ws);
  const [picked, setPicked] = useState("");
  const brandKey = brand.brand?.handle ?? picked ?? "";
  const [connected, setConnected] = useState<boolean | null>(null);
  const [settings, setSettings] = useState<RedditSettings | null>(null);
  const [budget, setBudget] = useState<RedditBudget | null>(null);
  const [report, setReport] = useState<RedditReport | null>(null);
  const [actions, setActions] = useState<RedditAction[]>([]);
  const [experiments, setExperiments] = useState<RedditExperiment[]>([]);
  const [groupBy, setGroupBy] = useState<Grouping>("subreddit");
  const [status, setStatus] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!brand.brand && !picked && brand.active.length > 0) setPicked(brand.active[0].handle);
  }, [brand.brand, brand.active, picked]);

  const load = useCallback(async () => {
    if (!brandKey) return;
    const path = { ws };
    const [conns, s, b, r, a, e] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/integrations", { params: { path, query: { connector: "engain" } } }),
      api.GET("/v1/workspaces/{ws}/reddit/settings", { params: { path, query: { brand: brandKey } } }),
      api.GET("/v1/workspaces/{ws}/reddit/budget", { params: { path, query: { brand: brandKey } } }),
      api.GET("/v1/workspaces/{ws}/reddit/report", { params: { path, query: { brand: brandKey, group_by: groupBy } } }),
      api.GET("/v1/workspaces/{ws}/reddit/actions", { params: { path, query: { brand: brandKey, limit: 200, ...(status ? { status } : {}) } } }),
      api.GET("/v1/workspaces/{ws}/reddit/experiments", { params: { path, query: { brand: brandKey } } }),
    ]);
    if (r.error || !r.data) return setError(errorMessage(r.error));
    setError("");
    setConnected(conns.error ? null : (conns.data?.data ?? []).some((c) => c.connector === "engain"));
    setSettings(s.data?.data[0]?.settings ?? null);
    setBudget(b.data ?? null);
    setReport(r.data);
    setActions(a.data?.data ?? []);
    setExperiments(e.data?.data ?? []);
  }, [ws, brandKey, groupBy, status]);
  useEffect(() => {
    load();
  }, [load]);
  // Actions on their way to Reddit move on their own.
  useEffect(() => {
    if (!actions.some((a) => a.status === "approved" || a.status === "sending")) return;
    const t = setTimeout(load, 5000);
    return () => clearTimeout(t);
  }, [actions, load]);

  const header = (
    <PageHeader
      title="Reddit campaigns"
      description="Comments, replies, posts, thread groups and upvotes placed through Engain, as experiments. Each one is followed on Reddit until it is removed, so you see which mentions and links stay live and what they cost. Agents run campaigns with the reddit tools; you set each brand's limits here."
      action={
        !brand.brand && brand.active.length > 1 ? (
          <select aria-label="Brand" className={`${field} h-8`} value={picked} onChange={(e) => setPicked(e.target.value)}>
            {brand.active.map((b) => <option key={b.id} value={b.handle}>{b.name}</option>)}
          </select>
        ) : undefined
      }
    />
  );
  if (brand.active.length === 0 && !brand.brand) {
    return <div className="space-y-6">{header}<Empty title="Add a brand first">Reddit campaigns run per brand, each with its own budget.</Empty></div>;
  }
  if (error && !report) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!report) return <div className="space-y-6">{header}<Loading /></div>;

  const t = report.totals;
  const cur = report.currency;
  const limit = budget?.monthly_credit_limit;

  return (
    <div className="space-y-6">
      {header}
      {error && <ErrorText>{error}</ErrorText>}
      {connected === false && (
        <Card title="Connect Engain" description="Actions are placed through your own Engain account (its Max plan has API access), billed to your Engain credits.">
          {canManage && <Button onClick={() => go("connectors", "social")}>Connect Engain</Button>}
        </Card>
      )}

      <Stats cols={5}>
        <Stat label="Brand mentions" value={t.mentions_placed.toLocaleString()} sub={`${t.mentions_live.toLocaleString()} live now`} />
        <Stat label="Links" value={t.links_placed.toLocaleString()} sub={`${t.links_live.toLocaleString()} live now`} />
        <Stat label="Cost" value={`${credits(t.credits)} credits`} sub={[money(t.cost_cents, cur), t.credits_per_mention != null ? `${credits(t.credits_per_mention)} a mention` : ""].filter(Boolean).join(" · ")} />
        <Stat label="Median lifespan" value={days(t.median_live_days)} sub={`mean ${days(t.mean_live_days)}`} />
        <Stat label="Still live at 7 days" value={pct(t.survived_7d)} sub={`1 day ${pct(t.survived_1d)} · 30 days ${pct(t.survived_30d)}`} tone={t.removed > 0 && (t.survived_7d ?? 1) < 0.5 ? "amber" : undefined} />
      </Stats>

      <Card
        title="Budget"
        description="Credits are Engain credits: Engain's figure once it reports one, else the estimate. Days are UTC."
        action={canManage && <Button variant="ghost" onClick={() => setEditing(true)}>Limits</Button>}
      >
        {budget ? (
          <div className="flex flex-wrap gap-x-8 gap-y-2 text-[13px]">
            <span>This month: <span className="font-medium">{credits(budget.month_credits)}</span>{limit != null ? ` of ${credits(limit)}` : " (no limit)"}</span>
            <span>Today: <span className="font-medium">{credits(budget.day_credits)}</span>{budget.daily_credit_limit != null ? ` of ${credits(budget.daily_credit_limit)}` : ""}</span>
            {budget.engain_credits != null && <span className="text-muted-foreground">Engain balance, every brand: {credits(budget.engain_credits)}</span>}
            {settings && <span className="text-muted-foreground">{settings.max_actions_per_subreddit_day} actions a subreddit a day · {settings.max_upvotes_per_item} upvotes an item · {settings.require_approval ? "a person approves each action" : "agents send within limits"}</span>}
          </div>
        ) : (
          <Loading />
        )}
      </Card>

      <Card
        title="Results by"
        action={
          <select aria-label="Group by" className={`${field} h-8`} value={groupBy} onChange={(e) => setGroupBy(e.target.value as Grouping)}>
            {groupings.map((g) => <option key={g} value={g}>{g.replace("_", " ")}</option>)}
          </select>
        }
      >
        {report.groups.length === 0 ? (
          <p className="text-[13px] text-muted-foreground">Nothing placed yet.</p>
        ) : (
          <DataTable
            data={report.groups}
            rowId={(g: RedditReportRow) => g.group}
            columns={[
              { id: "group", header: groupBy.replace("_", " "), accessorFn: (g) => g.group || "(none)" },
              { id: "actions", header: "Actions", accessorFn: (g) => g.actions, meta: { align: "right", mono: true } },
              { id: "mentions", header: "Mentions (live)", accessorFn: (g) => g.mentions_placed, meta: { align: "right", mono: true }, cell: ({ row: { original: g } }) => `${g.mentions_placed} (${g.mentions_live})` },
              { id: "links", header: "Links (live)", accessorFn: (g) => g.links_placed, meta: { align: "right", mono: true }, cell: ({ row: { original: g } }) => `${g.links_placed} (${g.links_live})` },
              { id: "removed", header: "Removed", accessorFn: (g) => g.removed, meta: { align: "right", mono: true } },
              { id: "median", header: "Median life", accessorFn: (g) => g.median_live_days ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => days(row.original.median_live_days) },
              { id: "s7", header: "Live at 7d", accessorFn: (g) => g.survived_7d ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => pct(row.original.survived_7d) },
              { id: "credits", header: "Credits", accessorFn: (g) => g.credits, meta: { align: "right", mono: true }, cell: ({ row }) => credits(row.original.credits) },
              { id: "per", header: "Per mention", accessorFn: (g) => g.credits_per_mention ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => (row.original.credits_per_mention == null ? "–" : credits(row.original.credits_per_mention)) },
            ]}
          />
        )}
      </Card>

      <Card
        title="Everything placed"
        description="Every Reddit link an action touched, newest first. Click one for its text, timeline and every check."
        action={
          <select aria-label="Status" className={`${field} h-8`} value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">Every status</option>
            <option value="live,frozen">Live</option>
            <option value="removed,hidden,gone">Removed or deleted</option>
            <option value="draft">Drafts</option>
            <option value="approved,sending,submitted">On the way</option>
            <option value="failed">Failed</option>
          </select>
        }
      >
        {actions.length === 0 ? (
          <Empty title="Nothing here yet">Give an agent the reddit tools and the run_reddit_campaign prompt, or place an action through the API.</Empty>
        ) : (
          <DataTable
            data={actions}
            rowId={(a) => a.id}
            searchPlaceholder="Search text, subreddits, accounts"
            onRowClick={(a) => setOpen(a.id)}
            columns={[
              {
                id: "target",
                header: "Where",
                accessorFn: (a) => `${a.subreddit} ${a.title} ${a.content} ${a.author}`,
                cell: ({ row: { original: a } }) => (
                  <div className="max-w-[28rem] space-y-0.5">
                    <div className="truncate font-medium">{a.title || a.content.slice(0, 90) || a.target_url}</div>
                    <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                      {a.subreddit && <span>r/{a.subreddit}</span>}
                      <a className="hover:underline" href={a.permalink || a.target_url} target="_blank" rel="noreferrer noopener" onClick={(e) => e.stopPropagation()}>{a.permalink ? "on Reddit" : "target"}</a>
                      {a.author && <span>u/{a.author}</span>}
                      {a.account_tier && <span>{a.account_tier}</span>}
                    </div>
                  </div>
                ),
              },
              { id: "kind", header: "What", accessorFn: (a) => a.kind, cell: ({ row: { original: a } }) => <span className="text-[13px]">{kindLabel[a.kind]}{a.upvotes_goal > 0 && a.kind !== "upvotes" ? ` + ${a.upvotes_goal} upvotes` : a.kind === "upvotes" ? ` ${a.upvotes_delivered ?? 0}/${a.upvotes_goal}` : ""}</span> },
              { id: "status", header: "Status", accessorFn: (a) => a.status, cell: ({ row: { original: a } }) => <Badge tone={statusTone[a.status]} title={a.status_reason ?? a.removal_category ?? undefined}>{statusLabel[a.status]}</Badge> },
              {
                id: "mention",
                header: "Mention / link",
                accessorFn: (a) => `${a.brand_mentioned} ${a.link_type}`,
                cell: ({ row: { original: a } }) => <span className="text-xs text-muted-foreground">{a.kind === "upvotes" ? "" : `${a.brand_mentioned ? "brand" : "no brand"} · ${a.link_type === "none" ? "no link" : a.link_type.replace("_", " ")}`}</span>,
              },
              { id: "score", header: "Score", accessorFn: (a) => a.score ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => row.original.score ?? "–" },
              { id: "life", header: "Lived", accessorFn: (a) => a.live_days ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => days(row.original.live_days) },
              { id: "credits", header: "Credits", accessorFn: (a) => a.credits ?? a.estimated_credits, meta: { align: "right", mono: true }, cell: ({ row }) => credits(row.original.credits ?? row.original.estimated_credits) },
              { id: "when", header: "Placed", accessorFn: (a) => a.created_at, cell: ({ row }) => <span className="text-muted-foreground">{ago(row.original.created_at)}</span> },
            ]}
          />
        )}
      </Card>

      <Card title="Experiments" description="Each tests one idea. A closed experiment keeps its conclusion, which the next agent reads before testing again.">
        {experiments.length === 0 ? (
          <p className="text-[13px] text-muted-foreground">No experiments yet.</p>
        ) : (
          <div className="divide-y">
            {experiments.map((e) => (
              <div key={e.id} className="space-y-1 py-3 text-[13px]">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{e.name}</span>
                  <Badge tone={e.status === "open" ? "blue" : "grey"}>{e.status === "open" ? "Running" : "Closed"}</Badge>
                  {e.strategy && <span className="text-xs text-muted-foreground">{e.strategy}</span>}
                  <span className="text-xs text-muted-foreground">{e.actions} actions · {ago(e.created_at)}</span>
                </div>
                {e.hypothesis && <p className="text-muted-foreground">{e.hypothesis}</p>}
                {e.conclusion && <p><span className="font-medium">Conclusion: </span>{e.conclusion}</p>}
              </div>
            ))}
          </div>
        )}
      </Card>

      {open && <ActionSheet ws={ws} id={open} canManage={canManage} onClose={() => setOpen(null)} onChange={load} />}
      {editing && settings && brandKey && <LimitsSheet ws={ws} brand={brandKey} settings={settings} onClose={() => setEditing(false)} onSaved={() => { setEditing(false); load(); }} />}
    </div>
  );
}

function ActionSheet({ ws, id, canManage, onClose, onChange }: { ws: string; id: string; canManage: boolean; onClose: () => void; onChange: () => void }) {
  const [a, setA] = useState<RedditAction | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/reddit/actions/{id}", { params: { path: { ws, id } } });
    if (error || !data) return setError(errorMessage(error));
    setA(data);
  }, [ws, id]);
  useEffect(() => {
    load();
  }, [load]);

  async function run(path: "approve" | "cancel" | "check") {
    const p = { params: { path: { ws, id } } };
    const res =
      path === "approve"
        ? await api.POST("/v1/workspaces/{ws}/reddit/actions/{id}/approve", p)
        : path === "cancel"
          ? await api.POST("/v1/workspaces/{ws}/reddit/actions/{id}/cancel", p)
          : await api.POST("/v1/workspaces/{ws}/reddit/actions/{id}/check", p);
    if (res.error) return toast(errorMessage(res.error), "error");
    toast(path === "check" ? "Checking now." : path === "approve" ? "Sent to Engain." : "Withdrawn.");
    await load();
    onChange();
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{a ? `${kindLabel[a.kind]}${a.subreddit ? ` in r/${a.subreddit}` : ""}` : "Action"}</SheetTitle>
          <SheetDescription>{a ? `${statusLabel[a.status]}${a.removal_category ? ` (${a.removal_category})` : ""}${a.status_reason ? `: ${a.status_reason}` : ""}` : ""}</SheetDescription>
        </SheetHeader>
        {error && <ErrorText>{error}</ErrorText>}
        {!a ? (
          <Loading />
        ) : (
          <div className="space-y-5 px-4 pb-6 text-[13px]">
            <div className="flex flex-wrap gap-2">
              {a.permalink && <a className="text-primary hover:underline" href={a.permalink} target="_blank" rel="noreferrer noopener">Open on Reddit</a>}
              <a className="text-muted-foreground hover:underline" href={a.target_url} target="_blank" rel="noreferrer noopener">Target</a>
            </div>
            {a.title && <div className="font-medium">{a.title}</div>}
            {a.content && <div className="whitespace-pre-wrap rounded-md border bg-muted/40 p-3">{a.content}</div>}
            {a.children.length > 0 && (
              <div className="space-y-2">
                <div className="font-medium">Replies in the thread group</div>
                {a.children.map((c, i) => <div key={i} className="whitespace-pre-wrap rounded-md border p-2 text-xs">{String((c as { content?: string }).content ?? "")}</div>)}
              </div>
            )}
            <dl className="grid grid-cols-2 gap-x-4 gap-y-1.5">
              <dt className="text-muted-foreground">Account</dt><dd>{a.author ? `u/${a.author}` : "–"}{a.account_tier ? ` (${a.account_tier})` : ""}</dd>
              <dt className="text-muted-foreground">Score, peak</dt><dd>{a.score ?? "–"}, {a.peak_score ?? "–"}</dd>
              <dt className="text-muted-foreground">Replies</dt><dd>{a.reply_count ?? "–"}</dd>
              <dt className="text-muted-foreground">Lived</dt><dd>{days(a.live_days)}</dd>
              <dt className="text-muted-foreground">Brand, link</dt><dd>{a.brand_mentioned ? "named" : "not named"} · {a.link_type.replace("_", " ")}{a.link_url ? ` (${a.link_url})` : ""}</dd>
              <dt className="text-muted-foreground">Strategy, tone</dt><dd>{a.strategy || "–"}{a.tone ? ` · ${a.tone}` : ""}</dd>
              <dt className="text-muted-foreground">Upvotes</dt><dd>{a.upvotes_goal ? `${a.upvotes_delivered ?? 0} of ${a.upvotes_goal}, ${a.upvotes_per_day} a day` : "none"}</dd>
              <dt className="text-muted-foreground">Credits</dt><dd>{credits(a.credits ?? a.estimated_credits)}{a.credits == null ? " (estimate)" : ""}</dd>
              <dt className="text-muted-foreground">Engain</dt><dd className="break-all">{a.engain_status || "–"} {a.engain_ids.join(", ")}</dd>
              <dt className="text-muted-foreground">Last checked</dt><dd>{a.last_checked_at ? ago(a.last_checked_at) : "–"}{a.next_check_at ? `, next ${ago(a.next_check_at)}` : ""}</dd>
            </dl>
            {a.check_error && <ErrorText>{a.check_error}</ErrorText>}
            {canManage && (
              <div className="flex flex-wrap gap-2">
                {a.status === "draft" && <Button onClick={() => run("approve")}>Send</Button>}
                {(a.status === "draft" || a.status === "approved") && <Button variant="ghost" onClick={() => run("cancel")}>Withdraw</Button>}
                {a.sent_at && <Button variant="ghost" onClick={() => run("check")}>Check now</Button>}
              </div>
            )}
            <div className="space-y-1.5">
              <div className="font-medium">Timeline</div>
              {(a.events ?? []).map((e, i) => (
                <div key={i} className="flex gap-3 text-xs">
                  <span className="w-28 shrink-0 text-muted-foreground">{ago(e.at)}</span>
                  <span>{e.kind.replace("_", " ")}{(e.detail as { removal_category?: string }).removal_category ? ` (${(e.detail as { removal_category?: string }).removal_category})` : ""}</span>
                </div>
              ))}
            </div>
            {(a.snapshots ?? []).length > 0 && (
              <div className="space-y-1.5">
                <div className="font-medium">Checks of Reddit</div>
                {(a.snapshots ?? []).slice(0, 30).map((s, i) => (
                  <div key={i} className="flex gap-3 text-xs">
                    <span className="w-28 shrink-0 text-muted-foreground">{ago(s.at)}</span>
                    <span>{s.state}{s.removal_category ? ` (${s.removal_category})` : ""} · score {s.score ?? "–"}{s.locked ? " · locked" : ""}</span>
                  </div>
                ))}
              </div>
            )}
            {a.engain_raw != null && (
              <details>
                <summary className="cursor-pointer text-muted-foreground">Engain's last answer</summary>
                <pre className="mt-2 max-h-64 overflow-auto rounded-md bg-muted p-2 text-[11px]">{JSON.stringify(a.engain_raw, null, 2)}</pre>
              </details>
            )}
          </div>
        )}
      </SheetContent>
    </Sheet>
  );
}

function LimitsSheet({ ws, brand, settings, onClose, onSaved }: { ws: string; brand: string; settings: RedditSettings; onClose: () => void; onSaved: () => void }) {
  const [monthly, setMonthly] = useState(settings.monthly_credit_limit?.toString() ?? "");
  const [daily, setDaily] = useState(settings.daily_credit_limit?.toString() ?? "");
  const [perSub, setPerSub] = useState(settings.max_actions_per_subreddit_day.toString());
  const [upvotes, setUpvotes] = useState(settings.max_upvotes_per_item.toString());
  const [approval, setApproval] = useState(settings.require_approval);
  const [price, setPrice] = useState(settings.credit_price_cents ? settings.credit_price_cents.toString() : "");
  const [project, setProject] = useState(settings.engain_project_id);
  const [busy, setBusy] = useState(false);

  async function save() {
    setBusy(true);
    const { error } = await api.PUT("/v1/workspaces/{ws}/reddit/settings/{brand}", {
      params: { path: { ws, brand } },
      body: {
        ...(monthly.trim() ? { monthly_credit_limit: Number(monthly) } : { no_monthly_limit: true }),
        ...(daily.trim() ? { daily_credit_limit: Number(daily) } : { no_daily_limit: true }),
        max_actions_per_subreddit_day: Number(perSub),
        max_upvotes_per_item: Number(upvotes),
        require_approval: approval,
        credit_price_cents: price.trim() ? Number(price) : 0,
        engain_project_id: project.trim(),
      },
    });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Limits saved.");
    onSaved();
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Reddit limits</SheetTitle>
          <SheetDescription>Checked before any action spends Engain credits. Only people change these; agents work within them.</SheetDescription>
        </SheetHeader>
        <div className="space-y-4 px-4 pb-6">
          <Input label="Credits a month" hint="Empty for no limit." inputMode="decimal" value={monthly} onChange={(e) => setMonthly(e.target.value)} />
          <Input label="Credits a day (UTC)" hint="Empty for no limit. Stops a runaway agent spending the month in an hour." inputMode="decimal" value={daily} onChange={(e) => setDaily(e.target.value)} />
          <Input label="Actions in one subreddit a day" inputMode="numeric" value={perSub} onChange={(e) => setPerSub(e.target.value)} />
          <Input label="Upvotes on one item" inputMode="numeric" value={upvotes} onChange={(e) => setUpvotes(e.target.value)} />
          <Input label="One credit costs (cents)" hint="For showing spend in money." inputMode="decimal" value={price} onChange={(e) => setPrice(e.target.value)} />
          <Input label="Engain project" hint="Empty uses the Engain account's only project." value={project} onChange={(e) => setProject(e.target.value)} />
          <label className="flex items-center gap-2 text-[13px]">
            <input type="checkbox" checked={approval} onChange={(e) => setApproval(e.target.checked)} />
            A person approves each action before it is sent
          </label>
          <Button disabled={busy} onClick={save}>Save</Button>
        </div>
      </SheetContent>
    </Sheet>
  );
}
