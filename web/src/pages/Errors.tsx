import { PickBrand } from "@/components/pick-brand";
import { pickedBrandId, useBrand } from "@/lib/brand";
// Errors: what breaks on the brand's website or app. The website script
// (with "Capture errors" on for the site) and the brand's own code send
// each error as the event Error Caught; this page groups them into issues,
// most frequent first, with tabs by status, filters, and actions on one or
// many at once (mark fixed, ignore, archive, delete, copy a prompt for an
// AI coding assistant). An issue opens to its own page: how often it
// happened, on which pages and browsers, and its latest errors with their
// stack and the person they happened to.

import { useCallback, useEffect, useMemo, useState } from "react";
import type { ErrorIssue, paths } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { api, errorMessage } from "../api";
import { ago, plural, when } from "../format";
import { Badge, Button, confirmAction, CopyTextButton, Empty, ErrorText, field, Loading, Menu, PageHeader, Stat, Stats, toast, toastUndo } from "../ui";
import { Checkbox } from "@/components/ui/checkbox";
import { track } from "@/lib/analytics";
import { browserLine, errorText, errorTitle, fixPrompt, properName } from "./error-prompt";
import { Snippet } from "./Tokens";
import type { Tab } from "../App";
import { Switch } from "@/components/ui/switch";
import { mainKeys, saveCapture, scriptHealth, siteName } from "./SiteKeys";

type Props = { ws: string; canManage: boolean; canSwitch: boolean; canAlerts: boolean; go: (tab: Tab, item?: string) => void; item?: string };
type List = paths["/v1/brands/{brand}/errors"]["get"]["responses"]["200"]["content"]["application/json"];
type Detail = paths["/v1/brands/{brand}/errors/{fingerprint}"]["get"]["responses"]["200"]["content"]["application/json"];
type Occurrence = Detail["occurrences"][number];
type Before = paths["/v1/brands/{brand}/errors/{fingerprint}/occurrences/{id}/before"]["get"]["responses"]["200"]["content"]["application/json"];
type SiteKey = paths["/v1/brands/{brand}/site-keys"]["get"]["responses"]["200"]["content"]["application/json"]["data"][number];
type SourceMap = paths["/v1/brands/{brand}/source-maps"]["get"]["responses"]["200"]["content"]["application/json"]["data"][number];
type Status = "open" | "resolved" | "ignored" | "archived";
type Bucket = Status | "filtered";
type Action = Status | "delete";
type Rule = paths["/v1/brands/{brand}/error-rules"]["get"]["responses"]["200"]["content"]["application/json"]["data"][number];
type RuleField = Rule["field"];

const periods = [
  { days: 1, label: "Last 24 hours" },
  { days: 7, label: "Last 7 days" },
  { days: 28, label: "Last 28 days" },
  { days: 90, label: "Last 90 days" },
];
const statusTone = (i: ErrorIssue) => (i.status === "resolved" ? "green" : i.status === "ignored" || i.status === "archived" ? "grey" : i.regressed ? "red" : "amber") as "green" | "grey" | "red" | "amber";
const statusLabel = (i: ErrorIssue) => (i.filtered ? "Filtered" : i.status === "resolved" ? "Fixed" : i.status === "ignored" ? "Ignored" : i.status === "archived" ? "Archived" : i.regressed ? "Back again" : i.new ? "New" : "Open");
// Why an error is filtered, in words.
const filterLabel: Record<string, string> = {
  extension: "A browser extension",
  link_scanner: "An email link scanner",
  injected: "Code a wallet or browser added",
  local_file: "A saved copy of the page",
  ip_host: "A copy on a bare IP address",
  rule: "One of your ignore rules",
};
const pct = (x: number) => (x > 0 && x < 0.001 ? "<0.1%" : `${(x * 100).toLocaleString(undefined, { maximumFractionDigits: x < 0.1 ? 1 : 0 })}%`);
const by = (who?: string | null) => (who ? ` by ${who}` : "");
// What a status means for what happens next, in a sentence.
function statusLine(i: ErrorIssue): string {
  const at = i.status_at ? ago(i.status_at) : "";
  if (i.filtered) return `Filtered: ${filterLabel[i.filtered]?.toLowerCase() ?? "a filter"} caused it, so it stays out of the open list and never alerts.`;
  if (i.status === "resolved") return `Marked fixed ${at}${by(i.status_by)}. If it happens again it opens as Back again.`;
  if (i.status === "ignored") return `Ignored ${at}${by(i.status_by)}. It stays out of the open list and never alerts, even if it happens again.`;
  if (i.status === "archived") return `Archived ${at}${by(i.status_by)}. It comes back to the open list only if it happens again.`;
  if (i.regressed) return `Marked fixed ${at}${by(i.status_by)}, and it has happened again since.`;
  return i.new ? "New in this period." : "Open.";
}

const tabs: { id: Bucket | "all"; label: string }[] = [
  { id: "open", label: "Open" },
  { id: "resolved", label: "Fixed" },
  { id: "ignored", label: "Ignored" },
  { id: "archived", label: "Archived" },
  { id: "filtered", label: "Filtered" },
  { id: "all", label: "All" },
];

// What an action did, to one issue or several.
const doneText = (a: Action, n: number) => {
  const what = n === 1 ? "Error" : `${n} errors`;
  return a === "resolved" ? `${what} marked fixed. ${n === 1 ? "It opens" : "They open"} again by ${n === 1 ? "itself" : "themselves"} if ${n === 1 ? "it happens" : "they happen"} again.`
    : a === "ignored" ? `${what} ignored. No alerts, even if ${n === 1 ? "it happens" : "they happen"} again.`
    : a === "archived" ? `${what} archived until ${n === 1 ? "it happens" : "they happen"} again.`
    : a === "open" ? `${what} open again.` : `${what} deleted.`;
};

/** Does one action to one or more issues. Delete asks first. Returns whether it ran. */
async function act(fingerprints: string[], action: Action, undo?: () => void): Promise<boolean> {
  const n = fingerprints.length;
  if (action === "delete") {
    const ok = await confirmAction(
      n === 1 ? "Delete this error?" : `Delete ${n} errors?`,
      <p>Every time {n === 1 ? "it" : "they"} happened is deleted too, so {n === 1 ? "it leaves" : "they leave"} trends and funnels. This can't be undone. If {n === 1 ? "it happens" : "they happen"} again, {n === 1 ? "it shows" : "they show"} as new. To keep the history, archive instead.</p>,
      n === 1 ? "Delete error" : `Delete ${n} errors`,
    );
    if (!ok) return false;
  }
  const { error } = n === 1 && action !== "delete"
    ? await api.PATCH("/v1/brands/{brand}/errors/{fingerprint}", { params: { path: { brand: pickedBrandId(), fingerprint: fingerprints[0] } }, body: { status: action } })
    : n === 1
      ? await api.DELETE("/v1/brands/{brand}/errors/{fingerprint}", { params: { path: { brand: pickedBrandId(), fingerprint: fingerprints[0] } } })
      : await api.POST("/v1/brands/{brand}/errors/bulk", { params: { path: { brand: pickedBrandId() } }, body: { fingerprints, action } });
  if (error) {
    toast(errorMessage(error), "error");
    return false;
  }
  if (undo && action !== "delete") toastUndo(doneText(action, n), undo);
  else toast(doneText(action, n));
  return true;
}

// Fetches issues' details (at most 10) and copies one prompt for them all.
async function copyFixPrompt(fingerprints: string[], days: number): Promise<void> {
  const picked = fingerprints.slice(0, 10);
  const got = await Promise.all(picked.map((fingerprint) => api.GET("/v1/brands/{brand}/errors/{fingerprint}", { params: { path: { brand: pickedBrandId(), fingerprint }, query: { days } } })));
  const details = got.flatMap((r) => (r.data ? [r.data] : []));
  if (details.length === 0) return toast("Couldn't read those errors. Try again.", "error");
  await navigator.clipboard?.writeText(fixPrompt(details));
  track("ui.copied");
  toast(`Fix prompt copied${details.length > 1 ? ` for ${details.length} errors` : ""}. Paste it into your AI coding assistant${fingerprints.length > 10 ? " (the first 10 selected)" : ""}.`);
}

export function Errors(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Errors" brands={active.length} />;
  if (props.item === "source-maps") return <SourceMaps key={brand.id} brand={brand.id} canManage={props.canManage} go={props.go} />;
  if (props.item === "setup") return <Setup key={brand.id} brand={brand.id} go={props.go} />;
  if (props.item === "ignore-rules") return <IgnoreRules key={brand.id} brand={brand.id} canManage={props.canManage} go={props.go} />;
  if (props.item?.startsWith("ignore-rules/")) return <IgnoreRuleEdit key={`${brand.id}${props.item}`} brand={brand.id} id={props.item.slice("ignore-rules/".length)} go={props.go} />;
  if (props.item) return <Issue key={`${brand.id}${props.item}`} fingerprint={props.item} canManage={props.canManage} go={props.go} />;
  return <IssueList key={brand.id} {...props} />;
}

type Kind = "all" | "new" | "back" | "uncaught";
type Where = "all" | "browser" | "server";

