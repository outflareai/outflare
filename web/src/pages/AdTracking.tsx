import { useEffect, useState } from "react";
import type { AdAccount, AdTrackingHealth, AdTrackingPlan, AdTrackingRun, AdTrackingTemplate } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ago } from "../format";
import { Badge, Button, Card, CopyButton, ErrorText, field, Loading } from "../ui";

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
  if (h.networks.length === 0 && h.sends.length === 0 && h.campaigns_without_visits.length === 0 && h.ads_without_template.length === 0 && h.issues.length === 0) return null;
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
        {h.ads_without_template.length > 0 && (
          <div className="divide-y rounded-md border">
            <div className="px-3 py-1.5 text-[12px] text-muted-foreground">Ads without the tracking template</div>
            {h.ads_without_template.map((a) => (
              <div key={a.ad_entity_id} className="flex flex-wrap items-baseline justify-between gap-x-3 px-3 py-1.5">
                <span className="min-w-0 truncate">
                  {a.name || a.external_id} <span className="text-muted-foreground">· {a.campaign_name}</span>
                </span>
                <span className="font-mono text-[12px] tabular-nums text-muted-foreground">
                  {a.spend.map((m) => `${(m.amount_cents / 100).toLocaleString(undefined, { style: "currency", currency: m.currency })}`).join(" · ") || "No spend yet"}
                </span>
              </div>
            ))}
          </div>
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

// Fixing tracking links: one box per Google Ads account, where Outflare
// adds the template for you.
export function AdTrackingFix({ ws, accounts, canManage }: { ws: string; accounts: AdAccount[]; canManage: boolean }) {
  const google = accounts.filter((a) => a.connector === "google_ads" && a.status === "active");
  if (google.length === 0) return null;
  return (
    <Card
      title="Fix tracking links"
      description="Outflare adds its tracking template to your Google Ads URL suffixes, so every click carries its campaign, ad group and ad ids. You see each change before anything is changed."
    >
      <div className="space-y-3 text-[13px]">
        {canManage ? (
          google.map((a) => <AddTracking key={a.id} ws={ws} account={a} />)
        ) : (
          <p className="text-muted-foreground">Ask an admin to fix tracking links: it changes your Google Ads account.</p>
        )}
      </div>
    </Card>
  );
}

const levelWord: Record<string, string> = { account: "Account", campaign: "Campaign", ad_group: "Ad group", ad: "Ad" };

// Adding the template for you: Outflare reads the account's URL suffixes,
// shows what it would change, and changes them only when you confirm.
function AddTracking({ ws, account }: { ws: string; account: AdAccount }) {
  const [plan, setPlan] = useState<AdTrackingPlan | null>(null);
  const [state, setState] = useState<"idle" | "reading" | "ready" | "adding">("idle");
  const [error, setError] = useState("");
  // The latest request and how it went, polled while it runs.
  const [run, setRun] = useState<AdTrackingRun | null>(null);
  const path = { params: { path: { ws, id: account.id } } };
  const running = run?.status === "queued" || run?.status === "running" || run?.status === "retrying";
  useEffect(() => {
    let stop = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = () =>
      api.GET("/v1/workspaces/{ws}/ads/accounts/{id}/tracking/run", { params: { path: { ws, id: account.id } } }).then(({ data }) => {
        if (stop || !data) return;
        setRun(data.run ?? null);
        const r = data.run;
        if (r && (r.status === "queued" || r.status === "running" || r.status === "retrying")) timer = setTimeout(poll, 3000);
      });
    poll();
    return () => {
      stop = true;
      clearTimeout(timer);
    };
  }, [ws, account.id, state]);
  const read = () => {
    setState("reading");
    setError("");
    api.GET("/v1/workspaces/{ws}/ads/accounts/{id}/tracking", path).then(({ data, error }) => {
      if (error) {
        setError(errorMessage(error));
        setState("idle");
        return;
      }
      setPlan(data);
      setState("ready");
    });
  };
  const add = () => {
    setState("adding");
    setError("");
    api.POST("/v1/workspaces/{ws}/ads/accounts/{id}/tracking", path).then(({ error }) => {
      if (error) {
        setError(errorMessage(error));
        setState("ready");
        return;
      }
      setPlan(null);
      // Starts polling the run.
      setState("idle");
    });
  };
  const changes = plan?.changes ?? [];
  const ads = changes.filter((c) => c.level === "ad").length;
  return (
    <div className="space-y-2 rounded-md border p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <div className="font-medium">{account.name || account.external_id}</div>
          <div className="text-[12px] text-muted-foreground">
            Google uses an ad's own URL suffix, else its ad group's, its campaign's, then the account's. Outflare adds the template to each one that needs it and keeps the parameters already there.
          </div>
        </div>
        {state === "idle" && !running && <Button variant="ghost" onClick={read}>{run ? "Check again" : "See what changes"}</Button>}
        {state === "reading" && <Loading />}
      </div>
      {error && <ErrorText>{error}</ErrorText>}
      {(state === "ready" || state === "adding") && plan && (
        changes.length === 0 ? (
          <p className="text-muted-foreground">Every URL suffix already has the template. If ads are still marked, press Sync now.</p>
        ) : (
          <>
            <div className="max-h-80 divide-y overflow-y-auto rounded-md border">
              {changes.map((c) => (
                <div key={c.level + c.id} className="space-y-0.5 px-3 py-1.5">
                  <div className="flex items-baseline gap-2">
                    <Badge>{levelWord[c.level] ?? c.level}</Badge>
                    <span className="min-w-0 truncate">{c.name || c.id}</span>
                  </div>
                  <div className="break-all font-mono text-[11px] text-muted-foreground">
                    {c.before ? <span className="line-through">{c.before}</span> : <span>No suffix</span>}
                    <span> → </span>
                    <span className="text-foreground">{c.after}</span>
                  </div>
                </div>
              ))}
            </div>
            <div className="flex flex-wrap items-center gap-3">
              <Button disabled={state === "adding"} onClick={add}>
                Add it to {changes.length === 1 ? "this one" : `these ${changes.length}`}
              </Button>
              <Button variant="ghost" disabled={state === "adding"} onClick={() => setState("idle")}>Cancel</Button>
              {ads > 0 && (
                <span className="text-[12px] text-muted-foreground">
                  {ads === 1 ? "One ad has" : `${ads} ads have`} a suffix of {ads === 1 ? "its" : "their"} own, so Google reviews {ads === 1 ? "it" : "them"} again.
                </span>
              )}
            </div>
          </>
        )
      )}
      {state === "idle" && run && <TrackingRun run={run} />}
    </div>
  );
}

// How the latest request went: a spinner while it runs, then what changed,
// what Google refused and why, or why it failed.
function TrackingRun({ run }: { run: AdTrackingRun }) {
  if (run.status === "queued" || run.status === "running" || run.status === "retrying")
    return (
      <div className="flex items-center gap-2 rounded-md bg-muted/50 px-3 py-2">
        <span className="size-3 animate-spin rounded-full border-2 border-muted-foreground border-t-transparent" aria-hidden />
        <span>
          {run.status === "queued" ? "Waiting to start…" : run.status === "running" ? "Adding the template in Google Ads…" : "Google didn't answer. Trying again shortly…"}
        </span>
        {run.error && <span className="truncate text-[12px] text-muted-foreground">{run.error}</span>}
      </div>
    );
  if (run.status === "failed")
    return (
      <div className="rounded-md border border-red-300 bg-red-50 px-3 py-2 dark:border-red-900 dark:bg-red-950/40">
        <div className="font-medium text-red-700 dark:text-red-400">Couldn't add the template {ago(run.finished_at ?? run.requested_at)}. Nothing was changed.</div>
        {run.error && <div className="mt-0.5 break-words text-[12px] text-muted-foreground">{run.error}</div>}
      </div>
    );
  const none = run.changed === 0 && run.failed === 0;
  return (
    <div className="space-y-2">
      <div className={`rounded-md border px-3 py-2 ${run.failed > 0 ? "border-amber-300 bg-amber-50 dark:border-amber-900 dark:bg-amber-950/40" : "border-green-300 bg-green-50 dark:border-green-900 dark:bg-green-950/40"}`}>
        <div className="font-medium">
          {none
            ? `Nothing needed changing ${ago(run.finished_at)}: every URL suffix already had the template.`
            : `Done ${ago(run.finished_at)}: ${run.changed} ${run.changed === 1 ? "suffix" : "suffixes"} changed${run.failed > 0 ? `, ${run.failed} refused by Google` : ""}.`}
        </div>
        {!none && <div className="text-[12px] text-muted-foreground">The account syncs now; the ads list below updates within a few minutes. Each change is in the audit log.</div>}
      </div>
      {run.errors.length > 0 && (
        <div className="max-h-60 divide-y overflow-y-auto rounded-md border">
          {run.errors.map((c) => (
            <div key={c.level + c.id} className="space-y-0.5 px-3 py-1.5">
              <div className="flex items-baseline gap-2">
                <Badge tone="red">{levelWord[c.level] ?? c.level}</Badge>
                <span className="min-w-0 truncate">{c.name || c.id}</span>
              </div>
              <div className="break-words text-[12px] text-muted-foreground">{c.error}</div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
