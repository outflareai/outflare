// Choosing what an analytics connection imports: every site it can see
// (each GA4 property, PostHog project or Search Console property), whether
// to store it and which brand it belongs to. The brand's website becomes
// the site's domain, so its numbers line up with the brand's other tools
// without anyone typing a domain. Opens right after a tool is connected and
// from Analytics while any site has no brand. Every row ends in a brand,
// shared by all brands or not imported: nothing is guessed silently, and a
// site no brand fits can start a new brand.

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
// shared by every brand, or "" for not chosen yet. why says where a guess
// came from, shown beside it until the person picks.
type Draft = { sync: boolean; brand: string; domain: string; why: string };

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
  return guess(site, brands)?.brand;
}

// guess is guessBrand with the reason: the site's address or its name.
function guess(site: AnalyticsSitePlan, brands: Brand[]): { brand: Brand; why: string } | undefined {
  const host = site.host || hostOf(site.url);
  const byHost = brands.find((b) => belongs(host, b));
  if (byHost) return { brand: byHost, why: `Matches ${host}` };
  const text = norm(site.name.replace(/\s*\([^)]*\)\s*$/, ""));
  let best: Brand | undefined, score = 0, tie = false;
  for (const b of brands) {
    const keys = [b.name, b.handle, ...domainsOf(b).map((d) => d.split(".")[0])].map(norm).filter((k) => k.length >= 3);
    const s = Math.max(0, ...keys.filter((k) => text.includes(k)).map((k) => k.length));
    if (s > score) [best, score, tie] = [b, s, false];
    else if (s > 0 && s === score && b.id !== best?.id) tie = true;
  }
  return tie || !best ? undefined : { brand: best, why: "Name matches" };
}

