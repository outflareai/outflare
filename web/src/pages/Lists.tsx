import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import type { CustomerProfile, DataPerson, DataProvider, LeadList, LeadListMember, ListRun, PeopleFilter, SmartList } from "@outflare/sdk";
import { ArrowDown, ArrowUp, Search, X } from "lucide-react";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, Stat, Stats, Status, toast } from "../ui";
import { DataTable } from "@/components/data-table";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { ago, plural } from "../format";
import type { Tab } from "../App";
import { ConnectorLogo } from "@/components/connector-logo";
import { SmartListDetail, SmartListSheet } from "./SmartLists";

// One row of the Lists page: a lead list or a smart list.
type Row = { id: string; name: string; description?: string | null; brand_id?: string | null; lead?: LeadList; smart?: SmartList };
type FilterKey = keyof PeopleFilter;

// Words people use, for the shared filter names and seniorities.
const filterLabels: Record<FilterKey, string> = {
  titles: "Job titles",
  seniorities: "Seniority",
  industries: "Industries",
  employee_ranges: "Company size",
  locations: "Person location",
  company_locations: "Company location",
  company_domains: "Company domains",
  keywords: "Keywords",
};
const filterHints: Partial<Record<FilterKey, string>> = {
  titles: "e.g. Head of Sales, VP Sales. Similar titles usually match too.",
  locations: "Where the person lives, e.g. Australia or Sydney.",
  company_locations: "Where the company is based.",
  company_domains: "Only people at these companies, e.g. acme-com.example.",
  keywords: "Words about the company, e.g. SaaS, fintech.",
};
const seniorityLabels: Record<string, string> = {
  owner: "Owner", founder: "Founder", c_suite: "C-suite", partner: "Partner", vp: "VP", head: "Head",
  director: "Director", manager: "Manager", senior: "Senior", entry: "Entry", intern: "Intern",
};
const skipLabels: Record<string, string> = {
  already_in_list: "already in this list",
  low_score: "below the minimum fit score",
  no_email: "no email found",
  unverified: "email not verified by the provider",
  suppressed: "on the suppression list",
  contacted: "emailed recently",
  in_sequence: "already in a sequence",
  undeliverable: "failed verification",
};
const kindLabels: Record<ListRun["kind"], string> = { preview: "Preview", build: "Build", refresh: "Refresh", enrich: "Enrich" };

const active = (r?: ListRun | null) => !!r && (r.status === "queued" || r.status === "running");

// Filling the list to max_leads is the normal end of a run, not a warning.
const stoppedEarly = (r: ListRun) => !!r.stop_reason && !/^Added \d+ new leads/.test(r.stop_reason);

function runTone(r: ListRun): "green" | "amber" | "red" | "grey" | "blue" {
  if (active(r)) return "blue";
  if (r.status === "failed") return "red";
  if (r.status === "cancelled") return "grey";
  return stoppedEarly(r) ? "amber" : "green";
}

function runStatus(r: ListRun): string {
  if (r.status === "queued") return "queued";
  if (r.status === "running") return "running";
  if (r.status === "failed") return "failed";
  if (r.status === "cancelled") return "cancelled";
  return stoppedEarly(r) ? "stopped early" : "done";
}

function skippedTotal(r: ListRun) {
  return Object.values(r.skipped).reduce((a, b) => a + b, 0);
}

function credits(n: number) {
  return `${Number.isInteger(n) ? n : n.toFixed(1)} ${n === 1 ? "credit" : "credits"}`;
}