function IssueList({ go, canManage, canSwitch, canAlerts }: Props) {
  // The brand's script, whose "Errors" switch this page turns on and off.
  const [site, setSite] = useState<SiteKey | null | undefined>(undefined);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/site-keys", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => {
      if (!data) return setSite(null);
      const main = mainKeys(data.data, true);
      setSite(data.data.find((k) => main.has(k.id)) ?? null);
    });
  }, []);
  const [days, setDays] = useState(7);
  const [status, setFilter] = useState<Bucket | "all">("open");
  const [host, setHost] = useState("");
  const [data, setData] = useState<List | null>(null);
  const [error, setError] = useState("");
  const [q, setQ] = useState("");
  const [kind, setKind] = useState<Kind>("all");
  const [where, setWhere] = useState<Where>("all");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => {
    api.GET("/v1/brands/{brand}/errors", { params: { path: { brand: pickedBrandId() }, query: { days, status, ...(host ? { host } : {}) } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setData(data);
      // Keep only what is still listed selected.
      setPicked((p) => new Set([...p].filter((f) => data.data.some((i) => i.fingerprint === f))));
    });
  }, [days, status, host]);
  useEffect(() => {
    setData(null);
    setPicked(new Set());
    load();
  }, [load]);

  const shown = useMemo(() => {
    const words = q.trim().toLowerCase();
    return (data?.data ?? []).filter((i) =>
      (!words || `${i.type} ${i.message} ${i.message_full} ${i.last_path} ${i.host}`.toLowerCase().includes(words)) &&
      (kind === "all" || (kind === "new" ? i.new : kind === "back" ? i.regressed : !i.handled)) &&
      (where === "all" || (where === "browser" ? i.source === "browser" : i.source !== "browser")));
  }, [data, q, kind, where]);
  const filtered = !!q.trim() || kind !== "all" || where !== "all" || !!host;
  const selected = shown.filter((i) => picked.has(i.fingerprint));
  const allPicked = shown.length > 0 && selected.length === shown.length;
  const toggle = (fp: string) => setPicked((p) => { const n = new Set(p); if (n.has(fp)) n.delete(fp); else n.add(fp); return n; });

  // One action on some issues, with Undo back to each one's earlier status.
  async function run(list: ErrorIssue[], action: Action) {
    setBusy(true);
    const before = list.map((i) => [i.fingerprint, i.status] as const);
    const undo = async () => {
      for (const s of new Set(before.map(([, st]) => st))) {
        const fps = before.filter(([, st]) => st === s).map(([fp]) => fp);
        if (fps.length === 1) await api.PATCH("/v1/brands/{brand}/errors/{fingerprint}", { params: { path: { brand: pickedBrandId(), fingerprint: fps[0] } }, body: { status: s as Status } });
        else await api.POST("/v1/brands/{brand}/errors/bulk", { params: { path: { brand: pickedBrandId() } }, body: { fingerprints: fps, action: s as Status } });
      }
      load();
    };
    const ok = await act(list.map((i) => i.fingerprint), action, undo);
    setBusy(false);
    if (ok) {
      setPicked(new Set());
      load();
    }
  }

  // Puts the selected issues together, into the one that happened most.
  async function merge(list: ErrorIssue[]) {
    const keep = [...list].sort((a, b) => b.count - a.count)[0];
    const ok = await confirmAction(
      `Merge ${list.length} errors into one?`,
      <p>They become one error, kept as <span className="font-medium">{errorTitle(keep.type, keep.message)}</span>, with every time each happened counted together, and its status. New ones join it too. You can split them apart again from the error's page.</p>,
      "Merge", false,
    );
    if (!ok) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/errors/merge", { params: { path: { brand: pickedBrandId() } }, body: { into: keep.fingerprint, fingerprints: list.filter((i) => i !== keep).map((i) => i.fingerprint) } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    toast(`${plural(data.merged.length + 1, "error")} merged into one.`);
    setPicked(new Set());
    load();
  }

  const header = (
    <PageHeader
      title="Errors"
      description="What breaks on your website or app, grouped so the same bug is one row. Open a row to see where it happened and to whom, and copy a prompt that has your AI coding assistant fix it."
      action={
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="ghost" onClick={() => go("errors", "setup")}>Set up</Button>
          <Button variant="ghost" onClick={() => go("errors", "ignore-rules")}>Ignore rules</Button>
          <Button variant="ghost" onClick={() => go("errors", "source-maps")}>Source maps</Button>
          <select aria-label="Period" className={field} value={days} onChange={(e) => setDays(Number(e.target.value))}>
            {periods.map((p) => <option key={p.days} value={p.days}>{p.label}</option>)}
          </select>
        </div>
      }
    />
  );
  const lastError = data?.data.reduce<string | null>((m, i) => (!m || i.last_seen > m ? i.last_seen : m), null) ?? null;
  const capture = site === undefined ? null : (
    <div className="space-y-2">
      <ErrorCapture site={site} canSwitch={canSwitch} lastError={lastError} go={go} onChange={setSite} />
      <ErrorEmails canSwitch={canAlerts} go={go} />
    </div>
  );
  if (error) return <div className="space-y-6">{header}{capture}<ErrorText>{error}</ErrorText></div>;
  if (!data) return <div className="space-y-6">{header}<Loading /></div>;
  const counts = data.counts as Record<Bucket, number>;
  const nothing = counts.open + counts.resolved + counts.ignored + (counts.archived ?? 0) + (counts.filtered ?? 0) === 0 && !host;
  const period = periods.find((p) => p.days === days)?.label.toLowerCase() ?? "";
  // Capture started inside the period: nothing can be told apart as new.
  const fresh = data.capture_since && new Date(data.capture_since) > new Date(data.from);
  if (nothing) {
    return (
      <div className="space-y-6">
        {header}
        {capture}
        <Empty title="No errors in this period">
          {site?.capture_errors
            ? "Error capture is on, so new errors show here within a minute of happening. Errors your own code catches can be sent with outflare.captureException(error)."
            : site
              ? "Switch on error capture above and errors from your site start showing here. Nothing changes on your site."
              : "Errors come from the website script. Add it to your site first, then switch on error capture here."}
        </Empty>
      </div>
    );
  }

  const rowActions = (i: ErrorIssue) => [
    { label: "Copy fix prompt for AI", onClick: () => void copyFixPrompt([i.fingerprint], days) },
    { label: "Copy error", onClick: () => void copyError(i.fingerprint, days) },
    canManage && !i.filtered && { label: "Ignore errors like this", onClick: () => ignoreLike(go, "message", i.message) },
    canManage && !i.filtered && !!i.host && { label: `Ignore errors on ${i.host}`, onClick: () => ignoreLike(go, "host", i.host) },
    canManage && i.status !== "resolved" && { label: "Mark fixed", onClick: () => void run([i], "resolved") },
    canManage && i.status !== "ignored" && { label: "Ignore", onClick: () => void run([i], "ignored") },
    canManage && i.status !== "archived" && { label: "Archive", onClick: () => void run([i], "archived") },
    canManage && i.status !== "open" && { label: "Reopen", onClick: () => void run([i], "open") },
    canManage && { label: "Delete", onClick: () => void run([i], "delete"), danger: true },
  ];

  return (
    <div className="space-y-6">
      {header}
      {capture}
      {fresh && (
        <p className="rounded-lg border px-4 py-3 text-[13px] text-muted-foreground">
          Error capture started {ago(data.capture_since!)}, so nothing is marked New yet: every error is the first of its kind. Once capture has run for longer than the {period}, New means an error that hadn't happened before.
        </p>
      )}
      <SetupCard setup={data.setup} go={go} />
      <Stats cols={4}>
        <Stat label="Open errors" value={counts.open.toLocaleString()} tone={counts.open ? "amber" : "green"} sub={data.data.some((i) => i.regressed) ? `${data.data.filter((i) => i.regressed).length} back again` : undefined} />
        <Stat label="Visitors who hit an error" value={data.visitors ? pct(Math.min(1, data.affected_visitors / data.visitors)) : data.affected_visitors.toLocaleString()}
          sub={data.visitors ? `${data.affected_visitors.toLocaleString()} of ${data.visitors.toLocaleString()} visitors${host ? ` to ${host}` : ""}` : "No visits recorded to compare with"} />
        <Stat label={status === "all" ? "Times they happened" : `Times ${tabs.find((t) => t.id === status)?.label.toLowerCase()} errors happened`} value={data.errors.toLocaleString()} sub={periods.find((p) => p.days === days)?.label} />
        <Stat label="Fixed" value={counts.resolved.toLocaleString()} sub={[counts.ignored && `${counts.ignored.toLocaleString()} ignored`, counts.archived && `${counts.archived.toLocaleString()} archived`].filter(Boolean).join(" · ") || undefined} />
      </Stats>

      <div className="flex gap-1 overflow-x-auto border-b text-[13px]" role="tablist" aria-label="Status">
        {tabs.map((t) => (
          <button key={t.id} type="button" role="tab" aria-selected={status === t.id} onClick={() => setFilter(t.id)}
            className={`-mb-px shrink-0 border-b-2 px-3 py-2 ${status === t.id ? "border-foreground font-medium" : "border-transparent text-muted-foreground hover:text-foreground"}`}>
            {t.label}{t.id !== "all" && <span className="ml-1.5 font-mono text-[11px] text-muted-foreground">{(counts[t.id] ?? 0).toLocaleString()}</span>}
          </button>
        ))}
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <input type="search" aria-label="Search errors" placeholder="Search errors or pages" className={`${field} min-w-0 flex-1 sm:max-w-xs`} value={q} onChange={(e) => setQ(e.target.value)} />
        <select aria-label="Which errors" className={field} value={kind} onChange={(e) => setKind(e.target.value as Kind)}>
          <option value="all">Every error</option>
          <option value="new">New in this period</option>
          <option value="back">Back again</option>
          <option value="uncaught">Uncaught only</option>
        </select>
        <select aria-label="Where" className={field} value={where} onChange={(e) => setWhere(e.target.value as Where)}>
          <option value="all">Website and server</option>
          <option value="browser">Website only</option>
          <option value="server">Server only</option>
        </select>
        {data.hosts.length > 1 || host ? (
          <select aria-label="Site" className={field} value={host} onChange={(e) => setHost(e.target.value)}>
            <option value="">Every site</option>
            {data.hosts.map((h) => <option key={h.host} value={h.host}>{h.host} ({h.count.toLocaleString()})</option>)}
            {host && !data.hosts.some((h) => h.host === host) && <option value={host}>{host}</option>}
          </select>
        ) : null}
        {filtered && <Button variant="ghost" onClick={() => { setQ(""); setKind("all"); setWhere("all"); setHost(""); }}>Clear filters</Button>}
        <span className="ml-auto text-[12px] text-muted-foreground">{filtered ? `${shown.length.toLocaleString()} of ${data.data.length.toLocaleString()}` : plural(data.data.length, "error")}</span>
      </div>

      {selected.length > 0 && (
        <div role="toolbar" aria-label="Selected errors" className="sticky top-2 z-10 flex flex-wrap items-center gap-2 rounded-lg border bg-card px-3 py-2 text-[13px] shadow-sm">
          <span className="font-medium">{selected.length.toLocaleString()} selected</span>
          <Button variant="ghost" disabled={busy} onClick={() => void copyFixPrompt(selected.map((i) => i.fingerprint), days)}>Copy fix prompt</Button>
          {canManage && selected.length > 1 && <Button variant="ghost" disabled={busy} onClick={() => void merge(selected)}>Merge into one</Button>}
          {canManage && (
            <>
              {status !== "resolved" && <Button variant="ghost" disabled={busy} onClick={() => void run(selected, "resolved")}>Mark fixed</Button>}
              {status !== "ignored" && <Button variant="ghost" disabled={busy} onClick={() => void run(selected, "ignored")}>Ignore</Button>}
              {status !== "archived" && <Button variant="ghost" disabled={busy} onClick={() => void run(selected, "archived")}>Archive</Button>}
              {status !== "open" && <Button variant="ghost" disabled={busy} onClick={() => void run(selected, "open")}>Reopen</Button>}
              <Button variant="ghost" disabled={busy} className="text-red-600 dark:text-red-400" onClick={() => void run(selected, "delete")}>Delete</Button>
            </>
          )}
          <Button variant="ghost" className="ml-auto" onClick={() => setPicked(new Set())}>Clear selection</Button>
        </div>
      )}

      <DataTable
        data={shown}
        rowId={(i: ErrorIssue) => i.fingerprint}
        onRowClick={(i: ErrorIssue) => go("errors", i.fingerprint)}
        columnsMenu={false}
        empty={filtered ? "No errors match these filters." : status === "open" ? "No open errors in this period. Nice." : status === "filtered" ? "Nothing filtered in this period." : "Nothing with this status in this period."}
        initialSort={[{ id: "count", desc: true }]}
        mobile={(i: ErrorIssue) => (
          <span className="block min-w-0">
            <span className="block truncate font-medium">{errorTitle(i.type, i.message)}</span>
            <span className="block truncate font-mono text-[12px] text-muted-foreground">{i.last_path || "—"}</span>
            <span className="mt-1 flex flex-wrap items-center gap-2 text-[12px] text-muted-foreground"><Badge tone={statusTone(i)}>{statusLabel(i)}</Badge>{plural(i.count, "time")} · {ago(i.last_seen)}</span>
          </span>
        )}
        columns={[
          { id: "pick", enableSorting: false, meta: { csv: false }, header: () => (
            <Checkbox aria-label="Select every error shown" checked={allPicked ? true : selected.length > 0 ? "indeterminate" : false}
              onCheckedChange={() => setPicked(allPicked ? new Set() : new Set(shown.map((i) => i.fingerprint)))} />
          ), cell: ({ row: { original: i } }: { row: { original: ErrorIssue } }) => (
            <span onClick={(e) => e.stopPropagation()} className="flex"><Checkbox checked={picked.has(i.fingerprint)} onCheckedChange={() => toggle(i.fingerprint)} aria-label={`Select ${errorTitle(i.type, i.message)}`} /></span>
          ) },
          { id: "error", header: "Error", accessorFn: (i: ErrorIssue) => `${i.type} ${i.message} ${i.last_path}`, cell: ({ row: { original: i } }: { row: { original: ErrorIssue } }) => (
            <span className={`block ${data.hosts.length > 1 ? "max-w-[20rem]" : "max-w-[26rem]"}`}>
              <span className="block truncate font-medium" title={i.message}>{errorTitle(i.type, i.message)}</span>
              <span className="flex items-center gap-2 text-[12px] text-muted-foreground">
                <span className="truncate font-mono">{i.host && data.hosts.length <= 1 ? `${i.host}${i.last_path}` : i.last_path || "—"}</span>
                {i.pages > 1 && <span className="shrink-0">+{(i.pages - 1).toLocaleString()} more {i.pages === 2 ? "page" : "pages"}</span>}
                <span className="shrink-0">· {i.source === "browser" ? "Website" : "Server"}{i.handled ? ", caught" : ""}</span>
                {i.filtered && <span className="shrink-0">· {filterLabel[i.filtered] ?? "Filtered"}</span>}
              </span>
            </span>
          ) },
          ...(data.hosts.length > 1 ? [{ id: "site", header: "Site", accessorFn: (i: ErrorIssue) => i.host, cell: ({ row: { original: i } }: { row: { original: ErrorIssue } }) => (
            i.host ? <span className="font-mono text-[12px]">{i.host}{i.hosts > 1 && <span className="text-muted-foreground" title={`It happened on ${i.hosts} sites in this period`}> +{i.hosts - 1}</span>}</span> : <span className="text-muted-foreground">Server</span>
          ) }] : []),
          { id: "status", header: "Status", accessorFn: (i: ErrorIssue) => statusLabel(i), cell: ({ row: { original: i } }: { row: { original: ErrorIssue } }) => <Badge tone={statusTone(i)} title={statusLine(i)}>{statusLabel(i)}</Badge> },
          { id: "count", header: "Times", accessorFn: (i: ErrorIssue) => i.count, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: i } }: { row: { original: ErrorIssue } }) => (
            <span className="flex flex-col items-end gap-1">{i.count.toLocaleString()}<Spark values={i.trend} /></span>
          ) },
          { id: "visitors", header: "Visitors", accessorFn: (i: ErrorIssue) => i.visitors, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: i } }: { row: { original: ErrorIssue } }) => (
            <span title={i.visitor_share == null ? "The website script saw no visits to compare with" : `It happened to ${i.visitors.toLocaleString()} visitors: ${pct(i.visitor_share)} of the site's visitors in this period${i.worst_page ? `, and ${pct(i.worst_page.share)} of the ${i.worst_page.page_visitors.toLocaleString()} visitors to ${i.worst_page.path}` : ""}`}>
              {i.visitors.toLocaleString()}{i.visitor_share != null && <span className="text-muted-foreground"> · {pct(i.visitor_share)}</span>}
              <span className="block max-w-[11rem] truncate text-[11px] text-muted-foreground">{i.worst_page ? `${pct(i.worst_page.share)} of ${i.worst_page.path}` : "\u00a0"}</span>
            </span>
          ) },
          { id: "last", header: "Last seen", accessorFn: (i: ErrorIssue) => i.last_seen, cell: ({ row: { original: i } }: { row: { original: ErrorIssue } }) => (
            <span className="whitespace-nowrap" title={`Last seen ${when(i.last_seen)}. First seen ${when(i.first_seen)}${i.first_release ? `, in release ${i.first_release}` : ""}.`}>
              {ago(i.last_seen)}
              <span className="block text-[11px] text-muted-foreground">first {ago(i.first_seen)}</span>
            </span>
          ) },
          { id: "act", header: "", enableSorting: false, meta: { csv: false, pin: "right" as const }, cell: ({ row: { original: i } }: { row: { original: ErrorIssue } }) => (
            <span onClick={(e) => e.stopPropagation()} className="flex justify-end"><Menu label="Actions" aria={`Actions for ${errorTitle(i.type, i.message)}`} items={rowActions(i)} /></span>
          ) },
        ]}
      />
    </div>
  );
}

