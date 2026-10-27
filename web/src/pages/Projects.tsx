import { useCallback, useEffect, useMemo, useState, type FormEvent, type KeyboardEvent, type ReactNode } from "react";
import type { ICP, LeadList, Mailbox, Project, ProjectMembers, SendingDomain, Sequence } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { api, errorMessage } from "../api";
import type { Tab } from "../App";
import { ask, Badge, Button, Card, confirmAction, CopyButton, Empty, ErrorText, field, Input, Loading, Menu, Mono, PageHeader, Stat, Stats, toast } from "../ui";
import { ago, plural } from "../format";
import { DataTable } from "@/components/data-table";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

// Projects: one motion each, an offer sold to one kind of customer in one
// market. The brief is what people and agents work from; the project owns
// the domains, mailboxes, lists and sequences that run the motion.

type Props = { ws: string; canManage: boolean; isAdmin: boolean; open?: string; setOpen: (id?: string) => void; go: (tab: Tab, item?: string) => void };

const statusTone = { active: "green", paused: "grey", archived: "grey" } as const;
const statusLabel = { active: "Active", paused: "Paused", archived: "Archived" } as const;

export const icpFields: { key: keyof ICP; label: string; placeholder: string }[] = [
  { key: "titles", label: "Job titles", placeholder: "CFO, VP Finance" },
  { key: "seniorities", label: "Seniority", placeholder: "C-suite, VP, Director" },
  { key: "industries", label: "Industries", placeholder: "Fintech, Banking" },
  { key: "employee_ranges", label: "Company size (employees)", placeholder: "51-200, 201-500" },
  { key: "locations", label: "Locations", placeholder: "Australia, New Zealand" },
  { key: "keywords", label: "Keywords", placeholder: "Series B, SOC 2" },
];

export const emptyICP = (): ICP => ({ titles: [], seniorities: [], industries: [], employee_ranges: [], locations: [], keywords: [] });

// How much of the brief is written, out of five parts.
function briefParts(p: Pick<Project, "offer" | "audience" | "market" | "brief" | "icp">) {
  const icp = Object.values(p.icp ?? {}).some((v) => v.length > 0);
  const parts = { Offer: !!p.offer, Audience: !!p.audience, Market: !!p.market, "Customer filters": icp, "Written brief": !!p.brief };
  return { done: Object.values(parts).filter(Boolean).length, missing: Object.entries(parts).filter(([, v]) => !v).map(([k]) => k) };
}

function BriefBadge({ p }: { p: Project }) {
  const { done, missing } = briefParts(p);
  const tone = done === 5 ? "green" : done >= 3 ? "blue" : "amber";
  return <Badge tone={tone} title={missing.length ? `Missing: ${missing.join(", ")}` : "Every part of the brief is written"}>{done === 5 ? "✓ " : ""}Brief {done}/5</Badge>;
}

export function Projects(props: Props) {
  return props.open ? <Detail key={props.open} {...props} id={props.open} /> : <List {...props} />;
}

// ---- All projects ----

type Filter = "current" | "archived" | "all";