export function Lists({ ws, canManage, open, setOpen, go }: { ws: string; canManage: boolean; open?: string; setOpen: (id?: string) => void; go: (t: Tab) => void }) {
  const [lists, setLists] = useState<LeadList[] | null>(null);
  const [smart, setSmart] = useState<SmartList[] | null>(null);
  const [editingSmart, setEditingSmart] = useState<SmartList | "new" | null>(null);
  const [choosing, setChoosing] = useState(false);
  const [providers, setProviders] = useState<DataProvider[]>([]);
  const [profiles, setProfiles] = useState<CustomerProfile[]>([]);
  const [editing, setEditing] = useState<LeadList | "new" | null>(null);
  const [connecting, setConnecting] = useState<DataProvider | null>(null);
  const [error, setError] = useState("");

  const brand = useBrand(ws).brand?.handle;
  const load = useCallback(async () => {
    const [l, sl, p, pr] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/lead-lists", { params: { path: { ws }, query: { ...(brand ? { brand } : {}) } } }),
      api.GET("/v1/workspaces/{ws}/smart-lists", { params: { path: { ws }, query: { ...(brand ? { brand } : {}) } } }),
      api.GET("/v1/workspaces/{ws}/data-providers", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/ideal-customer-profiles", { params: { path: { ws } } }),
    ]);
    if (l.data) setLists(l.data.data);
    else setError(errorMessage(l.error));
    setSmart(sl.data?.data ?? []);
    if (p.data) setProviders(p.data.data);
    if (pr.data) setProfiles(pr.data.data);
  }, [ws, brand]);

  useEffect(() => {
    load();
  }, [load]);

  // Refresh while any list has a run going.
  const busy = lists?.some((l) => active(l.last_run));
  useEffect(() => {
    if (!busy) return;
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, [busy, load]);

  const connected = providers.flatMap((p) => p.integrations.map((i) => ({ ...i, provider: p })));
  const connectedSearch = connected.filter((i) => i.provider.capabilities.includes("data.search_people"));
  const integrationName = (id?: string | null) => connected.find((i) => i.id === id)?.provider.name;

  const sheet = (
    <>
      {editing && (
        <ListSheet
          ws={ws}
          list={editing === "new" ? null : editing}
          providers={providers}
          profiles={profiles}
          onConnect={setConnecting}
          onClose={() => setEditing(null)}
          onSaved={(l) => {
            setEditing(null);
            load();
            setOpen(l.id);
          }}
        />
      )}
      {choosing && (
        <NewListChoice
          onClose={() => setChoosing(false)}
          onPick={(k) => {
            setChoosing(false);
            if (k === "smart") setEditingSmart("new");
            else setEditing("new");
          }}
        />
      )}
      {editingSmart && (
        <SmartListSheet
          ws={ws}
          list={editingSmart === "new" ? null : editingSmart}
          onClose={() => setEditingSmart(null)}
          onSaved={(l) => {
            setEditingSmart(null);
            load();
            setOpen(l.id);
          }}
        />
      )}
      {connecting && <ConnectDialog ws={ws} provider={connecting} onClose={() => setConnecting(null)} onDone={() => { setConnecting(null); load(); }} />}
    </>
  );

  const openSmart = open ? smart?.find((l) => l.id === open) : undefined;
  if (open && smart === null) return <Loading />;
  if (openSmart) {
    return (
      <>
        <SmartListDetail
          key={open}
          ws={ws}
          list={openSmart}
          canManage={canManage}
          onBack={() => setOpen(undefined)}
          onEdit={(l) => setEditingSmart(l)}
          onChanged={load}
        />
        {sheet}
      </>
    );
  }
  if (open) {
    return (
      <>
        <ListDetail
          key={open}
          ws={ws}
          id={open}
          canManage={canManage}
          integrationName={integrationName}
          onBack={() => setOpen(undefined)}
          onEdit={(l) => setEditing(l)}
          onChanged={load}
          go={go}
        />
        {sheet}
      </>
    );
  }

  const rows: Row[] = [
    ...(lists ?? []).map((l) => ({ id: l.id, name: l.name, description: l.description, brand_id: l.brand_id, lead: l })),
    ...(smart ?? []).map((l) => ({ id: l.id, name: l.name, description: l.description, brand_id: l.brand_id, smart: l })),
  ];
  const total = (lists?.length ?? 0) + (smart?.length ?? 0);
  const noProvider = connectedSearch.length === 0;
  const newList = canManage && <Button onClick={() => setChoosing(true)}>New list</Button>;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Lists"
        description="Lead lists hold people you found or imported. Smart lists fill themselves with the people who match your rules, and keep up as people change."
        action={newList}
      />
      <ErrorText>{error}</ErrorText>
      {lists === null && !error && <Loading />}
      {lists !== null && noProvider && (
        <Card
          title="Connect a data provider to build lists from a search"
          description="Outflare has no contact database of its own. It searches the providers you already pay for, with your API key, and you keep paying them directly."
        >
          <ul className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {providers.filter((p) => p.status === "available").map((p) => (
              <li key={p.connector} className="flex items-start gap-3 rounded-md border p-3">
                <ConnectorLogo id={p.connector} />
                <div className="min-w-0 flex-1">
                  <div className="text-[13px] font-medium">{p.name}</div>
                  <div className="text-[12px] text-muted-foreground">{p.summary}</div>
                  <div className="mt-1 font-mono text-[11px] text-muted-foreground">{p.capabilities.includes("data.search_people") ? "search" : "finds emails"}{p.pricing ? ` · ${p.pricing}` : ""}</div>
                </div>
                {canManage && <Button variant="ghost" onClick={() => setConnecting(p)}>Connect</Button>}
              </li>
            ))}
          </ul>
          <p className="mt-3 text-[12px] text-muted-foreground">
            Coming later: {providers.filter((p) => p.status === "planned").map((p) => p.name).join(", ")}. Until then, export from them and{" "}
            <button className="underline" onClick={() => go("leads")}>import a CSV</button>.
          </p>
        </Card>
      )}
      {lists !== null && total === 0 && (
        <Empty
          title="No lists yet"
          action={newList}
        >
          {noProvider
            ? "Make a smart list from the people you already have, connect a provider above to build a list from a search, or create an empty list and import leads into it."
            : `Search ${connectedSearch.map((i) => i.provider.name).filter((v, i, a) => a.indexOf(v) === i).join(" or ")} for the people you want to reach. You'll see a preview before any credits are spent.`}
        </Empty>
      )}
      {lists !== null && total > 0 && (
        <DataTable
          data={rows}
          rowId={(l) => l.id}
          onRowClick={(l) => setOpen(l.id)}
          searchPlaceholder="Search lists"
          mobile={({ lead: l, smart: s, name, brand_id }) => (
            <div className="space-y-1">
              <div className="flex items-start justify-between gap-2">
                <span className="flex flex-wrap items-center gap-1.5 font-medium">{name} <BrandBadge id={brand_id} /></span>
                <span className="font-mono text-[12px] tabular-nums">{s ? plural(s.members, "person", "people") : plural(l!.leads, "lead")}</span>
              </div>
              <div className="text-[12px] text-muted-foreground">{s ? "Smart list" : l!.kind === "search" ? `${integrationName(l!.search_integration_id) ?? "Search"} search` : "Manual"}</div>
              {l?.last_run && <RunLine run={l.last_run} />}
              {s && <SmartLine list={s} />}
            </div>
          )}
          columns={[
            {
              id: "name",
              header: "Name",
              accessorFn: (l) => l.name,
              cell: ({ row: { original: l } }) => (
                <div className="min-w-0">
                  <div className="flex items-center gap-1.5"><span className="font-medium">{l.name}</span> <BrandBadge id={l.brand_id} /></div>
                  {l.description && <div className="max-w-xs truncate text-[12px] text-muted-foreground">{l.description}</div>}
                </div>
              ),
            },
            {
              id: "source",
              header: "Source",
              accessorFn: ({ lead: l }) => (!l ? "Smart list" : l.kind === "search" ? integrationName(l.search_integration_id) ?? "Search" : "Manual"),
              cell: ({ row: { original: { lead: l } } }) =>
                !l ? (
                  <Badge tone="blue">Smart list</Badge>
                ) : l.kind === "search" ? (
                  <span>{integrationName(l.search_integration_id) ?? <span className="text-amber-600 dark:text-amber-400">Provider disconnected</span>} <span className="text-muted-foreground">search</span></span>
                ) : (
                  <span className="text-muted-foreground">Manual</span>
                ),
            },
            { id: "leads", header: "People", accessorFn: (r) => r.smart?.members ?? r.lead!.leads, meta: { align: "right" }, cell: ({ getValue }) => <span className="font-mono tabular-nums">{(getValue() as number).toLocaleString()}</span> },
            {
              id: "last",
              header: "Last run",
              accessorFn: (r) => r.smart?.refreshed_at ?? r.lead!.last_run?.created_at ?? "",
              cell: ({ row: { original: { lead: l, smart: s } } }) => (s ? <SmartLine list={s} /> : l!.last_run ? <RunLine run={l!.last_run} /> : <span className="text-muted-foreground">–</span>),
            },
            {
              id: "refresh",
              header: "Refresh",
              accessorFn: (r) => (r.smart ? -1 : r.lead!.refresh_every_days ?? 0),
              cell: ({ row: { original: { lead: l, smart: s } } }) => (s ? <span>Every 15 minutes</span> : l!.refresh_every_days ? <span>Every {plural(l!.refresh_every_days, "day")}</span> : <span className="text-muted-foreground">When asked</span>),
            },
          ]}
        />
      )}
      {sheet}
    </div>
  );
}

function SmartLine({ list }: { list: SmartList }) {
  if (list.error_code) return <Status tone="amber">Couldn't update</Status>;
  if (!list.refreshed_at) return <Status tone="blue">Filling</Status>;
  return <span className="text-[12px] text-muted-foreground">Updated {ago(list.refreshed_at)}</span>;
}