// Copies one issue's latest error as text.
async function copyError(fingerprint: string, days: number) {
  const { data } = await api.GET("/v1/brands/{brand}/errors/{fingerprint}", { params: { path: { brand: pickedBrandId(), fingerprint }, query: { days } } });
  if (!data) return toast("Couldn't read that error. Try again.", "error");
  await navigator.clipboard?.writeText(errorText(data));
  track("ui.copied");
  toast("Error copied.");
}

function Issue({ fingerprint, canManage, go }: { fingerprint: string; canManage: boolean; go: (tab: Tab, item?: string) => void }) {
  const [days, setDays] = useState(7);
  const [data, setData] = useState<Detail | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState(0);
  const [showPrompt, setShowPrompt] = useState(false);
  // Older errors, page by page.
  const [more, setMore] = useState<Occurrence[]>([]);
  const [next, setNext] = useState("");
  const [loadingMore, setLoadingMore] = useState(false);
  const load = useCallback(() => {
    api.GET("/v1/brands/{brand}/errors/{fingerprint}", { params: { path: { brand: pickedBrandId(), fingerprint }, query: { days } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setData(data);
      setMore([]);
      setNext(data.next_cursor ?? "");
      // A merged error's link opens the error it is part of.
      if (data.issue.fingerprint !== fingerprint) go("errors", data.issue.fingerprint);
    });
  }, [fingerprint, days]); // eslint-disable-line react-hooks/exhaustive-deps
  const loadMore = async () => {
    setLoadingMore(true);
    const { data, error } = await api.GET("/v1/brands/{brand}/errors/{fingerprint}/occurrences", { params: { path: { brand: pickedBrandId(), fingerprint }, query: { cursor: next, limit: 20 } } });
    setLoadingMore(false);
    if (error || !data) return toast(errorMessage(error), "error");
    setMore((m) => [...m, ...data.data]);
    setNext(data.next_cursor ?? "");
  };
  const split = async (merged: string) => {
    const { error } = await api.DELETE("/v1/brands/{brand}/errors/{fingerprint}/merged/{merged}", { params: { path: { brand: pickedBrandId(), fingerprint, merged } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Split apart. It is its own error again.");
    load();
  };
  useEffect(load, [load]);

  const back = { label: "Errors", onClick: () => go("errors") };
  if (error) return <div><PageHeader title="Error" back={back} /><ErrorText>{error}</ErrorText></div>;
  if (!data) return <div><PageHeader title="Error" back={back} /><Loading /></div>;
  const i = data.issue;
  const most = Math.max(1, ...data.days.map((d) => d.count));
  const prompt = fixPrompt([data]);
  const was = i.status;
  const doIt = async (a: Action) => {
    const undo = async () => {
      await api.PATCH("/v1/brands/{brand}/errors/{fingerprint}", { params: { path: { brand: pickedBrandId(), fingerprint } }, body: { status: was as Status } });
      load();
    };
    if (!(await act([fingerprint], a, undo))) return;
    if (a === "delete") go("errors");
    else load();
  };
  const browsers = [...new Set(data.occurrences.map(browserLine).filter(Boolean))];
  const all = [...data.occurrences, ...more];
  const worst = i.worst_page;
  return (
    <div className="space-y-6">
      <PageHeader
        back={back}
        title={<><span className="min-w-0 break-words">{errorTitle(i.type, i.message)}</span><Badge tone={statusTone(i)}>{statusLabel(i)}</Badge></>}
        description={`${statusLine(i)} First seen ${ago(i.first_seen)}${i.first_release ? ` in release ${i.first_release}` : ""}, last seen ${ago(i.last_seen)}${i.last_path ? ` on ${i.host ? i.host + i.last_path : i.last_path}` : ""}.`}
        action={
          <div className="flex flex-wrap items-center gap-2">
            <select aria-label="Period" className={field} value={days} onChange={(e) => setDays(Number(e.target.value))}>
              {periods.map((p) => <option key={p.days} value={p.days}>{p.label}</option>)}
            </select>
            <CopyTextButton text={errorText(data)} label="Copy error" />
            <CopyTextButton text={prompt} label="Copy fix prompt for AI" onCopied={() => toast("Fix prompt copied. Paste it into your AI coding assistant.")} />
            {canManage && i.status !== "resolved" && <Button onClick={() => doIt("resolved")}>Mark fixed</Button>}
            {canManage && i.status !== "open" && <Button variant={i.status === "resolved" ? "primary" : "ghost"} onClick={() => doIt("open")}>Reopen</Button>}
            {canManage && (
              <Menu aria="More actions" items={[
                i.status !== "ignored" && { label: "Ignore", onClick: () => void doIt("ignored") },
                !i.filtered && { label: "Ignore errors like this", onClick: () => ignoreLike(go, "message", i.message) },
                i.status !== "archived" && { label: "Archive", onClick: () => void doIt("archived") },
                { label: "Delete", onClick: () => void doIt("delete"), danger: true },
              ]} />
            )}
          </div>
        }
      />
      {i.message_full && (
        <section aria-label="Full message" className="rounded-lg border p-4 text-[13px]">
          <h2 className="font-medium">Full message</h2>
          <p className="text-muted-foreground">Your site's production build shortens this error to a code. In full, it says:</p>
          <p className="mt-2 break-words">{i.message_full}</p>
        </section>
      )}
      <Stats cols={4}>
        <Stat label="Times it happened" value={i.count.toLocaleString()} sub={periods.find((p) => p.days === days)?.label} />
        <Stat label="Visitors it happened to" value={i.visitors.toLocaleString()} sub={i.visitor_share != null ? `${pct(i.visitor_share)} of your site's visitors` : undefined} />
        <Stat label="Worst page" value={worst ? pct(worst.share) : "—"} sub={worst ? `of the ${worst.page_visitors.toLocaleString()} visitors to ${worst.path}` : data.pages[0] ? `Most on ${data.pages[0].path || "/"}` : "No page views to compare with"} />
        <Stat label="Where" value={i.source === "browser" ? "Website" : "Server"} sub={browsers.length ? browsers.slice(0, 2).join(", ") + (browsers.length > 2 ? ` +${browsers.length - 2}` : "") : (i.handled ? "Caught by your code" : "Uncaught")} />
      </Stats>

      <section aria-label="Fix it with AI" className="rounded-lg border p-4 text-[13px]">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 className="font-medium">Fix it with your AI coding assistant</h2>
            <p className="text-muted-foreground">One prompt with the error, its stack{data.occurrences[0]?.stack_original ? " in your own files" : ""}, the pages and browsers it happened on, and what to do. Paste it into Claude Code, Cursor or any assistant working in your code.</p>
          </div>
          <div className="flex gap-2">
            <Button variant="ghost" onClick={() => setShowPrompt(!showPrompt)} aria-expanded={showPrompt}>{showPrompt ? "Hide prompt" : "Show prompt"}</Button>
            <CopyTextButton text={prompt} label="Copy prompt" variant="primary" />
          </div>
        </div>
        {showPrompt && <pre className="mt-3 max-h-96 overflow-auto whitespace-pre-wrap rounded-md bg-muted p-3 font-mono text-[12px] leading-relaxed">{prompt}</pre>}
      </section>

      <section aria-label="Errors per day" className="rounded-lg border p-4">
        <h2 className="mb-3 text-[13px] font-medium">Per day</h2>
        <div className="flex h-28 items-end gap-1">
          {data.days.map((d) => (
            <div key={d.day} className="flex h-full min-w-0 flex-1 items-end" title={`${d.day}: ${plural(d.count, "error")}`}>
              <div className={`w-full rounded-sm ${d.count ? "bg-red-500/70" : "bg-muted"}`} style={{ height: d.count ? `${Math.max(4, (d.count / most) * 100)}%` : "2px" }} />
            </div>
          ))}
        </div>
        <div className="mt-1 flex justify-between font-mono text-[11px] text-muted-foreground"><span>{data.days[0]?.day}</span><span>{data.days[data.days.length - 1]?.day}</span></div>
      </section>

      <section aria-label="Every time it happened" className="space-y-2">
        <h2 className="text-[13px] font-medium">Every time it happened <span className="font-normal text-muted-foreground">(newest first, {all.length.toLocaleString()} shown)</span></h2>
        {all.map((o, n) => (
          <div key={o.id} className="rounded-lg border">
            <button className="flex w-full flex-wrap items-center gap-3 px-4 py-3 text-left text-[13px]" aria-expanded={open === n} onClick={() => setOpen(open === n ? -1 : n)}>
              <span className="font-medium" title={when(o.at)}>{ago(o.at)}</span>
              <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-muted-foreground">{o.host ? o.host + o.path : o.path || o.url || "—"}</span>
              {browserLine(o) && <span className="text-[12px] text-muted-foreground">{browserLine(o)}</span>}
              {o.release && <Badge title="The release it was sent with">{o.release}</Badge>}
              <Badge tone={o.handled ? "grey" : "red"} title={o.handled ? "Your code caught this and sent it with captureException." : "Nothing caught this error."}>{o.handled ? "Caught" : "Uncaught"}</Badge>
              <Badge>{o.source === "browser" ? "Website" : "Server"}</Badge>
            </button>
            {open === n && <OccurrenceDetail o={o} data={data} go={go} fingerprint={fingerprint} />}
          </div>
        ))}
        {next && <Button variant="ghost" disabled={loadingMore} onClick={() => void loadMore()}>{loadingMore ? "Loading…" : "Show 20 older"}</Button>}
      </section>

      {data.pages.length > 0 && (
        <section aria-label="Pages it happened on">
          <h2 className="mb-2 text-[13px] font-medium">Pages it happened on <span className="font-normal text-muted-foreground">(with how much of each page's traffic it reaches)</span></h2>
          <DataTable
            data={data.pages}
            rowId={(p: Detail["pages"][number]) => p.path}
            columnsMenu={false}
            columns={[
              { id: "path", header: "Page", accessorFn: (p: Detail["pages"][number]) => p.path, cell: ({ row: { original: p } }: { row: { original: Detail["pages"][number] } }) => <span className="font-mono text-[12px]">{p.path || "—"}</span> },
              { id: "count", header: "Times", accessorFn: (p: Detail["pages"][number]) => p.count, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: p } }: { row: { original: Detail["pages"][number] } }) => p.count.toLocaleString() },
              { id: "page_views", header: "Views of the page", accessorFn: (p: Detail["pages"][number]) => p.page_views, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: p } }: { row: { original: Detail["pages"][number] } }) => p.page_views ? p.page_views.toLocaleString() : <span className="text-muted-foreground">—</span> },
              { id: "view_share", header: "Share of views", accessorFn: (p: Detail["pages"][number]) => p.view_share, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: p } }: { row: { original: Detail["pages"][number] } }) => p.page_views ? pct(p.view_share) : <span className="text-muted-foreground">—</span> },
              { id: "visitors", header: "Visitors it happened to", accessorFn: (p: Detail["pages"][number]) => p.visitors, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: p } }: { row: { original: Detail["pages"][number] } }) => p.visitors.toLocaleString() },
              { id: "page_visitors", header: "Visitors to the page", accessorFn: (p: Detail["pages"][number]) => p.page_visitors, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: p } }: { row: { original: Detail["pages"][number] } }) => p.page_visitors ? p.page_visitors.toLocaleString() : <span className="text-muted-foreground">—</span> },
              { id: "share", header: "Share of visitors", accessorFn: (p: Detail["pages"][number]) => p.share, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: p } }: { row: { original: Detail["pages"][number] } }) => p.page_visitors ? pct(p.share) : <span className="text-muted-foreground">—</span> },
            ]}
          />
        </section>
      )}

      {data.merged_from.length > 0 && (
        <section aria-label="Merged into this error">
          <h2 className="mb-2 text-[13px] font-medium">Merged into this error</h2>
          <DataTable
            data={data.merged_from}
            rowId={(m: Detail["merged_from"][number]) => m.fingerprint}
            columnsMenu={false}
            columns={[
              { id: "error", header: "Error", accessorFn: (m: Detail["merged_from"][number]) => m.message, cell: ({ row: { original: m } }: { row: { original: Detail["merged_from"][number] } }) => <span className="block max-w-[34rem] truncate">{m.message ? errorTitle(m.type, m.message) : <span className="font-mono text-[12px]">{m.fingerprint}</span>}</span> },
              { id: "merged_at", header: "Merged", accessorFn: (m: Detail["merged_from"][number]) => m.merged_at, cell: ({ row: { original: m } }: { row: { original: Detail["merged_from"][number] } }) => <span title={when(m.merged_at)}>{ago(m.merged_at)}{by(m.merged_by)}</span> },
              ...(canManage ? [{ id: "act", header: "", enableSorting: false, cell: ({ row: { original: m } }: { row: { original: Detail["merged_from"][number] } }) => <Button variant="ghost" onClick={() => void split(m.fingerprint)}>Split apart</Button> }] : []),
            ]}
          />
        </section>
      )}

      {data.history.length > 0 && (
        <section aria-label="Status changes">
          <h2 className="mb-2 text-[13px] font-medium">Status changes</h2>
          <DataTable
            data={data.history}
            rowId={(h: Detail["history"][number]) => `${h.at}${h.status}`}
            columnsMenu={false}
            columns={[
              { id: "status", header: "Set to", accessorFn: (h: Detail["history"][number]) => h.status, cell: ({ row: { original: h } }: { row: { original: Detail["history"][number] } }) => ({ open: "Open", resolved: "Fixed", ignored: "Ignored", archived: "Archived" } as Record<string, string>)[h.status] },
              { id: "by", header: "By", accessorFn: (h: Detail["history"][number]) => h.by ?? "", cell: ({ row: { original: h } }: { row: { original: Detail["history"][number] } }) => <>{h.by ?? <span className="text-muted-foreground">Someone no longer here</span>}{h.bulk && <span className="text-muted-foreground"> (with other errors)</span>}</> },
              { id: "at", header: "When", accessorFn: (h: Detail["history"][number]) => h.at, cell: ({ row: { original: h } }: { row: { original: Detail["history"][number] } }) => <span title={when(h.at)}>{ago(h.at)}</span> },
            ]}
          />
        </section>
      )}
    </div>
  );
}

