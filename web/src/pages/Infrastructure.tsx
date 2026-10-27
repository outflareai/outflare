// Infrastructure: plans for the domains and mailboxes a motion needs, and
// every mailbox's place in its working life (warming, ready, resting,
// retired). Only ready mailboxes send cold email.

import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import type { InfraPlan, InfraPlanDraft, InfraPlanInput, InfraPlanItem, InfrastructureOverview, LifecycleStage, Mailbox } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, Stat, Stats, toast, type tones } from "../ui";
import { ago, placementTone, plural } from "../format";
import type { Tab } from "../App";
import { DataTable } from "@/components/data-table";

type Tone = keyof typeof tones;

const stages: Record<LifecycleStage, { label: string; tone: Tone; help: string }> = {
  warming: { label: "Warming", tone: "blue", help: "Warming up. No cold email yet." },
  ready: { label: "Ready", tone: "green", help: "Sends cold email." },
  resting: { label: "Resting", tone: "amber", help: "A break from cold email. Warm-up continues." },
  retired: { label: "Retired", tone: "grey", help: "Out of use for good." },
};

const planStatus: Record<InfraPlan["status"], { label: string; tone: Tone }> = {
  draft: { label: "Draft", tone: "grey" },
  building: { label: "Building", tone: "blue" },
  warming: { label: "Warming up", tone: "blue" },
  ready: { label: "Ready", tone: "green" },
  cancelled: { label: "Cancelled", tone: "grey" },
};

const itemStage: Record<InfraPlanItem["stage"], { label: string; tone: Tone }> = {
  planned: { label: "Planned", tone: "grey" },
  buying: { label: "Buying", tone: "blue" },
  dns: { label: "DNS", tone: "blue" },
  connecting: { label: "Connecting", tone: "blue" },
  done: { label: "Done", tone: "green" },
  failed: { label: "Failed", tone: "red" },
  cancelled: { label: "Cancelled", tone: "grey" },
};