// NewListChoice asks which kind of list to make.
function NewListChoice({ onClose, onPick }: { onClose: () => void; onPick: (k: "smart" | "lead") => void }) {
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>New list</SheetTitle>
          <SheetDescription>Pick the kind of list. You can use either in email, journeys and sequences.</SheetDescription>
        </SheetHeader>
        <div className="grid gap-2 px-4 pb-6 text-[13px]">
          <button type="button" className="rounded-md border p-3 text-left hover:bg-muted/50" onClick={() => onPick("smart")}>
            <span className="block font-medium">Smart list</span>
            <span className="block text-[12px] text-muted-foreground">People you already have who match rules, such as a field, something they did, or another list. It keeps itself up to date.</span>
          </button>
          <button type="button" className="rounded-md border p-3 text-left hover:bg-muted/50" onClick={() => onPick("lead")}>
            <span className="block font-medium">Lead list</span>
            <span className="block text-[12px] text-muted-foreground">New people found with a data provider search, or imported from a CSV.</span>
          </button>
        </div>
      </SheetContent>
    </Sheet>
  );
}

function RunLine({ run }: { run: ListRun }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[12px]">
      <Status tone={runTone(run)}>{kindLabels[run.kind]} {runStatus(run)}</Status>
      <span className="text-muted-foreground">
        {run.kind === "enrich" ? `${run.enriched} filled in` : `+${run.added.toLocaleString()}`}
        {run.credits > 0 && ` · ${credits(run.credits)}`} · {ago(run.finished_at ?? run.created_at)}
      </span>
    </span>
  );
}

// ---- One list ----