function List({ ws, canManage, setOpen }: Props) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [filter, setFilter] = useState<Filter>("current");
  const [creating, setCreating] = useState(false);

  const brand = useBrand(ws).brand?.handle;
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/projects", { params: { path: { ws }, query: { status: "all", ...(brand ? { brand } : {}) } } });
    if (data) setProjects(data.data);
    setLoaded(true);
  }, [ws, brand]);

  useEffect(() => {
    load();
  }, [load]);

  if (!loaded) return <Loading />;

  const current = projects.filter((p) => p.status !== "archived");
  const archived = projects.length - current.length;
  const shown = filter === "all" ? projects : filter === "archived" ? projects.filter((p) => p.status === "archived") : current;
  const sum = (f: (p: Project) => number) => current.reduce((n, p) => n + f(p), 0);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Projects"
        description="A project is one motion: an offer, sold to one kind of customer, in one market. It holds the brief people and agents work from, and owns its domains, mailboxes, lead lists and sequences. Anything outside a project is shared by the workspace."
        action={canManage && projects.length > 0 && <Button onClick={() => setCreating(true)}>New project</Button>}
      />

      {projects.length === 0 ? (
        <Empty title="No projects yet" action={canManage && <Button onClick={() => setCreating(true)}>Create your first project</Button>}>
          Start one project per offer and market, for example "Close automation for fintech CFOs in Australia". Write its brief once, then give it domains, mailboxes and sequences. Agents read the brief before they write a word.
        </Empty>
      ) : (
        <>
          <Stats cols={4}>
            <Stat label="Active" value={current.filter((p) => p.status === "active").length} />
            <Stat label="Paused" value={current.filter((p) => p.status === "paused").length} tone={current.some((p) => p.status === "paused") ? "amber" : undefined} />
            <Stat label="Sequences sending" value={sum((p) => p.counts.active_sequences ?? 0)} />
            <Stat label="Sent · 24h" value={sum((p) => p.sent_24h).toLocaleString()} />
          </Stats>
          <DataTable
            data={shown}
            rowId={(p) => p.id}
            onRowClick={(p) => setOpen(p.id)}
            searchPlaceholder="Search projects"
            empty={<>No projects match. <button className="underline" onClick={() => setFilter("all")}>Show all</button></>}
            toolbar={
              <select className={field} aria-label="Show" value={filter} onChange={(e) => setFilter(e.target.value as Filter)}>
                <option value="current">Active and paused ({current.length})</option>
                <option value="archived">Archived ({archived})</option>
                <option value="all">All projects ({projects.length})</option>
              </select>
            }
            mobile={(p) => (
              <div className="space-y-1.5">
                <div className="flex items-start justify-between gap-2">
                  <span className="flex flex-wrap items-center gap-1.5 font-medium">{p.name} <BrandBadge id={p.brand_id} /></span>
                  <Badge tone={statusTone[p.status]}>{statusLabel[p.status]}</Badge>
                </div>
                {p.offer && <p className="line-clamp-2 text-[13px] text-muted-foreground">{p.offer}</p>}
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                  <BriefBadge p={p} />
                  <span>{plural(p.counts.domains ?? 0, "domain")} · {plural(p.counts.mailboxes ?? 0, "mailbox", "mailboxes")} · {p.counts.active_sequences}/{plural(p.counts.sequences ?? 0, "sequence")} sending</span>
                  <span>{p.sent_24h}{p.daily_send_limit !== null && `/${p.daily_send_limit}`} sent today</span>
                </div>
              </div>
            )}
            columns={[
              {
                id: "name",
                header: "Project",
                accessorFn: (p) => `${p.name} ${p.offer} ${p.audience} ${p.market}`,
                enableHiding: false,
                cell: ({ row: { original: p } }) => (
                  <div className="max-w-md">
                    <div className="flex items-center gap-1.5"><span className="font-medium group-hover:underline">{p.name}</span> <BrandBadge id={p.brand_id} /></div>
                    <div className="truncate text-xs text-muted-foreground" title={p.offer}>{p.offer || <span className="italic">No offer written yet</span>}</div>
                  </div>
                ),
              },
              { id: "status", header: "Status", accessorFn: (p) => p.status, cell: ({ row: { original: p } }) => <Badge tone={statusTone[p.status]}>{statusLabel[p.status]}</Badge> },
              { id: "brief", header: "Brief", accessorFn: (p) => briefParts(p).done, cell: ({ row: { original: p } }) => <BriefBadge p={p} /> },
              { id: "domains", header: "Domains", accessorFn: (p) => p.counts.domains, meta: { align: "right", mono: true } },
              { id: "mailboxes", header: "Mailboxes", accessorFn: (p) => p.counts.mailboxes, meta: { align: "right", mono: true } },
              {
                id: "sequences",
                header: "Sequences",
                accessorFn: (p) => p.counts.sequences,
                meta: { align: "right", mono: true },
                cell: ({ row: { original: p } }) => <span title={`${p.counts.active_sequences} sending of ${p.counts.sequences}`}>{p.counts.active_sequences}<span className="text-muted-foreground">/{p.counts.sequences}</span></span>,
              },
              {
                id: "sent",
                header: "Sent 24h",
                accessorFn: (p) => p.sent_24h,
                meta: { align: "right", mono: true },
                cell: ({ row: { original: p } }) => <span title={p.daily_send_limit === null ? "No project limit" : `Limit ${p.daily_send_limit} a day`}>{p.sent_24h}{p.daily_send_limit !== null && <span className="text-muted-foreground">/{p.daily_send_limit}</span>}</span>,
              },
            ]}
          />
        </>
      )}

      <NewProject ws={ws} open={creating} onClose={() => setCreating(false)} onCreated={(id) => setOpen(id)} />
    </div>
  );
}

