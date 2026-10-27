import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import type { AnalyticsSiteBrand, Brand, BrandContext, BrandLinkSuggestion, Project, SendingDomain } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, toast } from "../ui";
import { DataTable } from "@/components/data-table";
import { ConnectFor, geonodeWhy } from "@/components/connect-for";
import { Check, LayoutGrid, List, Plus, Search, X } from "lucide-react";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { ago, plural } from "../format";
import { cn } from "@/lib/utils";
import { setBrand, useBrand } from "@/lib/brand";
import type { Tab } from "../App";

// Brands: the businesses one company markets from a single workspace, such
// as Geonode and Repocket. Connectors, people, tokens and leads stay shared,
// so nothing is reconnected or re-invited; each brand has its own context,
// memories, projects and sending setup, and the sidebar narrows every page
// to one brand.

const platformNames: Record<Brand["platforms"][number], string> = {
  web: "Web", ios: "iOS", android: "Android", tv: "TV", desktop: "Desktop", console: "Console", extension: "Browser extension", api: "API",
};
const platforms = Object.keys(platformNames) as Brand["platforms"];

type Link = Brand["links"][number];
type LinkKind = NonNullable<Link["kind"]>;
const linkNames: Record<LinkKind, string> = {
  app_store: "App Store", google_play: "Google Play", tv_app: "TV app store", chrome_web_store: "Chrome Web Store", microsoft_store: "Microsoft Store",
  steam: "Steam", docs: "Docs", help: "Help centre", blog: "Blog", status: "Status page", x: "X", linkedin: "LinkedIn", youtube: "YouTube",
  instagram: "Instagram", tiktok: "TikTok", facebook: "Facebook", discord: "Discord", reddit: "Reddit", github: "GitHub",
  bluesky: "Bluesky", threads: "Threads", mastodon: "Mastodon", dev_to: "DEV", docker_hub: "Docker Hub", other: "Other",
};
const linkName = (l: Link) => l.label || linkNames[l.kind ?? "other"];

// How the Brands list shows, remembered per browser.
type View = "cards" | "table";
const viewKey = "outflare.brands.view";
function savedView(): View {
  try {
    return localStorage.getItem(viewKey) === "table" ? "table" : "cards";
  } catch {
    return "cards";
  }
}

function TextArea({ label, hint, className, ...props }: React.TextareaHTMLAttributes<HTMLTextAreaElement> & { label: string; hint?: ReactNode }) {
  return (
    <label className="block text-sm">
      <span className="mb-1.5 block text-[13px] font-medium">{label}</span>
      <textarea className={cn(field, "h-auto w-full resize-y py-2 leading-relaxed", className)} {...props} />
      {hint && <span className="mt-1 block text-xs text-muted-foreground">{hint}</span>}
    </label>
  );
}

