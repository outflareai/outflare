import { useMemo, useState } from "react";
import { api, errorMessage } from "../../api";
import { Badge, Button, Card, Empty, ErrorText, field, FilePicker, toast } from "../../ui";
import {
  catalogExampleCSV,
  dash,
  durationLabel,
  idemp,
  moneyList,
  type LinkOrderDonor,
  type PageData,
} from "./shared";

type Filters = {
  q: string;
  country: string;
  lang: string;
  niche: string;
  drMin: string;
  priceMax: string;
  rel: string;
  duration: string;
  casino: string;
  pbn: "no" | "yes" | "any";
};

const defaults: Filters = { q: "", country: "", lang: "", niche: "", drMin: "", priceMax: "", rel: "", duration: "", casino: "", pbn: "no" };

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
  const [target, setTarget] = useState("/");
  const [addOpen, setAddOpen] = useState(false);
  const [add, setAdd] = useState({ domain: "", name: "", pbn: false, country: "", niches: "", amount: "", currency: "USD" as "USD" | "EUR" });

  const countries = [...new Set(data.donors.map((d) => d.country).filter(Boolean))].sort();
  const niches = [...new Set(data.donors.flatMap((d) => d.niches))].sort();
  const langs = [...new Set(data.donors.flatMap((d) => d.languages))].sort();

  const list = useMemo(() => {
    return data.donors.filter((d) => {
      if (f.q && !`${d.domain} ${d.niches.join(" ")}`.toLowerCase().includes(f.q.toLowerCase())) return false;
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
      return true;
    });
  }, [data.donors, f]);

  const pbnN = list.filter((d) => d.pbn).length;

  const toggleCart = (id: string) => {
    setCart(cart.includes(id) ? cart.filter((x) => x !== id) : [...cart, id]);
  };

  const requestQuotes = async () => {
    if (!cart.length || !data.canManage) return;
    const url = target.trim();
    if (!url) {
      setErr("Set a target URL for the request.");
      return;
    }
    setBusy("request");
    setErr("");
    try {
      for (const id of cart) {
        const d = data.donors.find((x) => x.id === id);
        const { error } = await api.POST("/v1/brands/{brand}/link-orders", {
          params: { path: { brand: data.brand }, header: idemp() },
          body: { donor_profile_id: id, target_url: url, slot_type: d?.slot_types[0] || "guest_post", rel_requested: d?.rel_offered[0] || "dofollow" },
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

  const importCSV = async () => {
    if (!file || !data.canManage) return;
    setBusy("import");
    setErr("");
    try {
      const csv = await file.text();
      const { data: result, error } = await api.POST("/v1/brands/{brand}/link-orders/catalog/import", {
        params: { path: { brand: data.brand }, header: idemp() },
        body: { csv },
      });
      if (error) throw error;
      const n = (result?.created ?? 0) + (result?.updated ?? 0);
      const failed = result?.errors?.length ?? 0;
      toast(failed ? `Imported ${n} row${n === 1 ? "" : "s"}, ${failed} with errors.` : `Imported ${n} row${n === 1 ? "" : "s"}.`);
      setImportOpen(false);
      setFile(null);
      data.reload();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy("");
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
      setAdd({ domain: "", name: "", pbn: false, country: "", niches: "", amount: "", currency: "USD" });
      data.reload();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy("");
    }
  };

  const exportCSV = () => {
    const header = ["domain", "pbn", "country", "languages", "niches", "casino_allowed", "rel_offered", "slot_types", "duration", "tat_days", "guarantee_days", "domain_rating", "amount_minor", "currency"];
    const lines = [header.join(",")];
    for (const d of list) {
      const price = d.prices[0];
      const cells = [
        d.domain,
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
        price?.amount_minor ?? "",
        price?.currency ?? "",
      ];
      lines.push(cells.map((c) => /[",\n]/.test(String(c)) ? `"${String(c).replaceAll('"', '""')}"` : String(c)).join(","));
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
          <input className={field} value={f.q} placeholder="Site or niche" onChange={(e) => setF({ ...f, q: e.target.value })} />
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
      </div>

      <div className="flex flex-wrap items-end gap-2">
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Target URL
          <input className={field} value={target} onChange={(e) => setTarget(e.target.value)} placeholder="/blog" />
        </label>
        {data.canManage ? (
          <>
            <Button variant="ghost" onClick={() => setAddOpen(true)}>Add donor</Button>
            <Button variant="ghost" onClick={() => { setImportOpen(true); setErr(""); }}>Import CSV</Button>
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
        <Card title="Import CSV" description="Import a CSV file. A Google Sheet is not imported. The first row is the header and must include a domain column. Amounts are the donor price only.">
          <FilePicker file={file} onChange={setFile} accept=".csv,text/csv" example={{ name: "link-order-catalog-example.csv", content: catalogExampleCSV }} />
          <div className="mt-3 flex gap-2">
            <Button disabled={!file || busy === "import"} onClick={() => void importCSV()}>Import</Button>
            <Button variant="ghost" onClick={() => { setImportOpen(false); setFile(null); }}>Cancel</Button>
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

      <Card title={<span>Donor catalog · {list.length} site{list.length === 1 ? "" : "s"}{pbnN ? ` · ${pbnN} PBN` : ""}</span>} description="Prices are what we pay the donor — there is no commission or client price. Import is a CSV file; a Google Sheet is not imported. DR and traffic come from the latest stored snapshot, never a fresh paid read.">
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
                  <th className="py-2 pr-3 font-medium">Rel</th>
                  <th className="py-2 pr-3 font-medium">Duration</th>
                  <th className="py-2 pr-3 text-right font-medium">Donor price</th>
                </tr>
              </thead>
              <tbody>
                {list.map((d) => (
                  <tr key={d.id} className="border-b last:border-0">
                    <td className="py-2 pr-3">
                      {data.canManage ? (
                        <input type="checkbox" checked={cart.includes(d.id)} onChange={() => toggleCart(d.id)} aria-label={`Add ${d.domain} to the request`} />
                      ) : null}
                    </td>
                    <td className="py-2 pr-3">
                      <button type="button" className="text-left font-medium hover:underline" onClick={() => onOpenDonor(d)}>{d.domain}</button>
                      <div className="flex flex-wrap gap-1">
                        {d.pbn ? <Badge tone="amber">PBN</Badge> : null}
                        {d.country ? <span className="text-[12px] text-muted-foreground">{d.country}</span> : null}
                      </div>
                    </td>
                    <td className="py-2 pr-3 tabular-nums">{d.domain_rating ?? dash}</td>
                    <td className="py-2 pr-3 tabular-nums">{d.traffic == null ? dash : d.traffic.toLocaleString()}</td>
                    <td className="py-2 pr-3">{d.niches.join(", ") || dash}</td>
                    <td className="py-2 pr-3">{d.rel_offered.join(", ") || dash}</td>
                    <td className="py-2 pr-3">{durationLabel(d.duration)}</td>
                    <td className="py-2 pr-3 text-right tabular-nums">{moneyList(d.prices)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty title="No donors match these filters">{data.donors.length ? "Clear a filter, or include PBN, to see more of the catalog." : "Add a donor or import a CSV to start the catalog."}</Empty>
        )}
        <p className="mt-2 text-[12px] text-muted-foreground">
          The default catalog filter excludes PBN. PBN is a manual flag; candidates from a competitor gap are not tagged as PBN on their own.
        </p>
      </Card>
    </div>
  );
}

// Local alias so the add-donor body type-checks without importing the write schema name in several places.
type componentsWrite = {
  domain: string;
  name?: string;
  pbn?: boolean;
  country?: string | null;
  niches?: string[];
  prices?: { amount_minor: number; currency: "USD" | "EUR" }[];
};