function NewProject({ ws, open, onClose, onCreated }: { ws: string; open: boolean; onClose: () => void; onCreated: (id: string) => void }) {
  const { brand, active: brands } = useBrand(ws);
  const [form, setForm] = useState({ name: "", offer: "", audience: "", market: "" });
  const [brandId, setBrandId] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      setForm({ name: "", offer: "", audience: "", market: "" });
      setBrandId(brand?.id ?? "");
      setError("");
    }
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/projects", { params: { path: { ws } }, body: { ...form, ...(brandId ? { brand_id: brandId } : {}) } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`Created ${data.name}. Now finish its brief and give it mailboxes.`);
    onClose();
    onCreated(data.id);
  }

  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>New project</DialogTitle>
            <DialogDescription>One offer, one kind of customer, one market. Only the name is needed now; you can write the rest of the brief next.</DialogDescription>
          </DialogHeader>
          <Input label="Name" value={form.name} onChange={set("name")} required maxLength={100} placeholder="Fintech CFOs, Australia" autoFocus />
          <TextArea label="Offer" hint="What you sell, in a sentence or two." value={form.offer} onChange={set("offer")} rows={2} placeholder="Month-end close in two days instead of ten, without new headcount." />
          <TextArea label="Audience" hint="Who buys it, in words." value={form.audience} onChange={set("audience")} rows={2} placeholder="Finance leaders at Series B to D fintechs with 50 to 500 staff." />
          <Input label="Market" hint="Where, and in which language." value={form.market} onChange={set("market")} placeholder="Australia and New Zealand, English" />
          {brands.length > 0 && (
            <label className="block text-sm">
              <span className="mb-1.5 block text-[13px] font-medium">Brand</span>
              <select className={`${field} w-full`} value={brandId} onChange={(e) => setBrandId(e.target.value)}>
                <option value="">Shared by every brand</option>
                {brands.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
              </select>
              <span className="mt-1 block text-xs text-muted-foreground">Everything you add to the project belongs to this brand too.</span>
            </label>
          )}
          <ErrorText>{error}</ErrorText>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={busy || !form.name.trim()}>{busy ? "Creating…" : "Create project"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function TextArea({ label, hint, className, ...props }: React.TextareaHTMLAttributes<HTMLTextAreaElement> & { label: string; hint?: ReactNode }) {
  return (
    <label className="block text-sm">
      <span className="mb-1.5 block text-[13px] font-medium">{label}</span>
      <textarea className={cn(field, "w-full resize-y leading-relaxed", className)} {...props} />
      {hint && <span className="mt-1 block text-xs text-muted-foreground">{hint}</span>}
    </label>
  );
}


// ---- One project ----

function Detail({ ws, id, canManage, isAdmin, setOpen, go }: Props & { id: string }) {
  const [p, setP] = useState<Project | null>(null);
  const [missing, setMissing] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const { active: brands, byId } = useBrand(ws);

  const load = useCallback(async () => {
    const { data, response } = await api.GET("/v1/workspaces/{ws}/projects/{id}", { params: { path: { ws, id } } });
    if (data) setP(data);
    else if (response.status === 404) setMissing(true);
  }, [ws, id]);

  useEffect(() => {
    load();
  }, [load]);

  if (missing) {
    return (
      <Empty title="Project not found" action={<Button onClick={() => setOpen()}>All projects</Button>}>
        It may have been deleted, or the link is wrong.
      </Empty>
    );
  }
  if (!p) return <Loading />;

  async function patch(body: Record<string, unknown>, done: string) {
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/projects/{id}", { params: { path: { ws, id } }, body });
    if (error) {
      toast(errorMessage(error), "error");
      return false;
    }
    setP(data);
    toast(done);
    return true;
  }

  async function changeBrand() {
    if (!p) return;
    const r = await ask({
      title: `Which brand is ${p.name} for?`,
      body: "Its domains, mailboxes, lists, sequences and memories move with it.",
      choices: [{ label: "Shared", value: "", variant: "ghost" }, ...brands.filter((b) => b.id !== p.brand_id).map((b) => ({ label: b.name, value: b.id, variant: "primary" as const }))],
    });
    if (!r || r.choice === (p.brand_id ?? "")) return;
    await patch({ brand_id: r.choice || null }, r.choice ? `${p.name} is ${byId(r.choice)?.name}'s now.` : `${p.name} is shared by every brand now.`);
  }

  async function remove() {
    const c = p!.counts;
    const ok = await confirmAction(
      `Delete ${p!.name}?`,
      <>
        <p>
          The brief is deleted. Its {plural(c.domains ?? 0, "domain")}, {plural(c.mailboxes ?? 0, "mailbox", "mailboxes")}, {plural(c.lead_lists ?? 0, "lead list")} and{" "}
          {plural(c.sequences ?? 0, "sequence")} stay and become shared by the whole workspace, so other sequences may start sending from its mailboxes.
        </p>
        <p>To stop it sending and keep its history, archive it instead.</p>
      </>,
      "Delete project",
    );
    if (!ok) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/projects/{id}", { params: { path: { ws, id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Deleted ${p!.name}. Its records are now shared.`);
    setOpen();
  }

  const c = p.counts;
  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "All projects", onClick: () => setOpen() }}
        title={<>{p.name} <Badge tone={statusTone[p.status]}>{statusLabel[p.status]}</Badge> <BrandBadge id={p.brand_id} /></>}
        description={p.offer || "No offer written yet. Start with the brief below."}
        action={
          canManage && (
            <>
              {p.status === "active" && (
                <Button variant="ghost" onClick={async () => (await confirmAction(`Pause ${p.name}?`, "None of its sequences send until you resume it. Leads keep their place, and follow-ups carry on from the same mailboxes afterwards.", "Pause project", false)) && patch({ status: "paused" }, `Paused ${p.name}.`)}>
                  Pause
                </Button>
              )}
              {p.status !== "active" && <Button onClick={() => patch({ status: "active" }, `${p.name} is active again.`)}>{p.status === "archived" ? "Restore" : "Resume"}</Button>}
              <Menu
                items={[
                  { label: "Rename", onClick: () => setRenaming(true) },
                  brands.length > 0 && { label: "Change brand", onClick: changeBrand },
                  p.status !== "archived" && { label: "Archive", onClick: async () => (await confirmAction(`Archive ${p.name}?`, "It stops sending and leaves the project list. Its records and history stay; restore it any time.", "Archive", false)) && patch({ status: "archived" }, `Archived ${p.name}.`) },
                  isAdmin && { label: "Delete project", onClick: remove, danger: true },
                ]}
              />
            </>
          )
        }
      />

      {p.status !== "active" && (
        <div role="status" className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-[13px] text-amber-800 dark:text-amber-300">
          {p.status === "paused" ? "Paused." : "Archived."} None of this project's sequences send until it is {p.status === "paused" ? "resumed" : "restored"}. Waiting leads show the reason "project paused".
        </div>
      )}

      <Stats cols={5}>
        <Stat label="Domains" value={c.domains} />
        <Stat label="Mailboxes" value={c.mailboxes} />
        <Stat label="Lead lists" value={c.lead_lists} />
        <Stat label="Sequences" value={<>{c.active_sequences}<span className="text-base text-muted-foreground">/{c.sequences}</span></>} sub="sending / total" />
        <div className="col-span-2 grid sm:col-span-1">
          <Stat label="Sent · 24h" value={<>{p.sent_24h}{p.daily_send_limit !== null && <span className="text-base text-muted-foreground">/{p.daily_send_limit}</span>}</>} sub={p.daily_send_limit === null ? "no project limit" : "of the daily limit"} tone={p.daily_send_limit !== null && p.sent_24h >= p.daily_send_limit ? "amber" : undefined} />
        </div>
      </Stats>

      <BriefCard ws={ws} project={p} canManage={canManage} onSaved={setP} onReload={load} />
      <Owns ws={ws} project={p} canManage={canManage} go={go} onChange={load} />
      <div className="grid gap-6 lg:grid-cols-2 [&>*]:min-w-0">
        <LimitCard project={p} canManage={canManage} onSave={(v) => patch({ daily_send_limit: v }, v === null ? "Removed the project's daily limit." : `The project now sends at most ${v} emails a day.`)} />
        <AgentsCard ws={ws} project={p} isAdmin={isAdmin} go={go} />
      </div>

      <RenameDialog open={renaming} name={p.name} onClose={() => setRenaming(false)} onSave={async (name) => (await patch({ name }, `Renamed to ${name}.`)) && setRenaming(false)} />
    </div>
  );
}