export function Brands({ ws, canManage, canEdit, open, setOpen, go }: { ws: string; canManage: boolean; canEdit: boolean; open?: string; setOpen: (id?: string) => void; go: (t: Tab, item?: string) => void }) {
  const { brands, loaded, reload } = useBrand(ws);
  const [adding, setAdding] = useState(false);
  const [view, setViewState] = useState<View>(savedView);
  const setView = (v: View) => {
    setViewState(v);
    try {
      localStorage.setItem(viewKey, v);
    } catch {
      // Private windows may refuse storage; the view still changes now.
    }
  };

  if (!loaded) return <Loading />;
  const current = open ? brands.find((b) => b.handle === open || b.id === open) : undefined;
  if (open && current) return <BrandPage ws={ws} brand={current} canManage={canManage} canEdit={canEdit} onBack={() => setOpen()} onChanged={reload} go={go} />;

  const active = brands.filter((b) => b.status === "active");
  const archived = brands.filter((b) => b.status === "archived");

  return (
    <div className="max-w-4xl space-y-6">
      <PageHeader
        title="Brands"
        description="The businesses you market from this workspace, each with its own website. Connectors, people and leads are shared by every brand, so nothing needs reconnecting; each brand keeps its own context, projects and sending."
        action={canManage && <Button onClick={() => setAdding(true)}>Add a brand</Button>}
      />
      {active.length === 0 ? (
        <Empty title="One brand so far" action={canManage && <Button onClick={() => setAdding(true)}>Add a brand</Button>}>
          If you market more than one product or website, such as an app and a game, add each as a brand. You can then switch between them in the sidebar, give agents each one's context, and keep their mailboxes apart.
        </Empty>
      ) : (
        <div className="space-y-3">
          <div className="flex justify-end">
            <div role="group" aria-label="Show brands as" className="inline-flex rounded-md border p-0.5">
              {(["cards", "table"] as const).map((v) => (
                <button
                  key={v}
                  type="button"
                  aria-pressed={view === v}
                  onClick={() => setView(v)}
                  className={cn("inline-flex h-8 items-center gap-1.5 rounded px-3 text-[13px] transition-colors", view === v ? "bg-muted font-medium" : "text-muted-foreground hover:text-foreground")}
                >
                  {v === "cards" ? <LayoutGrid className="size-3.5" /> : <List className="size-3.5" />}
                  {v === "cards" ? "Cards" : "Table"}
                </button>
              ))}
            </div>
          </div>
          {view === "cards" ? (
            <ul className="grid gap-3 md:grid-cols-2">
              {active.map((b) => <BrandCard key={b.id} brand={b} onOpen={() => setOpen(b.handle)} />)}
            </ul>
          ) : (
            <BrandTable brands={active} onOpen={(b) => setOpen(b.handle)} />
          )}
        </div>
      )}
      {active.length >= 2 && <ContactRuleCard ws={ws} canManage={canManage} />}
      {archived.length > 0 && (
        <Card title="Archived" description="Hidden from the sidebar and pickers. Their records are kept.">
          <ul className="divide-y divide-border rounded-lg border">
            {archived.map((b) => (
              <li key={b.id}>
                <button type="button" onClick={() => setOpen(b.handle)} className="flex min-h-11 w-full items-center gap-3 px-3 py-2 text-left text-sm hover:bg-muted/50">
                  <span className="flex-1 truncate">{b.name}</span>
                  <span className="font-mono text-xs text-muted-foreground">{b.website}</span>
                </button>
              </li>
            ))}
          </ul>
        </Card>
      )}
      <Card title="Ask your agent" description="In any MCP client connected to Outflare:">
        <ul className="space-y-1.5 text-sm text-muted-foreground">
          <li>“Add Repocket as a brand and move its domains into it.”</li>
          <li>“Read repocket.com and fill in what Repocket is, who it's for and how it sounds.”</li>
          <li>“How did each of our brands do last month?”</li>
        </ul>
      </Card>
      {adding && <AddBrand ws={ws} onClose={() => setAdding(false)} onAdded={(b) => { setAdding(false); reload(); setOpen(b.handle); }} />}
    </div>
  );
}

function BrandCard({ brand: b, onOpen }: { brand: Brand; onOpen: () => void }) {
  const c = b.counts;
  return (
    <li>
      <button type="button" onClick={onOpen} className="flex h-full w-full flex-col gap-2 rounded-lg border bg-card p-4 text-left transition-colors hover:border-foreground/20 hover:bg-muted/40">
        <span className="flex items-baseline justify-between gap-2">
          <span className="truncate font-medium">{b.name}</span>
          <span className="truncate font-mono text-xs text-muted-foreground">{b.website}</span>
        </span>
        {b.platforms.length > 0 && (
          <span className="flex flex-wrap gap-1">{b.platforms.map((p) => <Badge key={p}>{platformNames[p]}</Badge>)}</span>
        )}
        {b.links.length > 0 && <span className="truncate text-xs text-muted-foreground">Also on {b.links.map(linkName).join(", ")}</span>}
        <span className="text-xs text-muted-foreground">
          {[plural(c.projects, "project"), plural(c.domains, "domain"), plural(c.mailboxes, "mailbox", "mailboxes"), `${c.active_sequences} sending`].join(" · ")}
        </span>
      </button>
    </li>
  );
}

function BrandTable({ brands, onOpen }: { brands: Brand[]; onOpen: (b: Brand) => void }) {
  const num = (label: string, get: (b: Brand) => number) => ({ id: label.toLowerCase(), header: label, accessorFn: get, meta: { mono: true } });
  return (
    <DataTable
      data={brands}
      rowId={(b) => b.id}
      onRowClick={onOpen}
      columnsMenu={false}
      initialSort={[{ id: "name", desc: false }]}
      mobile={(b) => (
        <div className="space-y-1">
          <div className="flex items-baseline justify-between gap-2">
            <span className="font-medium">{b.name}</span>
            <span className="truncate font-mono text-xs text-muted-foreground">{b.website}</span>
          </div>
          <div className="text-xs text-muted-foreground">{plural(b.counts.projects, "project")} · {b.counts.active_sequences} sending</div>
        </div>
      )}
      columns={[
        { id: "name", header: "Brand", accessorFn: (b) => b.name, cell: ({ row: { original: b } }) => <span className="font-medium group-hover:underline">{b.name}</span> },
        { id: "website", header: "Website", accessorFn: (b) => b.website, cell: ({ row: { original: b } }) => <span className="font-mono text-[13px] text-muted-foreground">{b.website || "–"}</span> },
        {
          id: "platforms",
          header: "Ships on",
          accessorFn: (b) => b.platforms.join(" "),
          enableSorting: false,
          cell: ({ row: { original: b } }) => <span className="text-[13px]">{b.platforms.map((p) => platformNames[p]).join(", ") || "–"}</span>,
        },
        { id: "links", header: "Links", accessorFn: (b) => b.links.length, meta: { mono: true }, cell: ({ row: { original: b } }) => <span title={b.links.map(linkName).join(", ")}>{b.links.length}</span> },
        num("Projects", (b) => b.counts.projects),
        num("Domains", (b) => b.counts.domains),
        num("Mailboxes", (b) => b.counts.mailboxes),
        num("Sending", (b) => b.counts.active_sequences),
      ]}
    />
  );
}

