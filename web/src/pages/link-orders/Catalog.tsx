import { Fragment, useMemo, useState } from "react";
import { ContextMenu, ContextMenuContent, ContextMenuItem, ContextMenuLabel, ContextMenuSeparator, ContextMenuTrigger } from "@/components/ui/context-menu";
import { api, errorMessage } from "../../api";
import { Badge, Button, Card, Empty, ErrorText, field, FilePicker, toast } from "../../ui";
import {
  catalogExampleCSV,
  dash,
  durationLabel,
  idemp,
  metricFields,
  moneyList,
  type LinkOrderDonor,
  type PageData,
} from "./shared";
import { csvBatches } from "./csvBatches";
import { emptyRequestTarget, RequestTarget, requestTargetBody, type RequestTargetValue } from "./RequestTarget";
import { useTargets } from "./targets";
import { catalogCellActions, cellFromEvent, directContactReason, outreachActions, type CatalogActionHost, type CatalogCellContext } from "./catalogActions";
import { FindEmailDialog, SendToSequenceDialog } from "./DonorOutreach";

type Filters = {
  q: string;
  intermediary: string;
  country: string;
  lang: string;
  niche: string;
  drMin: string;
  priceMax: string;
  rel: string;
  duration: string;
  casino: string;
  pbn: "no" | "yes" | "any";
  direct: "any" | "yes" | "no";
};

const defaults: Filters = { q: "", intermediary: "", country: "", lang: "", niche: "", drMin: "", priceMax: "", rel: "", duration: "", casino: "", pbn: "no", direct: "any" };

