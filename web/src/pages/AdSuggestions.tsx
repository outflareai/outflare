import { useCallback, useEffect, useState } from "react";
import type { AdRecommendation, AdSuggestion } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, ErrorText, Loading, toast } from "../ui";
import { ago } from "../format";
import { money, platformName, scopeQuery, type AdScope } from "./Ads";
import { testKinds } from "./AdTests";
import { pickedBrandId } from "@/lib/brand";

// Suggestions at the level the page shows: open ones wait for a person,
// approved ones are measured over a window and then judged.

const kindWord = { budget_up: "Raise budget", budget_down: "Lower budget", pause: "Pause", resume: "Resume", keyword_add: "Add keyword", negative_add: "Add negative keyword", keyword_split: "Own ad group", bidding_target: "Bid to a target" } as Record<string, string>;
const verdictWord = { worked: "Worked", did_not_work: "Didn't work", not_sure: "Not sure", "": "" } as const;
const verdictTone = { worked: "green", did_not_work: "red", not_sure: "grey", "": "grey" } as const;

function Change({ s }: { s: AdSuggestion }) {
  const before = typeof s.evidence.daily_budget_micros === "number" ? s.evidence.daily_budget_micros : null;
  const after = typeof s.proposed.daily_budget_micros === "number" ? s.proposed.daily_budget_micros : null;
  return (
    <span>
      {kindWord[s.kind] ?? s.kind.replace(/^alert\./, "Alert: ").replaceAll("_", " ")}
      {typeof s.proposed.keyword === "string" && <> <span className="font-mono">[{s.proposed.keyword}]</span></>}
      {after !== null && <> {before !== null && <>{money(before, s.currency)} → </>}{money(after, s.currency)}</>}
    </span>
  );
}

function Campaign({ s }: { s: AdSuggestion }) {
  return (
    <div>
      <div className="font-medium">{s.campaign_name}</div>
      <div className="text-[12px] text-muted-foreground">{platformName(s.connector)} · {s.account_name}</div>
    </div>
  );
}

// What the platform itself recommends for one account, read live.
export function Recommendations({ ws, account }: { ws: string; account: string }) {
  const [rows, setRows] = useState<AdRecommendation[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    api.GET("/v1/brands/{brand}/ads/recommendations", { params: { path: { brand: pickedBrandId() }, query: { account_id: account } } }).then(({ data, error }) => {
      if (!live) return;
      setError(error ? errorMessage(error) : "");
      setRows(data?.supported ? data.data : null);
    });
    return () => {
      live = false;
    };
  }, [ws, account]);
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!rows) return null;
  const words = (t: string) => t.charAt(0) + t.slice(1).toLowerCase().replace(/_/g, " ");
  return (
    <Card title="The platform's own recommendations" description="What Google Ads itself recommends for this account, not yet applied or dismissed there. Apply them in Google Ads; Outflare's suggestions above are judged against your own targets.">
      <DataTable
        data={rows}
        rowId={(r) => r.id}
        columnsMenu={false}
        empty="Google Ads has no open recommendations for this account."
        columns={[
          { id: "type", header: "Recommendation", accessorFn: (r) => r.type, cell: ({ row: { original: r } }) => <span>{words(r.type)}</span> },
          { id: "impact", header: "Expected", accessorFn: (r) => r.impact?.potential_conversions ?? 0, cell: ({ row: { original: r } }) =>
            r.impact ? <span className="text-muted-foreground">{Math.round(r.impact.conversions ?? 0)} → {Math.round(r.impact.potential_conversions ?? 0)} conversions a week</span> : <span className="text-muted-foreground">—</span> },
        ]}
      />
    </Card>
  );
}