// ContactRuleCard turns the cross-brand contact rule on and off: no person
// gets a first email from one brand within some days of another brand's.
function ContactRuleCard({ ws, canManage }: { ws: string; canManage: boolean }) {
  const [saved, setSaved] = useState<number | null | undefined>(undefined);
  const [on, setOn] = useState(false);
  const [days, setDays] = useState("30");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/brand-rules", { params: { path: { ws } } }).then(({ data, error }) => {
      if (error) return setError(errorMessage(error));
      const d = data?.cross_brand_days ?? null;
      setSaved(d);
      setOn(d !== null);
      if (d !== null) setDays(String(d));
    });
  }, [ws]);
  if (saved === undefined && !error) return <Card title="Contacting the same person"><Loading /></Card>;
  const n = Number(days);
  const valid = !on || (Number.isInteger(n) && n >= 1 && n <= 365);
  const next = on ? n : null;

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/brand-rules", { params: { path: { ws } }, body: { cross_brand_days: next } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setSaved(data?.cross_brand_days ?? null);
    toast(next === null ? "Brands can email the same people again." : `Saved. A second brand now waits ${plural(next, "day")}.`);
  }

  return (
    <Card
      title="Contacting the same person"
      description="Leads and unsubscribes are shared, so an unsubscribe from one brand stops every brand. You can also keep two brands from cold emailing the same person close together."
    >
      <form onSubmit={save} className="space-y-4">
        <label className="flex min-h-11 items-center gap-3 text-sm">
          <Switch checked={on} onCheckedChange={setOn} disabled={!canManage} aria-label="Keep brands from emailing the same person" />
          <span>Don't start a brand's sequence for someone another brand emailed recently</span>
        </label>
        {on && (
          <div className="max-w-md">
            <Input
              label="Days to wait"
              hint="Their first email from a second brand is skipped, and they show as blocked in that sequence. Follow-ups already under way carry on. Sequences shared by every brand don't count."
              type="number"
              min={1}
              max={365}
              value={days}
              onChange={(e) => setDays(e.target.value)}
              readOnly={!canManage}
              required
            />
          </div>
        )}
        <ErrorText>{error}</ErrorText>
        {canManage && <Button type="submit" disabled={busy || next === saved || !valid}>{busy ? "Saving…" : "Save"}</Button>}
      </form>
    </Card>
  );
}

function splitDomains(s: string) {
  return s.split(/[\s,]+/).map((d) => d.trim()).filter(Boolean);
}

function AddBrand({ ws, onClose, onAdded }: { ws: string; onClose: () => void; onAdded: (b: Brand) => void }) {
  const [name, setName] = useState("");
  const [website, setWebsite] = useState("");
  const [picked, setPicked] = useState<Brand["platforms"]>(["web"]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/brands", { params: { path: { ws } }, body: { name: name.trim(), website: website.trim() || undefined, platforms: picked } });
    setBusy(false);
    if (error || !data || !("id" in data)) return setError(errorMessage(error));
    toast(`${data.name} added. Next, tell Outflare about it.`);
    onAdded(data);
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Add a brand</SheetTitle>
          <SheetDescription>A business you market with its own website. Connectors and people are shared, so there is nothing to reconnect.</SheetDescription>
        </SheetHeader>
        <form onSubmit={submit} className="space-y-5 px-4 pb-6">
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} maxLength={100} required autoFocus placeholder="Repocket" />
          <Input label="Website" hint="Its point of truth. Analytics for this domain count as the brand's." value={website} onChange={(e) => setWebsite(e.target.value)} maxLength={253} placeholder="repocket.com" />
          <PlatformPicker value={picked} onChange={setPicked} />
          <ErrorText>{error}</ErrorText>
          <div className="flex gap-2">
            <Button type="submit" disabled={busy || !name.trim()}>{busy ? "Adding…" : "Add brand"}</Button>
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}