export function Catalog({ data, cart, setCart, onOpenDonor }: {
  data: PageData;
  cart: string[];
  setCart: (ids: string[]) => void;
  onOpenDonor: (d: LinkOrderDonor) => void;
}) {
  const [f, setF] = useState<Filters>(defaults);
  const [importOpen, setImportOpen] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState("");
  const [target, setTarget] = useState<RequestTargetValue>(emptyRequestTarget);
  const { suggestions } = useTargets(data.brand, 0);
  const [addOpen, setAddOpen] = useState(false);
  const emptyAdd = { domain: "", name: "", intermediary: "", theme: "", siteAge: "", pbn: false, country: "", niches: "", amount: "", currency: "USD" as "USD" | "EUR" };
  const [add, setAdd] = useState(emptyAdd);
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null);
  const [summary, setSummary] = useState<ImportSummary | null>(null);
  // The cell the menu was opened on, and the dialogs its actions open.
  const [cell, setCell] = useState<CatalogCellContext | null>(null);
  const [findFor, setFindFor] = useState<LinkOrderDonor | undefined>();
  const [sendFor, setSendFor] = useState<{ donor: LinkOrderDonor; email: string; name: string } | undefined>();
  const host: CatalogActionHost = {
    openDonor: onOpenDonor,
    findEmail: (d) => setFindFor(d),
    sendToSequence: (d) => setSendFor({ donor: d, email: "", name: "" }),
  };
  const canSend = data.canManage && data.can("sequences.manage");

  const countries = [...new Set(data.donors.map((d) => d.country).filter(Boolean))].sort();
  const niches = [...new Set(data.donors.flatMap((d) => d.niches))].sort();
  const langs = [...new Set(data.donors.flatMap((d) => d.languages))].sort();
  const intermediaries = [...new Set(data.donors.map((d) => d.intermediary).filter(Boolean))].sort((a, b) => a.localeCompare(b));

  const list = useMemo(() => {
    return data.donors.filter((d) => {
      if (f.q && !`${d.domain} ${d.niches.join(" ")} ${d.intermediary} ${d.theme}`.toLowerCase().includes(f.q.toLowerCase())) return false;
      if (f.intermediary && d.intermediary.toLowerCase() !== f.intermediary.toLowerCase()) return false;
      if (f.country && d.country !== f.country) return false;
      if (f.lang && !d.languages.includes(f.lang)) return false;
      if (f.niche && !d.niches.includes(f.niche)) return false;
      if (f.drMin && (d.domain_rating == null || d.domain_rating < Number(f.drMin))) return false;
      if (f.priceMax) {
        const cap = Number(f.priceMax);
        const ok = d.prices.some((p) => p.amount_minor / 100 <= cap);
        if (!ok) return false;
      }
      if (f.rel && !d.rel_offered.includes(f.rel)) return false;
      if (f.duration && d.duration !== f.duration) return false;
      if (f.casino === "yes" && !d.casino_allowed) return false;
      if (f.casino === "no" && d.casino_allowed) return false;
      if (f.pbn === "no" && d.pbn) return false;
      if (f.pbn === "yes" && !d.pbn) return false;
      if (f.direct === "yes" && !d.direct_contact.recommended) return false;
      if (f.direct === "no" && d.direct_contact.recommended) return false;
      return true;
    });
  }, [data.donors, f]);

  const pbnN = list.filter((d) => d.pbn).length;
  const directN = list.filter((d) => d.direct_contact.recommended).length;

  const toggleCart = (id: string) => {
    setCart(cart.includes(id) ? cart.filter((x) => x !== id) : [...cart, id]);
  };

  const requestQuotes = async () => {
    if (!cart.length || !data.canManage) return;
    const body = requestTargetBody(target);
    if (!body.target_url && !body.target_id) {
      setErr("Tick a link target or type a page for the request.");
      return;
    }
    setBusy("request");
    setErr("");
    try {
      for (const id of cart) {
        const d = data.donors.find((x) => x.id === id);
        const { error } = await api.POST("/v1/brands/{brand}/link-orders", {
          params: { path: { brand: data.brand }, header: idemp() },
          body: { ...body, donor_profile_id: id, slot_type: d?.slot_types[0] || "guest_post", rel_requested: d?.rel_offered[0] || "dofollow" },
        });
        if (error) throw error;
      }
      toast(`Opened ${cart.length} request${cart.length === 1 ? "" : "s"} from the catalog.`);
      setCart([]);
      data.reload();
      data.goSection("orders");
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy("");
    }
  };

  // A large file (a broker's full database is 11,000+ sites) goes up in
  // batches of about 1 MB, each with the header row, one after another.
  // Rows upsert by domain, so running the import again after a failed
  // batch is safe.
  const importCSV = async () => {
    if (!file || !data.canManage) return;
    setBusy("import");
    setErr("");
    setSummary(null);
    const total: ImportSummary = { created: 0, updated: 0, errors: [], ignored: [], batches: 0, failedBatch: null };
    try {
      const batches = csvBatches(await file.text());
      if (!batches.length) throw new Error("The file is empty.");
      setProgress({ done: 0, total: batches.length });
      for (const [i, batch] of batches.entries()) {
        const { data: result, error } = await api.POST("/v1/brands/{brand}/link-orders/catalog/import", {
          params: { path: { brand: data.brand }, header: idemp() },
          body: batch.csv,
          bodySerializer: (b: string | { csv: string }) => (typeof b === "string" ? b : b.csv),
          headers: { "Content-Type": "text/csv" },
        });
        if (error || !result) {
          total.failedBatch = { index: i + 1, total: batches.length, message: errorMessage(error) };
          break;
        }
        total.created += result.created;
        total.updated += result.updated;
        total.errors.push(...result.errors.map((e) => ({ ...e, row: e.row + batch.offset })));
        if (i === 0) total.ignored = result.ignored_columns ?? [];
        total.batches = i + 1;
        setProgress({ done: i + 1, total: batches.length });
      }
      const n = total.created + total.updated;
      if (total.failedBatch) {
        setErr(`Batch ${total.failedBatch.index} of ${total.failedBatch.total} failed: ${total.failedBatch.message} ${n} row${n === 1 ? " was" : "s were"} imported before it. Import the same file again to finish; rows already in the catalog are updated, not duplicated.`);
      } else {
        toast(total.errors.length ? `Imported ${n} row${n === 1 ? "" : "s"}, ${total.errors.length} with errors.` : `Imported ${n} row${n === 1 ? "" : "s"}.`);
        setFile(null);
      }
      setSummary(total);
      if (n) data.reload();
    } catch (e) {
      setErr(e instanceof Error && !("error" in e) ? e.message : errorMessage(e));
    } finally {
      setBusy("");
      setProgress(null);
    }
  };

  const addDonor = async () => {
    if (!data.canManage) return;
    const domain = add.domain.trim();
    if (!domain) {
      setErr("Domain is required.");
      return;
    }
    setBusy("add");
    setErr("");
    try {
      const body: componentsWrite = {
        domain,
        name: add.name.trim() || undefined,
        intermediary: add.intermediary.trim() || undefined,
        theme: add.theme.trim() || undefined,
        site_age: add.siteAge.trim() || undefined,
        pbn: add.pbn,
        country: add.country.trim() || null,
        niches: add.niches ? add.niches.split(",").map((s) => s.trim()).filter(Boolean) : undefined,
      };
      const minor = add.amount.trim() ? Math.round(Number(add.amount) * 100) : null;
      if (minor != null && Number.isFinite(minor) && minor >= 0) {
        body.prices = [{ amount_minor: minor, currency: add.currency }];
      }
      const { error } = await api.POST("/v1/brands/{brand}/link-orders/catalog", {
        params: { path: { brand: data.brand }, header: idemp() },
        body,
      });
      if (error) throw error;
      toast(`Added ${domain} to the catalog.`);
      setAddOpen(false);
      setAdd(emptyAdd);
      data.reload();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy("");
    }
  };

  // The export reads back in as it is: the same column names the import maps.
  const exportCSV = () => {
    const header = ["domain", "intermediary", "theme", "site_since", "pbn", "country", "languages", "niches", "casino_allowed", "rel_offered", "slot_types", "duration", "tat_days", "guarantee_days", "domain_rating", "traffic", "amount_minor", "currency", ...metricFields.map((m) => m.key), "notes"];
    const lines = [header.join(",")];
    for (const d of list) {
      const price = d.prices[0];
      const cells = [
        d.domain,
        d.intermediary,
        d.theme,
        d.site_since ?? "",
        d.pbn ? "true" : "false",
        d.country,
        d.languages.join(";"),
        d.niches.join(";"),
        d.casino_allowed ? "true" : "false",
        d.rel_offered.join(";"),
        d.slot_types.join(";"),
        d.duration,
        d.tat_days ?? "",
        d.guarantee_days ?? "",
        d.domain_rating ?? "",
        d.traffic ?? "",
        price?.amount_minor ?? "",
        price?.currency ?? "",
        ...metricFields.map((m) => d.metrics?.[m.key] ?? ""),
        d.notes,
      ];
      lines.push(cells.map((c) => /[",\n\r]/.test(String(c)) ? `"${String(c).replaceAll('"', '""')}"` : String(c)).join(","));
    }
    const blob = new Blob([lines.join("\n")], { type: "text/csv" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "link-order-catalog.csv";
    a.click();
    URL.revokeObjectURL(a.href);
  };

  return (
    <div className="flex flex-col gap-3">
      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Search
          <input className={field} value={f.q} placeholder="Site, niche, intermediary or theme" onChange={(e) => setF({ ...f, q: e.target.value })} />
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Intermediary
          <select className={field} value={f.intermediary} onChange={(e) => setF({ ...f, intermediary: e.target.value })}>
            <option value="">Any</option>
            {intermediaries.map((c) => <option key={c}>{c}</option>)}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Country
          <select className={field} value={f.country} onChange={(e) => setF({ ...f, country: e.target.value })}>
            <option value="">Any</option>
            {countries.map((c) => <option key={c}>{c}</option>)}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Language
          <select className={field} value={f.lang} onChange={(e) => setF({ ...f, lang: e.target.value })}>
            <option value="">Any</option>
            {langs.map((c) => <option key={c}>{c}</option>)}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Niche
          <select className={field} value={f.niche} onChange={(e) => setF({ ...f, niche: e.target.value })}>
            <option value="">Any</option>
            {niches.map((c) => <option key={c}>{c}</option>)}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Min DR
          <input className={field} type="number" min={0} max={100} value={f.drMin} onChange={(e) => setF({ ...f, drMin: e.target.value })} />
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Max price
          <input className={field} type="number" min={0} value={f.priceMax} onChange={(e) => setF({ ...f, priceMax: e.target.value })} />
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Rel
          <select className={field} value={f.rel} onChange={(e) => setF({ ...f, rel: e.target.value })}>
            <option value="">Any</option>
            <option value="dofollow">Dofollow</option>
            <option value="sponsored">Sponsored</option>
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Duration
          <select className={field} value={f.duration} onChange={(e) => setF({ ...f, duration: e.target.value })}>
            <option value="">Any</option>
            <option value="permanent">Lifetime</option>
            <option value="limited">Time-limited</option>
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Casino / gambling
          <select className={field} value={f.casino} onChange={(e) => setF({ ...f, casino: e.target.value })}>
            <option value="">Any</option>
            <option value="no">Not allowed</option>
            <option value="yes">Allowed</option>
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          PBN
          <select className={field} value={f.pbn} onChange={(e) => setF({ ...f, pbn: e.target.value as Filters["pbn"] })}>
            <option value="no">Exclude PBN</option>
            <option value="any">Any</option>
            <option value="yes">PBN only</option>
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Direct contact
          <select className={field} value={f.direct} onChange={(e) => setF({ ...f, direct: e.target.value as Filters["direct"] })}>
            <option value="any">Any</option>
            <option value="yes">Recommended only</option>
            <option value="no">Not recommended</option>
          </select>
        </label>
      </div>

      {data.canManage ? <RequestTarget brand={data.brand} suggestions={suggestions} value={target} onChange={setTarget} /> : null}
      <div className="flex flex-wrap items-end gap-2">
        {data.canManage ? (
          <>
            <Button variant="ghost" onClick={() => setAddOpen(true)}>Add donor</Button>
            <Button variant="ghost" onClick={() => { setImportOpen(true); setErr(""); setSummary(null); }}>Import CSV</Button>
            <Button variant="ghost" onClick={exportCSV}>Export CSV</Button>
            <Button variant="ghost" disabled={!cart.length} onClick={() => setCart([])}>Clear</Button>
            <Button disabled={!cart.length || busy === "request"} onClick={() => void requestQuotes()}>
              Request quotes{cart.length ? ` (${cart.length})` : ""}
            </Button>
          </>
        ) : null}
      </div>
      {err ? <ErrorText>{err}</ErrorText> : null}

      {importOpen ? (
        <Card title="Import CSV" description="Import a CSV file, such as an intermediary's full database. The first row is the header and must include a domain column (domain, website, site or url); headers are matched ignoring case and punctuation, so Traffic / Organic is traffic. Intermediary, theme, site age and the intermediary's metrics (referring domains, backlinks, keywords, outgoing links and more) are kept on the donor. A large file is sent in batches of about 1 MB. price is the donor price in dollars or euros; amount_minor is in cents.">
          <FilePicker file={file} onChange={(f) => { setFile(f); setSummary(null); }} accept=".csv,text/csv" example={{ name: "link-order-catalog-example.csv", content: catalogExampleCSV }} />
          {progress ? (
            <div className="mt-3 flex flex-col gap-1 text-[12px] text-muted-foreground" role="status">
              <span>Importing batch {Math.min(progress.done + 1, progress.total)} of {progress.total}…</span>
              <div className="h-1.5 w-full max-w-sm overflow-hidden rounded bg-muted">
                <div className="h-full bg-primary transition-all" style={{ width: `${Math.round((progress.done / progress.total) * 100)}%` }} />
              </div>
            </div>
          ) : null}
          {summary ? <ImportSummaryView summary={summary} /> : null}
          <div className="mt-3 flex gap-2">
            <Button disabled={!file || busy === "import"} onClick={() => void importCSV()}>{busy === "import" ? "Importing…" : "Import"}</Button>
            <Button variant="ghost" disabled={busy === "import"} onClick={() => { setImportOpen(false); setFile(null); setSummary(null); }}>{summary ? "Close" : "Cancel"}</Button>
          </div>
        </Card>
      ) : null}

      {addOpen ? (
        <Card title="Add a donor" description="Prices are what we pay the donor — there is no commission or client price. PBN is a manual flag.">
          <div className="grid gap-2 sm:grid-cols-2">
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Domain
              <input className={field} value={add.domain} onChange={(e) => setAdd({ ...add, domain: e.target.value })} placeholder="example.com" />
            </label>
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Name
              <input className={field} value={add.name} onChange={(e) => setAdd({ ...add, name: e.target.value })} />
            </label>
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Intermediary
              <input className={field} value={add.intermediary} onChange={(e) => setAdd({ ...add, intermediary: e.target.value })} placeholder="Broker or contact" />
            </label>
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Theme
              <input className={field} value={add.theme} onChange={(e) => setAdd({ ...add, theme: e.target.value })} />
            </label>
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Site age
              <input className={field} value={add.siteAge} onChange={(e) => setAdd({ ...add, siteAge: e.target.value })} placeholder="2012, 2012-05-01 or 12 years" />
            </label>
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Country
              <input className={field} value={add.country} onChange={(e) => setAdd({ ...add, country: e.target.value })} />
            </label>
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Niches
              <input className={field} value={add.niches} onChange={(e) => setAdd({ ...add, niches: e.target.value })} placeholder="proxies, saas" />
            </label>
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Donor price
              <input className={field} type="number" min={0} step="0.01" value={add.amount} onChange={(e) => setAdd({ ...add, amount: e.target.value })} />
            </label>
            <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">Currency
              <select className={field} value={add.currency} onChange={(e) => setAdd({ ...add, currency: e.target.value as "USD" | "EUR" })}>
                <option>USD</option>
                <option>EUR</option>
              </select>
            </label>
            <label className="flex items-center gap-2 text-[13px] text-foreground">
              <input type="checkbox" checked={add.pbn} onChange={(e) => setAdd({ ...add, pbn: e.target.checked })} />
              Flag as PBN
            </label>
          </div>
          <div className="mt-3 flex gap-2">
            <Button disabled={busy === "add"} onClick={() => void addDonor()}>Save donor</Button>
            <Button variant="ghost" onClick={() => setAddOpen(false)}>Cancel</Button>
          </div>
        </Card>
      ) : null}

      <Card title={<span>Donor catalog · {list.length} site{list.length === 1 ? "" : "s"}{pbnN ? ` · ${pbnN} PBN` : ""}{directN ? ` · ${directN} direct contact` : ""}</span>} description="Prices are what we pay the donor — there is no commission or client price. Import is a CSV file; a Google Sheet is not imported. DR, traffic and the other metrics are the latest imported or stored values, never a fresh paid read. Open a site for its full metrics.">
        {list.length ? (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[800px] text-left text-[13px]">
              <thead className="text-muted-foreground">
                <tr className="border-b">
                  <th className="py-2 pr-3 font-medium">{data.canManage ? "Request" : ""}</th>
                  <th className="py-2 pr-3 font-medium">Site</th>
                  <th className="py-2 pr-3 font-medium">DR</th>
                  <th className="py-2 pr-3 font-medium">Traffic</th>
                  <th className="py-2 pr-3 font-medium">Niche</th>
                  <th className="py-2 pr-3 font-medium">Intermediary</th>
                  <th className="py-2 pr-3 font-medium">Rel</th>
                  <th className="py-2 pr-3 font-medium">Duration</th>
                  <th className="py-2 pr-3 text-right font-medium">Donor price</th>
                </tr>
              </thead>
              <ContextMenu modal={false} onOpenChange={(o) => { if (!o) setCell(null); }}>
              <ContextMenuTrigger asChild>
              <tbody
                aria-label="Catalog sites. Right-click a cell, or press the context-menu key, for actions."
                onContextMenu={(e) => {
                  // Only a site's cell opens the app's menu; it replaces the
                  // browser's menu there (Radix prevents the default).
                  const hit = cellFromEvent(e.target);
                  const donor = hit && list.find((x) => x.id === hit.donorId);
                  if (!hit || !donor) {
                    e.preventDefault();
                    return;
                  }
                  setCell({ donor, column: hit.column, data });
                }}
              >
                {list.map((d) => (
                  <tr key={d.id} data-donor={d.id} className="border-b last:border-0">
                    <td data-col="request" className="py-2 pr-3">
                      {data.canManage ? (
                        <input type="checkbox" checked={cart.includes(d.id)} onChange={() => toggleCart(d.id)} aria-label={`Add ${d.domain} to the request`} />
                      ) : null}
                    </td>
                    <td data-col="site" className="py-2 pr-3">
                      <button type="button" className="text-left font-medium hover:underline" onClick={() => onOpenDonor(d)}>{d.domain}</button>
                      <div className="flex flex-wrap gap-1">
                        {d.pbn ? <Badge tone="amber">PBN</Badge> : null}
                        {d.direct_contact.recommended ? <Badge tone="green" title={directContactReason(d)}>Direct contact</Badge> : null}
                        {d.country ? <span className="text-[12px] text-muted-foreground">{d.country}</span> : null}
                      </div>
                      {d.direct_contact.recommended ? (
                        <div className="mt-1 flex flex-wrap gap-2" data-direct-actions>
                          {outreachActions.map((a) => {
                            const reason = a.disabledReason({ donor: d, column: "site", data });
                            return (
                              <button key={a.id} type="button" data-action={a.id} disabled={!!reason} title={reason ?? undefined}
                                className="text-[12px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline disabled:cursor-not-allowed disabled:opacity-60 disabled:hover:no-underline"
                                onClick={() => a.run({ donor: d, column: "site", data }, host)}>
                                {a.label}
                              </button>
                            );
                          })}
                        </div>
                      ) : null}
                    </td>
                    <td data-col="dr" className="py-2 pr-3 tabular-nums">{d.domain_rating ?? dash}</td>
                    <td data-col="traffic" className="py-2 pr-3 tabular-nums">{d.traffic == null ? dash : d.traffic.toLocaleString()}</td>
                    <td data-col="niche" className="py-2 pr-3">{d.niches.join(", ") || dash}</td>
                    <td data-col="intermediary" className="py-2 pr-3">{d.intermediary || dash}</td>
                    <td data-col="rel" className="py-2 pr-3">{d.rel_offered.join(", ") || dash}</td>
                    <td data-col="duration" className="py-2 pr-3">{durationLabel(d.duration)}</td>
                    <td data-col="price" className="py-2 pr-3 text-right tabular-nums">{moneyList(d.prices)}</td>
                  </tr>
                ))}
              </tbody>
              </ContextMenuTrigger>
              <ContextMenuContent className="min-w-56" aria-label={cell ? `Actions for ${cell.donor.domain}` : "Actions"}>
                {cell ? <CellMenu cell={cell} host={host} /> : null}
              </ContextMenuContent>
              </ContextMenu>
            </table>
          </div>
        ) : (
          <Empty title="No donors match these filters">{data.donors.length ? "Clear a filter, or include PBN, to see more of the catalog." : "Add a donor or import a CSV to start the catalog."}</Empty>
        )}
        <p className="mt-2 text-[12px] text-muted-foreground">
          Right-click a cell for actions such as Find email and Send to a sequence. Direct contact marks a site worth writing to directly, not only through its intermediary: not a PBN, DR or traffic stored, and an email at the site already stored. It is a suggestion; nothing is sent until you send an address to a sequence. The default catalog filter excludes PBN. PBN is a manual flag; candidates from a competitor gap are not tagged as PBN on their own.
        </p>
      </Card>
      <FindEmailDialog
        brand={data.brand}
        donor={findFor}
        canSend={canSend}
        onClose={() => setFindFor(undefined)}
        onSend={(d, email, name) => { setFindFor(undefined); setSendFor({ donor: d, email, name }); }}
      />
      <SendToSequenceDialog brand={data.brand} target={sendFor} onClose={() => setSendFor(undefined)} />
    </div>
  );
}

// The menu for one cell: every action in catalogCellActions, grouped, with
// a disabled action saying why.
function CellMenu({ cell, host }: { cell: CatalogCellContext; host: CatalogActionHost }) {
  const groups = [...new Set(catalogCellActions.map((a) => a.group))];
  return (
    <>
      <ContextMenuLabel className="max-w-64 truncate">{cell.donor.domain}</ContextMenuLabel>
      {groups.map((g, i) => (
        <Fragment key={g}>
          {i > 0 ? <ContextMenuSeparator /> : null}
          {catalogCellActions.filter((a) => a.group === g).map((a) => {
            const reason = a.disabledReason(cell);
            return (
              <ContextMenuItem key={a.id} data-action={a.id} disabled={!!reason} className="flex-col items-start gap-0 text-[13px]" onSelect={() => a.run(cell, host)}>
                <span>{a.label}</span>
                {reason ? <span className="text-[11px] text-muted-foreground">{reason}</span> : null}
              </ContextMenuItem>
            );
          })}
        </Fragment>
      ))}
    </>
  );
}

// Local alias so the add-donor body type-checks without importing the write schema name in several places.
type componentsWrite = {
  domain: string;
  name?: string;
  intermediary?: string;
  theme?: string;
  site_age?: string;
  pbn?: boolean;
  country?: string | null;
  niches?: string[];
  prices?: { amount_minor: number; currency: "USD" | "EUR" }[];
};

type ImportSummary = {
  created: number;
  updated: number;
  errors: { row: number; domain: string; error: string }[];
  ignored: string[];
  batches: number;
  failedBatch: { index: number; total: number; message: string } | null;
};

function ImportSummaryView({ summary }: { summary: ImportSummary }) {
  const shown = summary.errors.slice(0, 20);
  return (
    <div className="mt-3 flex flex-col gap-2 text-[13px]">
      <p>
        {summary.created.toLocaleString()} added, {summary.updated.toLocaleString()} updated
        {summary.errors.length ? `, ${summary.errors.length.toLocaleString()} with errors` : ""}
        {summary.batches > 1 ? ` · ${summary.batches} batches` : ""}.
      </p>
      {summary.ignored.length ? (
        <p className="text-[12px] text-muted-foreground">Columns not imported: {summary.ignored.join(", ")}.</p>
      ) : null}
      {shown.length ? (
        <ul className="max-h-48 overflow-y-auto text-[12px] text-muted-foreground">
          {shown.map((e) => (
            <li key={`${e.row}-${e.domain}`}>Row {e.row}{e.domain ? ` (${e.domain})` : ""}: {e.error}</li>
          ))}
          {summary.errors.length > shown.length ? <li>…and {summary.errors.length - shown.length} more.</li> : null}
        </ul>
      ) : null}
    </div>
  );
}