// Stack shows an error's stack: in the brand's own code when a source map
// covers it, with the stack as it was sent one press away.
function Stack({ sent, original, browser, onMaps }: { sent: string; original: string; browser: boolean; onMaps: () => void }) {
  const [asSent, setAsSent] = useState(false);
  const shown = original && !asSent ? original : sent;
  return (
    <div>
      <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
        <span className="text-muted-foreground">{original ? (asSent ? "Stack as it was sent" : "Stack in your code") : "Stack"}</span>
        <span className="flex items-center gap-1">
          {original && <Button variant="ghost" onClick={() => setAsSent(!asSent)}>{asSent ? "Show in your code" : "Show as sent"}</Button>}
          <CopyTextButton text={shown} label="Copy stack" />
        </span>
      </div>
      <pre className="max-h-72 overflow-auto rounded-md bg-muted p-3 font-mono text-[12px] leading-relaxed">{shown}</pre>
      {!original && browser && (
        <p className="mt-1 text-[12px] text-muted-foreground">
          Short names like a, t or n mean your site's code is minified. <button className="underline" onClick={onMaps}>Upload source maps</button> to read stacks in your own files and function names.
        </p>
      )}
    </div>
  );
}

const size = (n: number) => (n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`);

// SourceMaps lists the maps a brand uploaded and says how to upload one.
function SourceMaps({ brand, canManage, go }: { brand: string; canManage: boolean; go: (tab: Tab, item?: string) => void }) {
  const [maps, setMaps] = useState<SourceMap[] | null>(null);
  const [limit, setLimit] = useState(0);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    api.GET("/v1/brands/{brand}/source-maps", { params: { path: { brand } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setMaps(data.data);
      setLimit(data.limit);
    });
  }, [brand]);
  useEffect(load, [load]);
  const remove = async (m: SourceMap) => {
    if (!(await confirmAction("Delete this source map?", <>Stacks from <span className="font-mono">{m.file}</span> will show the built names again. You can upload it again at any time.</>, "Delete"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/source-maps/{id}", { params: { path: { brand, id: m.id } } });
    if (error) return toast(errorMessage(error));
    toast("Source map deleted");
    load();
  };
  const command = `jq -n --arg file /assets/app.js --slurpfile map dist/assets/app.js.map '{file: $file, map: $map[0]}' \\
  | curl -X POST ${location.origin}/v1/brands/${brand}/source-maps \\
    -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" --data-binary @-`;
  const header = (
    <PageHeader
      back={{ label: "Errors", onClick: () => go("errors") }}
      title="Source maps"
      description="Your build makes a .map file for each file it minifies. Upload them and every error's stack shows your own files, lines and function names. Your source code is never kept: only names and positions."
    />
  );
  if (error) return <div>{header}<ErrorText>{error}</ErrorText></div>;
  if (!maps) return <div>{header}<Loading /></div>;
  return (
    <div className="space-y-6">
      {header}
      <section aria-label="How to upload" className="space-y-2 rounded-lg border p-4 text-[13px]">
        <h2 className="font-medium">Upload from your build</h2>
        <p className="text-muted-foreground">Run this after each build, once for every .map file, with an API token that can manage analytics. Set <span className="font-mono">file</span> to the built file as your site serves it. Uploading the same file again replaces its map.</p>
        <Snippet title="Upload a source map" code={command} />
        <p className="text-muted-foreground">Add <span className="font-mono">"release"</span> with a version or a commit to keep one map per build. A map uploaded after an error still explains it.</p>
      </section>
      {maps.length === 0 ? (
        <Empty title="No source maps yet">Until you upload one, stacks show the names in your built files.</Empty>
      ) : (
        <DataTable
          data={maps}
          rowId={(m: SourceMap) => m.id}
          columnsMenu={false}
          columns={[
            { id: "file", header: "Built file", accessorFn: (m: SourceMap) => m.file, cell: ({ row: { original: m } }: { row: { original: SourceMap } }) => <span className="font-mono text-[12px]">{m.file}</span> },
            { id: "release", header: "Release", accessorFn: (m: SourceMap) => m.release, cell: ({ row: { original: m } }: { row: { original: SourceMap } }) => m.release || <span className="text-muted-foreground">—</span> },
            { id: "sources", header: "Your files in it", accessorFn: (m: SourceMap) => m.sources, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: m } }: { row: { original: SourceMap } }) => m.sources.toLocaleString() },
            { id: "bytes", header: "Size", accessorFn: (m: SourceMap) => m.bytes, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: m } }: { row: { original: SourceMap } }) => size(m.bytes) },
            { id: "created_at", header: "Uploaded", accessorFn: (m: SourceMap) => m.created_at, cell: ({ row: { original: m } }: { row: { original: SourceMap } }) => <span title={when(m.created_at)}>{ago(m.created_at)}</span> },
            ...(canManage ? [{ id: "actions", header: "", enableSorting: false, cell: ({ row: { original: m } }: { row: { original: SourceMap } }) => <Button variant="ghost" onClick={() => remove(m)}>Delete</Button> }] : []),
          ]}
        />
      )}
      {limit > 0 && <p className="text-[12px] text-muted-foreground">A brand keeps its {limit.toLocaleString()} newest source maps.</p>}
    </div>
  );
}