function RenameDialog({ open, name, onClose, onSave }: { open: boolean; name: string; onClose: () => void; onSave: (name: string) => void }) {
  const [value, setValue] = useState(name);
  useEffect(() => {
    if (open) setValue(name);
  }, [open, name]);
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); onSave(value.trim()); }}>
          <DialogHeader>
            <DialogTitle>Rename project</DialogTitle>
            <DialogDescription>Names are unique in the workspace. Agents and API calls use the project's ID, so nothing breaks.</DialogDescription>
          </DialogHeader>
          <Input label="Name" value={value} onChange={(e) => setValue(e.target.value)} required maxLength={100} autoFocus />
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={!value.trim() || value.trim() === name}>Save name</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ---- Brief ----

type BriefForm = { offer: string; audience: string; market: string; brief: string; icp: ICP };

const toForm = (p: Project): BriefForm => ({ offer: p.offer, audience: p.audience, market: p.market, brief: p.brief, icp: { ...emptyICP(), ...p.icp } });

function BriefCard({ ws, project: p, canManage, onSaved, onReload }: { ws: string; project: Project; canManage: boolean; onSaved: (p: Project) => void; onReload: () => void }) {
  const [form, setForm] = useState<BriefForm>(() => toForm(p));
  const [base, setBase] = useState(p.brief_version);
  const [error, setError] = useState("");
  const [stale, setStale] = useState(false);
  const [busy, setBusy] = useState(false);

  // A newer version from elsewhere replaces the form only when nothing is
  // being edited here; otherwise saving reports the clash.
  const saved = useMemo(() => JSON.stringify(toForm(p)), [p]);
  const dirty = JSON.stringify(form) !== saved;
  useEffect(() => {
    if (!dirty) {
      setForm(toForm(p));
      setBase(p.brief_version);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p.brief_version]);

  // Leaving with unsaved edits asks first.
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    addEventListener("beforeunload", warn);
    return () => removeEventListener("beforeunload", warn);
  }, [dirty]);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error, response } = await api.PATCH("/v1/workspaces/{ws}/projects/{id}", { params: { path: { ws, id: p.id } }, body: { ...form, brief_version: base } });
    setBusy(false);
    if (error) {
      setStale(response.status === 409);
      return setError(response.status === 409 ? "Someone (or an agent) changed this brief while you were editing. Copy anything you want to keep, then load their version." : errorMessage(error));
    }
    setBase(data.brief_version);
    setForm(toForm(data));
    onSaved(data);
    toast(data.brief_version > p.brief_version ? `Brief saved as version ${data.brief_version}.` : "No changes to save.");
  }

  async function loadLatest() {
    const { data } = await api.GET("/v1/workspaces/{ws}/projects/{id}", { params: { path: { ws, id: p.id } } });
    if (!data) return;
    setForm(toForm(data));
    setBase(data.brief_version);
    setError("");
    setStale(false);
    onSaved(data);
    onReload();
  }

  const set = (k: Exclude<keyof BriefForm, "icp">) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const ro = !canManage;
  const { done, missing } = briefParts(form);

  return (
    <Card
      title={<span className="flex items-center gap-2">Brief <Badge tone={done === 5 ? "green" : done >= 3 ? "blue" : "amber"}>{done}/5</Badge></span>}
      description={<>What people and agents read before writing copy, building lists or planning a campaign. Version {p.brief_version}, updated {ago(p.brief_updated_at)}.</>}
    >
      <form onSubmit={save} className="space-y-5">
        {missing.length > 0 && !ro && <p className="text-[13px] text-muted-foreground">Still to write: {missing.join(", ").toLowerCase()}.</p>}
        <div className="grid gap-4 md:grid-cols-2">
          <TextArea label="Offer" hint="What you sell and the outcome it gets, in a sentence or two." value={form.offer} onChange={set("offer")} rows={3} maxLength={4000} readOnly={ro} placeholder="Month-end close in two days instead of ten, without new headcount." />
          <TextArea label="Audience" hint="Who buys it, in words a colleague would use." value={form.audience} onChange={set("audience")} rows={3} maxLength={4000} readOnly={ro} placeholder="Finance leaders at Series B to D fintechs with 50 to 500 staff." />
        </div>
        <Input label="Market" hint="Where, and in which language." value={form.market} onChange={set("market")} maxLength={4000} readOnly={ro} placeholder="Australia and New Zealand, English" />

        <fieldset className="space-y-3 rounded-lg border p-3">
          <legend className="px-1 text-[13px] font-medium">Ideal customer filters</legend>
          <p className="-mt-1 text-xs text-muted-foreground">The audience as filters list builders and data providers can run. Press Enter or type a comma after each value.</p>
          <div className="grid gap-3 md:grid-cols-2">
            {icpFields.map((f) => (
              <TagInput key={f.key} label={f.label} placeholder={f.placeholder} values={form.icp[f.key] ?? []} readOnly={ro} onChange={(v) => setForm((x) => ({ ...x, icp: { ...x.icp, [f.key]: v } }))} />
            ))}
          </div>
        </fieldset>

        <TextArea
          label="Written brief"
          hint="Markdown. Messaging, proof points, objections and how to answer them, tone, words to avoid, what never to claim."
          value={form.brief}
          onChange={set("brief")}
          rows={10}
          maxLength={100000}
          readOnly={ro}
          className="font-mono text-[12px]"
          placeholder={"## Angle\nLead with the audit-season pain, not the product.\n\n## Proof\n- Acme cut close from 9 days to 2\n\n## Never say\n- \"Revolutionary\", \"AI-powered\""}
        />

        <ErrorText>{error}</ErrorText>
        {!ro && (
          <div className="flex flex-wrap items-center gap-2">
            <Button type="submit" disabled={busy || !dirty}>{busy ? "Saving…" : "Save brief"}</Button>
            {dirty && <Button type="button" variant="ghost" onClick={() => { setForm(toForm(p)); setError(""); setStale(false); }}>Discard changes</Button>}
            {stale && <Button type="button" variant="ghost" onClick={loadLatest}>Load the latest version</Button>}
            {dirty && !error && <span className="text-xs text-amber-700 dark:text-amber-400">Unsaved changes</span>}
          </div>
        )}
      </form>
    </Card>
  );
}

