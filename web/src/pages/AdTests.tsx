import { useCallback, useEffect, useState } from "react";
import type { AdCampaign, AdTest } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, ErrorText, Loading, toast } from "../ui";
import { ago } from "../format";
import { money, platformName, scopeQuery, type AdScope } from "./Ads";
import { pickedBrandId } from "@/lib/brand";

// Tests inside the networks: the network splits a campaign between itself
// and a trial that tries one thing differently, on the same days. Outflare
// reads both sides after each sync and says which did better; a person
// keeps the trial's change or rolls it back.

// What each network's tests can try (the engine's AdTester.TestKinds).
export const testKinds: Record<string, "budget" | "bidding"> = { google_ads: "bidding", meta_ads: "budget" };

const statusWord = { starting: "Starting", running: "Running", judged: "Ready to decide", ending: "Ending", kept: "Kept", rolled_back: "Rolled back", failed: "Failed" } as const;
const statusTone = { starting: "grey", running: "blue", judged: "amber", ending: "grey", kept: "green", rolled_back: "grey", failed: "red" } as const;
const verdictWord = { worked: "Trial did better", did_not_work: "Trial did worse", not_sure: "No clear winner", "": "" } as const;
const verdictTone = { worked: "green", did_not_work: "red", not_sure: "grey", "": "grey" } as const;

type ArmNumbers = { spend_micros?: number; conversions?: number };

function Tries({ t }: { t: AdTest }) {
  const [before, after, what] =
    t.kind === "budget"
      ? [t.control.daily_budget_micros, t.trial.daily_budget_micros, "Daily budget"]
      : [t.control.target_cpa_micros, t.trial.target_cpa_micros, "Bids to"];
  return (
    <span>
      {what} {before != null && <>{money(before, t.currency)} → </>}
      {money(after, t.currency)}
      <span className="text-muted-foreground"> · {t.split_percent}% to the trial</span>
    </span>
  );
}

function Arms({ t }: { t: AdTest }) {
  const c = t.result.control as ArmNumbers | undefined;
  const tr = t.result.trial as ArmNumbers | undefined;
  if (!c || !tr) return <span className="text-muted-foreground">{t.status === "running" ? `Compared from ${t.measure_from}` : "—"}</span>;
  const cpa = (a: ArmNumbers) => (a.conversions ? money((a.spend_micros ?? 0) / a.conversions, t.currency) : "—");
  return (
    <div className="text-[13px] tabular-nums">
      <div>Campaign: {Math.round(c.conversions ?? 0)} conversions at {cpa(c)}</div>
      <div>Trial: {Math.round(tr.conversions ?? 0)} conversions at {cpa(tr)}</div>
    </div>
  );
}

function Campaign({ t }: { t: AdTest }) {
  return (
    <div>
      <div className="font-medium">{t.campaign_name}</div>
      <div className="text-[12px] text-muted-foreground">{platformName(t.connector)} · {t.account_name}</div>
    </div>
  );
}