const supplierName = (s: string) => (s === "manual" ? "You buy them" : s === "cheapinboxes" ? "Cheap Inboxes" : s);
const providerName = (p: string) => (p === "microsoft" ? "Microsoft 365" : "Google Workspace");
// "8 Nov", for big numbers in stat tiles.
const short = (iso: string | null | undefined) => (iso ? new Date(iso).toLocaleDateString(undefined, { day: "numeric", month: "short", timeZone: "UTC" }) : "–");
const day = (iso: string | null | undefined) => (iso ? new Date(iso).toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short", timeZone: "UTC" }) : "–");
const money = (cents: number | null | undefined, currency = "USD") =>
  cents === null || cents === undefined ? "–" : (cents / 100).toLocaleString(undefined, { style: "currency", currency: currency || "USD" });

type Props = { ws: string; canManage: boolean; canApprove: boolean; open: string; onOpen: (item: string) => void; go: (tab: Tab, item?: string) => void };

export function Infrastructure(props: Props) {
  if (props.open === "new") return <NewPlan {...props} />;
  if (props.open) return <PlanDetail key={props.open} {...props} />;
  return <Overview {...props} />;
}

// ---- Overview: stages, plans, mailboxes ----

type Filter = "not_ready" | LifecycleStage | "all";

function Overview({ ws, canManage, onOpen, go }: Props) {
  const [overview, setOverview] = useState<InfrastructureOverview | null>(null);
  const [plans, setPlans] = useState<InfraPlan[]>([]);
  const [mailboxes, setMailboxes] = useState<Mailbox[]>([]);
  const [filter, setFilter] = useState<Filter>("not_ready");

  const load = useCallback(async () => {
    const path = { params: { path: { ws } } };
    const [o, p, m] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/infrastructure", path),
      api.GET("/v1/workspaces/{ws}/infra-plans", path),
      api.GET("/v1/workspaces/{ws}/mailboxes", path),
    ]);
    if (o.data) setOverview(o.data);
    if (p.data) setPlans(p.data.data);
    if (m.data) setMailboxes(m.data.data);
  }, [ws]);

  useEffect(() => {
    load();
  }, [load]);

  if (!overview) return <Loading />;
  const c = overview.mailboxes;
  const total = c.warming + c.ready + c.resting + c.retired;
  const shown = mailboxes.filter((m) => (filter === "all" ? true : filter === "not_ready" ? m.lifecycle.stage !== "ready" : m.lifecycle.stage === filter));

  return (
    <div className="space-y-6">
      <PageHeader
        title="Infrastructure"
        description="Plan the domains and mailboxes a motion needs, then follow each mailbox through warm-up, rest and retirement. Only ready mailboxes send cold email."
        action={canManage && <Button onClick={() => onOpen("new")}>New plan</Button>}
      />

      <Stats cols={4}>
        {(Object.keys(stages) as LifecycleStage[]).map((s) => (
          <Stat
            key={s}
            label={<span title={stages[s].help}>{stages[s].label}</span>}
            value={c[s]}
            sub={stages[s].help}
            tone={s === "ready" && c.ready > 0 ? "green" : s === "resting" && c.resting > 0 ? "amber" : undefined}
          />
        ))}
      </Stats>

      <Card title="Plans" description="Each plan is one request, such as 10 domains and 30 mailboxes by a date, approved once and then built for you.">
        {plans.length === 0 ? (
          <Empty title="No plans yet" action={canManage && <Button onClick={() => onOpen("new")}>Plan new domains and mailboxes</Button>}>
            A plan works out domain names, mailbox addresses and a timeline from your goal. Buy through a connected supplier, or buy them yourself and the plan checks your work.
          </Empty>
        ) : (
          <DataTable
            data={plans}
            rowId={(p) => p.id}
            onRowClick={(p) => onOpen(p.id)}
            columnsMenu={false}
            initialSort={[{ id: "created", desc: true }]}
            mobile={(p) => (
              <div className="space-y-1">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-medium">{p.name}</span>
                  <Badge tone={planStatus[p.status].tone}>{planStatus[p.status].label}</Badge>
                </div>
                <div className="text-xs text-muted-foreground">{planSize(p)} · ready {day(p.timeline.ready_on)} · {supplierName(p.supplier)}</div>
              </div>
            )}
            columns={[
              { id: "name", header: "Plan", accessorFn: (p) => p.name, cell: ({ row: { original: p } }) => <><span className="font-medium group-hover:underline">{p.name}</span><div className="text-xs text-muted-foreground">{planSize(p)}</div></> },
              { id: "status", header: "Status", accessorFn: (p) => p.status, cell: ({ row: { original: p } }) => <Badge tone={planStatus[p.status].tone}>{planStatus[p.status].label}</Badge> },
              { id: "progress", header: "Mailboxes ready", enableSorting: false, meta: { align: "right", mono: true }, cell: ({ row: { original: p } }) => <>{p.counts.mailboxes.ready ?? 0}<span className="text-muted-foreground">/{p.domain_count * p.mailboxes_per_domain}</span></> },
              { id: "ready", header: "Ready on", accessorFn: (p) => p.timeline.ready_on, cell: ({ row: { original: p } }) => {
                const late = !!p.ready_by && p.timeline.ready_on.slice(0, 10) > p.ready_by;
                return <span className={late ? "text-amber-700 dark:text-amber-400" : ""} title={late ? `Later than the wanted date, ${day(p.ready_by)}` : undefined}>{day(p.timeline.ready_on)}{late && " · late"}</span>;
              } },
              { id: "supplier", header: "Bought by", accessorFn: (p) => p.supplier, cell: ({ row: { original: p } }) => supplierName(p.supplier) },
              { id: "created", header: "Created", accessorFn: (p) => p.created_at, cell: ({ row: { original: p } }) => <span className="text-muted-foreground">{ago(p.created_at)}</span> },
            ]}
          />
        )}
      </Card>

      <Card
        title="Mailboxes by stage"
        description="Mailboxes move on their own: warming ends when the sequencer's warm-up score is high enough, and mailboxes a plan built rest when it drops. You can move any mailbox by hand."
      >
        {total === 0 ? (
          <Empty title="No mailboxes yet" action={<Button onClick={() => go("mailboxes")}>Connect mailboxes</Button>}>Mailboxes you connect start as ready. A plan's new mailboxes start warming up.</Empty>
        ) : (
          <DataTable
            data={shown}
            rowId={(m) => m.id}
            searchPlaceholder="Search mailboxes"
            initialSort={[{ id: "stage", desc: false }]}
            empty={<>No mailboxes here. <button className="underline" onClick={() => setFilter("all")}>Show all</button></>}
            toolbar={
              <select className={field} aria-label="Show" value={filter} onChange={(e) => setFilter(e.target.value as Filter)}>
                <option value="not_ready">Not ready ({total - c.ready})</option>
                {(Object.keys(stages) as LifecycleStage[]).map((s) => <option key={s} value={s}>{stages[s].label} ({c[s]})</option>)}
                <option value="all">All ({total})</option>
              </select>
            }
            mobile={(m) => (
              <div className="space-y-1">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-mono text-[13px] break-all">{m.email}</span>
                  <StageBadge stage={m.lifecycle.stage} />
                </div>
                {m.lifecycle.reason && <div className="text-xs text-muted-foreground">{m.lifecycle.reason}</div>}
                {canManage && <MoveMenu ws={ws} m={m} onDone={load} />}
              </div>
            )}
            columns={[
              { id: "email", header: "Mailbox", accessorFn: (m) => m.email, enableHiding: false, cell: ({ row: { original: m } }) => <span className="font-mono text-[13px]">{m.email}</span> },
              { id: "stage", header: "Stage", accessorFn: (m) => ["resting", "warming", "ready", "retired"].indexOf(m.lifecycle.stage), cell: ({ row: { original: m } }) => <StageBadge stage={m.lifecycle.stage} /> },
              { id: "why", header: "Why", enableSorting: false, cell: ({ row: { original: m } }) => <span className="block min-w-56 text-[13px] text-muted-foreground">{m.lifecycle.reason || "–"}</span> },
              { id: "until", header: "Until", accessorFn: (m) => m.lifecycle.until ?? "", cell: ({ row: { original: m } }) => <span className="whitespace-nowrap">{m.lifecycle.until && m.lifecycle.stage !== "ready" ? day(m.lifecycle.until) : "–"}</span> },
              {
                id: "inbox",
                header: "Warm-up score",
                accessorFn: (m) => m.warmup.platform?.score ?? -1,
                meta: { align: "right", mono: true },
                cell: ({ row: { original: m } }) => {
                  const r = m.warmup.platform?.score == null ? null : m.warmup.platform.score / 100;
                  const t = placementTone(r);
                  return r === null ? <span className="text-muted-foreground">–</span> : <span className={t === "red" ? "text-red-600 dark:text-red-400" : t === "amber" ? "text-amber-700 dark:text-amber-400" : ""}>{Math.round(r * 100)}%</span>;
                },
              },
              ...(canManage ? [{ id: "actions", header: "", enableSorting: false, enableHiding: false, meta: { align: "right" as const }, cell: ({ row: { original: m } }: { row: { original: Mailbox } }) => <MoveMenu ws={ws} m={m} onDone={load} /> }] : []),
            ]}
          />
        )}
      </Card>
    </div>
  );
}