export function TagInput({ label, values, onChange, placeholder, readOnly }: { label: string; values: string[]; onChange: (v: string[]) => void; placeholder: string; readOnly?: boolean }) {
  const [draft, setDraft] = useState("");
  const add = (raw: string) => {
    const next = [...values];
    for (const v of raw.split(",").map((x) => x.trim()).filter(Boolean)) {
      if (!next.some((n) => n.toLowerCase() === v.toLowerCase())) next.push(v.slice(0, 200));
    }
    onChange(next);
    setDraft("");
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if ((e.key === "Enter" || e.key === ",") && draft.trim()) {
      e.preventDefault();
      add(draft);
    } else if (e.key === "Backspace" && !draft && values.length) {
      onChange(values.slice(0, -1));
    }
  };
  const inputId = `tag-${label.replace(/\W+/g, "-").toLowerCase()}`;
  return (
    <div className="text-sm">
      <label htmlFor={inputId} className="mb-1.5 block text-[13px] font-medium">{label}</label>
      <div className={cn(field, "flex h-auto min-h-8 w-full flex-wrap items-center gap-1 py-1", readOnly && "bg-muted/30")}>
        {values.map((v) => (
          <span key={v} className="inline-flex h-6 items-center gap-1 rounded border bg-muted px-1.5 text-[12px]">
            {v}
            {!readOnly && (
              <button type="button" aria-label={`Remove ${v}`} className="text-muted-foreground hover:text-foreground" onClick={() => onChange(values.filter((x) => x !== v))}>×</button>
            )}
          </span>
        ))}
        {!readOnly && (
          <input
            id={inputId}
            value={draft}
            onChange={(e) => (e.target.value.includes(",") ? add(e.target.value) : setDraft(e.target.value))}
            onKeyDown={onKey}
            onBlur={() => draft.trim() && add(draft)}
            placeholder={values.length ? "" : placeholder}
            className="min-w-24 flex-1 bg-transparent text-[13px] outline-none placeholder:text-muted-foreground"
          />
        )}
        {readOnly && values.length === 0 && <span id={inputId} className="text-[13px] text-muted-foreground">None</span>}
      </div>
    </div>
  );
}