function ListDetail({
  ws, id, canManage, integrationName, onBack, onEdit, onChanged, go,
}: {
  ws: string; id: string; canManage: boolean; integrationName: (id?: string | null) => string | undefined;
  onBack: () => void; onEdit: (l: LeadList) => void; onChanged: () => void; go: (t: Tab) => void;
}) {
  const [list, setList] = useState<LeadList | null>(null);
  const [runs, setRuns] = useState<ListRun[]>([]);
  const [members, setMembers] = useState<LeadListMember[]>([]);
  const [total, setTotal] = useState(0);
  const [q, setQ] = useState("");
  const [sort, setSort] = useState<"score" | "added" | "email">("score");
  const [error, setError] = useState("");
  const [starting, setStarting] = useState(false);

  const load = useCallback(async () => {
    const [l, r, m] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/lead-lists/{id}", { params: { path: { ws, id } } }),
      api.GET("/v1/workspaces/{ws}/lead-lists/{id}/runs", { params: { path: { ws, id } } }),
      api.GET("/v1/workspaces/{ws}/lead-lists/{id}/members", { params: { path: { ws, id }, query: { sort, limit: 200, ...(q ? { q } : {}) } } }),
    ]);
    if (l.error) return setError(errorMessage(l.error));
    if (l.data) setList(l.data);
    if (r.data) setRuns(r.data.data);
    if (m.data) {
      setMembers(m.data.data);
      setTotal(m.data.total);
    }
  }, [ws, id, q, sort]);

  useEffect(() => {
    const t = setTimeout(load, 150);
    return () => clearTimeout(t);
  }, [load]);

  const running = active(list?.last_run);
  const wasRunning = useRef(false);
  useEffect(() => {
    if (running) {
      wasRunning.current = true;
      const t = setInterval(load, 2000);
      return () => clearInterval(t);
    }
    if (wasRunning.current) {
      wasRunning.current = false;
      onChanged();
    }
  }, [running, load, onChanged]);

  async function start(kind: "build" | "refresh" | "enrich") {
    setStarting(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/lead-lists/{id}/runs", { params: { path: { ws, id } }, body: { kind } });
    setStarting(false);
    if (error) return toast(errorMessage(error), "error");
    toast(kind === "enrich" ? "Enrichment started." : "Build started. New leads appear as each page finishes.");
    load();
  }

  async function cancel(run: ListRun) {
    const { error } = await api.POST("/v1/workspaces/{ws}/list-runs/{run}/cancel", { params: { path: { ws, run: run.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Stopped. Leads already added stay in the list.");
    load();
  }

  async function remove() {
    if (!list) return;
    const ok = await confirmAction(`Delete ${list.name}?`, <p>The list is deleted. Its {plural(list.leads, "lead")} stay in Leads and in any sequence they are already in.</p>, "Delete list");
    if (!ok) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/lead-lists/{id}", { params: { path: { ws, id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Deleted ${list.name}.`);
    onChanged();
    onBack();
  }

  async function removeMember(m: LeadListMember) {
    const { error } = await api.DELETE("/v1/workspaces/{ws}/lead-lists/{id}/leads/{lead}", { params: { path: { ws, id, lead: m.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Removed ${m.email} from the list.`);
    load();
  }

  if (error) return <div className="space-y-4"><PageHeader title="Lead list" back={{ label: "Lists", onClick: onBack }} /><ErrorText>{error}</ErrorText></div>;
  if (!list) return <Loading />;

  const isSearch = list.kind === "search";
  const last = list.last_run;
  const spent = runs.reduce((a, r) => a + r.credits, 0);
  const canEnrich = list.enrich_integration_ids.length > 0 || isSearch;
  const f = list.filters;

  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Lists", onClick: onBack }}
        title={<>{list.name}<Badge tone={isSearch ? "blue" : "grey"}>{isSearch ? `${integrationName(list.search_integration_id) ?? "search"}` : "manual"}</Badge></>}
        description={list.description || (isSearch ? "Built from a search. Refresh to add people who match now and weren't found before." : "Add leads by importing a CSV, through the API, or from an agent.")}
        action={
          canManage && (
            <>
              {isSearch && !running && (
                <Button disabled={starting} onClick={() => start(last ? "refresh" : "build")}>{last ? "Refresh now" : "Build list"}</Button>
              )}
              {running && last && <Button variant="ghost" onClick={() => cancel(last)}>Stop run</Button>}
              <Button variant="ghost" onClick={() => onEdit(list)}>Settings</Button>
              <Menu
                items={[
                  canEnrich && !running && { label: "Fill in missing details", onClick: () => start("enrich") },
                  !isSearch && { label: "Import leads into this list", onClick: () => go("leads") },
                  { label: "Add to a sequence", onClick: () => go("sequences") },
                  { label: "Delete list", onClick: remove, danger: true },
                ]}
              />
            </>
          )
        }
      />

      <Stats cols={4}>
        <Stat label="Leads" value={list.leads.toLocaleString()} />
        <Stat label="Last run" value={last ? (last.kind === "enrich" ? last.enriched.toLocaleString() : `+${last.added.toLocaleString()}`) : "–"} sub={last ? `${kindLabels[last.kind].toLowerCase()} ${runStatus(last)}, ${ago(last.finished_at ?? last.created_at)}` : isSearch ? "Not built yet" : undefined} />
        <Stat label="Credits spent" value={spent % 1 === 0 ? spent.toLocaleString() : spent.toFixed(1)} sub={list.credit_cap != null ? `cap ${list.credit_cap} per run` : "no cap per run"} />
        <Stat label="Next refresh" value={list.next_refresh_at && list.refresh_every_days ? new Date(list.next_refresh_at).toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" }) : "–"} sub={list.refresh_every_days ? `every ${plural(list.refresh_every_days, "day")}` : "only when asked"} />
      </Stats>

      {last && <RunCard run={last} canManage={canManage} onCancel={() => cancel(last)} />}

      {isSearch && (
        <Card title="Search" action={canManage && <Button variant="ghost" onClick={() => onEdit(list)}>Change</Button>}>
          <dl className="grid gap-x-6 gap-y-2 text-[13px] sm:grid-cols-2">
            {(Object.keys(filterLabels) as FilterKey[]).filter((k) => (f[k]?.length ?? 0) > 0).map((k) => (
              <div key={k} className="min-w-0">
                <dt className="font-mono text-[11px] text-muted-foreground uppercase">{filterLabels[k]}</dt>
                <dd className="break-words">{k === "seniorities" ? f[k]!.map((s) => seniorityLabels[s] ?? s).join(", ") : f[k]!.join(", ")}</dd>
              </div>
            ))}
            <div>
              <dt className="font-mono text-[11px] text-muted-foreground uppercase">Find emails with</dt>
              <dd>{list.enrich_integration_ids.length ? list.enrich_integration_ids.map((i) => integrationName(i) ?? "disconnected").join(" → ") : `${integrationName(list.search_integration_id) ?? "the search provider"}`}</dd>
            </div>
            <div>
              <dt className="font-mono text-[11px] text-muted-foreground uppercase">Leave out</dt>
              <dd>
                {[
                  list.min_score > 0 && `fit below ${list.min_score}`,
                  list.skip_contacted_days > 0 && `emailed in the last ${plural(list.skip_contacted_days, "day")}`,
                  list.skip_in_sequences && "people in another sequence",
                  list.verify && "addresses that fail verification",
                  list.verified_emails_only && "unverified addresses",
                  "suppressed",
                ].filter(Boolean).join(", ")}
              </dd>
            </div>
          </dl>
        </Card>
      )}

      <section className="space-y-3">
        <h2 className="text-[13px] font-semibold">Leads in this list</h2>
        {list.leads === 0 && !q ? (
          <Empty title={isSearch ? (running ? "Building…" : "No leads yet") : "No leads yet"}>
            {isSearch ? (running ? "Leads appear here as each page of results finishes." : "Build the list to search for people and add the ones that fit.") : <>Import a CSV on <button className="underline" onClick={() => go("leads")}>Leads</button> and choose this list, or add leads through the API.</>}
          </Empty>
        ) : (
          <DataTable
            data={members}
            rowId={(m) => m.id}
            toolbar={
              <>
                <label className="relative min-w-0 flex-1 basis-60">
                  <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
                  <input type="search" aria-label="Search leads in this list" className={`${field} w-full pl-8`} placeholder="Search email, name, company or title" value={q} onChange={(e) => setQ(e.target.value)} />
                </label>
                <select aria-label="Sort" className={field} value={sort} onChange={(e) => setSort(e.target.value as typeof sort)}>
                  <option value="score">Best fit first</option>
                  <option value="added">Newest first</option>
                  <option value="email">By email</option>
                </select>
                <span className="font-mono text-[12px] text-muted-foreground">{plural(total, "lead")}{members.length < total ? `, showing ${members.length}` : ""}</span>
              </>
            }
            empty={<>No leads match. <button className="underline" onClick={() => setQ("")}>Clear the search</button></>}
            mobile={(m) => (
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="break-all font-mono text-[13px]">{m.email}</div>
                  <div className="text-xs text-muted-foreground">{[[m.first_name, m.last_name].filter(Boolean).join(" "), m.title, m.company].filter(Boolean).join(" · ") || "No name"}</div>
                </div>
                <Score member={m} />
              </div>
            )}
            columns={[
              { id: "score", header: "Fit", accessorFn: (m) => m.score ?? -1, cell: ({ row: { original: m } }) => <Score member={m} /> },
              { id: "name", header: "Name", accessorFn: (m) => [m.first_name, m.last_name].filter(Boolean).join(" "), cell: ({ getValue }) => (getValue() as string) || <span className="text-muted-foreground">–</span> },
              { id: "title", header: "Title", accessorFn: (m) => m.title ?? "", cell: ({ getValue }) => (getValue() as string) || <span className="text-muted-foreground">–</span> },
              { id: "company", header: "Company", accessorFn: (m) => m.company ?? "", cell: ({ getValue }) => (getValue() as string) || <span className="text-muted-foreground">–</span> },
              {
                id: "email",
                header: "Email",
                accessorFn: (m) => m.email,
                cell: ({ row: { original: m } }) => (
                  <span className="inline-flex flex-wrap items-center gap-1.5">
                    <span className="font-mono text-[12px]">{m.email}</span>
                    {m.suppressed && <Badge tone="red" title="On the suppression list: never emailed">Won't be emailed</Badge>}
                  </span>
                ),
              },
              { id: "added", header: "Added", accessorFn: (m) => m.added_at, cell: ({ row: { original: m } }) => <span className="text-muted-foreground">{ago(m.added_at)}</span> },
              {
                id: "actions",
                header: () => <span className="sr-only">Actions</span>,
                enableSorting: false,
                enableHiding: false,
                meta: { align: "right" },
                cell: ({ row: { original: m } }) => canManage && <Button variant="ghost" className="-my-1 opacity-0 group-hover:opacity-100 focus-visible:opacity-100" onClick={() => removeMember(m)}>Remove</Button>,
              },
            ]}
          />
        )}
      </section>

      {runs.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-[13px] font-semibold">Run history</h2>
          <DataTable
            data={runs}
            rowId={(r) => r.id}
            columnsMenu={false}
            mobile={(r) => <RunLine run={r} />}
            columns={[
              { id: "when", header: "Started", accessorFn: (r) => r.created_at, cell: ({ row: { original: r } }) => <span className="text-muted-foreground">{ago(r.created_at)}</span> },
              { id: "kind", header: "Run", accessorFn: (r) => r.kind, cell: ({ row: { original: r } }) => <Status tone={runTone(r)}>{kindLabels[r.kind]} {runStatus(r)}</Status> },
              { id: "found", header: "Found", accessorFn: (r) => r.found, meta: { align: "right" }, cell: ({ getValue }) => <span className="font-mono tabular-nums">{(getValue() as number).toLocaleString()}</span> },
              { id: "added", header: "Added", accessorFn: (r) => (r.kind === "enrich" ? r.enriched : r.added), meta: { align: "right" }, cell: ({ getValue }) => <span className="font-mono tabular-nums">{(getValue() as number).toLocaleString()}</span> },
              { id: "skipped", header: "Left out", accessorFn: (r) => skippedTotal(r), meta: { align: "right" }, cell: ({ row: { original: r } }) => <span className="font-mono tabular-nums" title={Object.entries(r.skipped).map(([k, n]) => `${n} ${skipLabels[k] ?? k}`).join("\n")}>{skippedTotal(r).toLocaleString()}</span> },
              { id: "credits", header: "Credits", accessorFn: (r) => r.credits, meta: { align: "right" }, cell: ({ getValue }) => <span className="font-mono tabular-nums">{getValue() as number}</span> },
              { id: "note", header: "Note", enableSorting: false, accessorFn: (r) => r.error ?? r.stop_reason ?? "", cell: ({ getValue }) => <span className="text-[12px] text-muted-foreground">{getValue() as string}</span> },
            ]}
          />
        </section>
      )}
    </div>
  );
}

function Score({ member: m }: { member: LeadListMember }) {
  if (m.score == null) return <span className="text-muted-foreground" title="No ideal customer profile to score against. Add filters, or pick an ideal customer profile in the list's settings.">–</span>;
  const tone = m.score >= 75 ? "green" : m.score >= 40 ? "amber" : "grey";
  const why = m.score_reasons.map((r) => (r.startsWith("+") ? `matches ${r.slice(2)}` : `doesn't match ${r.slice(2)}`)).join("\n");
  return <Badge tone={tone} title={why}>{m.score}</Badge>;
}

function RunCard({ run, canManage, onCancel }: { run: ListRun; canManage: boolean; onCancel: () => void }) {
  const skipped = Object.entries(run.skipped).filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1]);
  const going = active(run);
  return (
    <Card
      title={<span className="inline-flex items-center gap-2"><Status tone={runTone(run)}>{kindLabels[run.kind]} {runStatus(run)}</Status><span className="font-normal text-muted-foreground">{ago(run.started_at ?? run.created_at)}</span></span>}
      action={going && canManage && <Button variant="ghost" onClick={onCancel}>Stop</Button>}
    >
      <div className="space-y-3 text-[13px]">
        <p>
          {run.kind === "enrich" ? (
            <>Looked at {plural(run.found, "lead")} with missing details and filled in <b>{run.enriched.toLocaleString()}</b>.</>
          ) : (
            <>
              Found {plural(run.found, "person", "people")}
              {run.total_matches != null && run.total_matches > run.found && <> of {run.total_matches.toLocaleString()} matches</>}, added <b>{plural(run.added, "new lead")}</b>
              {run.enriched > 0 && <>, found {plural(run.enriched, "email")}</>}
              {run.credits > 0 && <>, spent {credits(run.credits)}</>}.
            </>
          )}
          {going && " Still going…"}
        </p>
        {skipped.length > 0 && (
          <div>
            <div className="mb-1 font-mono text-[11px] text-muted-foreground uppercase">Left out</div>
            <ul className="flex flex-wrap gap-x-4 gap-y-1">
              {skipped.map(([k, n]) => <li key={k}><span className="font-mono tabular-nums">{n.toLocaleString()}</span> <span className="text-muted-foreground">{skipLabels[k] ?? k}</span></li>)}
            </ul>
          </div>
        )}
        {run.stop_reason && <p className={stoppedEarly(run) ? "text-amber-700 dark:text-amber-400" : "text-muted-foreground"}>{run.stop_reason}</p>}
        {run.error && <p className="text-red-600 dark:text-red-400">{run.error}</p>}
      </div>
    </Card>
  );
}

// ---- Create and edit ----

type Draft = {
  name: string;
  description: string;
  profile_id: string;
  source: "search" | "manual";
  search_integration_id: string;
  filters: PeopleFilter;
  enrich: string[];
  max_leads: number;
  credit_cap: string;
  verify: boolean;
  verified_emails_only: boolean;
  skip_contacted_days: number;
  skip_in_sequences: boolean;
  min_score: number;
  refresh: string;
};

function draftFrom(l: LeadList | null, searchId: string): Draft {
  return {
    name: l?.name ?? "",
    description: l?.description ?? "",
    profile_id: l?.profile_id ?? "",
    source: l ? (l.kind === "search" ? "search" : "manual") : searchId ? "search" : "manual",
    search_integration_id: l?.search_integration_id ?? searchId,
    filters: l?.filters ?? {},
    enrich: l?.enrich_integration_ids ?? [],
    max_leads: l?.max_leads ?? 500,
    credit_cap: l?.credit_cap != null ? String(l.credit_cap) : "",
    verify: l?.verify ?? true,
    verified_emails_only: l?.verified_emails_only ?? false,
    skip_contacted_days: l?.skip_contacted_days ?? 90,
    skip_in_sequences: l?.skip_in_sequences ?? true,
    min_score: l?.min_score ?? 0,
    refresh: l?.refresh_every_days != null ? String(l.refresh_every_days) : "",
  };
}

function ListSheet({
  ws, list, providers, profiles, onConnect, onClose, onSaved,
}: {
  ws: string; list: LeadList | null; providers: DataProvider[]; profiles: CustomerProfile[];
  onConnect: (p: DataProvider) => void; onClose: () => void; onSaved: (l: LeadList) => void;
}) {
  const connected = providers.flatMap((p) => p.integrations.map((i) => ({ ...i, provider: p })));
  const searchers = connected.filter((i) => i.provider.capabilities.includes("data.search_people"));
  const finders = connected.filter((i) => i.provider.capabilities.includes("data.enrich_person") || i.provider.capabilities.includes("data.find_email"));
  const [d, setD] = useState<Draft>(() => draftFrom(list, searchers[0]?.id ?? ""));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<ListRun | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setD((x) => ({ ...x, [k]: v }));
  const searcher = searchers.find((s) => s.id === d.search_integration_id);
  const supported = searcher?.provider.filters ?? [];
  const profile = profiles.find((p) => p.id === d.profile_id);
  const icpKeys = profile ? (Object.keys(profile.filters ?? {}) as FilterKey[]).filter((k) => ((profile.filters as PeopleFilter)[k]?.length ?? 0) > 0) : [];
  const hasFilters = Object.values(d.filters).some((v) => (v?.length ?? 0) > 0);

  function setFilter(k: FilterKey, v: string[]) {
    setD((x) => ({ ...x, filters: { ...x.filters, [k]: v } }));
    setPreview(null);
  }

  function useICP() {
    if (!profile) return;
    const icp = profile.filters as PeopleFilter;
    const next: PeopleFilter = { ...d.filters };
    for (const k of icpKeys) if (supported.includes(k)) (next as Record<string, string[] | undefined>)[k] = icp[k];
    setD((x) => ({ ...x, filters: next }));
    setPreview(null);
  }

  // Poll a preview run until it finishes.
  useEffect(() => {
    if (!preview || !active(preview)) return;
    const t = setTimeout(async () => {
      const { data } = await api.GET("/v1/workspaces/{ws}/list-runs/{run}", { params: { path: { ws, run: preview.id } } });
      if (data) setPreview(data);
      if (data && !active(data)) setPreviewing(false);
    }, 1200);
    return () => clearTimeout(t);
  }, [preview, ws]);

  async function runPreview() {
    setPreviewing(true);
    setError("");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/lead-lists/preview", {
      params: { path: { ws } },
      body: { search_integration_id: d.search_integration_id, filters: clean(d.filters), profile_id: d.profile_id || null },
    });
    if (error) {
      setPreviewing(false);
      return setError(errorMessage(error));
    }
    setPreview(data ?? null);
  }

  async function submit(e: FormEvent, build: boolean) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const search = d.source === "search";
    const body = {
      name: d.name,
      description: d.description,
      profile_id: d.profile_id || null,
      search_integration_id: search ? d.search_integration_id || null : null,
      ...(search ? { filters: clean(d.filters), enrich_integration_ids: d.enrich } : {}),
      max_leads: d.max_leads,
      credit_cap: d.credit_cap === "" ? null : Number(d.credit_cap),
      verify: d.verify,
      verified_emails_only: d.verified_emails_only,
      skip_contacted_days: d.skip_contacted_days,
      skip_in_sequences: d.skip_in_sequences,
      min_score: d.min_score,
      refresh_every_days: search && d.refresh ? Number(d.refresh) : null,
    };
    const res = list
      ? await api.PATCH("/v1/workspaces/{ws}/lead-lists/{id}", { params: { path: { ws, id: list.id } }, body })
      : await api.POST("/v1/workspaces/{ws}/lead-lists", { params: { path: { ws } }, body: { ...body, name: d.name, build: build && search } });
    setBusy(false);
    if (res.error) return setError(errorMessage(res.error));
    if (res.data) {
      toast(list ? "Saved." : build && search ? `Created ${res.data.name}. Building it now.` : `Created ${res.data.name}.`);
      onSaved(res.data);
    }
  }

  const unsupported = (Object.keys(filterLabels) as FilterKey[]).filter((k) => !supported.includes(k) && (d.filters[k]?.length ?? 0) > 0);

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{list ? `Settings for ${list.name}` : "New list"}</SheetTitle>
          <SheetDescription>{list ? "Changes apply to the next build or refresh. Leads already in the list stay." : "Search a data provider for the people you want to reach, or start an empty list."}</SheetDescription>
        </SheetHeader>
        <form onSubmit={(e) => submit(e, true)} className="space-y-6 px-4 pb-6 text-[13px]">
          <div className="space-y-3">
            <label className="block">
              <span className="mb-1 block font-medium">Name</span>
              <input className={`${field} w-full`} required maxLength={100} placeholder="e.g. AU heads of sales, 11-50 staff" value={d.name} onChange={(e) => set("name", e.target.value)} />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Description <span className="font-normal text-muted-foreground">(optional)</span></span>
              <input className={`${field} w-full`} maxLength={2000} value={d.description} onChange={(e) => set("description", e.target.value)} />
            </label>
            {profiles.length > 0 && (
              <label className="block">
                <span className="mb-1 block font-medium">Ideal customer profile</span>
                <select className={`${field} w-full`} value={d.profile_id} onChange={(e) => set("profile_id", e.target.value)}>
                  <option value="">None</option>
                  {profiles.filter((p) => p.status === "active" || p.id === d.profile_id).map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
                </select>
                <span className="mt-1 block text-[12px] text-muted-foreground">{d.profile_id ? "Leads are scored against this profile." : "Pick a profile to score leads against it."}</span>
              </label>
            )}
          </div>

          {!list && (
            <fieldset className="space-y-2">
              <legend className="mb-1 font-medium">Where the leads come from</legend>
              <div className="grid gap-2 sm:grid-cols-2">
                <Choice checked={d.source === "search"} onChange={() => set("source", "search")} title="Search a data provider" body={searchers.length ? "Find people by title, seniority, company size and location." : "Connect a provider first."} disabled={searchers.length === 0} />
                <Choice checked={d.source === "manual"} onChange={() => set("source", "manual")} title="Empty list" body="Import a CSV, or add leads through the API or an agent." />
              </div>
              {searchers.length === 0 && (
                <p className="text-[12px] text-muted-foreground">
                  Connect:{" "}
                  {providers.filter((p) => p.status === "available" && p.capabilities.includes("data.search_people")).map((p, i) => (
                    <span key={p.connector}>{i > 0 && ", "}<button type="button" className="underline" onClick={() => onConnect(p)}>{p.name}</button></span>
                  ))}
                </p>
              )}
            </fieldset>
          )}

          {d.source === "search" && searchers.length > 0 && (
            <div className="space-y-4">
              <label className="block">
                <span className="mb-1 block font-medium">Search with</span>
                <select className={`${field} w-full`} value={d.search_integration_id} onChange={(e) => { set("search_integration_id", e.target.value); setPreview(null); }}>
                  {searchers.map((s) => <option key={s.id} value={s.id}>{s.name.toLowerCase().includes(s.provider.name.toLowerCase()) ? s.name : `${s.provider.name}: ${s.name}`}{s.status === "error" ? " – sign-in failing" : ""}</option>)}
                </select>
                {searcher?.provider.pricing && <span className="mt-1 block text-[12px] text-muted-foreground">{searcher.provider.pricing}.</span>}
              </label>

              <div className="space-y-3 rounded-md border p-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="font-medium">Who to find</span>
                  {profile && icpKeys.length > 0 && <Button type="button" variant="ghost" onClick={useICP}>Use the profile's filters</Button>}
                </div>
                {supported.includes("titles") && <Chips label={filterLabels.titles} hint={filterHints.titles} values={d.filters.titles ?? []} onChange={(v) => setFilter("titles", v)} />}
                {supported.includes("seniorities") && <Toggles label={filterLabels.seniorities} options={Object.keys(seniorityLabels)} labels={seniorityLabels} values={d.filters.seniorities ?? []} onChange={(v) => setFilter("seniorities", v)} />}
                {supported.includes("employee_ranges") && <Toggles label={filterLabels.employee_ranges} options={["1-10", "11-50", "51-200", "201-500", "501-1000", "1001-5000", "5001-10000", "10001+"]} values={d.filters.employee_ranges ?? []} onChange={(v) => setFilter("employee_ranges", v)} />}
                {(["industries", "locations", "company_locations", "company_domains", "keywords"] as FilterKey[]).filter((k) => supported.includes(k)).map((k) => (
                  <Chips key={k} label={filterLabels[k]} hint={filterHints[k]} values={d.filters[k] ?? []} onChange={(v) => setFilter(k, v)} />
                ))}
                {unsupported.length > 0 && <p className="text-amber-700 dark:text-amber-400">{searcher?.provider.name} can't filter by {unsupported.map((k) => filterLabels[k].toLowerCase()).join(", ")}. Clear those to save.</p>}
                <div className="flex flex-wrap items-center gap-2 border-t pt-3">
                  <Button type="button" variant="ghost" disabled={!hasFilters || previewing} onClick={runPreview}>{previewing ? "Searching…" : "Preview 10 people"}</Button>
                  <span className="text-[12px] text-muted-foreground">Nothing is saved{searcher?.provider.connector === "apollo" ? " and no credits are used" : ""}.</span>
                </div>
                {preview && <Preview run={preview} />}
              </div>

              <div className="space-y-2">
                <span className="block font-medium">Find missing emails with</span>
                <Waterfall finders={finders} value={d.enrich} onChange={(v) => set("enrich", v)} searchName={searcher?.provider.name} />
                {providers.some((p) => p.status === "available" && !p.capabilities.includes("data.search_people") && p.integrations.length === 0) && (
                  <p className="text-[12px] text-muted-foreground">
                    Add an email finder:{" "}
                    {providers.filter((p) => p.status === "available" && !p.capabilities.includes("data.search_people") && p.integrations.length === 0).map((p, i) => (
                      <span key={p.connector}>{i > 0 && ", "}<button type="button" className="underline" onClick={() => onConnect(p)}>{p.name}</button></span>
                    ))}
                  </p>
                )}
              </div>
            </div>
          )}

          <div className="space-y-3">
            <span className="block font-medium">Quality and dedupe</span>
            <Toggle checked={d.verify} onChange={(v) => set("verify", v)} title="Verify new addresses" body="Uses your email verification settings. Addresses they block are left out." />
            {d.source === "search" && <Toggle checked={d.verified_emails_only} onChange={(v) => set("verified_emails_only", v)} title="Only addresses the provider verified" body="Stricter: guessed addresses are left out even if they pass verification." />}
            <Toggle checked={d.skip_in_sequences} onChange={(v) => set("skip_in_sequences", v)} title="Leave out people already in a sequence" body="So two campaigns or two clients never email the same person at once." />
            <label className="flex flex-wrap items-center gap-2">
              <span>Leave out people emailed in the last</span>
              <input type="number" min={0} max={3650} className={`${field} w-20`} value={d.skip_contacted_days} onChange={(e) => set("skip_contacted_days", Number(e.target.value))} />
              <span>days <span className="text-muted-foreground">(0 to turn off)</span></span>
            </label>
            {d.source === "search" && (
              <label className="flex flex-wrap items-center gap-2">
                <span>Minimum fit score</span>
                <input type="number" min={0} max={100} className={`${field} w-20`} value={d.min_score} onChange={(e) => set("min_score", Number(e.target.value))} />
                <span className="text-muted-foreground">0 to 100. People below it are left out before any credits are spent.</span>
              </label>
            )}
          </div>

          {d.source === "search" && (
            <div className="space-y-3">
              <span className="block font-medium">Limits and refresh</span>
              <div className="grid gap-3 sm:grid-cols-3">
                <label className="block">
                  <span className="mb-1 block">New leads per run</span>
                  <input type="number" min={1} max={50000} className={`${field} w-full`} value={d.max_leads} onChange={(e) => set("max_leads", Number(e.target.value))} />
                </label>
                <label className="block">
                  <span className="mb-1 block">Credit cap per run</span>
                  <input type="number" min={0} step="any" className={`${field} w-full`} placeholder="No cap" value={d.credit_cap} onChange={(e) => set("credit_cap", e.target.value)} />
                </label>
                <label className="block">
                  <span className="mb-1 block">Refresh every</span>
                  <select className={`${field} w-full`} value={d.refresh} onChange={(e) => set("refresh", e.target.value)}>
                    <option value="">Only when asked</option>
                    <option value="1">Day</option>
                    <option value="7">Week</option>
                    <option value="14">2 weeks</option>
                    <option value="30">Month</option>
                  </select>
                </label>
              </div>
              <p className="text-[12px] text-muted-foreground">A run stops at whichever comes first. Answers are cached for 90 days, so a refresh never pays twice for the same person.</p>
            </div>
          )}

          <ErrorText>{error}</ErrorText>
          <div className="flex flex-wrap gap-2 border-t pt-4">
            {!list && d.source === "search" ? (
              <>
                <Button disabled={busy || !d.name || !hasFilters || unsupported.length > 0}>{busy ? "Creating…" : "Create and build"}</Button>
                <Button type="button" variant="ghost" disabled={busy || !d.name || unsupported.length > 0} onClick={(e) => submit(e, false)}>Create without building</Button>
              </>
            ) : (
              <Button disabled={busy || !d.name || unsupported.length > 0}>{busy ? "Saving…" : list ? "Save" : "Create list"}</Button>
            )}
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}

function clean(f: PeopleFilter): PeopleFilter {
  return Object.fromEntries(Object.entries(f).filter(([, v]) => (v?.length ?? 0) > 0)) as PeopleFilter;
}

function Choice({ checked, onChange, title, body, disabled }: { checked: boolean; onChange: () => void; title: string; body: string; disabled?: boolean }) {
  return (
    <label className={`flex cursor-pointer items-start gap-2 rounded-md border p-3 ${checked ? "border-primary bg-muted/50" : ""} ${disabled ? "cursor-not-allowed opacity-60" : ""}`}>
      <input type="radio" className="mt-0.5" checked={checked} onChange={onChange} disabled={disabled} />
      <span>
        <span className="block font-medium">{title}</span>
        <span className="block text-[12px] text-muted-foreground">{body}</span>
      </span>
    </label>
  );
}

function Toggle({ checked, onChange, title, body }: { checked: boolean; onChange: (v: boolean) => void; title: string; body: string }) {
  return (
    <label className="flex cursor-pointer items-start justify-between gap-3">
      <span>
        <span className="block">{title}</span>
        <span className="block text-[12px] text-muted-foreground">{body}</span>
      </span>
      <Switch checked={checked} onCheckedChange={onChange} />
    </label>
  );
}

// A free-text list: type a value and press Enter or comma.
function Chips({ label, hint, values, onChange }: { label: string; hint?: string; values: string[]; onChange: (v: string[]) => void }) {
  const [text, setText] = useState("");
  function add(raw: string) {
    const parts = raw.split(",").map((s) => s.trim()).filter(Boolean);
    const next = [...values];
    for (const p of parts) if (!next.some((v) => v.toLowerCase() === p.toLowerCase())) next.push(p);
    onChange(next.slice(0, 50));
    setText("");
  }
  return (
    <div>
      <span className="mb-1 block">{label}</span>
      <div className="flex min-h-8 flex-wrap items-center gap-1 rounded-md border border-input px-1.5 py-1 focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/40 dark:bg-input/30">
        {values.map((v) => (
          <span key={v} className="inline-flex items-center gap-1 rounded bg-muted px-1.5 py-0.5 text-[12px]">
            {v}
            <button type="button" aria-label={`Remove ${v}`} onClick={() => onChange(values.filter((x) => x !== v))}><X className="size-3" /></button>
          </span>
        ))}
        <input
          aria-label={label}
          className="min-w-24 flex-1 bg-transparent px-1 text-[13px] outline-none placeholder:text-muted-foreground"
          placeholder={values.length ? "" : "Type and press Enter"}
          value={text}
          onChange={(e) => (e.target.value.includes(",") ? add(e.target.value) : setText(e.target.value))}
          onKeyDown={(e) => {
            if (e.key === "Enter" && text.trim()) {
              e.preventDefault();
              add(text);
            } else if (e.key === "Backspace" && !text && values.length) {
              onChange(values.slice(0, -1));
            }
          }}
          onBlur={() => text.trim() && add(text)}
        />
      </div>
      {hint && <span className="mt-1 block text-[12px] text-muted-foreground">{hint}</span>}
    </div>
  );
}

function Toggles({ label, options, labels, values, onChange }: { label: string; options: string[]; labels?: Record<string, string>; values: string[]; onChange: (v: string[]) => void }) {
  return (
    <fieldset>
      <legend className="mb-1">{label}</legend>
      <div className="flex flex-wrap gap-1">
        {options.map((o) => {
          const on = values.includes(o);
          return (
            <button
              key={o}
              type="button"
              aria-pressed={on}
              onClick={() => onChange(on ? values.filter((v) => v !== o) : [...values, o])}
              className={`h-7 rounded-md border px-2 text-[12px] ${on ? "border-primary bg-primary text-primary-foreground" : "hover:bg-muted"}`}
            >
              {labels?.[o] ?? o}
            </button>
          );
        })}
      </div>
    </fieldset>
  );
}

// The email-finding waterfall: providers tried in order until one finds an
// address.
function Waterfall({ finders, value, onChange, searchName }: { finders: { id: string; name: string; status: string; provider: DataProvider }[]; value: string[]; onChange: (v: string[]) => void; searchName?: string }) {
  if (finders.length === 0) return <p className="text-[12px] text-muted-foreground">No email finders connected. {searchName ? `${searchName} reveals emails itself where it can.` : ""}</p>;
  const chosen = value.map((id) => finders.find((f) => f.id === id)).filter((f): f is (typeof finders)[number] => !!f);
  const rest = finders.filter((f) => !value.includes(f.id));
  const move = (i: number, by: number) => {
    const next = [...value];
    [next[i], next[i + by]] = [next[i + by], next[i]];
    onChange(next);
  };
  return (
    <div className="space-y-2">
      {chosen.length === 0 && <p className="text-[12px] text-muted-foreground">{searchName ? `${searchName} reveals emails itself where it can.` : "None chosen."} Add finders to try others in turn when it can't.</p>}
      <ol className="space-y-1">
        {chosen.map((f, i) => (
          <li key={f.id} className="flex items-center gap-2 rounded-md border px-2 py-1.5">
            <span className="w-5 font-mono text-[11px] text-muted-foreground">{i + 1}.</span>
            <span className="flex-1">{f.provider.name}{f.provider.pricing && <span className="ml-2 text-[12px] text-muted-foreground">{f.provider.pricing}</span>}</span>
            <button type="button" aria-label="Try earlier" disabled={i === 0} onClick={() => move(i, -1)} className="disabled:opacity-30"><ArrowUp className="size-3.5" /></button>
            <button type="button" aria-label="Try later" disabled={i === chosen.length - 1} onClick={() => move(i, 1)} className="disabled:opacity-30"><ArrowDown className="size-3.5" /></button>
            <button type="button" aria-label={`Remove ${f.provider.name}`} onClick={() => onChange(value.filter((v) => v !== f.id))}><X className="size-3.5" /></button>
          </li>
        ))}
      </ol>
      {rest.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {rest.map((f) => <Button key={f.id} type="button" variant="ghost" onClick={() => onChange([...value, f.id])}>+ {f.provider.name}</Button>)}
        </div>
      )}
    </div>
  );
}

function Preview({ run }: { run: ListRun }) {
  if (active(run)) return <p className="text-muted-foreground">Searching…</p>;
  if (run.status === "failed") return <ErrorText>{run.error ?? "The search failed."}</ErrorText>;
  if (run.sample.length === 0) return <p>No one matches. Try fewer or broader filters.</p>;
  return (
    <div className="space-y-2">
      <p>{run.total_matches != null ? <><b>{run.total_matches.toLocaleString()}</b> people match.</> : "People match."} The first {run.sample.length}:</p>
      <ul className="divide-y rounded-md border">
        {run.sample.map((p: DataPerson, i) => (
          <li key={p.provider_id ?? i} className="flex flex-wrap items-baseline justify-between gap-x-3 px-2 py-1.5">
            <span>{[p.first_name, p.last_name].filter(Boolean).join(" ") || "Name hidden"} <span className="text-muted-foreground">· {p.title || "No title"}</span></span>
            <span className="text-[12px] text-muted-foreground">{p.company?.name}{p.has_email ? "" : " · no email"}</span>
          </li>
        ))}
      </ul>
      <p className="text-[12px] text-muted-foreground">Some providers hide surnames and emails until a person is revealed, which happens when you build.</p>
    </div>
  );
}

// ---- Connect a provider ----

function ConnectDialog({ ws, provider, onClose, onDone }: { ws: string; provider: DataProvider; onClose: () => void; onDone: () => void }) {
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/integrations", {
      params: { path: { ws } },
      body: { connector: provider.connector, name: provider.name, credentials: values },
    } as never);
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`Connected ${provider.name}.`);
    onDone();
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Connect {provider.name}</SheetTitle>
          <SheetDescription>{provider.summary} Your key is encrypted and only used for the lists you build.</SheetDescription>
        </SheetHeader>
        <form onSubmit={submit} className="space-y-4 px-4 pb-6 text-[13px]">
          {provider.fields.map((f) => (
            <label key={f.name} className="block">
              <span className="mb-1 block font-medium">{f.label}{f.optional && <span className="font-normal text-muted-foreground"> (optional)</span>}</span>
              <input
                className={`${field} w-full font-mono`}
                type={f.secret ? "password" : "text"}
                autoComplete="off"
                required={!f.optional}
                placeholder={f.placeholder}
                value={values[f.name] ?? ""}
                onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))}
              />
              {f.help && <span className="mt-1 block text-[12px] text-muted-foreground">{f.help}</span>}
            </label>
          ))}
          {provider.key_url && <p><a className="underline" href={provider.key_url} target="_blank" rel="noreferrer">Find your {provider.name} API key ↗</a></p>}
          {provider.pricing && <p className="text-[12px] text-muted-foreground">{provider.pricing}. You pay {provider.name} directly.</p>}
          <ErrorText>{error}</ErrorText>
          <div className="flex gap-2">
            <Button disabled={busy}>{busy ? "Checking the key…" : "Connect"}</Button>
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}

