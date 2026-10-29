import { useEffect, useState } from "react";
import type { AdTrackingHealth, AdTrackingTemplate } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Card, CopyButton, ErrorText, field, Loading } from "../ui";

// The tracking template for each ad network: pasted once into the network,
// it makes every paid click land with UTM tags and the network's own
// campaign, ad set and ad ids, which website tracking stores on the visit.

export function AdTrackingTemplates({ ws }: { ws: string }) {
  const [all, setAll] = useState<AdTrackingTemplate[] | null>(null);
  const [network, setNetwork] = useState("google");
  const [error, setError] = useState("");

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/ads/tracking-templates", { params: { path: { ws } } })
      .then(({ data, error }) => (error ? setError(errorMessage(error)) : setAll(data.data)));
  }, [ws]);

  const t = all?.find((n) => n.network === network);
  return (
    <Card
      title="Tracking templates"
      description="Paste one into each ad network so every paid click arrives with its campaign, ad set and ad ids. Website tracking keeps them on the visit, so sales are credited to the ad even after it is renamed."
    >
      {error ? <ErrorText>{error}</ErrorText> : !all ? <Loading /> : (
        <div className="space-y-3 text-[13px]">
          <select aria-label="Ad network" className={field} value={network} onChange={(e) => setNetwork(e.target.value)}>
            {all.map((n) => <option key={n.network} value={n.network}>{n.label}</option>)}
          </select>
          {t && (
            <>
              {t.template ? (
                <>
                  <div className="text-muted-foreground">Paste into <span className="font-medium text-foreground">{t.field}</span>:</div>
                  <div className="flex items-start gap-1 rounded-md border bg-muted/40 p-2">
                    <code className="min-w-0 flex-1 break-all font-mono text-[12px]">{t.template}</code>
                    <CopyButton text={t.template} label="Copy template" />
                  </div>
                </>
              ) : null}
              {t.click_ids.length > 0 && (
                <div className="text-muted-foreground">
                  Click id: {t.click_ids.map((c) => <code key={c} className="font-mono text-[12px]">{c}</code>)}
                  {t.cookies.length > 0 && <> · Pixel cookies, read with consent: {t.cookies.map((c, i) => <code key={c} className="font-mono text-[12px]">{i ? ", " : ""}{c}</code>)}</>}
                </div>
              )}
              {t.note && <p className="text-muted-foreground">{t.note}</p>}
            </>
          )}
        </div>
      )}
    </Card>
  );
}

// Tracking health: per network, how many paid visits arrived with their
// click id, only the template's ad ids, or neither; synced campaigns that
// spent without a tracked visit; and whether conversions sent back are
// taken. Issues come first, each with what to do.
export function AdTrackingHealthCard({ ws, brand }: { ws: string; brand?: string }) {
  const [h, setH] = useState<AdTrackingHealth | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/ads/tracking-health", { params: { path: { ws }, query: brand ? { brand } : {} } })
      .then(({ data, error }) => (error ? setError(errorMessage(error)) : setH(data)));
  }, [ws, brand]);
  if (error) return <Card title="Tracking health"><ErrorText>{error}</ErrorText></Card>;
  if (!h) return null;
  if (h.networks.length === 0 && h.sends.length === 0 && h.campaigns_without_visits.length === 0) return null;
  const pct = (n: number, of: number) => (of > 0 ? `${Math.round((n * 100) / of)}%` : "—");
  return (
    <Card
      title="Tracking health"
      description={`The last ${h.days} days: how your paid clicks were tracked, campaigns that spent without a tracked visit, and whether the conversions you send back are taken.`}
    >
      <div className="space-y-4 text-[13px]">
        {h.issues.length === 0 ? (
          <p className="text-muted-foreground">Nothing to fix: paid clicks arrive with their ids and conversions are taken.</p>
        ) : (
          <ul className="space-y-2">
            {h.issues.map((i, n) => (
              <li key={n} className="rounded-md border p-3">
                <div className="flex items-start gap-2">
                  <Badge tone={i.severity === "critical" ? "red" : "amber"}>{i.severity === "critical" ? "Fix now" : "Check"}</Badge>
                  <div className="min-w-0 space-y-0.5">
                    <div>{i.message}</div>
                    <div className="text-[12px] text-muted-foreground">{i.fix}</div>
                  </div>
                </div>
              </li>
            ))}
          </ul>
        )}
        {h.networks.length > 0 && (
          <div className="divide-y rounded-md border">
            <div className="grid grid-cols-[1fr_auto_auto_auto] gap-3 px-3 py-1.5 text-[12px] text-muted-foreground">
              <span>Paid visits</span><span>Click id</span><span>Ad ids only</span><span>Untracked</span>
            </div>
            {h.networks.map((n) => (
              <div key={n.network} className="grid grid-cols-[1fr_auto_auto_auto] gap-3 px-3 py-1.5 tabular-nums">
                <span>{n.label} <span className="text-muted-foreground">· {n.visits.toLocaleString()}</span></span>
                <span>{pct(n.click_id, n.visits)}</span>
                <span>{pct(n.ad_ids_only, n.visits)}</span>
                <span className={n.untracked > 0 ? "text-amber-700 dark:text-amber-400" : ""}>{pct(n.untracked, n.visits)}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </Card>
  );
}