function planSize(p: InfraPlan) {
  return `${plural(p.domain_count, "domain")} × ${p.mailboxes_per_domain} = ${plural(p.domain_count * p.mailboxes_per_domain, "mailbox", "mailboxes")} on ${providerName(p.provider)}`;
}

function StageBadge({ stage }: { stage: LifecycleStage }) {
  return <Badge tone={stages[stage].tone} title={stages[stage].help}>{stages[stage].label}</Badge>;
}

// The actions a person can take on one mailbox's stage.
function MoveMenu({ ws, m, onDone }: { ws: string; m: Mailbox; onDone: () => void }) {
  const s = m.lifecycle.stage;
  async function move(stage: LifecycleStage, extra: { reason?: string; cancel_at_supplier?: boolean } = {}) {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/infrastructure/move", { params: { path: { ws } }, body: { mailbox_ids: [m.id], stage, ...extra } });
    if (error) return toast(errorMessage(error), "error");
    const r = data.results[0];
    if (r?.skipped) toast(`${m.email}: ${r.skipped}`, "error");
    else toast(`${m.email} is now ${stages[stage].label.toLowerCase()}.`);
    onDone();
  }
  async function retire() {
    const fromPlan = !!m.lifecycle.plan_id;
    const ok = await confirmAction(
      `Retire ${m.email}?`,
      <>
        It stops sending and warming up for good, and its unfinished follow-ups wait for you to cancel them or hand them to another mailbox.
        {fromPlan && " It stays paid at the supplier unless you cancel it there."} Retired mailboxes cannot come back; connect it again to reuse it.
      </>,
      "Retire",
    );
    if (ok) move("retired");
  }
  if (s === "retired") return null;
  return (
    <Menu
      label="Change stage"
      items={[
        s !== "ready" && { label: "Mark ready (starts cold email)", onClick: () => move("ready") },
        s !== "resting" && { label: "Rest for 7 days", onClick: () => move("resting") },
        s !== "warming" && { label: "Warm up again for 14 days", onClick: () => move("warming") },
        { label: "Retire…", onClick: retire, danger: true },
      ]}
    />
  );
}

// ---- New plan ----

type Form = {
  name: string;
  project_id: string;
  supplier: string;
  provider: "google" | "microsoft";
  domain_count: number;
  mailboxes_per_domain: number;
  brand: string;
  personas: { first_name: string; last_name: string }[];
  domains: string;
  ready_by: string;
  warmup_days: number;
  mailbox_daily_limit: number;
  rest_below: number;
  retire_after_days: string;
  notes: string;
};

function formFrom(p?: InfraPlan): Form {
  return {
    name: p?.name ?? "",
    project_id: p?.project_id ?? "",
    supplier: p?.supplier ?? "manual",
    provider: (p?.provider as Form["provider"]) ?? "google",
    domain_count: p?.domain_count ?? 10,
    mailboxes_per_domain: p?.mailboxes_per_domain ?? 3,
    brand: p?.brand ?? "",
    personas: p?.personas?.length ? p.personas.map((x) => ({ first_name: x.first_name, last_name: x.last_name ?? "" })) : [{ first_name: "", last_name: "" }],
    domains: (p as (InfraPlan & { items?: InfraPlanItem[] }) | undefined)?.items?.filter((i) => i.kind === "domain").map((i) => i.name).join("\n") ?? "",
    ready_by: p?.ready_by ?? "",
    warmup_days: p?.warmup_days ?? 14,
    mailbox_daily_limit: p?.mailbox_daily_limit ?? 30,
    rest_below: p?.rest_below ?? 75,
    retire_after_days: p?.retire_after_days ? String(p.retire_after_days) : "",
    notes: p?.notes ?? "",
  };
}