// ErrorCapture: the brand script's "Errors" switch, right on this page,
// with what is happening now: off, on and working, or on but the script
// has gone quiet. Turning it on changes nothing on the site: the script
// reads the switch from Outflare on the next page a visitor opens.
function ErrorCapture({ site, canSwitch, lastError, go, onChange }: { site: SiteKey | null; canSwitch: boolean; lastError: string | null; go: (tab: Tab, item?: string) => void; onChange: (k: SiteKey) => void }) {
  const [busy, setBusy] = useState(false);
  if (!site) {
    return (
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border px-4 py-3 text-[13px]">
        <div>
          <div className="flex items-center gap-2 font-medium">Error capture <Badge>Off</Badge></div>
          <div className="text-muted-foreground">Errors come from Outflare's website script, and this brand's site doesn't have it yet. Add it once and switch on error capture here.</div>
        </div>
        <Button onClick={() => go("tracking")}>Add the website script</Button>
      </div>
    );
  }
  const on = site.capture_errors;
  const health = scriptHealth(site);
  const name = siteName(site);
  async function flip(next: boolean) {
    if (!site) return;
    setBusy(true);
    const saved = await saveCapture(site, "capture_errors", next);
    setBusy(false);
    if (!saved) return;
    toast(next ? `Error capture is on for ${name}. It starts on the next page a visitor opens.` : `Error capture is off for ${name}.`);
    onChange(saved);
  }
  const status = !on
    ? <>Off. Switch it on to send errors on <span className="font-medium text-foreground">{name}</span> to this page. Nothing changes on your site: the script picks it up on the next page a visitor opens.</>
    : health === "live"
      ? <>Capturing errors on <span className="font-medium text-foreground">{name}</span>. The script is working (last visit {ago(site.last_used_at!)}){lastError ? <>, last error {ago(lastError)}.</> : <>; no errors so far.</>}</>
      : health === "quiet"
        ? <>On for <span className="font-medium text-foreground">{name}</span>, but no visit has arrived from the script since {ago(site.last_used_at!)}, so nothing can be captured. Check the script is still on the site.</>
        : <>On for <span className="font-medium text-foreground">{name}</span>, but the script hasn't sent its first visit yet. Errors show here once it's on the site.</>;
  const tone = !on ? "grey" : health === "live" ? "green" : "amber";
  return (
    <div className="flex items-center justify-between gap-4 rounded-lg border px-4 py-3 text-[13px]">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2 font-medium">
          Error capture
          <Badge tone={tone}>{!on ? "Off" : health === "live" ? "On" : "On, script quiet"}</Badge>
        </div>
        <div className="text-muted-foreground">
          {status}
          {on && health !== "live" && <> <button type="button" className="underline underline-offset-2" onClick={() => go("tracking")}>Open Website tracking</button></>}
          {!canSwitch && <> An owner or admin can switch this.</>}
        </div>
      </div>
      <Switch checked={on} disabled={!canSwitch || busy} onCheckedChange={(v) => void flip(v)} aria-label="Capture errors" />
    </div>
  );
}