function PlatformPicker({ value, onChange, readOnly }: { value: Brand["platforms"]; onChange: (v: Brand["platforms"]) => void; readOnly?: boolean }) {
  return (
    <fieldset className="text-sm">
      <legend className="mb-1.5 text-[13px] font-medium">Where it ships</legend>
      <div className="flex flex-wrap gap-1.5">
        {platforms.map((p) => {
          const on = value.includes(p);
          return (
            <button
              key={p}
              type="button"
              disabled={readOnly}
              aria-pressed={on}
              onClick={() => onChange(on ? value.filter((x) => x !== p) : [...value, p])}
              className={cn("h-9 rounded-md border px-3 text-[13px] transition-colors disabled:cursor-default", on ? "border-foreground/30 bg-muted font-medium" : "text-muted-foreground hover:bg-muted/50")}
            >
              {platformNames[p]}
            </button>
          );
        })}
      </div>
    </fieldset>
  );
}

function BrandPage({ ws, brand: b, canManage, canEdit, onBack, onChanged, go }: { ws: string; brand: Brand; canManage: boolean; canEdit: boolean; onBack: () => void; onChanged: () => void; go: (t: Tab, item?: string) => void }) {
  async function setStatus(status: "active" | "archived") {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/brands/{brand}", { params: { path: { ws, brand: b.id } }, body: { status } });
    if (error) return toast(errorMessage(error), "error");
    toast(status === "archived" ? `${b.name} archived. Its records are kept.` : `${b.name} restored.`);
    onChanged();
  }

  return (
    <div className="max-w-3xl space-y-6">
      <PageHeader
        back={{ label: "Brands", onClick: onBack }}
        title={<span className="flex items-center gap-2">{b.name} {b.status === "archived" && <Badge tone="amber">Archived</Badge>}</span>}
        description={b.website ? <span className="font-mono">{b.website}</span> : undefined}
        action={
          <div className="flex items-center gap-2">
            {b.status === "active" && <Button variant="ghost" onClick={() => { setBrand(b.handle); go("overview"); }}>Show only {b.name}</Button>}
            {canManage && <Menu aria={`Actions for ${b.name}`} items={[b.status === "active" ? { label: "Archive", onClick: () => setStatus("archived"), danger: true } : { label: "Restore", onClick: () => setStatus("active") }]} />}
          </div>
        }
      />
      <BrandContextCard ws={ws} brand={b} canEdit={canEdit} />
      <Members ws={ws} brand={b} canEdit={canEdit} onChanged={onChanged} />
      <AnalyticsSites ws={ws} brand={b} go={go} />
      <DetailsCard ws={ws} brand={b} canManage={canManage} onSaved={onChanged} go={go} />
    </div>
  );
}

type ContextForm = Pick<BrandContext, "about" | "audience" | "goals" | "voice" | "rules" | "notes">;
const contextKeys = ["about", "audience", "goals", "voice", "rules", "notes"] as const;
const toContextForm = (c: BrandContext): ContextForm => Object.fromEntries(contextKeys.map((k) => [k, c[k]])) as ContextForm;