function body(f: Form): InfraPlanInput {
  const domains = f.domains.split(/[\s,]+/).map((d) => d.trim()).filter(Boolean);
  return {
    name: f.name.trim(),
    project_id: f.project_id || null,
    supplier: f.supplier,
    provider: f.provider,
    domain_count: Math.max(f.domain_count, domains.length),
    mailboxes_per_domain: f.mailboxes_per_domain,
    brand: f.brand.trim(),
    personas: f.personas.filter((p) => p.first_name.trim()),
    domains,
    ready_by: f.ready_by || null,
    warmup_days: f.warmup_days,
    mailbox_daily_limit: f.mailbox_daily_limit,
    rest_below: f.rest_below,
    retire_after_days: f.retire_after_days ? Number(f.retire_after_days) : null,
    notes: f.notes,
  };
}

// Projects come from the Projects API when this server has it.
function useProjects(ws: string) {
  const [projects, setProjects] = useState<{ id: string; name: string }[]>([]);
  useEffect(() => {
    api.GET("/v1/meta")
      .then(({ data }) => (data?.capabilities?.includes("projects") ? fetch(`/v1/workspaces/${ws}/projects`, { credentials: "same-origin" }).then((r) => (r.ok ? r.json() : { data: [] })) : { data: [] }))
      .then((j) => setProjects((j.data ?? []).filter((p: { status?: string }) => p.status !== "archived")))
      .catch(() => setProjects([]));
  }, [ws]);
  return projects;
}

function NewPlan({ ws, onOpen }: Props) {
  return (
    <div className="space-y-6">
      <PageHeader
        title="New plan"
        description="Say how many domains and mailboxes you need and by when. The plan suggests names and a timeline; nothing is bought until it is approved."
        back={{ label: "Infrastructure", onClick: () => onOpen("") }}
      />
      <PlanForm ws={ws} onSaved={(p) => onOpen(p.id)} onCancel={() => onOpen("")} />
    </div>
  );
}

