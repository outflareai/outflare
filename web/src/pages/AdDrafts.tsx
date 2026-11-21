// New ads the brand agent (or a person) wrote for the brand: each waits
// for a person, who approves it (it is made on the platform, paused),
// checks it there, and turns it on.

import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { AdDraft } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, ErrorText, field, Loading, toast } from "../ui";
import { track } from "@/lib/analytics";
import { adLimits, isSearchAd, overLimit, SearchAdPreview } from "./ads/AdPreview";
import { ago } from "../format";
import { money, platformName } from "./Ads";

const kindWord = { ad: "New ad", ad_group: "New ad group", campaign: "New campaign" } as const;
const statusWord = { draft: "Waiting for you", creating: "Being made", paused: "Made, paused", going_live: "Turning on", live: "Live", failed: "Failed", dismissed: "Dismissed" } as const;
const statusTone = { draft: "amber", creating: "blue", paused: "blue", going_live: "blue", live: "green", failed: "red", dismissed: "grey" } as const;

function Where({ d }: { d: AdDraft }) {
  return (
    <div>
      <div className="font-medium">{kindWord[d.kind]}{d.kind !== "ad" && d.content.name ? `: ${d.content.name}` : ""}</div>
      <div className="text-[12px] text-muted-foreground">
        {platformName(d.connector)} · {d.account_name}
        {d.campaign_name && <> · {d.campaign_name}</>}
        {d.ad_group_name && <> · {d.ad_group_name}</>}
      </div>
    </div>
  );
}

// What the draft makes, as close to how the ad reads as a table allows.
function Preview({ d, edit }: { d: AdDraft; edit?: ReactNode }) {
  const c = d.content;
  const link = (() => {
    try {
      return new URL(c.final_url ?? "").host;
    } catch {
      return c.final_url ?? "";
    }
  })();
  // A search ad reads as it will in Google's results.
  const search = !c.primary_text && !c.image_url && isSearchAd(c);
  return (
    <div className="w-[440px] max-w-[60vw] space-y-1 whitespace-normal text-[13px]">
      {search ? <SearchAdPreview ad={c} where="new_ads" actions={edit} /> : (
        <>
          <div className="text-[12px] text-muted-foreground">{link}{c.path1 ? `/${c.path1}` : ""}{c.path2 ? `/${c.path2}` : ""}</div>
          {c.primary_text && <div>{c.primary_text}</div>}
          <div className="font-medium text-blue-700 dark:text-blue-400">{(c.headlines ?? []).join(" | ")}</div>
          {(c.descriptions ?? []).map((t) => <div key={t} className="text-muted-foreground">{t}</div>)}
        </>
      )}
      {c.image_url && <div className="text-[12px] text-muted-foreground">Image: <a className="underline" href={c.image_url} target="_blank" rel="noreferrer">{c.image_url}</a></div>}
      {c.keywords && c.keywords.length > 0 && (
        <div className="text-[12px] text-muted-foreground">Keywords: {c.keywords.map((k) => (k.match_type === "exact" ? `[${k.text}]` : k.match_type === "broad" ? k.text : `"${k.text}"`)).join(", ")}</div>
      )}
      {c.daily_budget_micros != null && (
        <div className="text-[12px] text-muted-foreground">
          {money(c.daily_budget_micros, d.currency)} a day{c.countries?.length ? ` · ${c.countries.join(", ")}` : ""}
          {c.bidding?.target_cpa_micros != null ? ` · target ${money(c.bidding.target_cpa_micros, d.currency)} a conversion` : ""}
        </div>
      )}
    </div>
  );
}

// One line per headline or description, each with its length against
// Google's limit.
function Lines({ label, value, max, rows, onChange }: { label: string; value: string; max: number; rows: number; onChange: (v: string) => void }) {
  const lines = value.split("\n").filter((l) => l.trim());
  const long = lines.filter((l) => [...l.trim()].length > max);
  return (
    <label className="block space-y-1 text-[13px]">
      <span className="flex justify-between font-medium">
        <span>{label}</span>
        <span className="font-normal text-muted-foreground">{lines.length} · one per line · up to {max} characters each</span>
      </span>
      <textarea className={`${field} w-full font-[inherit]`} rows={rows} value={value} onChange={(e) => onChange(e.target.value)} aria-invalid={long.length > 0} />
      {long.map((l) => <span key={l} className="block text-[12px] text-red-600 dark:text-red-400">"{l.trim()}" is {[...l.trim()].length} characters; cut {[...l.trim()].length - max}.</span>)}
    </label>
  );
}

