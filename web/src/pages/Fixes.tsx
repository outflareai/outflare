import { useCallback, useEffect, useMemo, useState } from "react";
import type { Fix, SEOSite } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, toast, type tones } from "../ui";
import { ago, plural } from "../format";
import type { Tab } from "../App";
import { severityLabel, severityTone } from "./SiteAudits";
import { FixAutonomy } from "./FixAutonomy";

// The fixes list: one list of work for the whole workspace, shared by
// people and agents. Site audits are its first source and link
// opportunities (seo_links, suggestions only) the next; another source adds
// its fixes here rather than keeping a list of its own. The page only needs
// the fixes API, so it can move to wherever the list lives next.

type Tone = keyof typeof tones;
type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };
type Status = Fix["status"];

const statusLabel: Record<Status, string> = { open: "Open", in_progress: "In progress", done: "Done", ignored: "Ignored" };
const statusTone: Record<Status, Tone> = { open: "grey", in_progress: "blue", done: "green", ignored: "grey" };
const views = { todo: "To do", open: "Open", in_progress: "In progress", done: "Done", ignored: "Ignored", all: "Everything" } as const;
type View = keyof typeof views;
const severityRank: Record<string, number> = { error: 0, warning: 1, notice: 2 };
const subjectName = (f: Fix, hosts: Record<string, string>) => (f.source === "social" ? "A social watch" : hosts[f.subject.id] ?? "A site");