export function AdSuggestions({ ws, brand, scope, canManage }: { ws: string; brand: string; scope: AdScope; canManage: boolean }) {
  const [rows, setRows] = useState<AdSuggestion[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/ads/suggestions", { params: { path: { brand: pickedBrandId() }, query: { ...scopeQuery(scope), limit: 300 } } });
    setError(error ? errorMessage(error) : "");
    setRows(data?.data ?? []);
  }, [ws, brand, scope.network, scope.account]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    load();
  }, [load]);

  async function decide(s: AdSuggestion, approve: boolean, test = false) {
    setBusy(true);
    const p = { params: { path: { brand: pickedBrandId(), id: s.id } } };
    const { error } = approve
      ? await api.POST("/v1/brands/{brand}/ads/suggestions/{id}/approve", { ...p, body: test ? { test: true } : {} })
      : await api.POST("/v1/brands/{brand}/ads/suggestions/{id}/dismiss", p);
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(test ? "Test queued: the platform tries the change on part of the audience first. Follow it on the Tests tab."
      : approve ? "Change sent to the platform. Outflare measures it and gives a verdict when the window ends." : "Dismissed.");
    load();
  }
  // A budget suggestion can be tried inside the network first where its
  // tests try a budget.
  const testable = (s: AdSuggestion) =>
    ((s.kind === "budget_up" || s.kind === "budget_down") && testKinds[s.connector] === "budget") || (s.kind === "bidding_target" && testKinds[s.connector] === "bidding");
  async function refresh() {
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/ads/suggestions/refresh", { params: { path: { brand: pickedBrandId() } } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Reading your campaigns again. New suggestions show in a minute.");
    setTimeout(load, 8000);
  }

  if (error && !rows) return <ErrorText>{error}</ErrorText>;
  if (!rows) return <Loading />;
  const open = rows.filter((s) => s.status === "open");
  const measuring = rows.filter((s) => s.status === "measuring" || s.status === "testing");
  const judged = rows.filter((s) => s.status === "judged");
  return (
    <>
      {error && <ErrorText>{error}</ErrorText>}
      <Card
        title="Suggestions"
        description="One change at a time per campaign, from its last 14 days against its target cost per conversion: raise the budget of a campaign that runs out of it at a good cost, lower it where conversions cost too much, pause one that spends without converting, resume one that did well. Nothing changes until you approve it."
        action={canManage ? <Button variant="ghost" disabled={busy} onClick={refresh}>Check again</Button> : undefined}
      >
        <DataTable
          data={open}
          rowId={(s) => s.id}
          columnsMenu={false}
          empty="Nothing to suggest right now. Campaigns need a target cost per conversion (set it on the Campaigns tab) and a few conversions."
          columns={[
            { id: "campaign", header: "Campaign", accessorFn: (s) => s.campaign_name, cell: ({ row: { original: s } }) => <Campaign s={s} /> },
            { id: "change", header: "Change", accessorFn: (s) => s.kind, cell: ({ row: { original: s } }) => <Change s={s} /> },
            { id: "why", header: "Why", accessorFn: (s) => s.reason, cell: ({ row: { original: s } }) => <span className="text-[13px] text-muted-foreground">{s.reason}</span> },
            ...(canManage
              ? [{ id: "decide", header: "", cell: ({ row: { original: s } }: { row: { original: AdSuggestion } }) => (
                  <div className="flex justify-end gap-2">
                    <Button disabled={busy} onClick={() => decide(s, true)}>Approve</Button>
                    {testable(s) && <Button variant="ghost" disabled={busy} onClick={() => decide(s, true, true)}>Test it first</Button>}
                    <Button variant="ghost" disabled={busy} onClick={() => decide(s, false)}>Dismiss</Button>
                  </div>
                ) }]
              : []),
          ]}
        />
      </Card>
      {scope.account && scope.network === "google_ads" && <Recommendations ws={ws} account={scope.account} />}
      {measuring.length > 0 && (
        <Card title="Measuring" description="Approved changes, measured long enough to judge: the platform's settling time, at least a week and about 30 conversions, plus a few days for late conversions. A change that costs more than twice the target three days running is judged early.">
          <DataTable
            data={measuring}
            rowId={(s) => s.id}
            columnsMenu={false}
            columns={[
              { id: "campaign", header: "Campaign", accessorFn: (s) => s.campaign_name, cell: ({ row: { original: s } }) => <Campaign s={s} /> },
              { id: "change", header: "Change", accessorFn: (s) => s.kind, cell: ({ row: { original: s } }) => <Change s={s} /> },
              { id: "window", header: "Verdict by", accessorFn: (s) => s.measure_until ?? "", cell: ({ row: { original: s } }) => <span title={s.window_note}>{s.status === "testing" ? "Being tested (Tests tab)" : (s.measure_until ?? "—")}</span> },
            ]}
          />
        </Card>
      )}
      {judged.length > 0 && (
        <Card title="Results" description="Each approved change against as many days before it.">
          <DataTable
            data={judged}
            rowId={(s) => s.id}
            columnsMenu={false}
            columns={[
              { id: "campaign", header: "Campaign", accessorFn: (s) => s.campaign_name, cell: ({ row: { original: s } }) => <Campaign s={s} /> },
              { id: "change", header: "Change", accessorFn: (s) => s.kind, cell: ({ row: { original: s } }) => <Change s={s} /> },
              { id: "verdict", header: "Verdict", accessorFn: (s) => s.verdict, cell: ({ row: { original: s } }) => <Badge tone={verdictTone[s.verdict]}>{verdictWord[s.verdict]}</Badge> },
              { id: "note", header: "", accessorFn: (s) => s.verdict_note, cell: ({ row: { original: s } }) => <span className="text-[13px] text-muted-foreground">{s.verdict_note}</span> },
              { id: "when", header: "Judged", accessorFn: (s) => s.judged_at ?? "", cell: ({ row: { original: s } }) => <span className="text-muted-foreground">{s.judged_at ? ago(s.judged_at) : ""}</span> },
            ]}
          />
        </Card>
      )}
    </>
  );
}
