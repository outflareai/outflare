import { useCallback, useEffect, useState } from "react";
import type { Deploy, DeployOption, DeployTarget, Integration } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, ErrorText, field, Loading, Status, confirmAction, toast } from "../ui";
import { ago, stamp } from "../format";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ConnectorLogo } from "@/components/connector-logo";

// A GitHub or Vercel connection's watched repositories and projects: pick
// one, tie it to the SEO site it serves, and see its latest deploys.
// Outflare only reads deploys, so fixes are re-checked and experiments
// start when a change goes live.

type Site = { id: string; host: string };

export function DeploysDialog({ ws, integration, canManage, onClose }: { ws: string; integration: Integration; canManage: boolean; onClose: () => void }) {
  const [targets, setTargets] = useState<DeployTarget[] | null>(null);
  const [options, setOptions] = useState<DeployOption[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  const [recent, setRecent] = useState<Deploy[]>([]);
  const [pick, setPick] = useState("");
  const [site, setSite] = useState("auto");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const [t, d] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/deploy-targets", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/deploys", { params: { path: { ws } } }),
    ]);
    if (t.error || !t.data) return setError(errorMessage(t.error));
    const mine = t.data.data.filter((x) => x.integration_id === integration.id);
    setTargets(mine);
    setRecent((d.data?.data ?? []).filter((x) => mine.some((m) => m.id === x.target_id)));
  }, [ws, integration.id]);

  useEffect(() => {
    void load();
    api.GET("/v1/workspaces/{ws}/seo-sites", { params: { path: { ws } } }).then(({ data }) => setSites((data?.data ?? []).map((s) => ({ id: s.id, host: s.host }))));
    if (canManage) {
      api.GET("/v1/workspaces/{ws}/integrations/{id}/deploy-options", { params: { path: { ws, id: integration.id } } }).then(({ data, error }) => {
        if (error || !data) return setError(errorMessage(error));
        setOptions(data.data);
      });
    }
  }, [ws, integration.id, canManage, load]);

  async function add() {
    if (!pick) return;
    setBusy(true);
    setError("");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/deploy-targets", {
      params: { path: { ws } },
      body: { integration_id: integration.id, target: pick, ...(site === "auto" ? {} : { site_id: site === "none" ? null : site }) },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    toast(`Watching ${"name" in data ? data.name : pick}${"site_host" in data && data.site_host ? ` for ${data.site_host}` : ""}. The first check runs now.`);
    setPick("");
    setSite("auto");
    setOptions((o) => o?.map((x) => (x.id === pick ? { ...x, watched_by: "id" in data ? data.id : "held" } : x)) ?? null);
    void load();
  }

  async function setTargetSite(t: DeployTarget, value: string) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/deploy-targets/{target}", { params: { path: { ws, target: t.id } }, body: { site_id: value || null } });
    if (error) return toast(errorMessage(error), "error");
    void load();
  }

  async function toggle(t: DeployTarget) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/deploy-targets/{target}", { params: { path: { ws, target: t.id } }, body: { status: t.status === "active" ? "paused" : "active" } });
    if (error) return toast(errorMessage(error), "error");
    void load();
  }

  async function checkNow(t: DeployTarget) {
    const { error } = await api.POST("/v1/workspaces/{ws}/deploy-targets/{target}/check", { params: { path: { ws, target: t.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Checking ${t.name} for deploys.`);
  }

  async function remove(t: DeployTarget) {
    if (!(await confirmAction(`Stop watching ${t.name}?`, "Its recorded deploys are removed too. Fixes on its site are still re-checked by the next crawl.", "Stop watching"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/deploy-targets/{target}", { params: { path: { ws, target: t.id } } });
    if (error) return toast(errorMessage(error), "error");
    setOptions((o) => o?.map((x) => (x.id === t.target ? { ...x, watched_by: null } : x)) ?? null);
    void load();
  }

  const free = (options ?? []).filter((o) => !o.watched_by);
  const what = integration.connector === "vercel" ? "project" : "repository";

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader className="text-left">
          <DialogTitle className="flex items-center gap-2.5 text-[15px]"><ConnectorLogo id={integration.connector} />Deploys from {integration.name}</DialogTitle>
          <DialogDescription className="text-[13px]">
            Pick the {what} behind each website. Outflare checks it for production deploys every five minutes, re-checks that site's fixes when a change goes live, and starts experiments on the real deploy. It only reads; it never pushes code or deploys.
          </DialogDescription>
        </DialogHeader>
        {error && <ErrorText>{error}</ErrorText>}
        {targets === null ? (
          <Loading />
        ) : (
          <div className="space-y-4">
            {targets.length > 0 && (
              <ul className="divide-y rounded-md border">
                {targets.map((t) => (
                  <li key={t.id} className="space-y-2 px-3 py-2.5">
                    <div className="flex items-center justify-between gap-2">
                      <div className="min-w-0">
                        <a className="truncate font-mono text-[13px] hover:underline" href={t.url || undefined} target="_blank" rel="noreferrer">{t.name}</a>
                        <div className="text-[12px] text-muted-foreground">
                          {t.last_deploy_at ? <span title={stamp(t.last_deploy_at)}>Last deploy {ago(t.last_deploy_at)}</span> : "No deploys seen yet"}
                          {t.last_checked_at && <> · checked {ago(t.last_checked_at)}</>}
                        </div>
                      </div>
                      {t.last_error ? <Status tone="red">Needs attention</Status> : t.status === "paused" ? <Status>Paused</Status> : <Status tone="green">Watching</Status>}
                    </div>
                    {t.last_error && <p className="text-[12px] text-muted-foreground">{t.last_error}</p>}
                    <div className="flex flex-wrap items-center gap-2">
                      <label className="flex items-center gap-2 text-[12px] text-muted-foreground">
                        Website
                        <select className={field} value={t.site_id ?? ""} disabled={!canManage} onChange={(e) => void setTargetSite(t, e.target.value)}>
                          <option value="">None</option>
                          {sites.map((s) => <option key={s.id} value={s.id}>{s.host}</option>)}
                        </select>
                      </label>
                      {canManage && (
                        <div className="ml-auto flex gap-1">
                          <Button variant="ghost" onClick={() => void checkNow(t)}>Check now</Button>
                          <Button variant="ghost" onClick={() => void toggle(t)}>{t.status === "active" ? "Pause" : "Resume"}</Button>
                          <Button variant="ghost" onClick={() => void remove(t)}>Stop</Button>
                        </div>
                      )}
                    </div>
                  </li>
                ))}
              </ul>
            )}
            {canManage && (
              <div className="space-y-2">
                <div className="text-[13px] font-medium">Watch a {what}</div>
                {options === null ? (
                  <Loading />
                ) : free.length === 0 ? (
                  <p className="text-[12px] text-muted-foreground">{options.length === 0 ? `This connection can't see any ${what === "project" ? "projects" : "repositories"}. Give it access on ${integration.connector_name}, then open this again.` : `Every ${what} it can see is watched.`}</p>
                ) : (
                  <div className="flex flex-wrap gap-2">
                    <select className={field + " min-w-0 flex-1"} value={pick} onChange={(e) => setPick(e.target.value)} aria-label={`Pick a ${what}`}>
                      <option value="">Pick a {what}</option>
                      {free.map((o) => <option key={o.id} value={o.id}>{o.name}{o.domains.length ? ` · ${o.domains.join(", ")}` : ""}</option>)}
                    </select>
                    <select className={field} value={site} onChange={(e) => setSite(e.target.value)} aria-label="Website it serves">
                      <option value="auto">Website: match its domain</option>
                      <option value="none">No website</option>
                      {sites.map((s) => <option key={s.id} value={s.id}>{s.host}</option>)}
                    </select>
                    <Button disabled={!pick || busy} onClick={() => void add()}>{busy ? "Adding…" : "Watch"}</Button>
                  </div>
                )}
                {sites.length === 0 && <p className="text-[12px] text-muted-foreground">Add your website under SEO, Site audits to tie deploys to it.</p>}
              </div>
            )}
            {recent.length > 0 && (
              <div className="space-y-1.5">
                <div className="text-[13px] font-medium">Latest deploys</div>
                <ul className="divide-y rounded-md border text-[12px]">
                  {recent.slice(0, 8).map((d) => (
                    <li key={d.id} className="flex items-center gap-3 px-3 py-2">
                      <Status tone={d.state === "ready" ? "green" : d.state === "failed" ? "red" : "grey"}>{d.state === "ready" ? "Live" : d.state === "failed" ? "Failed" : "Building"}</Status>
                      <span className="min-w-0 flex-1 truncate">{d.message || d.commit.slice(0, 7) || d.external_id}</span>
                      <a className="whitespace-nowrap text-muted-foreground hover:underline" href={d.url || undefined} target="_blank" rel="noreferrer" title={stamp(d.deployed_at)}>{ago(d.deployed_at)}</a>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