function BrandContextCard({ ws, brand: b, canEdit }: { ws: string; brand: Brand; canEdit: boolean }) {
  const [c, setC] = useState<BrandContext | null>(null);
  const [form, setForm] = useState<ContextForm | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/brands/{brand}/context", { params: { path: { ws, brand: b.id } } });
    if (data) {
      setC(data);
      setForm(toContextForm(data));
    }
  }, [ws, b.id]);
  useEffect(() => void load(), [load]);

  const saved = useMemo(() => (c ? JSON.stringify(toContextForm(c)) : ""), [c]);
  if (!c || !form) return <Card title={`About ${b.name}`}><Loading /></Card>;
  const dirty = JSON.stringify(form) !== saved;
  const ro = !canEdit;
  const set = (k: keyof ContextForm) => (e: { target: { value: string } }) => setForm((f) => f && { ...f, [k]: e.target.value });

  async function save(e: FormEvent) {
    e.preventDefault();
    if (!c || !form) return;
    setBusy(true);
    setError("");
    const { data, error, response } = await api.PATCH("/v1/workspaces/{ws}/brands/{brand}/context", { params: { path: { ws, brand: b.id } }, body: { ...form, version: c.version } });
    setBusy(false);
    if (error || !data || !("version" in data)) {
      return setError(response.status === 409 ? "Someone (or an agent) changed this while you were editing. Copy anything you want to keep, then reload the page." : errorMessage(error));
    }
    toast(data.version > c.version ? `Saved. Agents working on ${b.name} read this from now on.` : "No changes to save.");
    setC(data);
    setForm(toContextForm(data));
  }

  return (
    <Card
      title={`About ${b.name}`}
      description={<>What is true for {b.name} only. Agents read it with the company's context: rules add to the company's, the rest wins where set. {c.updated_at ? <>Version {c.version}, updated {ago(c.updated_at)}{c.updated_by ? ` by ${c.updated_by}` : ""}.</> : null}</>}
    >
      <form onSubmit={save} className="space-y-4">
        <TextArea label="What it is" hint="A few sentences, the way you'd explain it to a new hire." value={form.about} onChange={set("about")} rows={3} maxLength={4000} readOnly={ro} placeholder="Earn passive income by sharing unused internet." />
        <TextArea label="Who it's for" value={form.audience} onChange={set("audience")} rows={2} maxLength={4000} readOnly={ro} placeholder="Consumers who want a little extra money each month." />
        <TextArea label="Goals" value={form.goals} onChange={set("goals")} rows={2} maxLength={4000} readOnly={ro} placeholder="50,000 new installs a month on Android." />
        <TextArea label="Voice" hint="Replaces the company's voice for this brand." value={form.voice} onChange={set("voice")} rows={2} maxLength={4000} readOnly={ro} placeholder="Friendly and casual." />
        <TextArea label="Rules" hint="Added to the company's rules, never replacing them." value={form.rules} onChange={set("rules")} rows={2} maxLength={4000} readOnly={ro} placeholder="Never mention the parent company." />
        <TextArea label="Notes" hint="Markdown. Anything else: proof points, competitors, app store links." value={form.notes} onChange={set("notes")} rows={5} maxLength={100000} readOnly={ro} className="font-mono text-[12px]" />
        <ErrorText>{error}</ErrorText>
        {!ro && (
          <div className="flex flex-wrap items-center gap-2">
            <Button type="submit" disabled={busy || !dirty}>{busy ? "Saving…" : "Save"}</Button>
            {dirty && <Button type="button" variant="ghost" onClick={() => { setForm(toContextForm(c)); setError(""); }}>Discard changes</Button>}
            {dirty && !error && <span className="text-xs text-amber-700 dark:text-amber-400">Unsaved changes</span>}
          </div>
        )}
      </form>
    </Card>
  );
}

// Members moves projects and sending domains in and out of the brand. A
// project brings everything in it; a domain brings its mailboxes.
function Members({ ws, brand: b, canEdit, onChanged }: { ws: string; brand: Brand; canEdit: boolean; onChanged: () => void }) {
  const { byId } = useBrand(ws);
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [domains, setDomains] = useState<SendingDomain[] | null>(null);
  const [busy, setBusy] = useState("");

  const load = useCallback(async () => {
    const [p, d] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/projects", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/sending-domains", { params: { path: { ws } } }),
    ]);
    if (p.data) setProjects(p.data.data);
    if (d.data) setDomains(d.data.data);
  }, [ws]);
  useEffect(() => void load(), [load]);

  async function move(key: string, on: boolean, body: { project_ids?: string[]; domains?: string[] }) {
    setBusy(key);
    const call = on ? "/v1/workspaces/{ws}/brands/{brand}/add" : "/v1/workspaces/{ws}/brands/{brand}/remove";
    const { data, error } = await api.POST(call, { params: { path: { ws, brand: b.id } }, body });
    setBusy("");
    if (error || !data || !("moved" in data)) return toast(errorMessage(error), "error");
    if (data.moved.in_project > 0) toast("That domain is in a project, so it follows the project's brand. Move the project instead.", "error");
    load();
    onChanged();
  }

  const where = (id?: string | null) => (id === b.id ? undefined : byId(id)?.name);
  const row = (key: string, label: ReactNode, sub: ReactNode, on: boolean, locked: string | undefined, toggle: (on: boolean) => void) => (
    <li key={key} className="flex min-h-12 items-center gap-3 px-3 py-2">
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm">{label}</p>
        {sub && <p className="truncate text-xs text-muted-foreground">{sub}</p>}
      </div>
      {locked ? (
        <span className="text-xs text-muted-foreground">{locked}</span>
      ) : (
        <Switch checked={on} disabled={!canEdit || busy === key} onCheckedChange={toggle} aria-label={`${on ? "Remove from" : "Add to"} ${b.name}`} />
      )}
    </li>
  );

  return (
    <Card title={`What belongs to ${b.name}`} description="Turn on what is this brand's. Everything else is shared by every brand. A brand's mailboxes only send for its own sequences.">
      {!projects || !domains ? (
        <Loading />
      ) : (
        <div className="space-y-5">
          <section>
            <h3 className="mb-2 text-[13px] font-medium">Projects <span className="font-normal text-muted-foreground">(with their lists, sequences and domains)</span></h3>
            {projects.length === 0 ? (
              <p className="text-sm text-muted-foreground">No projects yet.</p>
            ) : (
              <ul className="divide-y divide-border rounded-lg border">
                {projects.map((p) => {
                  const other = where(p.brand_id);
                  return row(`p:${p.id}`, p.name, other ? `In ${other}` : p.brand_id ? undefined : "Shared", p.brand_id === b.id, undefined, (on) => move(`p:${p.id}`, on, { project_ids: [p.id] }));
                })}
              </ul>
            )}
          </section>
          <section>
            <h3 className="mb-2 text-[13px] font-medium">Sending domains <span className="font-normal text-muted-foreground">(with their mailboxes)</span></h3>
            {domains.length === 0 ? (
              <p className="text-sm text-muted-foreground">No sending domains yet.</p>
            ) : (
              <ul className="divide-y divide-border rounded-lg border">
                {domains.map((d) => {
                  const other = where(d.brand_id);
                  const inProject = d.project_id ? projects.find((p) => p.id === d.project_id)?.name ?? "a project" : undefined;
                  return row(
                    `d:${d.domain}`,
                    <span className="font-mono">{d.domain}</span>,
                    other ? `In ${other}` : d.brand_id ? `${plural(d.mailboxes, "mailbox", "mailboxes")}` : "Shared",
                    d.brand_id === b.id,
                    inProject ? `Follows ${inProject}` : undefined,
                    (on) => move(`d:${d.domain}`, on, { domains: [d.domain] }),
                  );
                })}
              </ul>
            )}
          </section>
        </div>
      )}
    </Card>
  );
}

