import { pickedBrandId } from "@/lib/brand";
import { useState } from "react";
import type { SearchInsightRow } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { Button, field, toast } from "../../ui";
import type { Tab } from "../../lib/paths";
import { track } from "../../lib/analytics";
import {
  findingAdvice,
  displayCell,
  columns,
  labels,
  type WorkItem,
  type Filters,
  type Report,
} from "./model";
import { TagEditor } from "./Primitives";
export function Finding({
  ws,
  path,
  row,
  filters,
  item,
  canManage,
  go,
  onSaved,
}: {
  ws: string;
  path: { brand: string; site: string };
  row: SearchInsightRow;
  filters: Filters;
  item?: WorkItem;
  canManage: boolean;
  go: (t: Tab, item?: string) => void;
  onSaved: (item: WorkItem) => void;
}) {
  const [tags, setTags] = useState(item?.tags ?? []);
  const [note, setNote] = useState(item?.note ?? "");
  const [status, setStatus] = useState<WorkItem["status"]>(
    item?.status ?? "open",
  );
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const report = filters.report as Report;
  const advice = findingAdvice(report, row);
  async function action(name: string, fn: () => Promise<void>) {
    setBusy(name);
    setError("");
    setMessage("");
    try {
      await fn();
    } catch (e) {
      setError(
        e instanceof Error ? e.message : "Could not complete this action.",
      );
    } finally {
      setBusy("");
    }
  }
  async function save(
    nextStatus: WorkItem["status"] = status,
  ): Promise<WorkItem> {
    const { data, error } = await api.POST(
      "/v1/brands/{brand}/seo-sites/{site}/insights/items",
      {
        params: { path },
        body: { report, key: row.key, tags, status: nextStatus, note, filters },
      },
    );
    if (error || !data || !("id" in data)) throw Error(errorMessage(error));
    onSaved(data);
    track("ui.search_work_saved", { status, tag_count: tags.length });
    return data;
  }
  // Save first so a finding can become work and a fix in one explicit action.
  async function createFix() {
    const saved = await save();
    const { data, error } = await api.POST(
      "/v1/brands/{brand}/seo-sites/{site}/insights/items/{item}/fix",
      { params: { path: { ...path, item: saved.id } } },
    );
    if (error || !data || !("id" in data)) throw Error(errorMessage(error));
    onSaved(data);
    setStatus("in_progress");
    setMessage(
      "Linked to a fix. Open Fixes to assign it and track implementation.",
    );
    track("ui.search_fix_created", { report });
  }
  async function claim() {
    if (!item?.fix_id) return;
    const { error } = await api.PATCH("/v1/workspaces/{ws}/fixes/{fix}", {
      params: { path: { ws, fix: item.fix_id } },
      body: { assignee_id: "me", status: "in_progress" },
    });
    if (error) throw Error(errorMessage(error));
    await save("in_progress");
    setStatus("in_progress");
    setMessage("Implementation fix assigned to you and marked in progress.");
  }
  async function target() {
    const { error } = await api.PUT(
      "/v1/workspaces/{ws}/seo-sites/{site}/targets",
      {
        params: { path: { ws, site: path.site } },
        body: { url: row.page!, keyword: row.query! },
      },
    );
    if (error) throw Error(errorMessage(error));
    toast("Page keyword target saved.");
    setMessage("Target saved. Internal link suggestions now use this keyword.");
  }
  async function experiment() {
    const { error } = await api.POST("/v1/brands/{brand}/experiments", {
      params: { path: { brand: pickedBrandId() } },
      body: {
        site_id: path.site,
        name: advice.title.slice(0, 120),
        hypothesis: advice.next,
        design: "before_after",
        pattern: new URL(row.page!).pathname,
        metric: "clicks",
        baseline_days: 28,
        measure_days: 28,
        start_on: "deploy",
      },
    });
    if (error) throw Error(errorMessage(error));
    setMessage(
      "Experiment draft created. Review it in Experiments before starting. Before/after measurements can also reflect outside changes.",
    );
  }
  return (
    <div className="space-y-5">
      <div className="rounded-lg border bg-muted/30 p-4 space-y-2">
        <h3 className="font-medium">Why this matters</h3>
        <p className="text-sm">{advice.why}</p>
        <h3 className="pt-2 font-medium">Recommended next step</h3>
        <p className="text-sm">{item?.recommendation || advice.next}</p>
      </div>
      <section className="space-y-2">
        <h3 className="font-medium">Evidence</h3>
        <p className="break-all text-sm">{row.page || row.query || row.key}</p>
        {row.page && row.query && <p className="text-sm">Query: {row.query}</p>}
        <dl className="grid grid-cols-2 gap-2">
          {columns[report]
            .filter((k) => k !== "key")
            .map((k) => (
              <div className="rounded border p-3" key={k}>
                <dt className="text-xs text-muted-foreground">{labels[k]}</dt>
                <dd className="mt-1 break-words text-sm font-medium tabular-nums">
                  {displayCell(row, k)}
                </dd>
              </div>
            ))}
        </dl>
        <p className="text-xs text-muted-foreground">
          {filters.from || filters.to
            ? `${filters.from || "Automatic start"} – ${filters.to || "Latest settled day"}`
            : "Latest available report period"}
          {item
            ? ` · Saved ${new Date(item.created_at).toLocaleString()}. Evidence retained from the original finding.`
            : " · Estimates indicate potential, not a guaranteed result."}
        </p>
        {row.values && (
          <details>
            <summary className="cursor-pointer text-sm">
              View observed history
            </summary>
            <div className="max-h-60 overflow-y-auto text-xs">
              {row.values.map((v) => (
                <p key={v.date} className="flex justify-between border-b p-2">
                  <span>{v.date}</span>
                  <span>{v.value.toLocaleString()}</span>
                </p>
              ))}
            </div>
          </details>
        )}
      </section>
      {canManage ? (
        <>
          <section className="space-y-3 border-t pt-4">
            <h3 className="font-medium">Organise this work</h3>
            <TagEditor tags={tags} onChange={setTags} />
            <label className="block text-sm">
              Status
              <select
                className={`${field} mt-1 block w-full`}
                value={status}
                onChange={(e) =>
                  setStatus(e.target.value as WorkItem["status"])
                }
              >
                {["open", "in_progress", "done", "dismissed"].map((s) => (
                  <option value={s} key={s}>
                    {s.replaceAll("_", " ")}
                  </option>
                ))}
              </select>
            </label>
            <label className="block text-sm">
              Notes
              <textarea
                className={`${field} mt-1 block w-full min-h-24`}
                value={note}
                maxLength={2000}
                onChange={(e) => setNote(e.target.value)}
                placeholder="Decision, context, owner or a follow-up to review"
              />
            </label>
            <Button
              disabled={!!busy}
              onClick={() =>
                action("save", async () => {
                  await save();
                  setMessage("Work saved.");
                })
              }
            >
              {busy === "save"
                ? "Saving…"
                : item
                  ? "Save changes"
                  : "Save to work"}
            </Button>
            {item?.fix_id && (
              <p className="text-xs text-muted-foreground">
                This work has a linked fix. Its implementation status is managed
                in Fixes.
              </p>
            )}
          </section>
          <section className="space-y-3 border-t pt-4">
            <h3 className="font-medium">Take action</h3>
            <div className="flex flex-wrap gap-2">
              {item?.fix_id ? (
                <Button onClick={() => go("fixes")}>Open Fixes</Button>
              ) : (
                <Button
                  disabled={!!busy}
                  onClick={() => action("fix", createFix)}
                >
                  {busy === "fix" ? "Creating…" : "Create implementation fix"}
                </Button>
              )}
              {item?.fix_id && (
                <Button
                  disabled={!!busy}
                  variant="ghost"
                  onClick={() => action("claim", claim)}
                >
                  Assign fix to me
                </Button>
              )}
              {row.page && row.query && (
                <Button
                  disabled={!!busy}
                  variant="ghost"
                  onClick={() => action("target", target)}
                >
                  Set page keyword target
                </Button>
              )}
              {row.page?.startsWith("https://") && (
                <Button
                  disabled={!!busy}
                  variant="ghost"
                  onClick={() => action("experiment", experiment)}
                >
                  Create experiment draft
                </Button>
              )}
              <Button variant="ghost" onClick={() => go("keywords")}>
                Keywords & rank tracking
              </Button>
              <Button variant="ghost" onClick={() => go("audits")}>
                Review technical audit
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              Fixes carry ownership and implementation. Experiments start as
              drafts for review. No change is deployed from this panel.
            </p>
          </section>
        </>
      ) : (
        <p className="text-sm text-muted-foreground">
          You have read access. A member with SEO management access can save and
          act on this finding.
        </p>
      )}
      {error && (
        <p
          role="alert"
          className="rounded border border-destructive/30 p-3 text-sm text-destructive"
        >
          {error}
        </p>
      )}
      {message && (
        <p role="status" className="rounded border bg-muted p-3 text-sm">
          {message}
        </p>
      )}
    </div>
  );
}
