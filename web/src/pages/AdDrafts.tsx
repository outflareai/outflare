// New ads the brand agent (or a person) wrote for the brand: each waits
// for a person, who approves it (it is made on the platform, paused),
// checks it there, and turns it on.

import { useCallback, useEffect, useState } from "react";
import type { AdDraft } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, ErrorText, Loading, toast } from "../ui";
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
function Preview({ d }: { d: AdDraft }) {
  const c = d.content;
  const link = (() => {
    try {
      return new URL(c.final_url ?? "").host;
    } catch {
      return c.final_url ?? "";
    }
  })();
  return (
    <div className="max-w-[460px] space-y-1 text-[13px]">
      <div className="text-[12px] text-muted-foreground">{link}{c.path1 ? `/${c.path1}` : ""}{c.path2 ? `/${c.path2}` : ""}</div>
      {c.primary_text && <div>{c.primary_text}</div>}
      <div className="font-medium text-blue-700 dark:text-blue-400">{(c.headlines ?? []).join(" | ")}</div>
      {(c.descriptions ?? []).map((t) => <div key={t} className="text-muted-foreground">{t}</div>)}
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

export function AdDrafts({ brand, canManage }: { brand: string; canManage: boolean }) {
  const [rows, setRows] = useState<AdDraft[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
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
            { id: "ad", header: "Ad", accessorFn: (d) => (d.content.headlines ?? []).join(" "), cell: ({ row: { original: d } }) => <Preview d={d} /> },
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