function AnalyticsSites({ ws, brand: b, go }: { ws: string; brand: Brand; go: (t: Tab, item?: string) => void }) {
  const [sites, setSites] = useState<AnalyticsSiteBrand[] | null>(null);
  useEffect(() => {
    void api.GET("/v1/workspaces/{ws}/analytics/site-brands", { params: { path: { ws }, query: { brand: b.id } } }).then(({ data }) => setSites(data?.data ?? []));
  }, [ws, b.id]);
  if (!sites) return null;
  return (
    <Card
      title="Analytics"
      description={<>Sites in your analytics tools count as {b.name}'s when their domain is one of its domains. Pick a different brand for a site on the Analytics page.</>}
      action={<Button variant="ghost" onClick={() => go("analytics")}>Open Analytics</Button>}
    >
      {sites.length === 0 ? (
        <p className="text-sm text-muted-foreground">No synced analytics for {b.domains.length ? b.domains.join(", ") : "this brand"} yet.</p>
      ) : (
        <ul className="divide-y divide-border rounded-lg border">
          {sites.map((s) => (
            <li key={s.integration_id + s.site} className="flex min-h-11 items-center gap-3 px-3 py-2 text-sm">
              <span className="min-w-0 flex-1 truncate font-mono">{s.host || s.site}</span>
              <Badge tone={s.source === "set" ? "blue" : "grey"}>{s.source === "set" ? "Picked" : "By domain"}</Badge>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function DetailsCard({ ws, brand: b, canManage, onSaved, go }: { ws: string; brand: Brand; canManage: boolean; onSaved: () => void; go: (t: Tab, item?: string) => void }) {
  const initial = useMemo(
    () => ({ name: b.name, handle: b.handle, website: b.website, domains: b.domains.filter((d) => d !== b.website).join(", "), platforms: b.platforms, links: b.links }),
    [b],
  );
  const [form, setForm] = useState(initial);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => setForm(initial), [initial]);
  const dirty = JSON.stringify(form) !== JSON.stringify(initial);
  const ro = !canManage;

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { error } = await api.PATCH("/v1/workspaces/{ws}/brands/{brand}", {
      params: { path: { ws, brand: b.id } },
      body: { name: form.name.trim(), handle: form.handle.trim(), website: form.website.trim(), domains: splitDomains(form.domains), platforms: form.platforms, links: form.links.filter((l) => l.url.trim()) },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast("Saved.");
    if (form.handle.trim() !== b.handle) setBrand("");
    onSaved();
  }

  return (
    <Card title="Details" description="Its name, website, other domains and links. The handle names the brand in links and API calls (?brand=).">
      <form onSubmit={save} className="space-y-4">
        <div className="grid gap-4 md:grid-cols-2">
          <Input label="Name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} maxLength={100} readOnly={ro} required />
          <Input label="Handle" value={form.handle} onChange={(e) => setForm({ ...form, handle: e.target.value.toLowerCase() })} maxLength={40} readOnly={ro} required />
        </div>
        <Input label="Website" value={form.website} onChange={(e) => setForm({ ...form, website: e.target.value })} maxLength={253} readOnly={ro} placeholder="repocket.com" />
        <Input label="Other domains" hint="Separated by commas, such as its app and help sites." value={form.domains} onChange={(e) => setForm({ ...form, domains: e.target.value })} readOnly={ro} placeholder="repocket.co, help.repocket.com" />
        <PlatformPicker value={form.platforms} onChange={(platforms) => setForm({ ...form, platforms })} readOnly={ro} />
        <LinksEditor value={form.links} onChange={(links) => setForm({ ...form, links })} readOnly={ro} />
        <LinkSuggestions
          ws={ws}
          brand={b}
          links={form.links}
          readOnly={ro}
          go={go}
          onFill={(kind) => setForm({ ...form, links: [...form.links, { url: "", kind, label: "" }] })}
          onAdded={(links) => {
            // Added on the server already: keep unsaved edits when there are any.
            if (dirty) setForm({ ...form, links: [...form.links, ...links.filter((l) => !form.links.some((f) => f.url === l.url))] });
            else onSaved();
          }}
        />
        <ErrorText>{error}</ErrorText>
        {!ro && (
          <div className="flex gap-2">
            <Button type="submit" disabled={busy || !dirty}>{busy ? "Saving…" : "Save"}</Button>
            {dirty && <Button type="button" variant="ghost" onClick={() => setForm(initial)}>Discard changes</Button>}
          </div>
        )}
      </form>
    </Card>
  );
}

// LinksEditor lists where else the brand lives: app store listings,
// socials, docs. The kind is guessed from the address when it is saved.
function LinksEditor({ value, onChange, readOnly }: { value: Link[]; onChange: (v: Link[]) => void; readOnly?: boolean }) {
  const set = (i: number, l: Partial<Link>) => onChange(value.map((x, j) => (j === i ? { ...x, ...l } : x)));
  return (
    <fieldset className="text-sm">
      <legend className="mb-1.5 text-[13px] font-medium">Links</legend>
      <p className="mb-2 text-xs text-muted-foreground">App store listings, social profiles, docs: anywhere the brand lives besides its website. Agents read these too.</p>
      {value.length > 0 && (
        <ul className="mb-2 space-y-2">
          {value.map((l, i) => (
            <li key={i} className="flex flex-wrap items-center gap-2 sm:flex-nowrap">
              <select aria-label="Kind of link" className={cn(field, "w-full sm:w-44")} value={l.kind ?? ""} disabled={readOnly} onChange={(e) => set(i, { kind: (e.target.value || undefined) as LinkKind | undefined })}>
                <option value="">Guess from address</option>
                {(Object.keys(linkNames) as LinkKind[]).map((k) => <option key={k} value={k}>{linkNames[k]}</option>)}
              </select>
              <input aria-label="Address" className={cn(field, "min-w-0 flex-1 font-mono text-[12px]")} value={l.url} readOnly={readOnly} placeholder="https://apps.apple.com/app/…" onChange={(e) => set(i, { url: e.target.value })} />
              <input aria-label="Label (optional)" className={cn(field, "w-full sm:w-36")} value={l.label ?? ""} readOnly={readOnly} maxLength={60} placeholder="Label (optional)" onChange={(e) => set(i, { label: e.target.value })} />
              {readOnly ? (
                <a href={l.url} target="_blank" rel="noreferrer" className="text-xs underline">Open</a>
              ) : (
                <button type="button" aria-label={`Remove ${linkName(l)} link`} onClick={() => onChange(value.filter((_, j) => j !== i))} className="inline-flex size-9 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground">
                  <X className="size-4" />
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {!readOnly && value.length < 50 && (
        <Button type="button" variant="ghost" onClick={() => onChange([...value, { url: "", label: "" }])}>
          <Plus className="size-3.5" /> Add a link
        </Button>
      )}
    </fieldset>
  );
}

// LinkSuggestions shows the social profiles and app listings found on the
// brand's website (read through the workspace's own Geonode connection),
// each to add, correct or dismiss, and the common places it has no link
// for yet, to fill in by hand.
function LinkSuggestions({ ws, brand: b, links, readOnly, go, onFill, onAdded }: {
  ws: string; brand: Brand; links: Link[]; readOnly: boolean; go: (t: Tab, item?: string) => void; onFill: (kind: LinkKind) => void; onAdded: (links: Link[]) => void;
}) {
  const [list, setList] = useState<{ data: BrandLinkSuggestion[]; gaps: string[]; can_discover: boolean }>();
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState("");
  const [looking, setLooking] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/brands/{brand}/link-suggestions", { params: { path: { ws, brand: b.id } } });
    if (error) setError(errorMessage(error));
    else if (data) setList(data);
    return data;
  }, [ws, b.id]);
  useEffect(() => { load(); }, [load, b.website]);

  async function find() {
    setLooking(true);
    setError("");
    const { error } = await api.POST("/v1/workspaces/{ws}/brands/{brand}/links/discover", { params: { path: { ws, brand: b.id } } });
    if (error) {
      setLooking(false);
      return setError(errorMessage(error));
    }
    // Reading the page takes a few seconds; check until something shows.
    const before = list?.data.length ?? 0;
    for (let i = 0; i < 8; i++) {
      await new Promise((r) => setTimeout(r, 2500));
      const d = await load();
      if (d && d.data.length > before) break;
    }
    setLooking(false);
  }

  async function decide(x: BrandLinkSuggestion, how: "add" | "dismiss") {
    setBusy(x.id);
    setError("");
    const url = (edits[x.id] ?? x.url).trim();
    const res = how === "add"
      ? await api.POST("/v1/workspaces/{ws}/brands/{brand}/link-suggestions/{suggestion}/add", { params: { path: { ws, brand: b.id, suggestion: x.id } }, body: url !== x.url ? { url } : {} })
      : await api.POST("/v1/workspaces/{ws}/brands/{brand}/link-suggestions/{suggestion}/dismiss", { params: { path: { ws, brand: b.id, suggestion: x.id } } });
    setBusy("");
    if (res.error) return setError(errorMessage(res.error));
    if (how === "add" && res.data && "links" in res.data) {
      toast(x.kind === "x" ? "Added. Its followers are read every day through Geonode." : "Added.");
      onAdded(res.data.links);
    }
    load();
  }

  if (!list) return null;
  const waiting = list.data;
  const gaps = list.gaps.filter((k) => !links.some((l) => l.kind === k)) as LinkKind[];
  if (readOnly && waiting.length === 0) return null;
  return (
    <div className="rounded-lg border border-dashed p-3 text-sm">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="text-[13px] font-medium">Found on {b.website || "the website"}</div>
          <p className="text-xs text-muted-foreground">
            {waiting.length > 0 ? "Check each one, correct the address if it's wrong, then add it." : b.website ? "Social profiles and app listings linked from the brand's website." : "Give the brand a website to find its links."}
          </p>
        </div>
        {!readOnly && list.can_discover && (
          <Button type="button" variant="ghost" onClick={find} disabled={looking}>
            <Search className="size-3.5" /> {looking ? "Reading the website…" : "Find links"}
          </Button>
        )}
      </div>
      {!readOnly && b.website && !list.can_discover && (
        <p className="mt-2 text-xs text-muted-foreground">
          To find its links, Outflare reads the website with your own Geonode account (a token or two per brand).
          <span className="mt-2 block"><ConnectFor connector="geonode" name="Geonode" back="brands" item={b.id} why={geonodeWhy.brands} go={go} variant="ghost" /></span>
        </p>
      )}
      {waiting.length > 0 && (
        <ul className="mt-3 space-y-2">
          {waiting.map((x) => (
            <li key={x.id} className="flex flex-wrap items-center gap-2 sm:flex-nowrap">
              <Badge>{linkNames[x.kind as LinkKind] ?? x.kind}</Badge>
              <input aria-label={`${linkNames[x.kind as LinkKind] ?? x.kind} address`} className={cn(field, "min-w-0 flex-1 font-mono text-[12px]")} value={edits[x.id] ?? x.url} readOnly={readOnly}
                onChange={(e) => setEdits({ ...edits, [x.id]: e.target.value })} />
              {!readOnly && (
                <>
                  <Button type="button" onClick={() => decide(x, "add")} disabled={busy === x.id}><Check className="size-3.5" /> Add</Button>
                  <Button type="button" variant="ghost" onClick={() => decide(x, "dismiss")} disabled={busy === x.id}>Not theirs</Button>
                </>
              )}
            </li>
          ))}
        </ul>
      )}
      {!readOnly && gaps.length > 0 && (
        <div className="mt-3 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
          <span>No link yet for</span>
          {gaps.map((k) => (
            <button key={k} type="button" onClick={() => onFill(k)} className="inline-flex items-center gap-1 rounded-md border px-2 py-0.5 hover:bg-muted hover:text-foreground">
              <Plus className="size-3" /> {linkNames[k]}
            </button>
          ))}
        </div>
      )}
      <ErrorText>{error}</ErrorText>
    </div>
  );
}
