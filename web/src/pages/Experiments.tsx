import { useCallback, useEffect, useState } from "react";
import type { Experiment, ExperimentGroup, SEOSite } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Input, Loading, PageHeader, confirmAction, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

// SEO experiments: split a site's pages into matched treatment and control
// groups, change only the treatment pages, and see whether it helped once
// the change has been live long enough. Results come from Search Console.

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };
type Tone = keyof typeof tones;

const statusLabel: Record<Experiment["status"], string> = { draft: "Draft", waiting: "Waiting for a deploy", measuring: "Measuring", done: "Done", stopped: "Stopped" };
const statusTone: Record<Experiment["status"], Tone> = { draft: "grey", waiting: "amber", measuring: "blue", done: "green", stopped: "grey" };
const verdictLabel = { better: "Better", worse: "Worse", no_clear_change: "No clear change", too_early: "Too early to tell" } as const;
const verdictTone: Record<keyof typeof verdictLabel, Tone> = { better: "green", worse: "red", no_clear_change: "grey", too_early: "grey" };
const metricLabel = { clicks: "Clicks", impressions: "Impressions", ctr: "CTR", position: "Position" } as const;
const pct = (x: number | null | undefined) => (x === null || x === undefined ? "–" : `${x > 0 ? "+" : ""}${Math.round(x * 100)}%`);