function PlanForm({ ws, plan, onSaved, onCancel }: { ws: string; plan?: InfraPlan; onSaved: (p: InfraPlan) => void; onCancel: () => void }) {
  const [f, setF] = useState<Form>(() => formFrom(plan));
  const [preview, setPreview] = useState<InfraPlanDraft | null>(null);
  const [suppliers, setSuppliers] = useState<string[]>(["manual"]);
  const [advanced, setAdvanced] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const projects = useProjects(ws);
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((x) => ({ ...x, [k]: v }));

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/infrastructure", { params: { path: { ws } } }).then(({ data }) => data && setSuppliers(data.suppliers));
  }, [ws]);

  // A live preview of what the plan would be, a moment after typing stops.
  const req = useMemo(() => body({ ...f, name: f.name || "Preview" }), [f]);
  useEffect(() => {
    const t = setTimeout(async () => {
      const { data } = await api.POST("/v1/workspaces/{ws}/infrastructure/preview", { params: { path: { ws } }, body: req });
      setPreview(data ?? null);
    }, 300);
    return () => clearTimeout(t);
  }, [ws, req]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const res = plan
      ? await api.PATCH("/v1/workspaces/{ws}/infra-plans/{id}", { params: { path: { ws, id: plan.id } }, body: body(f) })
      : await api.POST("/v1/workspaces/{ws}/infra-plans", { params: { path: { ws } }, body: { ...body(f), name: f.name.trim(), domain_count: Math.max(f.domain_count, 1) } });
    setBusy(false);
    if (res.error) return setError(errorMessage(res.error));
    toast(plan ? "Plan saved." : "Draft saved. Nothing is bought until you approve it.");
    onSaved(res.data);
  }

  const num = (k: "domain_count" | "mailboxes_per_domain" | "warmup_days" | "mailbox_daily_limit" | "rest_below") => (e: React.ChangeEvent<HTMLInputElement>) => set(k, Number(e.target.value));
  const label = "mb-1.5 block text-[13px] font-medium";
  const hint = "mt-1 block text-xs text-muted-foreground";

  return (
    <form onSubmit={submit} className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
      <div className="space-y-5">
        <Card title="What you need">
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="block sm:col-span-2">
              <span className={label}>Name</span>
              <input className={`${field} w-full`} value={f.name} onChange={(e) => set("name", e.target.value)} placeholder="Q4 founders outbound" required maxLength={200} />
            </label>
            {projects.length > 0 && (
              <label className="block sm:col-span-2">
                <span className={label}>Project</span>
                <select className={`${field} w-full`} value={f.project_id} onChange={(e) => set("project_id", e.target.value)}>
                  <option value="">Shared by the whole workspace</option>
                  {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
                </select>
                <span className={hint}>The domains and mailboxes will belong to this project.</span>
              </label>
            )}
            <label className="block">
              <span className={label}>Domains</span>
              <input type="number" min={1} max={200} className={`${field} w-full`} value={f.domain_count} onChange={num("domain_count")} />
            </label>
            <label className="block">
              <span className={label}>Mailboxes per domain</span>
              <input type="number" min={1} max={10} className={`${field} w-full`} value={f.mailboxes_per_domain} onChange={num("mailboxes_per_domain")} />
              <span className={hint}>2 or 3 is usual.</span>
            </label>
            <label className="block">
              <span className={label}>Mailbox provider</span>
              <select className={`${field} w-full`} value={f.provider} onChange={(e) => set("provider", e.target.value as Form["provider"])}>
                <option value="google">Google Workspace</option>
                <option value="microsoft">Microsoft 365</option>
              </select>
            </label>
            <label className="block">
              <span className={label}>Ready by</span>
              <input type="date" className={`${field} w-full`} value={f.ready_by} onChange={(e) => set("ready_by", e.target.value)} />
              <span className={hint}>Optional. The plan warns if it is too soon.</span>
            </label>
            <label className="block sm:col-span-2">
              <span className={label}>Who buys them</span>
              <select className={`${field} w-full`} value={f.supplier} onChange={(e) => set("supplier", e.target.value)}>
                {suppliers.map((s) => <option key={s} value={s}>{s === "manual" ? "I'll buy them myself (the plan checks my work)" : supplierName(s)}</option>)}
              </select>
              <span className={hint}>{f.supplier === "manual" ? "Buy at any registrar and mailbox provider. The plan moves on when it sees the domains and mailboxes." : "Bought on your own account; you pay the supplier directly. You'll see the price before approving."}</span>
            </label>
          </div>
        </Card>

        <Card title="Names">
          <div className="space-y-4">
            <label className="block">
              <span className={label}>Brand</span>
              <input className={`${field} w-full`} value={f.brand} onChange={(e) => set("brand", e.target.value)} placeholder="Acme" maxLength={60} />
              <span className={hint}>Domain names are made from it, such as getacme-com.example and acmehq-com.example. Cold email never goes from your main domain.</span>
            </label>
            <label className="block">
              <span className={label}>Domains you want (optional)</span>
              <textarea className={`${field} w-full font-mono`} rows={3} value={f.domains} onChange={(e) => set("domains", e.target.value)} placeholder={"acme-sales-com.example\nacmeteam.com"} />
              <span className={hint}>One per line. Domains you already have here get mailboxes added instead of being bought. The rest are suggested.</span>
            </label>
            <fieldset>
              <legend className={label}>Who the mailboxes send as</legend>
              <div className="space-y-2">
                {f.personas.map((p, i) => (
                  <div key={i} className="flex gap-2">
                    <input className={`${field} min-w-0 flex-1`} aria-label={`First name ${i + 1}`} placeholder="First name" value={p.first_name} onChange={(e) => set("personas", f.personas.map((x, j) => (j === i ? { ...x, first_name: e.target.value } : x)))} />
                    <input className={`${field} min-w-0 flex-1`} aria-label={`Last name ${i + 1}`} placeholder="Last name" value={p.last_name} onChange={(e) => set("personas", f.personas.map((x, j) => (j === i ? { ...x, last_name: e.target.value } : x)))} />
                    {f.personas.length > 1 && <Button type="button" variant="danger" aria-label={`Remove person ${i + 1}`} onClick={() => set("personas", f.personas.filter((_, j) => j !== i))}>Remove</Button>}
                  </div>
                ))}
                {f.personas.length < 20 && <Button type="button" variant="ghost" onClick={() => set("personas", [...f.personas, { first_name: "", last_name: "" }])}>Add a person</Button>}
              </div>
              <span className={hint}>Mailboxes are named after them: jane@, jane.doe@, j.doe@.</span>
            </fieldset>
          </div>
        </Card>

        <Card title="Warm-up and rest" action={<Button type="button" variant="ghost" onClick={() => setAdvanced((a) => !a)} aria-expanded={advanced}>{advanced ? "Hide" : "Change"}</Button>}>
          {!advanced ? (
            <p className="text-[13px] text-muted-foreground">
              {f.warmup_days} days of warm-up, then {f.mailbox_daily_limit} cold emails a day per mailbox. Warm-up runs in your connected sequencer. A mailbox rests for 7 days when its warm-up score falls below {f.rest_below}
              {f.retire_after_days ? `, and retires after ${f.retire_after_days} days of rest without recovering.` : ". It is never retired on its own."}
            </p>
          ) : (
            <div className="grid gap-4 sm:grid-cols-2">
              <label className="block"><span className={label}>Warm-up days</span><input type="number" min={7} max={60} className={`${field} w-full`} value={f.warmup_days} onChange={num("warmup_days")} /><span className={hint}>14 to 21 is usual for new domains.</span></label>
              <label className="block"><span className={label}>Cold emails a day per mailbox</span><input type="number" min={1} max={500} className={`${field} w-full`} value={f.mailbox_daily_limit} onChange={num("mailbox_daily_limit")} /></label>
              <label className="block"><span className={label}>Rest below (warm-up score)</span><input type="number" min={0} max={100} className={`${field} w-full`} value={f.rest_below} onChange={num("rest_below")} /></label>
              <label className="block"><span className={label}>Retire after resting (days)</span><input type="number" min={3} max={120} className={`${field} w-full`} value={f.retire_after_days} placeholder="Never" onChange={(e) => set("retire_after_days", e.target.value)} /><span className={hint}>Leave empty to decide yourself.</span></label>
            </div>
          )}
        </Card>

        <ErrorText>{error}</ErrorText>
        <div className="flex gap-2">
          <Button disabled={busy || !f.name.trim()}>{busy ? "Saving…" : plan ? "Save changes" : "Save draft"}</Button>
          <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
        </div>
      </div>

      <div className="space-y-5 lg:sticky lg:top-16 lg:self-start">
        <Card title="Preview" description="Updates as you type. Nothing is saved or bought.">
          {!f.brand.trim() && !f.domains.trim() && !f.personas.some((x) => x.first_name.trim()) ? (
            <p className="text-[13px] text-muted-foreground">Add a brand (or the domains you want) and who the mailboxes send as, and the domains, mailbox addresses and timeline appear here.</p>
          ) : !preview ? <Loading /> : <DraftView d={preview} perDomain={f.mailboxes_per_domain} />}
        </Card>
      </div>
    </form>
  );
}