// ---- What the project owns ----

const healthLabel: Record<string, string> = { healthy: "healthy", warning: "needs attention", critical: "at risk", pending: "checking DNS" };
const mailboxLabel: Record<string, string> = { verifying: "checking sign-in", active: "active", paused: "paused", error: "sign-in failing" };
const sequenceLabel: Record<string, string> = { draft: "draft", active: "sending", paused: "paused", archived: "archived" };

type Kind = "domains" | "mailboxes" | "lead_lists" | "sequences";
type Row = { id: string; label: string; sub?: string; project: string | null };

const kinds: { kind: Kind; title: string; one: string; many: string; tab: Tab; field: keyof ProjectMembers }[] = [
  { kind: "domains", title: "Domains", one: "domain", many: "domains", tab: "domains", field: "domains" },
  { kind: "mailboxes", title: "Mailboxes", one: "mailbox", many: "mailboxes", tab: "mailboxes", field: "mailbox_ids" },
  { kind: "lead_lists", title: "Lead lists", one: "lead list", many: "lead lists", tab: "leads", field: "lead_list_ids" },
  { kind: "sequences", title: "Sequences", one: "sequence", many: "sequences", tab: "sequences", field: "sequence_ids" },
];

function Owns({ ws, project: p, canManage, go, onChange }: { ws: string; project: Project; canManage: boolean; go: (t: Tab, item?: string) => void; onChange: () => void }) {
  const [rows, setRows] = useState<Record<Kind, Row[]> | null>(null);
  const [names, setNames] = useState<Record<string, string>>({});
  const [kind, setKind] = useState<Kind>("domains");
  const [adding, setAdding] = useState(false);
  // Lists only say which project they are in once the API returns it.
  const [listsKnown, setListsKnown] = useState(true);

  const load = useCallback(async () => {
    const path = { ws };
    const [d, m, l, s, pr] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/sending-domains", { params: { path } }),
      api.GET("/v1/workspaces/{ws}/mailboxes", { params: { path } }),
      api.GET("/v1/workspaces/{ws}/lead-lists", { params: { path } }),
      api.GET("/v1/workspaces/{ws}/sequences", { params: { path } }),
      api.GET("/v1/workspaces/{ws}/projects", { params: { path, query: { status: "all" } } }),
    ]);
    const lists = (l.data?.data ?? []) as (LeadList & { project_id?: string | null })[];
    setListsKnown(lists.length === 0 || lists.some((x) => "project_id" in x));
    setRows({
      domains: (d.data?.data ?? []).map((x: SendingDomain) => ({ id: x.domain, label: x.domain, sub: `${plural(x.mailboxes, "mailbox", "mailboxes")} · ${healthLabel[x.health] ?? x.health}`, project: x.project_id ?? null })),
      mailboxes: (m.data?.data ?? []).map((x: Mailbox) => ({ id: x.id, label: x.email, sub: `${mailboxLabel[x.status] ?? x.status}${x.assignment ? ` · sends for ${x.assignment.sequence_name}` : ""}`, project: x.project_id ?? null })),
      lead_lists: lists.map((x) => ({ id: x.id, label: x.name, sub: plural(x.leads, "lead"), project: x.project_id ?? null })),
      sequences: (s.data?.data ?? []).map((x: Sequence) => ({ id: x.id, label: x.name, sub: sequenceLabel[x.status] ?? x.status, project: x.project_id ?? null })),
    });
    setNames(Object.fromEntries((pr.data?.data ?? []).map((x) => [x.id, x.name])));
  }, [ws]);

  useEffect(() => {
    load();
  }, [load, p.counts]);

  const meta = kinds.find((k) => k.kind === kind)!;
  const mine = rows?.[kind].filter((r) => r.project === p.id) ?? [];
  const others = rows?.[kind].filter((r) => r.project !== p.id) ?? [];

  async function move(add: boolean, ids: string[], withMailboxes = true) {
    const body: ProjectMembers = { [meta.field]: ids };
    if (kind === "domains") body.with_mailboxes = withMailboxes;
    const { data, error } = add
      ? await api.POST("/v1/workspaces/{ws}/projects/{id}/add", { params: { path: { ws, id: p.id } }, body })
      : await api.POST("/v1/workspaces/{ws}/projects/{id}/remove", { params: { path: { ws, id: p.id } }, body });
    if (error) return toast(errorMessage(error), "error");
    const n = data.moved;
    const parts = [n.domains && plural(n.domains, "domain"), n.mailboxes && plural(n.mailboxes, "mailbox", "mailboxes"), n.lead_lists && plural(n.lead_lists, "lead list"), n.sequences && plural(n.sequences, "sequence")].filter(Boolean);
    toast(parts.length ? `${add ? "Moved" : "Made shared:"} ${parts.join(", ")}${add ? ` into ${p.name}` : ""}.` : "Nothing changed.");
    onChange();
    load();
  }

  return (
    <Card
      title="What this project owns"
      description="A project's mailboxes send only for its own sequences. Shared mailboxes, outside any project, can still help when the project is short."
      action={canManage && rows && (kind !== "lead_lists" || listsKnown) && <Button variant="ghost" onClick={() => setAdding(true)}>Add {meta.many}</Button>}
    >
      <div role="tablist" aria-label="Records" className="-mt-1 mb-4 flex flex-wrap gap-1 border-b">
        {kinds.map((k) => {
          // Until lists report their project, the project's own count stands in.
          const count = k.kind === "lead_lists" && !listsKnown ? (p.counts.lead_lists ?? 0) : rows ? rows[k.kind].filter((r) => r.project === p.id).length : 0;
          return (
            <button
              key={k.kind}
              role="tab"
              aria-selected={kind === k.kind}
              onClick={() => setKind(k.kind)}
              className={cn("-mb-px border-b-2 px-3 py-2 text-[13px]", kind === k.kind ? "border-brand font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground")}
            >
              {k.title} <span className="font-mono text-[11px] text-muted-foreground">{count}</span>
            </button>
          );
        })}
      </div>

      {!rows ? (
        <Loading />
      ) : kind === "lead_lists" && !listsKnown ? (
        <p className="text-[13px] text-muted-foreground">Lead lists show here once list building is updated for projects. Agents and the API can already move lists with <code className="font-mono">add_to_project</code>.</p>
      ) : mine.length === 0 ? (
        <Empty title={`No ${meta.many} in this project yet`} action={canManage && others.length > 0 && <Button onClick={() => setAdding(true)}>Add {meta.many}</Button>}>
          {others.length === 0 ? <>The workspace has no {meta.many} yet. <button className="underline" onClick={() => go(meta.tab)}>Go to {meta.title}</button></> : <>{plural(others.length, meta.one, meta.many)} in the workspace can be moved here.</>}
        </Empty>
      ) : (
        <ul className="divide-y rounded-lg border">
          {mine.slice(0, 200).map((r) => (
            <li key={r.id} className="flex items-center justify-between gap-3 px-3 py-2">
              <button className="min-w-0 text-left" onClick={() => go(meta.tab, kind === "domains" || kind === "sequences" ? r.id : undefined)}>
                <div className={cn("truncate text-[13px] font-medium hover:underline", (kind === "domains" || kind === "mailboxes") && "font-mono")}>{r.label}</div>
                {r.sub && <div className="truncate text-xs text-muted-foreground">{r.sub}</div>}
              </button>
              {canManage && (
                <Button variant="ghost" className="shrink-0" onClick={async () => (await confirmAction(`Make ${r.label} shared?`, kind === "mailboxes" || kind === "domains" ? "It leaves this project. Any sequence in the workspace may then send from it." : "It leaves this project and is shared by the whole workspace.", "Make shared", false)) && move(false, [r.id])}>
                  Remove
                </Button>
              )}
            </li>
          ))}
          {mine.length > 200 && <li className="px-3 py-2 text-xs text-muted-foreground">And {mine.length - 200} more.</li>}
        </ul>
      )}

      <Picker
        open={adding}
        onClose={() => setAdding(false)}
        title={`Add ${meta.many} to ${p.name}`}
        rows={others}
        names={names}
        domains={kind === "domains"}
        noun={meta}
        onPick={async (ids, withMailboxes) => {
          await move(true, ids, withMailboxes);
          setAdding(false);
        }}
      />
    </Card>
  );
}