export function Fixes({ ws, canManage, go }: Props) {
  const [fixes, setFixes] = useState<Fix[] | null>(null);
  const [sites, setSites] = useState<SEOSite[]>([]);
  const [error, setError] = useState("");
  const [view, setView] = useState<View>("todo");
  const [severity, setSeverity] = useState("");
  const [site, setSite] = useState("");
  const [picked, setPicked] = useState<string>("");
  const [autonomy, setAutonomy] = useState(false);
  const brandKey = useBrand(ws).query.brand ?? "";

  const load = useCallback(async () => {
    const query = { ...(brandKey ? { brand: brandKey } : {}), ...(view !== "todo" && view !== "all" ? { status: view } : {}), ...(severity ? { severity: severity as Fix["rule"]["severity"] } : {}), ...(site ? { subject_id: site } : {}) };
    const { data, error } = await api.GET("/v1/workspaces/{ws}/fixes", { params: { path: { ws }, query } });
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setFixes(data.data);
  }, [ws, brandKey, view, severity, site]);
  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo-sites", { params: { path: { ws }, query: brandKey ? { brand: brandKey } : {} } }).then(({ data }) => setSites(data?.data ?? []));
  }, [ws, brandKey]);

  const hosts = useMemo(() => Object.fromEntries(sites.map((s) => [s.id, s.host])), [sites]);
  const shown = useMemo(() => (fixes ?? []).filter((f) => view !== "todo" || f.status === "open" || f.status === "in_progress"), [fixes, view]);
  const current = fixes?.find((f) => f.id === picked);

  async function update(fix: Fix, body: { status?: Status; assignee_id?: string | null; note?: string }, done: string) {
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/fixes/{fix}", { params: { path: { ws, fix: fix.id } }, body });
    if (error || !data) return toast(errorMessage(error), "error");
    if ("queued" in data) return toast("Held for approval by the mission's owner.");
    setFixes((all) => all?.map((f) => (f.id === data.id ? data : f)) ?? null);
    toast(done);
  }

  async function verify(fix: Fix) {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/fixes/{fix}/verify", { params: { path: { ws, fix: fix.id } }, body: {} });
    if (error || !data) return toast(errorMessage(error), "error");
    if ("queued" in data) return toast("Held for approval by the mission's owner.");
    setFixes((all) => all?.map((f) => (f.id === data.fix.id ? data.fix : f)) ?? null);
    toast("Checking now. A fresh crawl confirms it, or opens it again if the issue is still there.");
  }

  const header = (
    <PageHeader
      title="Fixes"
      description="Everything worth fixing, in one list for people and agents: in progress first, then the worst and biggest. A fix marked done stays done once the next check no longer finds it. Hand a fix to your coding agent with its prompt."
      action={
        <div className="flex gap-2">
          {canManage && <Button variant="ghost" onClick={() => setAutonomy(true)}>Agent autonomy</Button>}
          <Button variant="ghost" onClick={() => go("audits")}>Site audits</Button>
        </div>
      }
    />
  );
  const autonomySheet = (
    <Sheet open={autonomy} onOpenChange={setAutonomy}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader className="p-0">
          <SheetTitle>Agent autonomy</SheetTitle>
          <SheetDescription>How far agents in a mission may go with this list on their own. Agents take a fix before they change the site; this decides whether that waits for a person.</SheetDescription>
        </SheetHeader>
        {autonomy && <FixAutonomy ws={ws} sites={sites} />}
      </SheetContent>
    </Sheet>
  );
  if (error && !fixes) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!fixes) return <div className="space-y-6">{header}<Loading /></div>;

  const filtered = view !== "todo" || severity || site;
  const counts = { errors: shown.filter((f) => f.rule.severity === "error").length, mine: shown.filter((f) => f.status === "in_progress").length, pages: shown.reduce((n, f) => n + (f.unit === "pages" ? f.count : 0), 0) };

  const toolbar = (
    <>
      <select className={field} aria-label="Show" value={view} onChange={(e) => setView(e.target.value as View)}>
        {(Object.keys(views) as View[]).map((v) => <option key={v} value={v}>{views[v]}</option>)}
      </select>
      <select className={field} aria-label="Severity" value={severity} onChange={(e) => setSeverity(e.target.value)}>
        <option value="">Every severity</option>
        {Object.entries(severityLabel).map(([k, v]) => <option key={k} value={k}>{v}s</option>)}
      </select>
      {sites.length > 1 && (
        <select className={field} aria-label="Site" value={site} onChange={(e) => setSite(e.target.value)}>
          <option value="">Every site</option>
          {sites.map((s) => <option key={s.id} value={s.id}>{s.host}</option>)}
        </select>
      )}
    </>
  );

  return (
    <div className="space-y-6">
      {header}
      {autonomySheet}
      {error && <ErrorText>{error}</ErrorText>}
      {fixes.length === 0 && !filtered && (
        <Empty title={sites.length ? "Nothing to fix" : "Nothing on the list yet"} action={!sites.length && <Button onClick={() => go("audits")}>Audit a site</Button>}>
          {sites.length ? "The latest audits found nothing worth fixing. New issues show up here after each crawl." : "Site audits are the first thing that adds fixes here. Add your website and the first crawl fills this list."}
        </Empty>
      )}
      {(fixes.length > 0 || filtered) && (
        <>
          <Stats cols={4}>
            <Stat label="Fixes" value={shown.length.toLocaleString()} />
            <Stat label="Errors" value={counts.errors.toLocaleString()} tone={counts.errors > 0 ? "red" : undefined} />
            <Stat label="In progress" value={counts.mine.toLocaleString()} />
            <Stat label="Pages to fix" value={counts.pages.toLocaleString()} />
          </Stats>
          <DataTable
            data={shown}
            rowId={(f) => f.id}
            onRowClick={(f) => setPicked(f.id)}
            searchPlaceholder="Search fixes"
            toolbar={toolbar}
            empty="Nothing matches these filters."
            mobile={(f) => (
              <div className="space-y-1.5">
                <div className="flex items-start justify-between gap-2">
                  <span className="font-medium">{f.rule.title}</span>
                  <Badge tone={severityTone[f.rule.severity]}>{severityLabel[f.rule.severity]}</Badge>
                </div>
                <div className="text-xs text-muted-foreground">{subjectName(f, hosts)} · <span className="font-mono">{f.group}</span> · {plural(f.count, f.unit.replace(/s$/, ""))} · {statusLabel[f.status]}</div>
              </div>
            )}
            columns={[
              { id: "severity", header: "Severity", accessorFn: (f) => severityRank[f.rule.severity], cell: ({ row }) => <Badge tone={severityTone[row.original.rule.severity]}>{severityLabel[row.original.rule.severity]}</Badge> },
              {
                id: "fix",
                header: "Fix",
                accessorFn: (f) => `${f.rule.title} ${f.group} ${hosts[f.subject.id] ?? ""}`,
                cell: ({ row: { original: f } }) => (
                  <div className="space-y-0.5">
                    <div className="font-medium">{f.rule.title}</div>
                    <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                      <span>{subjectName(f, hosts)}</span>
                      <span className="font-mono">{f.group}</span>
                      <BrandBadge id={f.brand_id} />
                    </div>
                  </div>
                ),
              },
              { id: "count", header: "How many", accessorFn: (f) => f.count, meta: { align: "right" }, cell: ({ row }) => <span className="whitespace-nowrap">{plural(row.original.count, row.original.unit.replace(/s$/, ""))}</span> },
              { id: "status", header: "Status", accessorFn: (f) => f.status, cell: ({ row: { original: f } }) => <Badge tone={statusTone[f.status]}>{statusLabel[f.status]}</Badge> },
              { id: "assignee", header: "Who", accessorFn: (f) => f.assignee?.name ?? "", cell: ({ row }) => row.original.assignee?.name ?? <span className="text-muted-foreground">Nobody</span> },
              { id: "updated", header: "Updated", accessorFn: (f) => f.updated_at, cell: ({ row }) => <span className="text-muted-foreground">{ago(row.original.updated_at)}</span> },
            ]}
          />
        </>
      )}
      {current && <FixSheet ws={ws} fix={current} host={hosts[current.subject.id]} canManage={canManage} onClose={() => setPicked("")} onUpdate={update} onVerify={verify} go={go} />}
    </div>
  );
}