export function Experiments({ ws, canManage, go }: Props) {
  const [list, setList] = useState<Experiment[] | null>(null);
  const [sites, setSites] = useState<SEOSite[]>([]);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const [open, setOpen] = useState("");
  const brandKey = useBrand(ws).query.brand ?? "";

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/experiments", { params: { path: { ws }, query: brandKey ? { brand: brandKey } : {} } });
    setList(data?.data ?? []);
    setError(error ? errorMessage(error) : "");
  }, [ws, brandKey]);
  useEffect(() => {
    load();
    api.GET("/v1/workspaces/{ws}/seo-sites", { params: { path: { ws }, query: brandKey ? { brand: brandKey } : {} } }).then(({ data }) => setSites(data?.data ?? []));
  }, [load, ws, brandKey]);

  return (
    <div className="max-w-5xl space-y-6">
      <PageHeader
        title="Experiments"
        description="Find out whether a change to your pages helped. Outflare holds back a matched group of pages, so a Google update or a seasonal dip isn't mistaken for your change."
        action={canManage && sites.length > 0 && <Button onClick={() => setCreating(true)}>New experiment</Button>}
      />
      <ErrorText>{error}</ErrorText>
      {creating && <CreateForm ws={ws} sites={sites} onDone={(id) => { setCreating(false); if (id) { load(); setOpen(id); } }} />}
      {!list && <Loading />}
      {list && list.length === 0 && !creating && (
        <Empty title="No experiments yet" action={sites.length === 0 ? <Button variant="ghost" onClick={() => go("audits")}>Add a site</Button> : undefined}>
          {sites.length === 0 ? "Add a site under Site audits and connect Google Search Console first." : "Make one to test a change on some of a site's pages before rolling it out everywhere."}
        </Empty>
      )}
      {list && list.length > 0 && (
        <ul className="divide-y rounded-lg border">
          {list.map((e) => (
            <li key={e.id}>
              <button className="flex w-full items-center justify-between gap-3 px-4 py-3 text-left text-sm hover:bg-muted/50" onClick={() => setOpen(e.id)}>
                <span className="min-w-0">
                  <span className="block truncate font-medium">{e.name}</span>
                  <span className="block truncate text-muted-foreground">
                    {e.site_host}{e.pattern || "/*"} · {metricLabel[e.metric]} · {e.design === "holdout" ? `${e.treatment_pages} vs ${e.control_pages} pages` : `${e.treatment_pages} pages, before and after`}
                  </span>
                </span>
                <span className="flex shrink-0 items-center gap-2">
                  {e.result && e.status === "done" && <Badge tone={verdictTone[e.result.verdict]}>{verdictLabel[e.result.verdict]} {pct(e.result.lift)}</Badge>}
                  <Badge tone={statusTone[e.status]}>{statusLabel[e.status]}</Badge>
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      <Sheet open={!!open} onOpenChange={(o) => !o && setOpen("")}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
          {open && <Detail key={open} ws={ws} id={open} canManage={canManage} onChange={load} onClose={() => setOpen("")} go={go} />}
        </SheetContent>
      </Sheet>
    </div>
  );
}

function CreateForm({ ws, sites, onDone }: { ws: string; sites: SEOSite[]; onDone: (id?: string) => void }) {
  const [f, setF] = useState({ site_id: sites[0]?.id ?? "", name: "", hypothesis: "", pattern: "", design: "holdout", metric: "clicks", start_on: "deploy" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/experiments", {
      params: { path: { ws } },
      body: { ...f, design: f.design as "holdout", metric: f.metric as "clicks", start_on: f.start_on as "deploy" },
    });
    setBusy(false);
    setError(error ? errorMessage(error) : "");
    if (data && "id" in data) {
      toast(`Picked ${data.treatment_pages + data.control_pages} pages.`);
      onDone(data.id);
    }
  }
  const select = (label: string, key: keyof typeof f, options: [string, string][]) => (
    <label className="block text-sm">
      <span className="mb-1.5 block text-[13px] font-medium">{label}</span>
      <select className={field + " w-full"} value={f[key]} onChange={(e) => setF({ ...f, [key]: e.target.value })}>
        {options.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
      </select>
    </label>
  );
  return (
    <Card title="New experiment" description="Outflare picks the matching pages from Search Console and splits them into matched groups.">
      <div className="grid gap-3 sm:grid-cols-2">
        <Input label="Name" placeholder="Shorter titles on blog posts" value={f.name} maxLength={200} onChange={(e) => setF({ ...f, name: e.target.value })} />
        {select("Site", "site_id", sites.map((s) => [s.id, s.host]))}
        <Input label="Pages" placeholder="/blog/* (empty is every page)" value={f.pattern} maxLength={300} onChange={(e) => setF({ ...f, pattern: e.target.value })} />
        {select("Measure", "metric", Object.entries(metricLabel))}
        {select("Design", "design", [["holdout", "Hold back half the pages (recommended)"], ["before_after", "Every page, before and after"]])}
        {select("Starts", "start_on", [["deploy", "On the site's next deploy"], ["manual", "When I say"]])}
      </div>
      <div className="mt-3">
        <Input label="Hypothesis (optional)" placeholder="Shorter titles get more clicks." value={f.hypothesis} maxLength={2000} onChange={(e) => setF({ ...f, hypothesis: e.target.value })} />
      </div>
      <div className="mt-4 flex gap-2">
        <Button disabled={busy || !f.name.trim() || !f.site_id} onClick={save}>{busy ? "Picking pages…" : "Make it"}</Button>
        <Button variant="ghost" onClick={() => onDone()}>Cancel</Button>
      </div>
      <ErrorText>{error}</ErrorText>
    </Card>
  );
}

function Detail({ ws, id, canManage, onChange, onClose, go }: { ws: string; id: string; canManage: boolean; onChange: () => void; onClose: () => void; go: (tab: Tab, item?: string) => void }) {
  const path = { params: { path: { ws, id } } };
  const [e, setE] = useState<Experiment | null>(null);
  const [pages, setPages] = useState<{ url: string; group: string }[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/experiments/{id}", path).then(({ data, error }) => { if (data) setE(data); setError(error ? errorMessage(error) : ""); });
    api.GET("/v1/workspaces/{ws}/experiments/{id}/pages", path).then(({ data }) => setPages(data?.data ?? []));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, id]);

  async function act(kind: "start" | "stop" | "delete") {
    if (kind === "delete" && !(await confirmAction("Delete this experiment?", "Its pages and result are removed.", "Delete"))) return;
    setBusy(kind);
    const res =
      kind === "delete"
        ? await api.DELETE("/v1/workspaces/{ws}/experiments/{id}", path)
        : await api.POST(kind === "start" ? "/v1/workspaces/{ws}/experiments/{id}/start" : "/v1/workspaces/{ws}/experiments/{id}/stop", path);
    setBusy("");
    setError(res.error ? errorMessage(res.error) : "");
    if (res.error) return;
    onChange();
    if (kind === "delete") return onClose();
    if (res.data && "id" in res.data) setE(res.data as Experiment);
  }

  if (!e) return <><Loading /><ErrorText>{error}</ErrorText></>;
  const r = e.result;
  const treatment = pages.filter((p) => p.group === "treatment");
  return (
    <div className="space-y-5 p-1">
      <SheetHeader className="p-0">
        <SheetTitle>{e.name}</SheetTitle>
        <SheetDescription>{e.hypothesis || `${e.site_host}${e.pattern || "/*"}, ${metricLabel[e.metric].toLowerCase()}`}</SheetDescription>
      </SheetHeader>
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <Badge tone={statusTone[e.status]}>{statusLabel[e.status]}</Badge>
        {e.changed_at && <span className="text-muted-foreground">Change live {ago(e.changed_at)}{e.deploy_id ? " (from a deploy)" : ""}</span>}
      </div>
      {e.status === "draft" && (
        <p className="text-sm">
          {e.design === "holdout" ? <>Make the change on the <b>{e.treatment_pages} treatment pages</b> below, and leave the {e.control_pages} control pages alone until it ends.</> : <>Make the change on all {e.treatment_pages} pages.</>}{" "}
          {e.start_on === "deploy" ? <>Then start it: the site's next production deploy starts the measurement (<button className="underline" onClick={() => go("connectors")}>watch its repository</button> if you haven't).</> : "Then start it once the change is live."}
        </p>
      )}
      {r && (
        <div className="rounded-lg border p-4">
          <div className="mb-3 flex items-center justify-between">
            <Badge tone={verdictTone[r.verdict]}>{verdictLabel[r.verdict]}</Badge>
            <span className="text-sm text-muted-foreground">{r.days_measured} of {e.measure_days} days{r.p_value !== null && ` · p = ${r.p_value.toFixed(3)}`}</span>
          </div>
          <div className="text-2xl font-semibold tabular-nums">{pct(r.lift)}</div>
          <div className="mb-3 text-sm text-muted-foreground">{e.design === "holdout" ? `${metricLabel[e.metric]}, treatment against control` : `${metricLabel[e.metric]}, after against before`}</div>
          <dl className="grid grid-cols-2 gap-3 text-sm">
            <GroupStat label="Treatment" g={r.treatment} />
            {r.control && <GroupStat label="Control" g={r.control} />}
          </dl>
        </div>
      )}
      {canManage && (
        <div className="flex flex-wrap gap-2">
          {e.status === "draft" && <Button disabled={busy !== ""} onClick={() => act("start")}>{e.start_on === "deploy" ? "Start on the next deploy" : "The change is live"}</Button>}
          {e.status !== "done" && e.status !== "stopped" && <Button variant="ghost" disabled={busy !== ""} onClick={() => act("stop")}>{e.status === "measuring" ? "End it now" : "Stop"}</Button>}
          <Button variant="danger" disabled={busy !== ""} onClick={() => act("delete")}>Delete</Button>
        </div>
      )}
      <ErrorText>{error}</ErrorText>
      <div>
        <h3 className="mb-1 text-sm font-medium">{e.design === "holdout" ? `Treatment pages (${treatment.length})` : `Pages (${treatment.length})`}</h3>
        <ul className="max-h-80 overflow-y-auto font-mono text-xs">
          {treatment.map((p) => <li key={p.url} className="truncate py-0.5">{p.url}</li>)}
        </ul>
      </div>
    </div>
  );
}

function GroupStat({ label, g }: { label: string; g: ExperimentGroup }) {
  return (
    <div>
      <dt className="text-muted-foreground">{label} · {g.pages} pages</dt>
      <dd className="tabular-nums">{g.before.toFixed(1)} → {g.after.toFixed(1)} <span className="text-muted-foreground">({pct(g.change)})</span></dd>
    </div>
  );
}