function Warnings({ list }: { list: { code: string; message: string; blocking: boolean }[] }) {
  if (list.length === 0) return null;
  return (
    <ul className="space-y-2">
      {list.map((w) => (
        <li key={w.code} className={`rounded-md border px-3 py-2 text-[13px] ${w.blocking ? "border-red-300 bg-red-50 dark:border-red-900 dark:bg-red-950/40" : "border-amber-300 bg-amber-50 dark:border-amber-900 dark:bg-amber-950/40"}`}>
          <span className="font-medium">{w.blocking ? "Fix before approving: " : "Heads up: "}</span>
          {w.message}
        </li>
      ))}
    </ul>
  );
}

function DraftView({ d, perDomain }: { d: InfraPlanDraft; perDomain: number }) {
  const n = d.domains.reduce((s, x) => s + x.mailboxes.length, 0);
  return (
    <div className="space-y-4 text-[13px]">
      <Stats cols={3}>
        <Stat label="Mailboxes" value={n} sub={`${plural(d.domains.length, "domain")} × ${perDomain}`} />
        <Stat label="Ready on" value={short(d.timeline.ready_on)} sub={`Set up by ${day(d.timeline.setup_by)}`} />
        <Stat label="Cold emails / day" value={d.daily_capacity.toLocaleString()} sub="once ready" />
      </Stats>
      <Warnings list={d.warnings} />
      <ul className="divide-y rounded-md border">
        {d.domains.map((x) => (
          <li key={x.name} className="px-3 py-2">
            <div className="font-mono font-medium">{x.name}</div>
            <div className="font-mono text-xs text-muted-foreground">{x.mailboxes.map((m) => m.email.split("@")[0]).join(", ") || "no mailboxes yet: add a person"}</div>
          </li>
        ))}
      </ul>
    </div>
  );
}

// ---- One plan ----

type Step = { label: string; done: number; of: number; help: string };

function steps(p: InfraPlan & { items?: InfraPlanItem[] }): Step[] {
  const items = p.items ?? [];
  const doms = items.filter((i) => i.kind === "domain");
  const boxes = items.filter((i) => i.kind === "mailbox");
  const past = (i: InfraPlanItem, ...stages: InfraPlanItem["stage"][]) => !stages.includes(i.stage) && i.stage !== "failed" && i.stage !== "cancelled";
  return [
    { label: "Approve", done: p.approved_at ? 1 : 0, of: 1, help: "A person, or an agent allowed to spend, approves the plan once." },
    { label: "Buy", done: doms.filter((i) => past(i, "planned", "buying")).length, of: doms.length, help: "Domains are bought, or you mark them bought." },
    { label: "DNS", done: doms.filter((i) => i.stage === "done").length, of: doms.length, help: "SPF, DKIM and DMARC pass." },
    { label: "Connect", done: boxes.filter((i) => i.stage === "done").length, of: boxes.length, help: "Each mailbox is connected and starts warming up." },
    { label: "Warm up", done: boxes.filter((i) => i.lifecycle_stage && i.lifecycle_stage !== "warming").length, of: boxes.length, help: "Warm-up mail reaches the inbox; then the mailbox is ready." },
  ];
}

