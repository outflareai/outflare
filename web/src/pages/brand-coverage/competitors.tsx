// Where the brand's competitors appear: each tracked competitor is scanned
// on a cadence (monthly unless changed), through
// the workspace's own search results and SEO connections, plus pages AI
// answers cite when they name it. Gaps are sites where the brand has no
// publisher yet.
import { useEffect, useState } from "react";
import { api, errorMessage } from "../../api";
import type { components } from "@outflare/sdk";
import { appPath } from "../../lib/paths";
import { Badge, Button, ErrorText, field, toast } from "../../ui";
import { label } from "./client";
import { AccessBadge, AccessSelect, Reasons, ValueCell, type Access } from "./linkvalue";

type Competitor = components["schemas"]["CoverageCompetitor"];
type Settings = components["schemas"]["CoverageCompetitorSettings"];
type Page = components["schemas"]["CoverageCompetitorPlacement"];

const sourceNames: Record<string, string> = { web_search: "Web search", backlinks: "Backlinks", ai_citations: "AI answers" };
const needs: Record<string, string> = {
  web_search: "needs a search results connection",
  backlinks: "needs an SEO connection that reads links",
};
const evidenceNames: Record<string, string> = {
  link_sponsored: "the link is marked sponsored",
  link_affiliate: "the link carries an affiliate code",
  address_sponsored: "the address says sponsored",
  address_advertorial: "the address says advertorial",
  address_partner_content: "the address says partner content",
  address_paid_post: "the address says paid post",
  address_promoted: "the address says promoted",
  title_sponsored: "the title says sponsored",
  title_advertorial: "the title says advertorial",
  title_paid_post: "the title says paid post",
  title_partner_content: "the title says partner content",
  title_promoted: "the title says promoted",
};
const why = (e: string) => evidenceNames[e] ?? e.replaceAll("_", " ");
const cadence = (n: number) => (n === 7 ? "Weekly" : n === 30 ? "Monthly" : n === 1 ? "Daily" : `Every ${n} days`);
const number = (n: number | null | undefined) => (n == null ? "–" : Math.round(n).toLocaleString());

