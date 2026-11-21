import { useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Markdown } from "@/components/markdown-editor";
import { api, errorMessage } from "../api";
import { Badge, Button, ErrorText } from "../ui";
import { track } from "../lib/analytics";

// A page's passport on the target report: what the page is for (intent,
// page type), what it holds (a Markdown summary), what to fix, and the
// import of a whole keyword map from a CSV file.

type Report = components["schemas"]["SEOTargetReport"];
type Row = Report["targets"][number];
type ImportRow = {
  url: string;
  keyword?: string;
  secondary?: string[];
  intent?: string;
  page_type?: string;
  summary?: string;
};

export const intents = ["informational", "commercial", "transactional", "navigational"] as const;
export const pageTypes = ["homepage", "product", "pricing", "use-case", "comparison", "blog", "location", "tool", "docs", "other"] as const;

export const statusText: Record<Row["status"], string> = {
  on_target: "On target",
  cannibalized: "Cannibalized",
  wrong_intent: "Wrong intent",
  missing_content: "Missing content",
  no_data: "No data",
};
export const statusTone: Record<Row["status"], "green" | "red" | "amber" | "grey"> = {
  on_target: "green",
  cannibalized: "red",
  wrong_intent: "amber",
  missing_content: "amber",
  no_data: "grey",
};

const pathOf = (u: string) => {
  try {
    const x = new URL(u);
    return x.pathname + x.search;
  } catch {
    return u;
  }
};