// A new search ad's text, edited beside its preview. Saving checks it like
// a new draft; the ad still waits for approval.
function EditDraft({ brand, d, done }: { brand: string; d: AdDraft; done: (saved: boolean) => void }) {
  const c = d.content;
  const [url, setUrl] = useState(c.final_url ?? "");
  const [path1, setPath1] = useState(c.path1 ?? "");
  const [path2, setPath2] = useState(c.path2 ?? "");
  const [headlines, setHeadlines] = useState((c.headlines ?? []).join("\n"));
  const [descriptions, setDescriptions] = useState((c.descriptions ?? []).join("\n"));
  const [busy, setBusy] = useState(false);
  const list = (v: string) => v.split("\n").map((l) => l.trim()).filter(Boolean);
  // Pins are per headline, so they hold only while the headlines keep their places.
  const same = list(headlines).join("\n") === (c.headlines ?? []).join("\n");
  const next = { ...c, final_url: url.trim(), path1: path1.trim() || undefined, path2: path2.trim() || undefined, headlines: list(headlines), descriptions: list(descriptions), headline_pins: same ? c.headline_pins : undefined };
  const over = overLimit(next);
  async function save() {
    setBusy(true);
    const { error } = await api.PATCH("/v1/brands/{brand}/ads/drafts/{id}", { params: { path: { brand, id: d.id } }, body: { ...next, reason: d.reason } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Saved. It still waits for your approval.");
    done(true);
  }
  return (
    <Card title="Edit this ad" description={`${d.campaign_name ?? d.account_name}${d.ad_group_name ? ` · ${d.ad_group_name}` : ""}. Google shows up to three headlines and two descriptions at a time, in any order.`}>
      <div className="grid gap-6 lg:grid-cols-2">
        <div className="space-y-3">
          <Lines label="Headlines" value={headlines} max={adLimits.headline} rows={Math.max(6, list(headlines).length + 1)} onChange={setHeadlines} />
          <Lines label="Descriptions" value={descriptions} max={adLimits.description} rows={5} onChange={setDescriptions} />
          <label className="block space-y-1 text-[13px]">
            <span className="font-medium">Page the ad opens</span>
            <input className={`${field} w-full`} value={url} onChange={(e) => setUrl(e.target.value)} />
          </label>
          <div className="grid grid-cols-2 gap-3">
            {([["Path, first part", path1, setPath1], ["Path, second part", path2, setPath2]] as const).map(([label, v, set]) => (
              <label key={label} className="block space-y-1 text-[13px]">
                <span className="flex justify-between font-medium"><span>{label}</span><span className={`font-normal ${[...v].length > adLimits.path ? "text-red-600 dark:text-red-400" : "text-muted-foreground"}`}>{[...v].length}/{adLimits.path}</span></span>
                <input className={`${field} w-full`} value={v} onChange={(e) => set(e.target.value)} />
              </label>
            ))}
          </div>
        </div>
        <div className="space-y-2">
          <div className="text-[13px] font-medium">How it reads in Google</div>
          <SearchAdPreview ad={next} where="edit" />
        </div>
      </div>
      <div className="mt-4 flex gap-2">
        <Button disabled={busy || over > 0 || !next.headlines.length || !next.descriptions.length} onClick={save}>Save</Button>
        <Button variant="ghost" disabled={busy} onClick={() => done(false)}>Cancel</Button>
      </div>
    </Card>
  );
}

export function AdDrafts({ brand, canManage }: { brand: string; canManage: boolean }) {
  const [rows, setRows] = useState<AdDraft[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<AdDraft | null>(null);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/ads/drafts", { params: { path: { brand }, query: { limit: 200 } } });
    setError(error ? errorMessage(error) : "");
    setRows(data?.data ?? []);
  }, [brand]);
  useEffect(() => {
    load();
  }, [load]);

  async function act(d: AdDraft, step: "approve" | "go-live" | "dismiss") {
    if (step === "go-live" && !(await confirmAction(`Turn on this ${kindWord[d.kind].toLowerCase()}?`,
      `It starts spending on ${platformName(d.connector)}${d.kind === "campaign" ? " within its daily budget" : " from its ad group's budget"}. Check it on the platform first.`, "Turn it on", false))) return;
    setBusy(true);
    const p = { params: { path: { brand, id: d.id } } };
    const { error } = step === "approve"
      ? await api.POST("/v1/brands/{brand}/ads/drafts/{id}/approve", p)
      : step === "go-live" ? await api.POST("/v1/brands/{brand}/ads/drafts/{id}/go-live", p) : await api.POST("/v1/brands/{brand}/ads/drafts/{id}/dismiss", p);
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(step === "approve" ? `Making it on ${platformName(d.connector)}, paused. Check it there, then turn it on here.` : step === "go-live" ? "Turning it on." : "Dismissed.");
    setTimeout(load, step === "dismiss" ? 0 : 4000);
  }

  if (error && !rows) return <ErrorText>{error}</ErrorText>;
  if (!rows) return <Loading />;
  if (editing) return <EditDraft brand={brand} d={editing} done={(saved) => { setEditing(null); if (saved) load(); }} />;
  const editLink = (d: AdDraft) => canManage && d.status === "draft"
    ? <button type="button" className="text-[12px] text-blue-700 hover:underline dark:text-blue-400" onClick={() => { track("ui.ad_draft_edit_opened"); setEditing(d); }}>Edit</button>
    : undefined;
  const open = rows.filter((d) => d.status !== "live" && d.status !== "dismissed");
  const done = rows.filter((d) => d.status === "live");
  return (
    <>
      {error && <ErrorText>{error}</ErrorText>}
      <Card
        title="New ads"
        description="Ads, ad groups and search campaigns the brand agent wrote, each with why. Approving makes it on the platform, paused, so you can check it there; then turn it on here. Nothing spends until you do."
      >
        <DataTable
          data={open}
          rowId={(d) => d.id}
          columnsMenu={false}
          empty="No new ads waiting. The brand agent writes them with the playbook Write new ads where the old ones are tired."
          columns={[
            { id: "where", header: "Where", accessorFn: (d) => d.campaign_name ?? d.account_name, cell: ({ row: { original: d } }) => <Where d={d} /> },
            { id: "ad", header: "Ad", accessorFn: (d) => (d.content.headlines ?? []).join(" "), cell: ({ row: { original: d } }) => <Preview d={d} edit={editLink(d)} /> },
            { id: "why", header: "Why", accessorFn: (d) => d.reason, cell: ({ row: { original: d } }) => (
              <div className="max-w-[280px] text-[13px] text-muted-foreground">
                {d.reason}
                {d.error && <div className="mt-1 text-red-600 dark:text-red-400">{d.error}</div>}
              </div>
            ) },
            { id: "status", header: "Status", accessorFn: (d) => d.status, cell: ({ row: { original: d } }) => <Badge tone={statusTone[d.status]}>{statusWord[d.status]}</Badge> },
            ...(canManage
              ? [{ id: "decide", header: "", cell: ({ row: { original: d } }: { row: { original: AdDraft } }) => (
                  <div className="flex justify-end gap-2">
                    {(d.status === "draft" || d.status === "failed") && <Button disabled={busy} onClick={() => act(d, "approve")}>{d.status === "failed" ? "Try again" : "Approve"}</Button>}
                    {d.status === "paused" && <Button disabled={busy} onClick={() => act(d, "go-live")}>Turn on</Button>}
                    {(d.status === "draft" || d.status === "failed") && <Button variant="ghost" disabled={busy} onClick={() => act(d, "dismiss")}>Dismiss</Button>}
                  </div>
                ) }]
              : []),
          ]}
        />
      </Card>
      {done.length > 0 && (
        <Card title="New ads that are live" description="Compare each with the ads beside it on the Overview tab, at the ad group's level.">
          <DataTable
            data={done}
            rowId={(d) => d.id}
            columnsMenu={false}
            columns={[
              { id: "where", header: "Where", accessorFn: (d) => d.campaign_name ?? d.account_name, cell: ({ row: { original: d } }) => <Where d={d} /> },
              { id: "ad", header: "Ad", accessorFn: (d) => (d.content.headlines ?? []).join(" "), cell: ({ row: { original: d } }) => <Preview d={d} /> },
              { id: "when", header: "Live since", accessorFn: (d) => d.live_at ?? "", cell: ({ row: { original: d } }) => <span className="text-muted-foreground">{d.live_at ? ago(d.live_at) : ""}</span> },
            ]}
          />
        </Card>
      )}
    </>
  );
}