type SetupFacts = List["setup"];
// What is still missing for errors to be easy to fix, as steps.
function setupSteps(s: SetupFacts) {
  const share = (n: number) => (s.browser_errors ? `${n.toLocaleString()} of ${s.browser_errors.toLocaleString()} errors` : "No errors yet");
  return [
    { id: "maps", label: "Source maps", done: s.source_maps > 0, status: s.source_maps > 0 ? `${plural(s.source_maps, "map")} uploaded` : "None uploaded", why: "Stacks show your own files, lines and function names instead of a, t and n." },
    { id: "release", label: "Release", done: s.browser_errors > 0 && s.with_release === s.browser_errors, status: `${share(s.with_release)} carry one`, why: "Each error says which version of your site it happened in, and which one it first appeared in." },
    { id: "react", label: "React component names", done: s.with_component_stack > 0, status: s.with_component_stack > 0 ? `${share(s.with_component_stack)} carry them` : "Not sent", why: "For sites built with React: the components the error happened inside.", optional: true },
  ];
}

// SetupCard says, on the list, what the brand's errors still lack.
function SetupCard({ setup, go }: { setup: SetupFacts; go: (tab: Tab, item?: string) => void }) {
  const todo = setupSteps(setup).filter((x) => !x.done && !x.optional);
  if (setup.browser_errors === 0 || todo.length === 0) return null;
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border px-4 py-3 text-[13px]">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2 font-medium">Make these errors easier to fix <Badge tone="amber">{plural(todo.length, "step")} left</Badge></div>
        <div className="text-muted-foreground">{todo.map((x) => `${x.label}: ${x.status.toLowerCase()}`).join(". ")}. Each takes a few minutes, once.</div>
      </div>
      <Button onClick={() => go("errors", "setup")}>Show me how</Button>
    </div>
  );
}

// Setup: the three things that make an error fixable from this page
// alone, each with whether it is done and exactly what to add.
function Setup({ brand, go }: { brand: string; go: (tab: Tab, item?: string) => void }) {
  const [setup, setSetup] = useState<SetupFacts | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/errors", { params: { path: { brand }, query: { days: 7, status: "all" } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setSetup(data.setup);
    });
  }, [brand]);
  const header = (
    <PageHeader
      back={{ label: "Errors", onClick: () => go("errors") }}
      title="Set up"
      description="Three additions make an error fixable from this page alone. Each is done once, by whoever builds your site. Status is from the last 7 days."
    />
  );
  if (error) return <div>{header}<ErrorText>{error}</ErrorText></div>;
  if (!setup) return <div>{header}<Loading /></div>;
  const steps = setupSteps(setup);
  const badge = (x: (typeof steps)[number]) => <Badge tone={x.done ? "green" : x.optional ? "grey" : "amber"}>{x.done ? "Done" : x.optional ? "Optional" : "Not yet"}</Badge>;
  const releaseTag = `<script async src="…/site.js" data-key="…" data-release="2026.11.18"></script>`;
  const releaseCode = `outflare.setRelease(process.env.NEXT_PUBLIC_COMMIT_SHA);`;
  const react = `import { createRoot } from "react-dom/client";

// Sends React's errors to Outflare with the components they happened in.
const report = (handled) => (error, info) =>
  window.outflare?.captureException(error, { component_stack: info.componentStack, handled });

createRoot(document.getElementById("root"), {
  onUncaughtError: report(false),   // nothing caught it
  onCaughtError: report(true),      // an error boundary caught it
  onRecoverableError: report(true), // React recovered by itself
}).render(<App />);`;
  return (
    <div className="space-y-6">
      {header}
      <DataTable
        data={steps}
        rowId={(x: (typeof steps)[number]) => x.id}
        columnsMenu={false}
        columns={[
          { id: "label", header: "Step", enableSorting: false, accessorFn: (x: (typeof steps)[number]) => x.label, cell: ({ row: { original: x } }: { row: { original: (typeof steps)[number] } }) => <span className="font-medium">{x.label}</span> },
          { id: "done", header: "Status", enableSorting: false, accessorFn: (x: (typeof steps)[number]) => x.status, cell: ({ row: { original: x } }: { row: { original: (typeof steps)[number] } }) => <span className="flex items-center gap-2">{badge(x)}<span className="text-muted-foreground">{x.status}</span></span> },
          { id: "why", header: "What you get", enableSorting: false, accessorFn: (x: (typeof steps)[number]) => x.why },
        ]}
      />
      <section aria-label="Source maps" className="space-y-2 rounded-lg border p-4 text-[13px]">
        <h2 className="flex items-center gap-2 font-medium">1. Source maps {badge(steps[0])}</h2>
        <p className="text-muted-foreground">Your build makes a .map file for each file it minifies. Upload them after every build and stacks read in your own code. Maps you upload later still explain errors that already happened.</p>
        <Button onClick={() => go("errors", "source-maps")}>{setup.source_maps ? "Open source maps" : "Upload source maps"}</Button>
      </section>
      <section aria-label="Release" className="space-y-2 rounded-lg border p-4 text-[13px]">
        <h2 className="flex items-center gap-2 font-medium">2. Release {badge(steps[1])}</h2>
        <p className="text-muted-foreground">Tell the website script which version of your site is running: a version number, a date or a commit. Add <span className="font-mono">data-release</span> to the Outflare script tag your site already has, and have your build fill it in.</p>
        <Snippet title="On the script tag" code={releaseTag} />
        <p className="text-muted-foreground">Or, when your build can't change the tag, set it from your code once the script has loaded (here with Next.js's way of reading a build value):</p>
        <Snippet title="From your code" code={releaseCode} />
        <p className="text-muted-foreground">Use the same value as <span className="font-mono">release</span> when you upload source maps, so each version's errors are read with its own maps.</p>
      </section>
      <section aria-label="React component names" className="space-y-2 rounded-lg border p-4 text-[13px]">
        <h2 className="flex items-center gap-2 font-medium">3. React component names {badge(steps[2])}</h2>
        <p className="text-muted-foreground">Only for sites built with React 19 or newer. React knows which components an error happened inside; these three lines where your app starts pass that on. Short codes such as "Minified React error #418" are already spelled out for you without this.</p>
        <Snippet title="Where your app starts" code={react} />
        <p className="text-muted-foreground">With Next.js, the same call goes in <span className="font-mono">error.tsx</span> and <span className="font-mono">global-error.tsx</span>: <span className="font-mono">{"outflare.captureException(error, { handled: true })"}</span>.</p>
      </section>
    </div>
  );
}