export function SiteSetup({ ws, source, canManage, onSaved, onCancel, compact = false, focus }: {
  ws: string; source: Source; canManage: boolean; onSaved?: () => void; onCancel?: () => void; compact?: boolean;
  // A brand to pick sites for: the list shows its sites and the ones no
  // brand has yet.
  focus?: Brand;
}) {
  const { active: brands, reload: reloadBrands } = useBrand(ws);
  const [creating, setCreating] = useState("");
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
      // Every brand's sites: this is where each site gets one.
      api.GET("/v1/workspaces/{ws}/analytics/site-brands", { params: { path: { ws }, query: { integration: source.id, brand: "all" } } }),
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
      const g = !now && brands.length > 0 ? guess(s, brands) : undefined;
      const fresh = !now && !s.host;
      // A brand found by the site's domain says so too.
      const byDomain = now && now !== "none" && mapped[s.id]?.source !== "set" && s.host ? `Matches ${s.host}` : "";
      return [s.id, { sync: fresh && testCopy.test(s.name) ? false : s.sync, brand: now || g?.brand.handle || "", domain: "", why: g?.why ?? byDomain }];
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
  const set = (id: string, patch: Partial<Draft>) => setDrafts((all) => ({ ...all, [id]: { ...all[id], ...patch, why: patch.brand !== undefined ? "" : all[id].why } }));
  // Focused on one brand: its sites and the ones nobody has placed, never
  // another brand's.
  const shown = focus ? sites.filter((s) => { const now = current(s); return now === focus.handle || now === "" || drafts[s.id]?.brand === focus.handle; }) : sites;
  const others = sites.length - shown.length;

  async function newBrand(s: AnalyticsSitePlan, name: string, website: string) {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/brands", { params: { path: { ws } }, body: { name, website } });
    if (error || !data) return toast(errorMessage(error), "error");
    track("ui.analytics_site_brand_created", { connector: source.connector });
    await reloadBrands();
    setCreating("");
    set(s.id, { brand: data.handle, sync: true, domain: "" });
  }

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
            {shown.map((s) => {
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
                          <select aria-label={`Brand for ${s.name}`} className={`${field} w-48 ${!d.brand ? "border-amber-500/60" : ""}`} value={d.brand} disabled={!canManage || busy}
                            onChange={(e) => {
                              const v = e.target.value;
                              if (v === newOption) return setCreating(s.id);
                              if (v === skipOption) return set(s.id, { sync: false });
                              set(s.id, { brand: v, domain: "" });
                            }}>
                            {!d.brand && <option value="">Choose a brand…</option>}
                            {brands.map((x) => <option key={x.id} value={x.handle}>{x.name}</option>)}
                            <option value="none">Shared by all brands</option>
                            <option value={skipOption}>Don't import</option>
                            <option value={newOption}>New brand from this site…</option>
                          </select>
                          {d.why && <span className="rounded bg-muted px-1.5 py-0.5 text-[11px] whitespace-nowrap text-muted-foreground" title="Why this brand">{d.why}</span>}
                        </div>
                        {creating === s.id && <NewBrandForm site={s} onCreate={(name, website) => newBrand(s, name, website)} onCancel={() => setCreating("")} />}
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
      {others > 0 && focus && <p className="text-[12px] text-muted-foreground">{others === 1 ? "1 other site belongs" : `${others} other sites belong`} to other brands or every brand. Pick All brands in the sidebar to see them.</p>}
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
            <Button onClick={save} disabled={busy || changed.length === 0 || missing > 0} title={missing > 0 ? "Choose a brand, Shared by all brands or Don't import for every site" : undefined}>
              {busy ? "Saving…" : missing > 0 ? `${missing === 1 ? "1 site needs" : `${missing} sites need`} a choice` : changed.length ? `Save ${changed.length === 1 ? "1 site" : `${changed.length} sites`}` : "Saved"}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}

const newOption = "__new", skipOption = "__skip";

// A brand made from a site no brand fits: named after the site (without
// GA4's account in brackets), with the site's domain as its website.
function NewBrandForm({ site, onCreate, onCancel }: { site: AnalyticsSitePlan; onCreate: (name: string, website: string) => Promise<void>; onCancel: () => void }) {
  const [name, setName] = useState(site.name.replace(/\s*\([^)]*\)\s*$/, ""));
  const [website, setWebsite] = useState(site.host || hostOf(site.url));
  const [busy, setBusy] = useState(false);
  return (
    <form className="flex flex-wrap items-center gap-1.5 rounded-md border bg-muted/40 p-2" onSubmit={async (e) => { e.preventDefault(); setBusy(true); await onCreate(name.trim(), hostOf(website.trim()) || website.trim()); setBusy(false); }}>
      <input aria-label="Brand name" autoFocus className={`${field} h-7 w-36`} value={name} onChange={(e) => setName(e.target.value)} />
      <input aria-label="Brand website" className={`${field} h-7 w-40`} placeholder="example.com" value={website} onChange={(e) => setWebsite(e.target.value)} />
      <Button type="submit" disabled={busy || !name.trim()}>{busy ? "Creating…" : "Create brand"}</Button>
      <Button variant="ghost" type="button" onClick={onCancel}>Cancel</Button>
    </form>
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
export function SiteSetupDialog({ ws, source, from, focus, onClose }: { ws: string; source: Source; from: "connect" | "analytics" | "brand"; focus?: Brand; onClose: (saved: boolean) => void }) {
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
          <DialogTitle className="flex items-center gap-2.5 text-[15px]"><ConnectorLogo id={source.connector} />{focus ? `Pick ${focus.name}'s sites from ${source.name}` : `Choose what to import from ${source.name}`}</DialogTitle>
          <DialogDescription className="text-[13px]">
            {focus
              ? `Set ${focus.name} on the sites that belong to it. Sites other brands use stay as they are.`
              : "Give every site a brand, share it with all brands, or leave it out. Test copies start unticked, and suggestions say why they matched: check them and save. A site no brand fits can start a new brand."}
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-[60vh] overflow-y-auto">
          <SiteSetup ws={ws} source={source} canManage compact focus={focus} onSaved={() => onClose(true)} onCancel={later} />
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