type IssuePage = { id: string; url: string; detail: string };

function FixSheet({ ws, fix, host, canManage, onClose, onUpdate, onVerify, go }: { ws: string; fix: Fix; host?: string; canManage: boolean; onClose: () => void; onUpdate: (fix: Fix, body: { status?: Status; assignee_id?: string | null; note?: string }, done: string) => Promise<void>; onVerify: (fix: Fix) => Promise<void>; go: Props["go"] }) {
  const [note, setNote] = useState(fix.note);
  const [pages, setPages] = useState<IssuePage[] | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => setNote(fix.note), [fix.id, fix.note]);
  useEffect(() => {
    setPages(null);
    if (fix.source !== "seo_audit" || !fix.last_found_by || fix.count === 0) return setPages([]);
    api.GET("/v1/workspaces/{ws}/crawls/{crawl}/issues/{rule}", { params: { path: { ws, crawl: fix.last_found_by, rule: fix.rule.id }, query: { template: fix.group, limit: 20 } } }).then(({ data }) => setPages(data?.data ?? []));
  }, [ws, fix.id, fix.source, fix.last_found_by, fix.rule.id, fix.group, fix.count]);

  async function copyPrompt() {
    setBusy(true);
    const { data, error } = await api.GET("/v1/workspaces/{ws}/fixes/{fix}/prompt", { params: { path: { ws, fix: fix.id } } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    await navigator.clipboard?.writeText(data.prompt);
    track("ui.copied");
    toast(fix.source === "seo_links" ? "Prompt copied. Paste it into your own agent." : "Prompt copied. Paste it into Claude Code, Cursor or Codex in the site's repository.");
  }

  async function check() {
    setBusy(true);
    await onVerify(fix);
    setBusy(false);
  }

  async function act(body: { status?: Status; assignee_id?: string | null; note?: string }, done: string) {
    setBusy(true);
    await onUpdate(fix, body, done);
    setBusy(false);
  }

  const active = fix.status === "open" || fix.status === "in_progress";
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{fix.rule.title}</SheetTitle>
          <SheetDescription>
            {fix.source === "social" ? "A social watch" : host ?? "A site"}{fix.group && <> · <span className="font-mono">{fix.group}</span></>} · {plural(fix.count, fix.unit.replace(/s$/, ""))}
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-5 px-4 pb-6 text-[13px]">
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge tone={severityTone[fix.rule.severity]}>{severityLabel[fix.rule.severity]}</Badge>
            <Badge tone={statusTone[fix.status]}>{statusLabel[fix.status]}</Badge>
            {fix.assignee && <Badge>{fix.assignee.name}</Badge>}
            <BrandBadge id={fix.brand_id} />
            <span className="text-xs text-muted-foreground">Found {ago(fix.created_at)}{fix.fixed_at && ` · fixed ${ago(fix.fixed_at)}`}</span>
          </div>
          <div className="space-y-2">
            <p><span className="font-medium">Why it matters:</span> {fix.rule.why}</p>
            <p><span className="font-medium">How to fix it:</span> {fix.rule.fix}</p>
          </div>
          {fix.status === "done" && !fix.confirmed_by && (
            <p className="text-muted-foreground">
              {fix.source === "seo_links" ? "Marked done. The next weekly links read checks it, and it opens again if it is still there." : "Marked done. The next complete crawl checks it, and it opens again if the issue is still there."}
            </p>
          )}

          {canManage && (
            <div className="flex flex-wrap gap-2">
              {fix.status !== "in_progress" && active && <Button disabled={busy} onClick={() => act({ status: "in_progress", assignee_id: "me" }, "It's yours.")}>Take it</Button>}
              {active && <Button variant={fix.status === "in_progress" ? "primary" : "ghost"} disabled={busy} onClick={() => act({ status: "done" }, "Marked done. The next crawl confirms it.")}>Mark done</Button>}
              {active && <Button variant="ghost" disabled={busy} onClick={() => act({ status: "ignored" }, "Ignored. It stays off the to-do list.")}>Ignore</Button>}
              {!active && <Button variant="ghost" disabled={busy} onClick={() => act({ status: "open" }, "Opened again.")}>Open again</Button>}
              {fix.assignee && active && <Button variant="ghost" disabled={busy} onClick={() => act({ assignee_id: null }, "Nobody has it now.")}>Unassign</Button>}
            </div>
          )}

          {fix.source === "seo_audit" && fix.status !== "ignored" && (
            <div className="space-y-2 rounded-lg border p-3">
              <div className="font-medium">Hand it to a coding agent</div>
              <p className="text-muted-foreground">Copy a prompt with the issue, how to fix it and its pages. Once the change is live, check it: a fresh crawl confirms the fix or opens it again. An agent with an API token can report back itself.</p>
              <div className="flex flex-wrap gap-2">
                <Button variant="ghost" disabled={busy} onClick={copyPrompt}>Copy prompt</Button>
                {canManage && fix.count > 0 && <Button variant="ghost" disabled={busy} onClick={check}>It's live, check it</Button>}
              </div>
            </div>
          )}

          {fix.source === "seo_links" && fix.status !== "ignored" && (
            <div className="space-y-2 rounded-lg border p-3">
              <div className="font-medium">A suggestion, not a task Outflare runs</div>
              <p className="text-muted-foreground">
                Outflare never contacts a site, asks for a link or disavows anything for you. Copy a prompt with the links behind it for your own agent, or look at them yourself.
              </p>
              <div className="flex flex-wrap gap-2">
                <Button variant="ghost" disabled={busy} onClick={copyPrompt}>Copy prompt</Button>
                <Button variant="ghost" onClick={() => go(fix.rule.id === "link_gap_domain" ? "competitors" : "backlinks")}>
                  {fix.rule.id === "link_gap_domain" ? "Open Competitors" : "Open Backlinks"}
                </Button>
              </div>
            </div>
          )}

          {canManage && (
            <form
              className="space-y-2"
              onSubmit={(e) => {
                e.preventDefault();
                act({ note }, "Note saved.");
              }}
            >
              <label className="block space-y-1">
                <span className="font-medium">Note</span>
                <textarea className={`${field} min-h-20 w-full`} value={note} maxLength={2000} onChange={(e) => setNote(e.target.value)} placeholder="What you tried, or why you're leaving it" />
              </label>
              {note !== fix.note && <Button type="submit" variant="ghost" disabled={busy}>Save note</Button>}
            </form>
          )}
          {!canManage && fix.note && <p><span className="font-medium">Note:</span> {fix.note}</p>}

          {fix.source === "social" && <Button variant="ghost" onClick={() => go("reddit")}>Open Reddit</Button>}
          {fix.source === "seo_audit" && (
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <span className="font-medium">Pages</span>
                <button className="text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline" onClick={() => go("audits", fix.subject.id)}>Open the site's audit</button>
              </div>
              {!pages && <Loading />}
              {pages && pages.length === 0 && <p className="text-muted-foreground">{fix.count === 0 ? "The latest crawl found no pages with this issue." : "No pages to show."}</p>}
              {pages && pages.length > 0 && (
                <ul className="space-y-1">
                  {pages.map((p) => (
                    <li key={p.id} className="flex flex-wrap items-baseline gap-2">
                      <a className="font-mono text-[12px] break-all underline-offset-2 hover:underline" href={p.url} target="_blank" rel="noreferrer noopener">{p.url}</a>
                      {p.detail && <span className="text-[12px] text-muted-foreground">{p.detail}</span>}
                    </li>
                  ))}
                  {fix.count > pages.length && <li className="text-[12px] text-muted-foreground">And {plural(fix.count - pages.length, "more page")} on the site's audit.</li>}
                </ul>
              )}
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