// Passport shows and edits a target's intent, page type and summary, with
// the report's suggested fixes. The summary is read when the row opens.
export function Passport({ r, brand, onSaved }: { r: Row; brand: string; onSaved: () => void }) {
  const [summary, setSummary] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [intent, setIntent] = useState(r.intent);
  const [pageType, setPageType] = useState(r.page_type);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!r.summary_length) {
      setSummary("");
      return;
    }
    let live = true;
    api
      .GET("/v1/brands/{brand}/seo-sites/{site}/targets/{target}", { params: { path: { brand, site: r.site_id, target: r.id } } })
      .then(({ data, error }) => {
        if (!live) return;
        if (error || !data) return setError(errorMessage(error));
        setSummary(data.summary ?? "");
      });
    return () => {
      live = false;
    };
  }, [brand, r.id, r.site_id, r.summary_length]);

  const save = async () => {
    setBusy(true);
    setError("");
    const { error } = await api.PATCH("/v1/brands/{brand}/seo-sites/{site}/targets/{target}", {
      params: { path: { brand, site: r.site_id, target: r.id } },
      body: { intent: intent as Row["intent"], page_type: pageType as Row["page_type"], summary: draft },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setSummary(draft);
    setEditing(false);
    onSaved();
  };

  return (
    <div className="grid gap-5 lg:grid-cols-[1fr_1.4fr]">
      <div>
        <div className="mb-1 flex items-center gap-2 text-[12px] font-semibold">
          Suggested fixes
          <Badge tone={statusTone[r.status]}>{statusText[r.status]}</Badge>
        </div>
        {r.owner && (
          <p className="mb-1 text-[12px]">
            Ranking instead: <span className="font-mono text-[11px]">{pathOf(r.owner)}</span>
          </p>
        )}
        {r.suggestions.length ? (
          <ul className="list-disc space-y-1 pl-4 text-[12px]">
            {r.suggestions.map((s, i) => (
              <li key={i}>
                {s.text}
                {s.url && <span className="ml-1 font-mono text-[11px] text-muted-foreground">{pathOf(s.url)}</span>}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-[12px] text-muted-foreground">Nothing to fix.</p>
        )}
      </div>
      <div>
        <div className="mb-1 flex items-center gap-2 text-[12px] font-semibold">
          The page
          {!editing && (
            <button
              type="button"
              className="text-[12px] font-normal underline underline-offset-2"
              onClick={() => {
                setDraft(summary ?? "");
                setIntent(r.intent);
                setPageType(r.page_type);
                setEditing(true);
              }}
            >
              Edit
            </button>
          )}
        </div>
        {editing ? (
          <div className="space-y-2">
            <div className="flex flex-wrap gap-2 text-[12px]">
              <label className="flex items-center gap-1">
                Intent
                <select value={intent} onChange={(e) => setIntent(e.target.value as Row["intent"])} className="h-7 rounded border bg-background px-1">
                  <option value="">Not set</option>
                  {intents.map((x) => (
                    <option key={x}>{x}</option>
                  ))}
                </select>
              </label>
              <label className="flex items-center gap-1">
                Page type
                <select value={pageType} onChange={(e) => setPageType(e.target.value as Row["page_type"])} className="h-7 rounded border bg-background px-1">
                  <option value="">Not set</option>
                  {pageTypes.map((x) => (
                    <option key={x}>{x}</option>
                  ))}
                </select>
              </label>
            </div>
            <textarea
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              rows={10}
              maxLength={20000}
              placeholder={"# Page title\n## Sections (H2)\n- Plans and prices shown\n- Features\n- Calls to action\n- FAQ questions"}
              aria-label="Page summary in Markdown"
              className="w-full rounded border bg-background p-2 font-mono text-[12px]"
            />
            {error && <ErrorText>{error}</ErrorText>}
            <div className="flex gap-2">
              <Button onClick={save} disabled={busy}>
                {busy ? "Saving…" : "Save"}
              </Button>
              <Button variant="ghost" onClick={() => setEditing(false)}>
                Cancel
              </Button>
            </div>
          </div>
        ) : (
          <div className="space-y-2 text-[12px]">
            <div className="text-muted-foreground">
              Intent: <span className="text-foreground">{r.intent || "not set"}</span> · Page type: <span className="text-foreground">{r.page_type || "not set"}</span>
            </div>
            {error && <ErrorText>{error}</ErrorText>}
            {summary === null ? (
              <p className="text-muted-foreground">Loading summary…</p>
            ) : summary ? (
              <div className="max-h-72 overflow-auto rounded border bg-background p-2">
                <Markdown text={summary} />
              </div>
            ) : (
              <p className="text-muted-foreground">No summary yet. Add what the page holds (headings, plans and prices, features, calls to action, FAQ) so its content can be checked against the keywords.</p>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

// parseCSV reads RFC 4180 CSV: quoted fields may hold commas, quotes ("")
// and line breaks.
export function parseCSV(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quoted) {
      if (c === '"') {
        if (text[i + 1] === '"') {
          field += '"';
          i++;
        } else quoted = false;
      } else field += c;
      continue;
    }
    if (c === '"') quoted = true;
    else if (c === ",") {
      row.push(field);
      field = "";
    } else if (c === "\n" || c === "\r") {
      if (c === "\r" && text[i + 1] === "\n") i++;
      row.push(field);
      if (row.some((x) => x.trim() !== "")) rows.push(row);
      row = [];
      field = "";
    } else field += c;
  }
  row.push(field);
  if (row.some((x) => x.trim() !== "")) rows.push(row);
  return rows;
}

const columns: Record<string, keyof ImportRow> = {
  url: "url",
  page: "url",
  primary_keyword: "keyword",
  keyword: "keyword",
  primary: "keyword",
  secondary_keywords: "secondary",
  secondary: "secondary",
  intent: "intent",
  search_intent: "intent",
  page_type: "page_type",
  type: "page_type",
  summary: "summary",
};

// toImportRows turns CSV rows (with a header) into targets. Secondary
// keywords are split on | or ;. A literal \n in a summary is a line break.
export function toImportRows(rows: string[][]): { targets: ImportRow[]; problems: string[] } {
  const problems: string[] = [];
  if (rows.length < 2) return { targets: [], problems: ["The file has no rows under its header."] };
  const head = rows[0].map((h) => columns[h.trim().toLowerCase().replace(/[\s-]+/g, "_")]);
  if (!head.includes("url")) return { targets: [], problems: ["The header needs a url column."] };
  const targets: ImportRow[] = [];
  for (const r of rows.slice(1)) {
    const t: ImportRow = { url: "" };
    head.forEach((k, i) => {
      const v = (r[i] ?? "").trim();
      if (!k || v === "") return;
      if (k === "secondary") t.secondary = v.split(/[|;]/).map((x) => x.trim()).filter(Boolean).slice(0, 10);
      else if (k === "summary") t.summary = v.replaceAll("\\n", "\n");
      else if (k === "intent" || k === "page_type") t[k] = v.toLowerCase();
      else t[k] = v;
    });
    if (t.url) targets.push(t);
  }
  return { targets, problems };
}

const bare = (h: string) => h.toLowerCase().replace(/^www\./, "").replace(/:\d+$/, "");

type Outcome = { created: number; updated: number; skipped: number; errors: { url: string; message: string }[] };

// ImportTargets loads a keyword map from a CSV file into the brand's SEO
// sites, by each URL's host, 500 pages per request.
export function ImportTargets({ brand, sites, open, onClose, onDone }: { brand: string; sites: Report["sites"]; open: boolean; onClose: () => void; onDone: () => void }) {
  const [rows, setRows] = useState<ImportRow[] | null>(null);
  const [problems, setProblems] = useState<string[]>([]);
  const [progress, setProgress] = useState("");
  const [result, setResult] = useState<Outcome | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) {
      setRows(null);
      setProblems([]);
      setProgress("");
      setResult(null);
      setError("");
    }
  }, [open]);

  const read = async (f: File) => {
    const parsed = toImportRows(parseCSV(await f.text()));
    setRows(parsed.targets);
    setProblems(parsed.problems);
    setResult(null);
  };

  const siteOf = (u: string) => {
    try {
      const h = bare(new URL(u).host);
      return sites.find((s) => bare(s.host) === h);
    } catch {
      return undefined;
    }
  };

  const run = async () => {
    if (!rows) return;
    setError("");
    const out: Outcome = { created: 0, updated: 0, skipped: 0, errors: [] };
    const bySite = new Map<string, ImportRow[]>();
    for (const t of rows) {
      const s = siteOf(t.url);
      if (!s) {
        out.skipped++;
        out.errors.push({ url: t.url, message: "not a page of the brand's SEO sites" });
        continue;
      }
      bySite.set(s.id, [...(bySite.get(s.id) ?? []), t]);
    }
    let done = 0;
    for (const [site, list] of bySite) {
      for (let i = 0; i < list.length; i += 500) {
        const part = list.slice(i, i + 500);
        setProgress(`${done} of ${rows.length} pages`);
        const { data, error } = await api.POST("/v1/brands/{brand}/seo-sites/{site}/targets/import", {
          params: { path: { brand, site } },
          body: { targets: part as never },
        });
        if (error || !data) {
          setError(errorMessage(error));
          setResult(out);
          return;
        }
        out.created += data.created;
        out.updated += data.updated;
        out.skipped += data.skipped;
        out.errors.push(...data.errors.map((e) => ({ url: e.url, message: e.message })));
        done += part.length;
      }
    }
    setProgress("");
    setResult(out);
    track("ui.seo_targets_imported", { rows: rows.length, created: out.created, updated: out.updated, skipped: out.skipped });
    onDone();
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Import a keyword map</DialogTitle>
          <DialogDescription>
            A CSV file with a header row: url, primary_keyword, secondary_keywords (split with |), intent, page_type, summary (Markdown). Pages that have a target keep what a row leaves
            empty.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3 text-[13px]">
          <input type="file" accept=".csv,text/csv" aria-label="CSV file" onChange={(e) => e.target.files?.[0] && read(e.target.files[0])} />
          <p className="text-[12px] text-muted-foreground">
            intent: {intents.join(", ")}. page_type: {pageTypes.join(", ")}.
          </p>
          {problems.map((p) => (
            <ErrorText key={p}>{p}</ErrorText>
          ))}
          {rows && !result && (
            <div className="flex items-center gap-3">
              <span>{rows.length} pages read.</span>
              <Button onClick={run} disabled={!rows.length || !!progress}>
                {progress ? `Importing… ${progress}` : "Import"}
              </Button>
            </div>
          )}
          {error && <ErrorText>{error}</ErrorText>}
          {result && (
            <div className="space-y-2">
              <p>
                {result.created} added, {result.updated} updated, {result.skipped} skipped.
              </p>
              {result.errors.length > 0 && (
                <div className="max-h-56 overflow-auto rounded border p-2 text-[12px]">
                  {result.errors.slice(0, 200).map((e, i) => (
                    <div key={i}>
                      <span className="font-mono text-[11px]">{e.url || "(no url)"}</span>: {e.message}
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