function Picker({ open, onClose, title, rows, names, domains, noun, onPick }: {
  open: boolean;
  onClose: () => void;
  title: string;
  rows: Row[];
  names: Record<string, string>;
  domains: boolean;
  noun: { one: string; many: string };
  onPick: (ids: string[], withMailboxes: boolean) => Promise<void>;
}) {
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [q, setQ] = useState("");
  const [withMailboxes, setWithMailboxes] = useState(true);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) {
      setPicked(new Set());
      setQ("");
      setWithMailboxes(true);
    }
  }, [open]);

  const shown = rows.filter((r) => !q || r.label.toLowerCase().includes(q.toLowerCase()));
  // Shared records first: taking from another project is the rarer move.
  shown.sort((a, b) => Number(!!a.project) - Number(!!b.project) || a.label.localeCompare(b.label));
  const fromOther = [...picked].filter((id) => rows.find((r) => r.id === id)?.project).length;
  const toggle = (id: string) => setPicked((s) => {
    const n = new Set(s);
    if (n.has(id)) n.delete(id);
    else n.add(id);
    return n;
  });

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>Pick from the shared ones or take them from another project.</DialogDescription>
        </DialogHeader>
        <input type="search" aria-label={`Search ${noun.many}`} placeholder={`Search ${noun.many}`} value={q} onChange={(e) => setQ(e.target.value)} className={cn(field, "w-full")} />
        <div className="flex items-center justify-between text-xs text-muted-foreground">
          <span>{picked.size} of {rows.length} selected</span>
          {shown.length > 0 && (
            <button className="underline" onClick={() => setPicked(picked.size === shown.length ? new Set() : new Set(shown.map((r) => r.id)))}>
              {picked.size === shown.length ? "Clear" : `Select all ${shown.length}`}
            </button>
          )}
        </div>
        <ul className="min-h-0 flex-1 divide-y overflow-y-auto rounded-lg border">
          {shown.length === 0 && <li className="px-3 py-6 text-center text-[13px] text-muted-foreground">{rows.length === 0 ? `Every ${noun.one} is already in this project.` : "Nothing matches."}</li>}
          {shown.map((r) => (
            <li key={r.id}>
              <label className="flex cursor-pointer items-start gap-3 px-3 py-2 hover:bg-accent/50">
                <Checkbox checked={picked.has(r.id)} onCheckedChange={() => toggle(r.id)} className="mt-0.5" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13px] font-medium">{r.label}</span>
                  {r.sub && <span className="block truncate text-xs text-muted-foreground">{r.sub}</span>}
                </span>
                <Badge tone={r.project ? "amber" : "grey"}>{r.project ? names[r.project] ?? "Another project" : "Shared"}</Badge>
              </label>
            </li>
          ))}
        </ul>
        {domains && (
          <label className="flex items-center gap-2 text-[13px]">
            <Checkbox checked={withMailboxes} onCheckedChange={(v) => setWithMailboxes(v === true)} />
            Move their mailboxes too
          </label>
        )}
        {fromOther > 0 && <p className="text-xs text-amber-700 dark:text-amber-400">{plural(fromOther, noun.one, noun.many)} will leave {fromOther === 1 ? "its" : "their"} current project.</p>}
        <DialogFooter>
          <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          <Button disabled={busy || picked.size === 0} onClick={async () => { setBusy(true); await onPick([...picked], withMailboxes); setBusy(false); }}>
            {busy ? "Moving…" : picked.size ? `Add ${plural(picked.size, noun.one, noun.many)}` : `Add ${noun.many}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- Limits and agents ----

function LimitCard({ project: p, canManage, onSave }: { project: Project; canManage: boolean; onSave: (v: number | null) => void }) {
  const [value, setValue] = useState(p.daily_send_limit?.toString() ?? "");
  useEffect(() => setValue(p.daily_send_limit?.toString() ?? ""), [p.daily_send_limit]);
  const n = value.trim() === "" ? null : Number(value);
  const invalid = n !== null && (!Number.isInteger(n) || n < 1 || n > 1000000);
  return (
    <Card title="Daily send limit" description="The most cold emails all of this project's sequences send together in any 24 hours, on top of each mailbox's and domain's own limits.">
      <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); if (!invalid) onSave(n); }}>
        <div className="flex flex-wrap items-end gap-2">
          <label className="block text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">Emails per day</span>
            <input className={cn(field, "w-36 font-mono")} inputMode="numeric" value={value} onChange={(e) => setValue(e.target.value.replace(/[^\d]/g, ""))} placeholder="No limit" readOnly={!canManage} aria-invalid={invalid} />
          </label>
          {canManage && <Button type="submit" variant="ghost" disabled={invalid || n === p.daily_send_limit}>Save limit</Button>}
          {canManage && p.daily_send_limit !== null && <Button type="button" variant="danger" onClick={() => onSave(null)}>Remove limit</Button>}
        </div>
        <p className="text-xs text-muted-foreground">
          {invalid ? "Use a whole number from 1 to 1,000,000, or leave it empty for no limit." : p.daily_send_limit === null ? "No project limit. Leave empty to keep it that way." : `Sent ${p.sent_24h} of ${p.daily_send_limit} in the last 24 hours. Once reached, leads wait with the reason "project daily limit".`}
        </p>
      </form>
    </Card>
  );
}

function AgentsCard({ ws, project: p, isAdmin, go }: { ws: string; project: Project; isAdmin: boolean; go: (t: Tab) => void }) {
  const path = `/v1/workspaces/${ws}/projects/${p.id}/brief`;
  const curl = `curl "${location.origin}${path}" \\\n  -H "Authorization: Bearer $OUTFLARE_TOKEN"`;
  return (
    <Card title="For agents" description="Agents read the brief over the API or MCP, and can be limited to this project with a project token.">
      <dl className="space-y-3 text-[13px]">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <dt className="text-muted-foreground">Project ID</dt>
          <dd><Mono value={p.id} short /></dd>
        </div>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <dt className="text-muted-foreground">MCP tool</dt>
          <dd className="font-mono text-[12px]">get_project_brief</dd>
        </div>
        <div>
          <dt className="mb-1 flex items-center justify-between text-muted-foreground">Read the brief <CopyButton text={curl} label="Copy the curl command" /></dt>
          <dd><pre className="overflow-x-auto rounded-md border bg-muted/40 p-2 font-mono text-[11px] leading-relaxed">{curl}</pre></dd>
        </div>
      </dl>
      {isAdmin && (
        <div className="mt-4 border-t pt-3">
          <Button variant="ghost" onClick={() => go("tokens")}>Create a token for this project</Button>
          <p className="mt-1.5 text-xs text-muted-foreground">On API tokens, pick this project: the token then sees and changes only this project's records.</p>
        </div>
      )}
    </Card>
  );
}