function PlanDetail({ ws, canManage, canApprove, open, onOpen, go }: Props) {
  const [p, setP] = useState<(InfraPlan & { items?: InfraPlanItem[] }) | null>(null);
  const [missing, setMissing] = useState(false);
  const [editing, setEditing] = useState(false);
  const path = { params: { path: { ws, id: open } } };

  const load = useCallback(async () => {
    const { data, response } = await api.GET("/v1/workspaces/{ws}/infra-plans/{id}", { params: { path: { ws, id: open } } });
    if (data) setP(data);
    else if (response.status === 404) setMissing(true);
  }, [ws, open]);

  useEffect(() => {
    load();
  }, [load]);

  // Prices and building steps arrive in the background.
  const live = p && (p.status === "building" || (p.status === "draft" && p.quote.status === "pending"));
  useEffect(() => {
    if (!live) return;
    const t = setInterval(load, p?.status === "draft" ? 2000 : 15000);
    return () => clearInterval(t);
  }, [live, load, p?.status]);

  if (missing) return <Empty title="Plan not found" action={<Button onClick={() => onOpen("")}>All plans</Button>}>It may have been deleted.</Empty>;
  if (!p) return <Loading />;
  if (editing) {
    return (
      <div className="space-y-6">
        <PageHeader title={`Edit ${p.name}`} back={{ label: p.name, onClick: () => setEditing(false) }} />
        <PlanForm ws={ws} plan={p} onSaved={(np) => { setP(np); setEditing(false); }} onCancel={() => setEditing(false)} />
      </div>
    );
  }

  const blocking = p.warnings.some((w) => w.blocking);
  const quote = p.quote;
  const priced = quote.status === "ready";

  async function approve() {
    const price = priced ? <> It costs <b>{money(quote.once_cents, quote.currency)}</b> now and <b>{money(quote.monthly_cents, quote.currency)}</b> a month, paid to {supplierName(p!.supplier)} on your account.</> : null;
    const ok = await confirmAction(
      `Approve ${p!.name}?`,
      <>
        {p!.supplier === "manual"
          ? <>The plan will list what to buy and check each step as you go.</>
          : <>This buys {plural(p!.counts.domains.planned ?? p!.domain_count, "domain")} and {plural(p!.domain_count * p!.mailboxes_per_domain, "mailbox", "mailboxes")} now.{price}</>}{" "}
        New mailboxes warm up for {p!.warmup_days} days before they send cold email.
      </>,
      p!.supplier === "manual" ? "Approve" : "Approve and buy",
      false,
    );
    if (!ok) return;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/infra-plans/{id}/approve", { ...path, body: priced ? { once_cents: quote.once_cents ?? undefined, monthly_cents: quote.monthly_cents ?? undefined } : {} });
    if (error) return toast(errorMessage(error), "error");
    setP(data);
    toast("Approved. Building has started.");
  }
  async function bought() {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/infra-plans/{id}/bought", { ...path, body: {} });
    if (error) return toast(errorMessage(error), "error");
    toast(`${plural(data.updated, "domain")} moved on to the DNS check.`);
    load();
  }
  async function refresh() {
    const { error } = await api.POST("/v1/workspaces/{ws}/infra-plans/{id}/refresh", path);
    if (error) return toast(errorMessage(error), "error");
    toast(p!.status === "draft" ? "Getting a new price…" : "Checking now…");
    setTimeout(load, 1500);
  }
  async function cancel() {
    const ok = await confirmAction(`Cancel ${p!.name}?`, <>Steps not done yet stop. Anything already ordered from a supplier stays ordered; cancel it in your supplier account if you need to. Domains and mailboxes already set up stay.</>, "Cancel plan");
    if (!ok) return;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/infra-plans/{id}/cancel", path);
    if (error) return toast(errorMessage(error), "error");
    setP(data);
  }
  async function remove() {
    const ok = await confirmAction(`Delete ${p!.name}?`, <>The plan is removed. Domains and mailboxes it set up stay.</>, "Delete");
    if (!ok) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/infra-plans/{id}", path);
    if (error) return toast(errorMessage(error), "error");
    onOpen("");
  }

  const waitingToBuy = (p.items ?? []).some((i) => i.kind === "domain" && i.stage === "buying");
  const actions: ReactNode = canManage && (
    <>
      {p.status === "draft" && <Button variant="ghost" onClick={() => setEditing(true)}>Edit</Button>}
      {p.status === "draft" && p.supplier !== "manual" && <Button variant="ghost" onClick={refresh}>Refresh price</Button>}
      {p.status === "draft" && (
        <Button onClick={approve} disabled={!canApprove || blocking} title={!canApprove ? "Only owners and admins can approve plans" : blocking ? "Fix the items marked below first" : undefined}>
          {p.supplier === "manual" ? "Approve" : "Approve and buy"}
        </Button>
      )}
      {p.status === "building" && p.supplier === "manual" && waitingToBuy && <Button onClick={bought}>I bought the domains</Button>}
      {(p.status === "building" || p.status === "warming") && <Button variant="ghost" onClick={refresh}>Check now</Button>}
      {(p.status === "building" || p.status === "warming" || p.status === "draft") && (
        <Menu items={[p.status === "draft" ? { label: "Delete draft", onClick: remove, danger: true } : { label: "Cancel plan…", onClick: cancel, danger: true }]} />
      )}
      {p.status === "cancelled" && <Button variant="danger" onClick={remove}>Delete</Button>}
    </>
  );

  // Items grouped by domain.
  const byDomain = new Map<string, { domain?: InfraPlanItem; boxes: InfraPlanItem[] }>();
  for (const i of p.items ?? []) {
    const g = byDomain.get(i.domain) ?? { boxes: [] };
    if (i.kind === "domain") g.domain = i;
    else g.boxes.push(i);
    byDomain.set(i.domain, g);
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={<>{p.name} <Badge tone={planStatus[p.status].tone}>{planStatus[p.status].label}</Badge></>}
        description={`${planSize(p)}. ${p.supplier === "manual" ? "You buy them; the plan checks each step." : `Bought from ${supplierName(p.supplier)} on your account.`}`}
        back={{ label: "Infrastructure", onClick: () => onOpen("") }}
        action={actions}
      />

      {p.status !== "draft" && p.status !== "cancelled" && (
        <ol className="grid grid-cols-2 gap-2 sm:grid-cols-5" aria-label="Progress">
          {steps(p).map((s) => {
            const complete = s.of > 0 && s.done >= s.of;
            return (
              <li key={s.label} title={s.help} className={`rounded-lg border px-3 py-2 ${complete ? "border-green-600/25 bg-green-500/5" : ""}`}>
                <div className="text-[12px] text-muted-foreground">{complete ? "✓ " : ""}{s.label}</div>
                <div className="font-mono text-lg font-semibold">{s.done}<span className="text-sm text-muted-foreground">/{s.of}</span></div>
              </li>
            );
          })}
        </ol>
      )}

      {p.status === "draft" && <Warnings list={p.warnings} />}
      {p.status === "draft" && !canApprove && <p className="text-[13px] text-muted-foreground">An owner or admin approves plans, since approving can spend money. Share this page with them.</p>}

      <Stats cols={4}>
        <Stat label="Ready on" value={short(p.timeline.ready_on)} sub={p.ready_by ? `${day(p.timeline.ready_on)} · wanted by ${day(p.ready_by)}` : `${day(p.timeline.ready_on)} at the earliest`} tone={p.ready_by && p.timeline.ready_on.slice(0, 10) > p.ready_by ? "amber" : undefined} />
        <Stat label="Cold emails / day" value={p.daily_capacity.toLocaleString()} sub={`${p.mailbox_daily_limit} per mailbox once ready`} />
        <Stat label="Warm-up" value={`${p.warmup_days} days`} sub={`Rests below a score of ${p.rest_below}`} />
        {p.supplier === "manual" ? (
          <Stat label="Cost" value="Your accounts" sub="You buy and pay your registrar and mailbox provider" />
        ) : (
          <Stat
            label="Price"
            value={priced ? money(quote.once_cents, quote.currency) : quote.status === "pending" ? "Getting price…" : quote.status === "failed" ? "No price" : "–"}
            sub={priced ? `+ ${money(quote.monthly_cents, quote.currency)} a month` : quote.error}
            tone={quote.status === "failed" ? "red" : undefined}
          />
        )}
      </Stats>

      <Card title="Domains and mailboxes" description={p.status === "draft" ? "What the plan will set up. Edit the plan to change names." : "Each step updates on its own every few minutes."}>
        <ul className="divide-y">
          {[...byDomain.entries()].map(([name, g]) => (
            <li key={name} className="py-3 first:pt-0 last:pb-0">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex flex-wrap items-center gap-2">
                  <button className="font-mono text-[13px] font-medium hover:underline" onClick={() => go("domains", name)} disabled={p.status === "draft"}>{name}</button>
                  {g.domain?.owned && <Badge tone="grey" title="Already in this workspace; nothing to buy">Yours</Badge>}
                  {g.domain?.available === false && !g.domain.owned && <Badge tone="red">Taken</Badge>}
                  {g.domain?.price_cents != null && <span className="font-mono text-xs text-muted-foreground">{money(g.domain.price_cents, quote.currency)}</span>}
                </div>
                {g.domain && p.status !== "draft" && <Badge tone={itemStage[g.domain.stage].tone}>{itemStage[g.domain.stage].label}</Badge>}
              </div>
              {g.domain?.detail && <p className="mt-1 text-[13px] text-muted-foreground">{g.domain.detail}</p>}
              <ul className="mt-2 grid gap-1.5 sm:grid-cols-2">
                {g.boxes.map((b) => (
                  <li key={b.id} className="flex items-center justify-between gap-2 rounded-md border px-2.5 py-1.5" title={b.detail || undefined}>
                    <span className="truncate font-mono text-[12px]">{b.name}</span>
                    {b.lifecycle_stage ? <StageBadge stage={b.lifecycle_stage} /> : p.status !== "draft" && <Badge tone={itemStage[b.stage].tone}>{itemStage[b.stage].label}</Badge>}
                  </li>
                ))}
              </ul>
              {g.boxes.some((b) => b.stage === "connecting") && (
                <p className="mt-1.5 text-xs text-muted-foreground">
                  {g.boxes.find((b) => b.stage === "connecting")?.detail}{" "}
                  {p.supplier === "manual" && <button className="underline" onClick={() => go("mailboxes")}>Go to Mailboxes</button>}
                </p>
              )}
            </li>
          ))}
        </ul>
      </Card>

      {p.notes && <Card title="Notes"><p className="whitespace-pre-wrap text-[13px]">{p.notes}</p></Card>}
    </div>
  );
}