const ERROR_ALERT = "error.new_issue";

// ErrorEmails: the brand's switch for emails about new errors. Off until
// someone switches it on; the errors show in the app either way.
function ErrorEmails({ canSwitch, go }: { canSwitch: boolean; go: (tab: Tab, item?: string) => void }) {
  const [to, setTo] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/alerts/settings", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => {
      if (!data) return;
      setTo(data.settings.find((s) => s.kind === ERROR_ALERT)?.email ?? data.kinds.find((k) => k.id === ERROR_ALERT)?.default_email ?? "off");
    });
  }, []);
  if (to === null) return null;
  const on = to !== "off";
  async function flip(next: boolean) {
    setBusy(true);
    const { error } = await api.PUT("/v1/brands/{brand}/alerts/settings", { params: { path: { brand: pickedBrandId() } }, body: { kind: ERROR_ALERT, email: next ? "admins" : "off" } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    setTo(next ? "admins" : "off");
    toast(next ? "Emails about new errors are on for this brand's owners and admins." : "Emails about new errors are off. New errors still show here and under Alerts.");
  }
  return (
    <div className="flex items-center justify-between gap-4 rounded-lg border px-4 py-3 text-[13px]">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2 font-medium">Emails about new errors <Badge tone={on ? "green" : "grey"}>{on ? "On" : "Off"}</Badge></div>
        <div className="text-muted-foreground">
          {on
            ? "Owners and admins get one email the first time each new error happens, and when one you marked fixed comes back. At most 10 an hour. Filtered and ignored errors never email."
            : "Nobody is emailed. Switch on to email owners and admins once for each new error, at most 10 an hour. New errors show here and under Alerts either way."}
          {" "}<button type="button" className="underline underline-offset-2" onClick={() => go("alerts")}>Slack, Telegram and muting for yourself</button>
          {!canSwitch && <> An owner or admin can switch this.</>}
        </div>
      </div>
      <Switch checked={on} disabled={!canSwitch || busy} onCheckedChange={(v) => void flip(v)} aria-label="Email about new errors" />
    </div>
  );
}

// Spark draws errors per day as small bars.
function Spark({ values }: { values: number[] }) {
  const most = Math.max(1, ...values);
  return (
    <span className="flex h-3 w-16 items-end gap-px" aria-label={`Per day: ${values.join(", ")}`} title={values.join(" · ")}>
      {values.map((v, n) => <span key={n} className={`min-w-0 flex-1 rounded-[1px] ${v ? "bg-red-500/70" : "bg-muted"}`} style={{ height: v ? `${Math.max(12, (v / most) * 100)}%` : "2px" }} />)}
    </span>
  );
}

// OccurrenceDetail is one error opened: its message, stack, the browser
// and setting it happened in, what the visitor did before it, and the way
// to their record and the recording of the visit.
function OccurrenceDetail({ o, data, go, fingerprint }: { o: Occurrence; data: Detail; go: (tab: Tab, item?: string) => void; fingerprint: string }) {
  const [before, setBefore] = useState<Before | null>(null);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/errors/{fingerprint}/occurrences/{id}/before", { params: { path: { brand: pickedBrandId(), fingerprint, id: o.id } } }).then(({ data }) => setBefore(data ?? null));
  }, [o.id, fingerprint]);
  const facts: [string, string][] = ([
    ["Site", o.host],
    ["Browser", [properName(o.browser), o.browser_version].filter(Boolean).join(" ")],
    ["System", [properName(o.os), o.os_version].filter(Boolean).join(" ")],
    ["Device", o.device && o.device[0].toUpperCase() + o.device.slice(1)],
    ["Country", o.country],
    ["Language", o.language],
    ["Window", o.viewport],
    ["Colours", o.color_scheme && (o.color_scheme === "dark" ? "Dark mode" : "Light mode")],
    ["Motion", o.reduced_motion && (o.reduced_motion === "reduce" ? "Reduced" : "Normal")],
    ["Release", o.release],
  ] as [string, string][]).filter(([, v]) => v);
  return (
    <div className="space-y-3 border-t px-4 py-3 text-[13px]">
      <p className="break-words">{errorTitle(o.type, o.message)}</p>
      {o.message_full && <p className="break-words text-muted-foreground">In full: {o.message_full}</p>}
      {o.stack
        ? <Stack sent={o.stack} original={o.stack_original} browser={o.source === "browser"} onMaps={() => go("errors", "source-maps")} />
        : <p className="text-muted-foreground">This error came with no stack.</p>}
      {o.component_stack && (
        <div>
          <div className="mb-1 flex items-center justify-between gap-2"><span className="text-muted-foreground">React components it happened in</span><CopyTextButton text={o.component_stack} label="Copy" /></div>
          <pre className="max-h-48 overflow-auto rounded-md bg-muted p-3 font-mono text-[12px] leading-relaxed">{o.component_stack.trim()}</pre>
        </div>
      )}
      {facts.length > 0 && (
        <dl className="grid grid-cols-2 gap-x-6 gap-y-1 sm:grid-cols-3 lg:grid-cols-5">
          {facts.map(([k, v]) => <div key={k} className="min-w-0"><dt className="text-[11px] text-muted-foreground">{k}</dt><dd className="truncate" title={v}>{v}</dd></div>)}
        </dl>
      )}
      {o.user_agent && (
        <div className="flex min-w-0 items-center gap-2 text-[12px] text-muted-foreground">
          <span className="shrink-0">User agent</span><span className="min-w-0 truncate font-mono" title={o.user_agent}>{o.user_agent}</span><CopyTextButton text={o.user_agent} label="Copy" />
        </div>
      )}
      <div>
        <h3 className="mb-1 text-[12px] font-medium">What the visitor did before</h3>
        {!before ? <Loading /> : before.data.length === 0 ? (
          <p className="text-muted-foreground">{o.anonymous_id ? "Nothing in the 30 minutes before." : "No visitor, so nothing to show."}</p>
        ) : (
          <ol className="space-y-0.5">
            {before.data.map((b, n) => (
              <li key={n} className="flex gap-3">
                <span className="w-24 shrink-0 font-mono text-[12px] text-muted-foreground" title={when(b.at)}>{secondsBefore(b.at, before.at)}</span>
                <span className="shrink-0">{b.kind === "page" ? "Viewed" : b.name === "error_caught" ? "Another error" : b.name}</span>
                <span className="min-w-0 truncate font-mono text-[12px] text-muted-foreground">{b.path}</span>
              </li>
            ))}
          </ol>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        {o.recording_id && <Button variant="ghost" onClick={() => go("recordings", o.recording_id!)}>Watch the recording</Button>}
        {o.record_id
          ? <Button variant="ghost" onClick={() => go("people", o.record_id!)}>Open this person</Button>
          : <span className="text-muted-foreground">{o.anonymous_id ? "A visitor who hasn't signed up or signed in yet." : "No visitor."}</span>}
        <CopyTextButton text={errorText(data, o)} label="Copy this error" />
        {o.url && <CopyTextButton text={o.url} label="Copy page address" />}
        {o.url && /^https?:\/\//.test(o.url) && <a className="text-[12px] underline underline-offset-2" href={o.url} target="_blank" rel="noreferrer noopener">Open the page</a>}
      </div>
    </div>
  );
}

// How long before the error something happened: "2 min 5 s before".
function secondsBefore(at: string, error: string): string {
  const s = Math.max(0, Math.round((new Date(error).getTime() - new Date(at).getTime()) / 1000));
  if (s < 60) return `${s} s before`;
  return `${Math.floor(s / 60)} min ${s % 60} s before`;
}

// What an ignore rule looks in, in words.
const ruleFields: { id: RuleField; label: string; hint: string }[] = [
  { id: "message", label: "Message", hint: "The error's message, such as Object Not Found Matching Id." },
  { id: "type", label: "Type", hint: "The kind of error, such as TypeError." },
  { id: "stack", label: "Files in the stack", hint: "The addresses of the scripts it went through, such as chrome-extension:// or a third-party script." },
  { id: "page", label: "Page address", hint: "The full address of the page it happened on." },
  { id: "host", label: "Site", hint: "The site it happened on, such as staging.example.com." },
];
const DRAFT = "of_error_rule_draft";

// ignoreLike opens a new ignore rule filled in from an error.
function ignoreLike(go: (tab: Tab, item?: string) => void, field: RuleField, pattern: string) {
  try { sessionStorage.setItem(DRAFT, JSON.stringify({ field, pattern: pattern.slice(0, 200) })); } catch { /* the form starts empty */ }
  go("errors", "ignore-rules/new");
}

// IgnoreRules lists the filters every brand has and the brand's own
// ignore rules.
function IgnoreRules({ brand, canManage, go }: { brand: string; canManage: boolean; go: (tab: Tab, item?: string) => void }) {
  const [data, setData] = useState<paths["/v1/brands/{brand}/error-rules"]["get"]["responses"]["200"]["content"]["application/json"] | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    api.GET("/v1/brands/{brand}/error-rules", { params: { path: { brand } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setData(data);
    });
  }, [brand]);
  useEffect(load, [load]);
  const remove = async (r: Rule) => {
    if (!(await confirmAction("Delete this rule?", <p>Errors it matched go back to their lists, and new ones can alert again.</p>, "Delete rule"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/error-rules/{id}", { params: { path: { brand, id: r.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Rule deleted.");
    load();
  };
  const header = (
    <PageHeader
      back={{ label: "Errors", onClick: () => go("errors") }}
      title="Ignore rules"
      description="Errors these match stay out of your open list and never alert. They aren't deleted: find them under Filtered, and delete a rule to bring them back."
      action={canManage ? <Button variant="primary" onClick={() => { try { sessionStorage.removeItem(DRAFT); } catch { /* nothing kept */ } go("errors", "ignore-rules/new"); }}>Add rule</Button> : undefined}
    />
  );
  if (error) return <div>{header}<ErrorText>{error}</ErrorText></div>;
  if (!data) return <div>{header}<Loading /></div>;
  const label = (f: RuleField) => ruleFields.find((x) => x.id === f)?.label ?? f;
  return (
    <div className="space-y-6">
      {header}
      <section aria-label="Your rules" className="space-y-2">
        <h2 className="text-[13px] font-medium">Your rules</h2>
        {data.data.length === 0 ? (
          <Empty title="No rules yet">Add one here, or open an error's menu and choose Ignore errors like this.</Empty>
        ) : (
          <DataTable
            data={data.data}
            rowId={(r: Rule) => r.id}
            onRowClick={canManage ? (r: Rule) => go("errors", `ignore-rules/${r.id}`) : undefined}
            columnsMenu={false}
            columns={[
              { id: "field", header: "Looks in", accessorFn: (r: Rule) => label(r.field) },
              { id: "pattern", header: "For", accessorFn: (r: Rule) => r.pattern, cell: ({ row: { original: r } }: { row: { original: Rule } }) => <span className="font-mono text-[12px]">{r.pattern}</span> },
              { id: "note", header: "Why", accessorFn: (r: Rule) => r.note, cell: ({ row: { original: r } }: { row: { original: Rule } }) => r.note || <span className="text-muted-foreground">—</span> },
              { id: "matched", header: "Matched (7 days)", accessorFn: (r: Rule) => r.matched, meta: { align: "right" as const, mono: true } },
              { id: "created_at", header: "Added", accessorFn: (r: Rule) => r.created_at, cell: ({ row: { original: r } }: { row: { original: Rule } }) => <span title={when(r.created_at)}>{ago(r.created_at)}{by(r.created_by)}</span> },
              ...(canManage ? [{ id: "act", header: "", enableSorting: false, meta: { csv: false, pin: "right" as const }, cell: ({ row: { original: r } }: { row: { original: Rule } }) => (
                <span onClick={(e) => e.stopPropagation()} className="flex justify-end gap-1">
                  <Button variant="ghost" onClick={() => go("errors", `ignore-rules/${r.id}`)}>Edit</Button>
                  <Button variant="ghost" className="text-red-600 dark:text-red-400" onClick={() => void remove(r)}>Delete</Button>
                </span>
              ) }] : []),
            ]}
          />
        )}
      </section>
      <section aria-label="Always filtered" className="space-y-2">
        <h2 className="text-[13px] font-medium">Always filtered</h2>
        <p className="text-[13px] text-muted-foreground">These errors don't come from your site's own code, so every brand has them filtered.</p>
        <DataTable
          data={data.builtin}
          rowId={(b: typeof data.builtin[number]) => b.reason}
          columnsMenu={false}
          columns={[
            { id: "label", header: "What", accessorFn: (b: typeof data.builtin[number]) => b.label, cell: ({ row: { original: b } }: { row: { original: typeof data.builtin[number] } }) => <span className="font-medium">{b.label}</span> },
            { id: "description", header: "Why", accessorFn: (b: typeof data.builtin[number]) => b.description },
          ]}
        />
        <p className="text-[12px] text-muted-foreground">Errors from other people's copies of your site are refused before they arrive when you list your own sites under <button type="button" className="underline underline-offset-2" onClick={() => go("tracking")}>Website tracking</button>: edit the script and fill in "Sites it records". Left empty, any site can send errors with your script.</p>
      </section>
    </div>
  );
}

// IgnoreRuleEdit adds a rule, or changes one, with Save and Cancel.
function IgnoreRuleEdit({ brand, id, go }: { brand: string; id: string; go: (tab: Tab, item?: string) => void }) {
  const adding = id === "new";
  const draft = useMemo(() => {
    if (!adding) return null;
    try { return JSON.parse(sessionStorage.getItem(DRAFT) ?? "null") as { field: RuleField; pattern: string } | null; } catch { return null; }
  }, [adding]);
  const [field, setField] = useState<RuleField>(draft?.field ?? "message");
  const [pattern, setPattern] = useState(draft?.pattern ?? "");
  const [note, setNote] = useState("");
  const [loaded, setLoaded] = useState(adding);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (adding) return;
    api.GET("/v1/brands/{brand}/error-rules", { params: { path: { brand } } }).then(({ data, error }) => {
      const r = data?.data.find((x) => x.id === id);
      if (error || !r) return setError(error ? errorMessage(error) : "This rule doesn't exist any more.");
      setField(r.field); setPattern(r.pattern); setNote(r.note); setLoaded(true);
    });
  }, [adding, brand, id]);
  const back = () => go("errors", "ignore-rules");
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    const body = { field, pattern: pattern.trim(), note: note.trim() };
    const { error } = adding
      ? await api.POST("/v1/brands/{brand}/error-rules", { params: { path: { brand } }, body })
      : await api.PATCH("/v1/brands/{brand}/error-rules/{id}", { params: { path: { brand, id } }, body });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    try { sessionStorage.removeItem(DRAFT); } catch { /* nothing kept */ }
    toast(adding ? "Rule added. Errors it matches are now under Filtered." : "Rule saved.");
    back();
  };
  const header = <PageHeader back={{ label: "Ignore rules", onClick: back }} title={adding ? "Add an ignore rule" : "Edit ignore rule"} description="Errors whose chosen part contains this text, in any case, are filtered: kept, but out of your open list and without alerts. It applies to errors already here too." />;
  if (error) return <div>{header}<ErrorText>{error}</ErrorText></div>;
  if (!loaded) return <div>{header}<Loading /></div>;
  return (
    <div className="space-y-6">
      {header}
      <form onSubmit={save} className="max-w-xl space-y-4 rounded-lg border p-4 text-[13px]">
        <label className="block space-y-1">
          <span className="font-medium">Look in</span>
          <select className={`${field} w-full`} value={field} onChange={(e) => setField(e.target.value as RuleField)}>
            {ruleFields.map((f) => <option key={f.id} value={f.id}>{f.label}</option>)}
          </select>
          <span className="block text-[12px] text-muted-foreground">{ruleFields.find((f) => f.id === field)?.hint}</span>
        </label>
        <label className="block space-y-1">
          <span className="font-medium">For this text</span>
          <input className={`${field} w-full font-mono`} value={pattern} maxLength={500} required onChange={(e) => setPattern(e.target.value)} placeholder="Object Not Found Matching Id" />
          <span className="block text-[12px] text-muted-foreground">Any error containing it matches. Shorter text matches more errors.</span>
        </label>
        <label className="block space-y-1">
          <span className="font-medium">Why <span className="font-normal text-muted-foreground">(optional)</span></span>
          <input className={`${field} w-full`} value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} placeholder="Outlook's link scanner, not a real visitor" />
        </label>
        <div className="flex gap-2">
          <Button type="submit" variant="primary" disabled={busy || !pattern.trim()}>{busy ? "Saving…" : "Save"}</Button>
          <Button type="button" variant="ghost" onClick={back}>Cancel</Button>
        </div>
      </form>
    </div>
  );
}