export function CompetitorPresence({ ws, brand, manage }: { ws: string; brand: string; manage: boolean }) {
  const [settings, setSettings] = useState<Settings>(),
    [rivals, setRivals] = useState<Competitor[]>([]),
    [pages, setPages] = useState<Page[]>([]),
    [total, setTotal] = useState(0),
    [rival, setRival] = useState(""),
    [gap, setGap] = useState(true),
    [paid, setPaid] = useState(false),
    [access, setAccess] = useState<Access | "">(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [reload, setReload] = useState(0);
  useEffect(() => {
    let active = true;
    api.GET("/v1/brands/{brand}/coverage/competitors", { params: { path: { brand } } }).then(({ data, error }) => {
      if (!active) return;
      if (error) return setError(errorMessage(error));
      setSettings(data.settings);
      setRivals(data.data);
    });
    return () => {
      active = false;
    };
  }, [brand, reload]);
  useEffect(() => {
    let active = true;
    api
      .GET("/v1/brands/{brand}/coverage/competitor_placements", {
        params: {
          path: { brand },
          query: {
            limit: 100,
            ...(rival ? { competitor_id: rival } : {}),
            ...(gap ? { gap: "true" as const } : {}),
            ...(paid ? { arrangement: "likely_paid" as const } : {}),
            ...(access ? { access } : {}),
          },
        },
      })
      .then(({ data, error }) => {
        if (!active) return;
        if (error) return setError(errorMessage(error));
        setPages(data.data);
        setTotal(data.total);
      });
    return () => {
      active = false;
    };
  }, [brand, rival, gap, paid, access, reload]);
  const act = async (fn: () => Promise<{ error?: unknown }>, done: string) => {
    setBusy(true);
    setError("");
    const { error } = await fn();
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setReload((n) => n + 1);
    toast(done);
  };
  const saveSettings = (patch: Partial<Settings>) =>
    act(() => api.PATCH("/v1/brands/{brand}/coverage/competitors/settings", { params: { path: { brand } }, body: patch }), "Scan settings saved");
  return (
    <div className="space-y-6">
      <ErrorText>{error}</ErrorText>
      <section className="space-y-3 rounded-xl border p-4">
        <div>
          <h2 className="font-medium">How often competitors are scanned</h2>
          <p className="text-sm text-muted-foreground">
            Each scan reads Google results for the competitor's name and who links to its site, through your own{" "}
            <a className="text-brand underline" href={appPath(ws, "connectors")}>
              DataForSEO, Geonode or Ahrefs connection
            </a>{" "}
            and within its budget, plus pages AI answers cite when they name it. Competitors come from the{" "}
            <a className="text-brand underline" href={appPath(ws, "competitors")}>
              Competitors page
            </a>
            , ranked by the keywords you share.
          </p>
        </div>
        {settings && (
          <div className="flex flex-wrap items-end gap-3 text-sm">
            <label className="grid gap-1">
              Top competitors
              <input
                type="number"
                min={0}
                max={1000}
                className={`${field} !w-24`}
                defaultValue={settings.top_competitors}
                disabled={!manage || busy}
                onBlur={(e) => Number(e.target.value) !== settings.top_competitors && saveSettings({ top_competitors: Number(e.target.value) })}
              />
            </label>
            <label className="grid gap-1">
              Top ones
              <Every value={settings.top_every_days} disabled={!manage || busy} onChange={(n) => saveSettings({ top_every_days: n })} />
            </label>
            <label className="grid gap-1">
              The rest
              <Every value={settings.rest_every_days} disabled={!manage || busy} onChange={(n) => saveSettings({ rest_every_days: n })} />
            </label>
          </div>
        )}
        {rivals.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No tracked competitors yet. Track some on the{" "}
            <a className="text-brand underline" href={appPath(ws, "competitors")}>
              Competitors page
            </a>
            .
          </p>
        ) : (
          <div className="divide-y text-sm">
            {rivals.map((c) => (
              <div key={c.id} className="flex flex-wrap items-center gap-3 py-2">
                <div className="min-w-0 flex-1">
                  <strong>{c.name || c.host}</strong> <span className="text-muted-foreground">{c.host}</span>{" "}
                  {c.top && <Badge tone="blue">Top</Badge>}
                  <p className="text-muted-foreground">
                    {cadence(c.every_days)}
                    {c.own_every_days == null ? "" : " (its own)"}
                    {c.scanned_at ? ` · scanned ${new Date(c.scanned_at).toLocaleDateString()}` : " · not scanned yet"}
                    {` · on ${c.placements ?? 0} sites, ${c.likely_paid ?? 0} likely paid`}
                  </p>
                  {Object.entries(c.outcome)
                    .filter(([, code]) => code)
                    .map(([source, code]) => (
                      <p key={source} className="text-amber-500">
                        {sourceNames[source] ?? source}: {code === "connector_needed" ? needs[source] ?? label(code) : label(code)}
                        {code === "connector_needed" && (
                          <>
                            {" "}
                            ·{" "}
                            <a className="underline" href={appPath(ws, "connectors")}>
                              Connect one
                            </a>
                          </>
                        )}
                      </p>
                    ))}
                </div>
                {manage && (
                  <>
                    <select
                      className={`${field} !w-auto`}
                      aria-label={`How often to scan ${c.host}`}
                      value={c.own_every_days ?? ""}
                      disabled={busy}
                      onChange={(e) =>
                        act(
                          () =>
                            api.PATCH("/v1/brands/{brand}/coverage/competitors/{id}", {
                              params: { path: { brand, id: c.id } },
                              body: { every_days: e.target.value ? Number(e.target.value) : null },
                            }),
                          "Cadence saved",
                        )
                      }
                    >
                      <option value="">Brand setting</option>
                      <option value={7}>Weekly</option>
                      <option value={30}>Monthly</option>
                    </select>
                    <Button
                      variant="ghost"
                      disabled={busy}
                      onClick={() =>
                        act(
                          () => api.POST("/v1/brands/{brand}/coverage/competitors/{id}/scan", { params: { path: { brand, id: c.id } } }),
                          "Scan queued; its pages appear below when it's done",
                        )
                      }
                    >
                      Scan now
                    </Button>
                  </>
                )}
              </div>
            ))}
          </div>
        )}
      </section>
      <section className="space-y-3">
        <div className="flex flex-wrap items-center gap-3 text-sm">
          <h2 className="mr-auto font-medium">Where competitors appear · {total}</h2>
          <select className={`${field} !w-auto`} aria-label="Competitor" value={rival} onChange={(e) => setRival(e.target.value)}>
            <option value="">All competitors</option>
            {rivals.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name || c.host}
              </option>
            ))}
          </select>
          <AccessSelect className={`${field} !w-auto`} value={access} onChange={setAccess} />
          <label className="flex items-center gap-1.5">
            <input type="checkbox" checked={gap} onChange={(e) => setGap(e.target.checked)} />
            Only sites we're not on
          </label>
          <label className="flex items-center gap-1.5">
            <input type="checkbox" checked={paid} onChange={(e) => setPaid(e.target.checked)} />
            Only likely paid
          </label>
        </div>
        <div className="overflow-x-auto rounded-xl border">
          <table className="w-full text-sm">
            <thead className="text-left text-muted-foreground">
              <tr className="border-b">
                <th className="p-2 font-normal">Page</th>
                <th className="p-2 text-right font-normal" title="What the link is worth to you, 0 to 100: the page's authority, readers, trust, relevance to your topic and how many competitors it features, weighted by how it's got">
                  Value
                </th>
                <th className="p-2 font-normal">How it's got</th>
                <th className="p-2 font-normal">Competitor</th>
                <th className="p-2 font-normal">Type</th>
                <th className="p-2 font-normal">Paid?</th>
                <th className="p-2 text-right font-normal">Domain rating</th>
                <th className="p-2 text-right font-normal">Traffic</th>
                <th className="p-2 font-normal">Found by</th>
              </tr>
            </thead>
            <tbody>
              {pages.map((p) => (
                <tr key={p.id} className="border-b last:border-0">
                  <td className="max-w-80 p-2">
                    <a className="block truncate text-brand underline" href={p.url} target="_blank" rel="noreferrer noopener">
                      {p.title || p.url}
                    </a>
                    <span className="text-muted-foreground">{p.host}</span> {p.brand_present && <Badge tone="green">We're there</Badge>}
                    <Reasons reasons={p.value_reasons} />
                  </td>
                  <td className="p-2 text-right">
                    <ValueCell value={p.value} reasons={p.value_reasons} />
                  </td>
                  <td className="p-2">
                    <AccessBadge access={p.access} />
                  </td>
                  <td className="p-2">{p.competitor_name || p.competitor_host}</td>
                  <td className="p-2">{p.kind ? label(p.kind) : "–"}</td>
                  <td className="p-2">
                    {p.arrangement === "likely_paid" ? (
                      <Badge tone="amber" title={`Because ${p.evidence.map(why).join(", ")}`}>
                        Likely paid
                      </Badge>
                    ) : (
                      <span className="text-muted-foreground">Unknown</span>
                    )}
                  </td>
                  <td className="p-2 text-right tabular-nums">{number(p.domain_rating)}</td>
                  <td className="p-2 text-right tabular-nums">{number(p.domain_traffic)}</td>
                  <td className="p-2">{p.sources.map((s) => sourceNames[s] ?? s).join(", ")}</td>
                </tr>
              ))}
              {pages.length === 0 && (
                <tr>
                  <td colSpan={9} className="p-4 text-center text-muted-foreground">
                    Nothing found yet. Scans run on their schedule, or select Scan now.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

function Every({ value, disabled, onChange }: { value: number; disabled: boolean; onChange: (n: number) => void }) {
  const options = [1, 7, 14, 30, 60, 90];
  return (
    <select className={`${field} !w-auto`} value={value} disabled={disabled} onChange={(e) => onChange(Number(e.target.value))}>
      {(options.includes(value) ? options : [...options, value]).map((n) => (
        <option key={n} value={n}>
          {cadence(n)}
        </option>
      ))}
    </select>
  );
}