export function AdTests({ ws, brand, scope, campaigns, canManage }: { ws: string; brand: string; scope: AdScope; campaigns: AdCampaign[]; canManage: boolean }) {
  const [rows, setRows] = useState<AdTest[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/ads/tests", { params: { path: { brand: pickedBrandId() }, query: { ...(brand ? { brand } : {}), ...scopeQuery(scope), limit: 200 } } });
    setError(error ? errorMessage(error) : "");
    setRows(data?.data ?? []);
  }, [ws, brand, scope.network, scope.account]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    load();
  }, [load]);

  async function start(c: AdCampaign) {
    const kind = testKinds[c.connector];
    const budget = kind === "budget";
    const current = budget ? c.daily_budget_micros : (c.platform_target_cpa_micros ?? c.target_cost_per_conversion_micros);
    const r = await ask({
      title: `Test a change to ${c.name}`,
      body: budget
        ? `Meta splits the campaign's audience in half: one half sees the campaign as it is, the other a copy with this daily budget (in ${c.currency}). Outflare compares them and you keep the better one.`
        : `Google Ads sends half the campaign's traffic to a trial that bids to this cost per conversion (in ${c.currency}). Outflare compares them and you keep the better one.`,
      input: { label: budget ? "Trial's daily budget" : "Trial's target cost per conversion", type: "number", value: current ? (current / 1e6).toFixed(2) : "", placeholder: "25.00" },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Start the test", value: "ok", variant: "primary" }],
    });
    const v = Math.round(Number(r?.value) * 1e6);
    if (r?.choice !== "ok" || !(v > 0)) return;
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/ads/tests", {
      params: { path: { brand: pickedBrandId() } },
      body: { campaign_id: c.id, kind, ...(budget ? { daily_budget_micros: v } : { target_cpa_micros: v }) },
    });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Test queued. It starts on the platform tomorrow.");
    load();
  }
  async function end(t: AdTest, keep: boolean) {
    setBusy(true);
    const p = { params: { path: { brand: pickedBrandId(), id: t.id } } };
    const { error } = keep ? await api.POST("/v1/brands/{brand}/ads/tests/{id}/keep", p) : await api.POST("/v1/brands/{brand}/ads/tests/{id}/roll-back", p);
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(keep ? "Keeping the trial's change. The platform shows it within a few minutes." : "Rolling back. The campaign stays as it was.");
    load();
  }

  if (error && !rows) return <ErrorText>{error}</ErrorText>;
  if (!rows) return <Loading />;
  const live = rows.filter((t) => ["starting", "running", "judged", "ending"].includes(t.status));
  const ended = rows.filter((t) => !live.includes(t));
  const testing = new Set(live.map((t) => t.campaign_id));
  const ready = campaigns.filter((c) => c.status === "active" && testKinds[c.connector] && !testing.has(c.id));
  return (
    <>
      {error && <ErrorText>{error}</ErrorText>}
      <Card
        title="Tests"
        description="The network itself splits a campaign between how it is and a trial that tries one thing differently, on the same days, so the two compare fairly. Google Ads tries a different target cost per conversion, Meta a different daily budget. A test runs until the smaller side has about 30 conversions (two to six weeks); Outflare then says which did better, and you keep the trial's change or roll it back."
      >
        <DataTable
          data={live}
          rowId={(t) => t.id}
          columnsMenu={false}
          empty="No tests running."
          columns={[
            { id: "campaign", header: "Campaign", accessorFn: (t) => t.campaign_name, cell: ({ row: { original: t } }) => <Campaign t={t} /> },
            { id: "tries", header: "Tries", accessorFn: (t) => t.kind, cell: ({ row: { original: t } }) => <Tries t={t} /> },
            { id: "arms", header: "So far", accessorFn: (t) => t.status, cell: ({ row: { original: t } }) => <Arms t={t} /> },
            {
              id: "status",
              header: "Status",
              accessorFn: (t) => t.status,
              cell: ({ row: { original: t } }) => (
                <div className="space-y-1">
                  <Badge tone={statusTone[t.status]}>{statusWord[t.status]}</Badge>
                  {t.verdict && <div><Badge tone={verdictTone[t.verdict]}>{verdictWord[t.verdict]}</Badge></div>}
                  <div className="text-[12px] text-muted-foreground" title={t.window_note}>{t.status === "judged" ? t.verdict_note : `Until ${t.ends_on}`}</div>
                  {t.error && <div className="text-[12px] text-destructive">{t.error}</div>}
                </div>
              ),
            },
            ...(canManage
              ? [{ id: "decide", header: "", cell: ({ row: { original: t } }: { row: { original: AdTest } }) =>
                  t.status === "running" || t.status === "judged" ? (
                    <div className="flex justify-end gap-2">
                      <Button disabled={busy} onClick={() => end(t, true)}>Keep it</Button>
                      <Button variant="ghost" disabled={busy} onClick={() => end(t, false)}>Roll back</Button>
                    </div>
                  ) : null }]
              : []),
          ]}
        />
      </Card>
      {canManage && ready.length > 0 && (
        <Card title="Start a test" description="Running campaigns on Google Ads and Meta without a test. Nothing changes on the platform until you start one.">
          <DataTable
            data={ready}
            rowId={(c) => c.id}
            columnsMenu={false}
            columns={[
              { id: "campaign", header: "Campaign", accessorFn: (c) => c.name, cell: ({ row: { original: c } }) => <div><div className="font-medium">{c.name}</div><div className="text-[12px] text-muted-foreground">{platformName(c.connector)} · {c.account_name}</div></div> },
              { id: "now", header: "Now", accessorFn: (c) => c.connector, cell: ({ row: { original: c } }) =>
                  <span className="text-muted-foreground">{testKinds[c.connector] === "budget" ? `${money(c.daily_budget_micros, c.currency)} a day` : c.platform_target_cpa_micros != null ? `Bids to ${money(c.platform_target_cpa_micros, c.currency)}` : "No target"}</span> },
              { id: "start", header: "", cell: ({ row: { original: c } }) => <div className="flex justify-end"><Button variant="ghost" disabled={busy} onClick={() => start(c)}>Test a change</Button></div> },
            ]}
          />
        </Card>
      )}
      {ended.length > 0 && (
        <Card title="Finished tests" description="What each test found, and what was kept.">
          <DataTable
            data={ended}
            rowId={(t) => t.id}
            columnsMenu={false}
            columns={[
              { id: "campaign", header: "Campaign", accessorFn: (t) => t.campaign_name, cell: ({ row: { original: t } }) => <Campaign t={t} /> },
              { id: "tries", header: "Tried", accessorFn: (t) => t.kind, cell: ({ row: { original: t } }) => <Tries t={t} /> },
              { id: "status", header: "Outcome", accessorFn: (t) => t.status, cell: ({ row: { original: t } }) => <Badge tone={statusTone[t.status]}>{statusWord[t.status]}</Badge> },
              { id: "note", header: "", accessorFn: (t) => t.verdict_note, cell: ({ row: { original: t } }) => <span className="text-[13px] text-muted-foreground">{t.error ?? t.verdict_note}</span> },
              { id: "when", header: "Ended", accessorFn: (t) => t.ended_at ?? "", cell: ({ row: { original: t } }) => <span className="text-muted-foreground">{t.ended_at ? ago(t.ended_at) : ""}</span> },
            ]}
          />
        </Card>
      )}
    </>
  );
}
