// Choosing what an analytics connection imports: every site it can see
// (each GA4 property, PostHog project or Search Console property), whether
// to store it and which brand it belongs to. The brand's website becomes
// the site's domain, so its numbers line up with the brand's other tools
// without anyone typing a domain. Opens right after a tool is connected and
// from Analytics while any site has no brand.

import { useEffect, useMemo, useState } from "react";
import type { AnalyticsSiteBrand, AnalyticsSitePlan, Brand } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, ErrorText, field, Loading, toast } from "../ui";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { Checkbox } from "@/components/ui/checkbox";
import { ConnectorLogo } from "@/components/connector-logo";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";

type Source = { id: string; name: string; connector: string };

// A row as the person is setting it: brand is a brand's handle, "none" for
// shared by every brand, or "" for not chosen yet.
type Draft = { sync: boolean; brand: string; domain: string; suggested: boolean };

const norm = (s: string) => s.toLowerCase().replace(/[^a-z0-9]/g, "");
const hostOf = (url?: string) => (url ?? "").replace(/^[a-z-]+:(\/\/)?/i, "").replace(/^www\./, "").split(/[/?#:]/)[0].toLowerCase();
const domainsOf = (b: Brand) => [b.website, ...b.domains].filter(Boolean).map((d) => d.toLowerCase());
const belongs = (host: string, b: Brand) => !!host && domainsOf(b).some((d) => host === d || host.endsWith("." + d));

// Sites that look like test copies start switched off.
const testCopy = /\b(staging|stage|test|testing|dev|development|sandbox|qa|preview|local)\b/i;

// guessBrand reads the brand from the site's address, else from its name
// ("zenshield-4fe59" is Zenshield). GA4 adds the account in brackets, which
// names the company rather than the site, so that part is left out.
export function guessBrand(site: AnalyticsSitePlan, brands: Brand[]): Brand | undefined {
  const host = site.host || hostOf(site.url);
  const byHost = brands.find((b) => belongs(host, b));
  if (byHost) return byHost;
  const text = norm(site.name.replace(/\s*\([^)]*\)\s*$/, ""));
  let best: Brand | undefined, score = 0, tie = false;
  for (const b of brands) {
    const keys = [b.name, b.handle, ...domainsOf(b).map((d) => d.split(".")[0])].map(norm).filter((k) => k.length >= 3);
    const s = Math.max(0, ...keys.filter((k) => text.includes(k)).map((k) => k.length));
    if (s > score) [best, score, tie] = [b, s, false];
    else if (s > 0 && s === score && b.id !== best?.id) tie = true;
  }
  return tie ? undefined : best;
}

export function SiteSetup({ ws, source, canManage, onSaved, onCancel, compact = false }: {
  ws: string; source: Source; canManage: boolean; onSaved?: () => void; onCancel?: () => void; compact?: boolean;
}) {
  const { active: brands } = useBrand(ws);
  const [sites, setSites] = useState<AnalyticsSitePlan[] | null>(null);
  const [mapped, setMapped] = useState<Record<string, AnalyticsSiteBrand>>({});
  const [drafts, setDrafts] = useState<Record<string, Draft>>({});
  const [editing, setEditing] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);

  useEffect(() => {
    Promise.all([
      api.GET("/v1/workspaces/{ws}/analytics/{id}/sites", { params: { path: { ws, id: source.id } } }),
      api.GET("/v1/workspaces/{ws}/analytics/site-brands", { params: { path: { ws }, query: { integration: source.id } } }),
    ]).then(([s, m]) => {
      if (s.error || !s.data) return setError(errorMessage(s.error));
      setSites(s.data.data);
      setMapped(Object.fromEntries((m.data?.data ?? []).map((b) => [b.site, b])));
    });
  }, [ws, source.id, reload]);

  // The brand list is rebuilt on every render; this changes only when the
  // brands do.
  const brandKey = brands.map((b) => b.id + b.website + b.domains.join()).join("|");
  const byHandle = useMemo(() => new Map(brands.map((b) => [b.handle, b])), [brandKey]); // eslint-disable-line react-hooks/exhaustive-deps

  // What each site is now: a picked brand, the brand its domain belongs
  // to, or nothing.
  const current = (s: AnalyticsSitePlan): string => {
    const m = mapped[s.id];
    if (m?.source === "set") return m.brand_id ? (brands.find((b) => b.id === m.brand_id)?.handle ?? "") : "none";
    if (m?.brand_id) return brands.find((b) => b.id === m.brand_id)?.handle ?? "";
    return brands.find((b) => belongs(s.host, b))?.handle ?? "";
  };

  // Drafts start from what is saved, with a guess for sites that have no
  // brand yet; a guess is only kept once the person saves.
  useEffect(() => {
    if (!sites) return;
    setDrafts(Object.fromEntries(sites.map((s) => {
      const now = current(s);
      const guess = !now && brands.length > 0 ? guessBrand(s, brands) : undefined;
      const fresh = !now && !s.host;
      return [s.id, { sync: fresh && testCopy.test(s.name) ? false : s.sync, brand: now || guess?.handle || "", domain: "", suggested: !!guess }];
    })));
  }, [sites, mapped, brandKey]); // eslint-disable-line react-hooks/exhaustive-deps

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!sites) return <Loading />;
  if (sites.length === 0) return <p className="text-[13px] text-muted-foreground">{source.name} can't see any sites. Give the account you connected access in the tool, or connect another account.</p>;

  // The site's domain: one typed for it, else the brand's website when the
  // site has none yet or moves to a brand that doesn't own it, else its own.
  const hostFor = (s: AnalyticsSitePlan, d: Draft) => {
    const b = byHandle.get(d.brand);
    return d.domain || (b && !belongs(s.host, b) && (!s.host || d.brand !== current(s)) ? b.website : s.host);
  };
  const changed = sites.filter((s) => {
    const d = drafts[s.id];
    return d && (d.sync !== s.sync || (d.sync && (d.brand !== current(s) || hostFor(s, d) !== s.host)));
  });
  const missing = sites.filter((s) => drafts[s.id]?.sync && (brands.length ? !drafts[s.id].brand : !s.host && !drafts[s.id].domain)).length;
  const set = (id: string, patch: Partial<Draft>) => setDrafts({ ...drafts, [id]: { ...drafts[id], ...patch, suggested: patch.brand !== undefined ? false : drafts[id].suggested } });

  async function save() {
    setBusy(true);
    let failed = "";
    for (const s of changed) {
      const d = drafts[s.id];
      const patch = (body: { sync?: boolean; domain?: string }) => api.PATCH("/v1/workspaces/{ws}/analytics/{id}/sites/{site}", { params: { path: { ws, id: source.id, site: s.id } }, body });
      const pick = (brand: string) => api.PUT("/v1/workspaces/{ws}/analytics/{id}/site-brand", { params: { path: { ws, id: source.id } }, body: { site: s.id, brand } });
      let res: { error?: unknown } = {};
      if (!d.sync) {
        res = await patch({ sync: false });
      } else {
        const b = byHandle.get(d.brand);
        const host = hostFor(s, d);
        if (host !== s.host || d.sync !== s.sync) res = await patch({ sync: true, ...(host !== s.host ? { domain: host } : {}) });
        if (!res.error && d.brand && d.brand !== current(s)) {
          // A brand that owns the domain needs nothing more; otherwise the
          // choice is kept for this site.
          const byDomain = b && belongs(host, b);
          if (!byDomain || mapped[s.id]?.source === "set") res = await pick(byDomain ? "auto" : d.brand);
        }
      }
      if (res.error) failed = errorMessage(res.error);
    }
    setBusy(false);
    if (failed) return toast(failed, "error");
    const on = changed.filter((s) => drafts[s.id].sync).length;
    toast(on ? `Saved. ${on === 1 ? "That site's" : "Those sites'"} history arrives in a few minutes.` : "Saved.");
    setReload((n) => n + 1);
    onSaved?.();
  }

  return (
    <div className="space-y-3">
      <div className="overflow-x-auto rounded-lg border">
        <table className="w-full text-[13px]">
          <thead>
            <tr className="border-b text-left font-mono text-[11px] tracking-wide text-muted-foreground uppercase">
              <th className="w-10 px-3 py-2 font-medium"><span className="sr-only">Import</span></th>
              <th className="px-3 py-2 font-medium">Site in {source.name.replace(/\s*\(.*\)$/, "")}</th>
              <th className="px-3 py-2 font-medium">{brands.length ? "Brand" : "Website"}</th>
            </tr>
          </thead>
          <tbody>
            {sites.map((s) => {
              const d = drafts[s.id];
              if (!d) return null;
              const host = hostFor(s, d);
              return (
                <tr key={s.id} className={`border-b last:border-0 ${d.sync ? "" : "text-muted-foreground"}`}>
                  <td className="px-3 py-2 align-top">
                    <Checkbox aria-label={`Import ${s.name}`} checked={d.sync} disabled={!canManage || busy} onCheckedChange={(v) => set(s.id, { sync: v === true })} className="mt-1" />
                  </td>
                  <td className="px-3 py-2 align-top">
                    <div className="font-medium">{s.name}</div>
                    <div className="font-mono text-[11px] text-muted-foreground">{s.id}</div>
                  </td>
                  <td className="px-3 py-2 align-top">
                    {!d.sync ? (
                      <span className="text-[12px]">Not imported</span>
                    ) : brands.length > 0 ? (
                      <div className="space-y-1">
                        <div className="flex items-center gap-2">
                          <select aria-label={`Brand for ${s.name}`} className={`${field} w-48 ${!d.brand ? "border-amber-500/60" : ""}`} value={d.brand} disabled={!canManage || busy} onChange={(e) => set(s.id, { brand: e.target.value, domain: "" })}>
                            {!d.brand && <option value="">Choose a brand…</option>}
                            {brands.map((x) => <option key={x.id} value={x.handle}>{x.name}</option>)}
                            <option value="none">Shared by all brands</option>
                          </select>
                          {d.suggested && <span className="rounded bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground" title="Guessed from the site's name. Saving keeps it.">Suggested</span>}
                        </div>
                        {editing === s.id ? (
                          <DomainInput initial={host} onDone={(v) => { setEditing(""); if (v !== null) set(s.id, { domain: v }); }} />
                        ) : (
                          <div className="text-[12px] text-muted-foreground">
                            {host || "No website"}
                            {canManage && <button className="ml-1.5 underline underline-offset-2 hover:text-foreground" onClick={() => setEditing(s.id)}>{host ? "Change" : "Add"}</button>}
                          </div>
                        )}
                      </div>
                    ) : (
                      <DomainInput initial={d.domain || s.host} inline onDone={(v) => v !== null && set(s.id, { domain: v })} />
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div className={`flex flex-wrap items-center gap-3 ${compact ? "justify-end" : "justify-between"}`}>
        {!compact && (
          <p className="text-[12px] text-muted-foreground">
            {missing > 0 ? `${missing === 1 ? "1 site needs" : `${missing} sites need`} a brand. ` : ""}
            A site's numbers count for its brand and line up with the brand's other tools by its website.
          </p>
        )}
        <div className="flex gap-2">
          {onCancel && <Button variant="ghost" onClick={onCancel} disabled={busy}>Later</Button>}
          {canManage && (onCancel || changed.length > 0) && (
            <Button onClick={save} disabled={busy || changed.length === 0}>{busy ? "Saving…" : changed.length ? `Save ${changed.length === 1 ? "1 site" : `${changed.length} sites`}` : "Saved"}</Button>
          )}
        </div>
      </div>
    </div>
  );
}

function DomainInput({ initial, inline = false, onDone }: { initial: string; inline?: boolean; onDone: (v: string | null) => void }) {
  const [value, setValue] = useState(initial);
  const clean = hostOf(value.trim()) || value.trim().toLowerCase();
  return (
    <form className="flex gap-1.5" onSubmit={(e) => { e.preventDefault(); onDone(clean); }}>
      <input aria-label="Website" autoFocus={!inline} className={`${field} h-7 w-48`} placeholder="example.com" value={value} onChange={(e) => setValue(e.target.value)}
        onBlur={() => inline && clean !== initial && onDone(clean)} />
      {!inline && <Button variant="ghost" type="submit">Done</Button>}
      {!inline && <Button variant="ghost" type="button" onClick={() => onDone(null)}>Cancel</Button>}
    </form>
  );
}

// The step after connecting an analytics tool: what to import, and for
// which brand.
export function SiteSetupDialog({ ws, source, from, onClose }: { ws: string; source: Source; from: "connect" | "analytics"; onClose: (saved: boolean) => void }) {
  useEffect(() => {
    track("ui.analytics_sites_opened", { from, connector: source.connector });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const later = () => {
    track("ui.analytics_sites_skipped", { from, connector: source.connector });
    onClose(false);
  };
  return (
    <Dialog open onOpenChange={(o) => !o && later()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader className="text-left">
          <DialogTitle className="flex items-center gap-2.5 text-[15px]"><ConnectorLogo id={source.connector} />Choose what to import from {source.name}</DialogTitle>
          <DialogDescription className="text-[13px]">Tick the sites to store and pick the brand each belongs to. Test copies start unticked, and brands are guessed from the site names: check them and save.</DialogDescription>
        </DialogHeader>
        <div className="max-h-[60vh] overflow-y-auto">
          <SiteSetup ws={ws} source={source} canManage compact onSaved={() => onClose(true)} onCancel={later} />
        </div>
        <DialogFooter className="hidden" />
      </DialogContent>
    </Dialog>
  );
}

// After a connection comes back from its sign-in or key form: an analytics
// tool opens the import step.
export function useSiteSetupAfterConnect(ws: string) {
  const [source, setSource] = useState<Source | null>(null);
  async function after(id?: string | null) {
    if (!id) return;
    const { data } = await api.GET("/v1/workspaces/{ws}/integrations/{id}", { params: { path: { ws, id } } });
    if (data && data.category === "analytics") setSource({ id: data.id, name: data.name, connector: data.connector });
  }
  const dialog = source && <SiteSetupDialog ws={ws} source={source} from="connect" onClose={() => setSource(null)} />;
  return { after, dialog };
}
