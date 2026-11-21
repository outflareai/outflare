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
import { DonorPicks } from "./DonorPicks";
import { CatalogShare } from "./CatalogShare";
import { useBrand } from "../../lib/brand";
import { pbnSignals } from "./pbnSignals";
import { qualityLine, verdictLabel, verdictTone } from "./quality";
import { CATALOG_PAGE, intermediaryFromFileName, loadMetricColumns, nextSort, saveMetricColumns, sortChoiceIndex, sortChoices, sortDonors, type MetricKey, type Sort, type SortKey } from "./catalogView";
import { activeFilterCount, defaultFilters, filterDonors, metricOptions, moreFilterKeys, pricePerThousandVisits, type Filters } from "./catalogFilters";
import { emptyRequestTarget, RequestTarget, requestTargetBody, type RequestTargetValue } from "./RequestTarget";
import { useTargets } from "./targets";
import { catalogCellActions, cellFromEvent, directContactReason, outreachActions, type CatalogActionHost, type CatalogCellContext } from "./catalogActions";
import { FindEmailDialog, SendToSequenceDialog } from "./DonorOutreach";

export function Catalog({ data, cart, setCart, onOpenDonor }: {
  data: PageData;
  cart: string[];
  setCart: (ids: string[]) => void;
  onOpenDonor: (d: LinkOrderDonor) => void;
}) {
  const [f, setF] = useState<Filters>(defaultFilters);
  const [importOpen, setImportOpen] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  // The intermediary a whole export came from, added to every row whose
  // file has no intermediary column of its own.
  const [importIntermediary, setImportIntermediary] = useState("");
  // A Google Sheet link instead of a file; an .xlsx file or a sheet is
  // read into CSV by the server first, then imported like a CSV file.
  const [importSource, setImportSource] = useState<"file" | "sheet">("file");
  const [sheetURL, setSheetURL] = useState("");
  // Import the file into every active brand, not only this one: a
  // broker's database is the same for all of them.
  const { active: activeBrands } = useBrand(data.ws);
  const otherBrands = activeBrands.filter((b) => b.id !== data.brand);
  const [everyBrand, setEveryBrand] = useState(true);
  const [metricCols, setMetricColsState] = useState<MetricKey[]>(loadMetricColumns);
  const setMetricCols = (keys: MetricKey[]) => {
    setMetricColsState(keys);
    saveMetricColumns(keys);
  };
  const [sort, setSort] = useState<Sort>(null);
  const [limit, setLimit] = useState(CATALOG_PAGE);
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
    toggleBestLink: (d) => void toggleBestLink(d),
    togglePBN: (d) => void togglePBN(d),
  };
  const togglePBN = async (d: LinkOrderDonor) => {
    const { error } = await api.PATCH("/v1/brands/{brand}/link-orders/catalog/items/{id}", {
      params: { path: { brand: data.brand, id: d.id } },
      body: { pbn: !d.pbn },
    });
    if (error) {
      setErr(errorMessage(error));
      return;
    }
    toast(d.pbn ? `${d.domain} is no longer flagged as a PBN.` : `${d.domain} is flagged as a PBN and hidden by the default filter.`);
    data.reload();
  };
  const toggleBestLink = async (d: LinkOrderDonor) => {
    const { error } = await api.PATCH("/v1/brands/{brand}/link-orders/catalog/items/{id}", {
      params: { path: { brand: data.brand, id: d.id } },
      body: { best_link: !d.best_link },
    });
    if (error) {
      setErr(errorMessage(error));
      return;
    }
    toast(d.best_link ? `${d.domain} is no longer a best link.` : `${d.domain} is marked as a best link.`);
    data.reload();
  };
  const canSend = data.canManage && data.can("sequences.manage");

  const countries = [...new Set(data.donors.map((d) => d.country).filter(Boolean))].sort();
  const niches = [...new Set(data.donors.flatMap((d) => d.niches))].sort();
  const langs = [...new Set(data.donors.flatMap((d) => d.languages))].sort();
  const intermediaries = [...new Set(data.donors.map((d) => d.intermediary).filter(Boolean))].sort((a, b) => a.localeCompare(b));

  const categories = useMemo(() => metricOptions(data.donors, "main_category"), [data.donors]);
  const subcategories = useMemo(() => metricOptions(data.donors, "subcategory", f.category ? (d) => d.metrics?.main_category === f.category : undefined), [data.donors, f.category]);
  const articleTypes = useMemo(() => metricOptions(data.donors, "article_type"), [data.donors]);
  const geos = useMemo(() => metricOptions(data.donors, "geo_top1"), [data.donors]);
  const list = useMemo(() => filterDonors(data.donors, f), [data.donors, f]);
  const setFilter = (patch: Partial<Filters>) => { setF({ ...f, ...patch }); setLimit(CATALOG_PAGE); };
  const activeN = activeFilterCount(f);
  const moreN = activeFilterCount(f, moreFilterKeys);
  const numberField = (key: keyof Filters, label: string, placeholder = "") => (
    <label key={key} className="flex flex-col gap-1 text-[12px] text-muted-foreground">
      {label}
      <input className={field} type="number" min={0} inputMode="decimal" value={f[key]} placeholder={placeholder} onChange={(e) => setFilter({ [key]: e.target.value } as Partial<Filters>)} />
    </label>
  );

  const sorted = useMemo(() => sortDonors(list, sort), [list, sort]);
  const shown = sorted.slice(0, limit);
  const metricColumns = metricFields.filter((m) => metricCols.includes(m.key));
  const sortHeader = (key: SortKey, label: string, textual = false, align = "") => {
    const dir = sort?.key === key ? sort.dir : 0;
    return (
      <th key={key} className={`py-2 pr-3 font-medium whitespace-nowrap ${align}`} aria-sort={dir === 1 ? "ascending" : dir === -1 ? "descending" : "none"}>
        <button type="button" className="hover:text-foreground" title="Sort by this column" onClick={() => { setSort(nextSort(sort, key, textual)); setLimit(CATALOG_PAGE); }}>
          {label}{dir === 1 ? " ↑" : dir === -1 ? " ↓" : ""}
        </button>
      </th>
    );
  };

  const pbnN = list.filter((d) => d.pbn).length;
  const likelyN = useMemo(() => list.filter((d) => !d.pbn && pbnSignals(d).likely).length, [list]);
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
  // The import's CSV text: a CSV file as it is, or an .xlsx file or a
  // Google Sheet link read into CSV by the server (which writes nothing).
  const importText = async (): Promise<string> => {
    let body: { sheet_url?: string; xlsx_base64?: string };
    if (importSource === "sheet") {
      body = { sheet_url: sheetURL.trim() };
    } else if (file && /\.xlsx$/i.test(file.name)) {
      body = { xlsx_base64: await fileBase64(file) };
    } else {
      return file ? file.text() : "";
    }
    setProgress({ done: 0, total: 1 });
    const { data: table, error } = await api.POST("/v1/brands/{brand}/link-orders/catalog/table", { params: { path: { brand: data.brand } }, body });
    if (error || !table) throw error ?? new Error("The sheet could not be read.");
    return table.csv;
  };

  const importCSV = async () => {
    if (!data.canManage || (importSource === "file" ? !file : !sheetURL.trim())) return;
    setBusy("import");
    setErr("");
    setSummary(null);
    const total: ImportSummary = { created: 0, updated: 0, errors: [], ignored: [], batches: 0, failedBatch: null };
    try {
      const batches = csvBatches(await importText(), undefined, undefined, { header: "intermediary", value: importIntermediary });
      if (!batches.length) throw new Error("The file is empty.");
      const extra = everyBrand ? otherBrands : [];
      const steps = batches.length * (1 + extra.length);
      setProgress({ done: 0, total: steps });
      for (const [i, batch] of batches.entries()) {
        // Each batch goes to every other brand too; the summary counts
        // this brand's rows.
        for (const [j, b] of extra.entries()) {
          const { error } = await api.POST("/v1/brands/{brand}/link-orders/catalog/import", {
            params: { path: { brand: b.id }, header: idemp() },
            body: batch.csv,
            bodySerializer: (x: string | { csv: string }) => (typeof x === "string" ? x : x.csv),
            headers: { "Content-Type": "text/csv" },
          });
          if (error) throw new Error(`${b.name}: ${errorMessage(error)}`);
          setProgress({ done: i * (1 + extra.length) + j + 1, total: steps });
        }
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
        setProgress({ done: (i + 1) * (1 + extra.length), total: steps });
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
    const header = ["domain", "intermediary", "theme", "site_since", "pbn", "best_link", "country", "languages", "niches", "casino_allowed", "rel_offered", "slot_types", "duration", "tat_days", "guarantee_days", "domain_rating", "traffic", "amount_minor", "currency", ...metricFields.map((m) => m.key), "notes"];
    const lines = [header.join(",")];
    for (const d of list) {
      const price = d.prices[0];
      const cells = [
        d.domain,
        d.intermediary,
        d.theme,
        d.site_since ?? "",
        d.pbn ? "true" : "false",
        d.best_link ? "true" : "false",
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
          <input className={field} value={f.q} placeholder="Site, niche, category, intermediary or theme" onChange={(e) => setFilter({ q: e.target.value })} />
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Intermediary
          <select className={field} value={f.intermediary} onChange={(e) => setFilter({ intermediary: e.target.value })}>
            <option value="">Any</option>
            {intermediaries.map((c) => <option key={c}>{c}</option>)}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Category
          <select className={field} value={f.category} onChange={(e) => setFilter({ category: e.target.value, subcategory: "" })}>
            <option value="">Any</option>
            {categories.map((c) => <option key={c.value} value={c.value}>{c.value} ({c.n})</option>)}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Language
          <select className={field} value={f.lang} onChange={(e) => setFilter({ lang: e.target.value })}>
            <option value="">Any</option>
            {langs.map((c) => <option key={c}>{c}</option>)}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Country
          <select className={field} value={f.country} onChange={(e) => setFilter({ country: e.target.value })}>
            <option value="">Any</option>
            {countries.map((c) => <option key={c}>{c}</option>)}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Niche
          <select className={field} value={f.niche} onChange={(e) => setFilter({ niche: e.target.value })}>
            <option value="">Any</option>
            {niches.map((c) => <option key={c}>{c}</option>)}
          </select>
        </label>
        {numberField("drMin", "Min DR")}
        {numberField("priceMax", "Max price")}
        {numberField("trafficMin", "Min traffic")}
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          PBN
          <select className={field} value={f.pbn} onChange={(e) => setFilter({ pbn: e.target.value as Filters["pbn"] })}>
            <option value="no">Exclude PBN</option>
            <option value="any">Any</option>
            <option value="yes">PBN only</option>
          </select>
        </label>
      </div>
      <details className="text-[12px]" open={moreN > 0 ? true : undefined}>
        <summary className="cursor-pointer text-muted-foreground hover:text-foreground">More filters{moreN ? ` · ${moreN} on` : ""}</summary>
        <div className="mt-2 grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
            Subcategory
            <select className={field} value={f.subcategory} onChange={(e) => setFilter({ subcategory: e.target.value })}>
              <option value="">Any</option>
              {subcategories.map((c) => <option key={c.value} value={c.value}>{c.value} ({c.n})</option>)}
            </select>
          </label>
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
            Audience geo (Ahrefs top 1 or 2)
            <select className={field} value={f.geo} onChange={(e) => setFilter({ geo: e.target.value })}>
              <option value="">Any</option>
              {geos.map((c) => <option key={c.value} value={c.value}>{c.value} ({c.n})</option>)}
            </select>
          </label>
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
            Article type
            <select className={field} value={f.articleType} onChange={(e) => setFilter({ articleType: e.target.value })}>
              <option value="">Any</option>
              <option value="-">Not stated</option>
              {articleTypes.map((c) => <option key={c.value} value={c.value}>{c.value} ({c.n})</option>)}
            </select>
          </label>
          {numberField("drMax", "Max DR")}
          {numberField("priceMin", "Min price")}
          {numberField("trafficMax", "Max traffic")}
          {numberField("valueMax", "Max price per 1,000 visits", "e.g. 20")}
          {numberField("rdMin", "Min referring domains")}
          {numberField("kwMin", "Min keywords")}
          {numberField("outgoingMax", "Max outgoing domains", "fewer = less link-farm")}
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
            Added since
            <input className={field} type="date" value={f.addedSince} onChange={(e) => setFilter({ addedSince: e.target.value })} />
          </label>
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
            Rel
            <select className={field} value={f.rel} onChange={(e) => setFilter({ rel: e.target.value })}>
              <option value="">Any</option>
              <option value="dofollow">Dofollow</option>
              <option value="sponsored">Sponsored</option>
            </select>
          </label>
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
            Duration
            <select className={field} value={f.duration} onChange={(e) => setFilter({ duration: e.target.value })}>
              <option value="">Any</option>
              <option value="permanent">Lifetime</option>
              <option value="limited">Time-limited</option>
            </select>
          </label>
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
            Casino / gambling
            <select className={field} value={f.casino} onChange={(e) => setFilter({ casino: e.target.value })}>
              <option value="">Any</option>
              <option value="no">Not allowed</option>
              <option value="yes">Allowed</option>
            </select>
          </label>
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
            Traffic and links
            <select className={field} value={f.verdict} onChange={(e) => setFilter({ verdict: e.target.value as Filters["verdict"] })} title="Skip: over 45% of search traffic on the home page, 80% on the top 3 pages or one page, or over 0.3 linked domains per referring domain.">
              <option value="keep">Hide sites to skip</option>
              <option value="any">Any</option>
              <option value="skip">Only sites to skip</option>
              <option value="unknown">Not checked yet</option>
            </select>
          </label>
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
            PBN signs
            <select className={field} value={f.pbnHint} onChange={(e) => setFilter({ pbnHint: e.target.value as Filters["pbnHint"] })}>
              <option value="any">Any</option>
              <option value="likely">Likely PBN (to review)</option>
              <option value="none">No PBN signs</option>
            </select>
          </label>
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
            Best link
            <select className={field} value={f.best} onChange={(e) => setFilter({ best: e.target.value as Filters["best"] })}>
              <option value="any">Any</option>
              <option value="yes">Best links only</option>
            </select>
          </label>
          <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
            Direct contact
            <select className={field} value={f.direct} onChange={(e) => setFilter({ direct: e.target.value as Filters["direct"] })}>
              <option value="any">Any</option>
              <option value="yes">Recommended only</option>
              <option value="no">Not recommended</option>
            </select>
          </label>
        </div>
      </details>
      <div className="flex flex-wrap items-end gap-2">
        <label className="flex min-w-64 flex-col gap-1 text-[12px] text-muted-foreground">
          Sort by
          <select className={field} value={sortChoiceIndex(sort)} onChange={(e) => { const c = sortChoices[Number(e.target.value)]; setSort(e.target.value === "" ? null : c ? c.sort : sort); setLimit(CATALOG_PAGE); }}>
            <option value="">Catalog order</option>
            {sortChoices.map((c, i) => <option key={c.label} value={String(i)}>{c.label}</option>)}
            {sortChoiceIndex(sort) === "custom" ? <option value="custom" disabled>By a column heading</option> : null}
          </select>
        </label>
        <Button variant="ghost" disabled={!activeN && !sort} onClick={() => { setF(defaultFilters); setSort(null); setLimit(CATALOG_PAGE); }}>Reset filters{activeN ? ` (${activeN})` : ""}</Button>
      </div>

      {data.canManage ? <RequestTarget brand={data.brand} suggestions={suggestions} value={target} onChange={setTarget} /> : null}
      <DonorPicks
        data={data}
        donors={list}
        targetURL={target.url}
        suggestion={suggestions.find((x) => (target.targetId && x.target_id === target.targetId) || x.url === target.url)}
        suggestions={suggestions}
        cart={cart}
        setCart={setCart}
        onOpenDonor={onOpenDonor}
      />
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
      <CatalogShare data={data} />

      {importOpen ? (
        <Card title="Import donors" description="Import a CSV or Excel (.xlsx) file, or a Google Sheet link, such as an intermediary's full database. The first row is the header and must include a domain column (domain, website, site or url); headers are matched ignoring case and punctuation, so Traffic / Organic is traffic. Intermediary, theme, site age and the intermediary's metrics (referring domains, backlinks, keywords, outgoing links and more) are kept on the donor. A large file is sent in batches of about 1 MB. price is the donor price in dollars or euros; amount_minor is in cents.">
          <div className="mb-3 flex gap-2" role="group" aria-label="Import from">
            <Button variant={importSource === "file" ? "primary" : "ghost"} aria-pressed={importSource === "file"} onClick={() => setImportSource("file")}>File (CSV or Excel)</Button>
            <Button variant={importSource === "sheet" ? "primary" : "ghost"} aria-pressed={importSource === "sheet"} onClick={() => setImportSource("sheet")}>Google Sheet link</Button>
          </div>
          {importSource === "sheet" ? (
            <label className="flex max-w-xl flex-col gap-1 text-[12px] text-muted-foreground">
              Google Sheet link
              <input className={field} value={sheetURL} onChange={(e) => { setSheetURL(e.target.value); setSummary(null); }} placeholder="https://docs.google.com/spreadsheets/d/…/edit#gid=0" />
              <span>Share it as Anyone with the link (Viewer), or publish it to the web. The tab in the link is imported.</span>
            </label>
          ) : (
          <FilePicker file={file} onChange={(f) => { setFile(f); setSummary(null); if (f && !importIntermediary) setImportIntermediary(intermediaryFromFileName(f.name)); }} accept=".csv,text/csv,.xlsx,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" label="Choose CSV or Excel file" example={{ name: "link-order-catalog-example.csv", content: catalogExampleCSV }} />
          )}
          {otherBrands.length ? (
            <label className="mt-3 flex items-center gap-2 text-[12px]">
              <input type="checkbox" checked={everyBrand} onChange={(e) => setEveryBrand(e.target.checked)} />
              Import into every brand ({otherBrands.length + 1}), so each brand has the same catalog
            </label>
          ) : null}
          <label className="mt-3 flex max-w-sm flex-col gap-1 text-[12px] text-muted-foreground">
            Intermediary for every row
            <input className={field} value={importIntermediary} onChange={(e) => setImportIntermediary(e.target.value)} placeholder="Broker the file came from, such as Acme Links" />
            <span>Used when the file has no intermediary column; a column in the file wins.</span>
          </label>
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
            <Button disabled={(importSource === "file" ? !file : !sheetURL.trim()) || busy === "import"} onClick={() => void importCSV()}>{busy === "import" ? "Importing…" : "Import"}</Button>
            <Button variant="ghost" disabled={busy === "import"} onClick={() => { setImportOpen(false); setFile(null); setSummary(null); setImportIntermediary(""); }}>{summary ? "Close" : "Cancel"}</Button>
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

      <Card title={<span>Donor catalog · {list.length} site{list.length === 1 ? "" : "s"}{pbnN ? ` · ${pbnN} PBN` : ""}{likelyN ? ` · ${likelyN} likely PBN to review` : ""}{directN ? ` · ${directN} direct contact` : ""}</span>} description="Prices are what we pay the donor — there is no commission or client price. Import is a CSV or Excel file, or a Google Sheet link. DR, traffic and the other metrics are the latest imported or stored values, never a fresh paid read. Open a site for its full metrics. Select a column heading to sort.">
        <details className="mb-2 text-[12px]">
          <summary className="cursor-pointer text-muted-foreground hover:text-foreground">Metric columns · {metricColumns.length} of {metricFields.length}</summary>
          <div className="mt-2 flex flex-wrap gap-2">
            <Button variant="ghost" onClick={() => setMetricCols(metricFields.map((m) => m.key))}>Show all</Button>
            <Button variant="ghost" onClick={() => setMetricCols([])}>Hide all</Button>
          </div>
          <div className="mt-2 grid gap-1 sm:grid-cols-2 lg:grid-cols-4">
            {metricFields.map((m) => (
              <label key={m.key} className="flex items-center gap-2 text-[13px] text-foreground">
                <input type="checkbox" checked={metricCols.includes(m.key)} onChange={(e) => setMetricCols(e.target.checked ? [...metricCols, m.key] : metricCols.filter((k) => k !== m.key))} />
                {metricColumnLabel(m)}
              </label>
            ))}
          </div>
        </details>
        {list.length ? (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[800px] text-left text-[13px]">
              <caption className="sr-only">Donor catalog. Select a column heading to sort.</caption>
              <thead className="text-muted-foreground">
                <tr className="border-b">
                  <th className="py-2 pr-3 font-medium">{data.canManage ? "Request" : ""}</th>
                  {sortHeader("site", "Site", true)}
                  {sortHeader("price", "Donor price", false, "text-right")}
                  {sortHeader("dr", "DR")}
                  {sortHeader("traffic", "Traffic")}
                  {sortHeader("value", "Price / 1k visits", false, "text-right")}
                  <th className="py-2 pr-3 font-medium">Niche</th>
                  <th className="py-2 pr-3 font-medium">Intermediary</th>
                  <th className="py-2 pr-3 font-medium">Rel</th>
                  <th className="py-2 pr-3 font-medium">Duration</th>
                  {metricColumns.map((m) => sortHeader(m.key, metricColumnLabel(m), isTextMetric(m.key)))}
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
                {shown.map((d) => (
                  <tr key={d.id} data-donor={d.id} className="border-b last:border-0">
                    <td data-col="request" className="py-2 pr-3">
                      {data.canManage ? (
                        <input type="checkbox" checked={cart.includes(d.id)} onChange={() => toggleCart(d.id)} aria-label={`Add ${d.domain} to the request`} />
                      ) : null}
                    </td>
                    <td data-col="site" className="py-2 pr-3">
                      <button type="button" className="text-left font-medium hover:underline" onClick={() => onOpenDonor(d)}>{d.domain}</button>
                      <div className="flex flex-wrap gap-1">
                        {d.best_link ? <Badge tone="blue" title="Marked as a best link: shown first">Best link</Badge> : null}
                        {d.pbn ? <Badge tone="amber">PBN</Badge> : null}
                        {!d.pbn && pbnSignals(d).likely ? <Badge tone="red" title={`Signs of a PBN (a suggestion; flag it from the right-click menu): ${pbnSignals(d).reasons.join("; ")}`}>Likely PBN?</Badge> : null}
                        {d.quality && d.quality.verdict !== "ok" && d.quality.verdict !== "unknown" ? <Badge tone={verdictTone[d.quality.verdict]} title={[d.quality.reasons.join("; "), qualityLine(d)].filter(Boolean).join(" — ")}>{verdictLabel[d.quality.verdict]}</Badge> : null}
                        {d.direct_contact.recommended ? <Badge tone="green" title={directContactReason(d)}>Direct contact</Badge> : null}
                        {d.country ? <span className="text-[12px] text-muted-foreground">{d.country}</span> : null}
                        {d.languages.length ? <span className="text-[12px] text-muted-foreground">{d.languages.join(", ")}</span> : null}
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
                    <td data-col="price" className="py-2 pr-3 text-right tabular-nums whitespace-nowrap">{moneyList(d.prices)}</td>
                    <td data-col="dr" className="py-2 pr-3 tabular-nums">{d.domain_rating ?? dash}</td>
                    <td data-col="traffic" className="py-2 pr-3 tabular-nums">{d.traffic == null ? dash : d.traffic.toLocaleString()}</td>
                    <td data-col="value" className="py-2 pr-3 text-right tabular-nums" title="Lowest price divided by monthly organic traffic in thousands">{valueLabel(d)}</td>
                    <td data-col="niche" className="py-2 pr-3">{d.niches.join(", ") || dash}</td>
                    <td data-col="intermediary" className="py-2 pr-3">{d.intermediary || dash}</td>
                    <td data-col="rel" className="py-2 pr-3">{d.rel_offered.join(", ") || dash}</td>
                    <td data-col="duration" className="py-2 pr-3">{durationLabel(d.duration)}</td>
                    {metricColumns.map((m) => (
                      <td key={m.key} data-col="metric" data-metric={m.key} className={`py-2 pr-3 whitespace-nowrap ${isTextMetric(m.key) ? "" : "tabular-nums"}`}>{metricCell(d.metrics?.[m.key])}</td>
                    ))}
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
        ) : null}
        {list.length > shown.length ? (
          <div className="mt-2 flex flex-wrap items-center gap-2 text-[12px] text-muted-foreground">
            <span>Showing {shown.length.toLocaleString()} of {list.length.toLocaleString()} sites.</span>
            <Button variant="ghost" onClick={() => setLimit(limit + CATALOG_PAGE)}>Show {Math.min(CATALOG_PAGE, list.length - shown.length)} more</Button>
            <Button variant="ghost" onClick={() => setLimit(list.length)}>Show all</Button>
          </div>
        ) : null}
        {list.length ? null : (
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
                <span>{a.labelFor?.(cell) ?? a.label}</span>
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

const textMetricKeys = new Set<string>(["date_added", "geo_top1", "geo_top2", "article_type", "main_category", "subcategory"]);

function isTextMetric(key: string): boolean {
  return textMetricKeys.has(key);
}

// "Followed" alone is ambiguous as a column heading, so short labels carry
// their group: "Backlinks followed", "Ref. domains not followed".
function metricColumnLabel(m: { label: string; group: string }): string {
  if (m.label.toLowerCase().startsWith(m.group.toLowerCase().split(" ")[0])) return m.label;
  if (["Listing", "Audience", "Keywords", "Outgoing"].includes(m.group)) return m.label;
  const group = m.group === "Referring domains" ? "Ref. domains" : m.group;
  return `${group} ${m.label.toLowerCase()}`;
}

function metricCell(v: string | number | null | undefined) {
  if (v == null || v === "") return dash;
  return typeof v === "number" ? v.toLocaleString() : v;
}

/** A file as base64, for the JSON body that reads an .xlsx workbook. */
async function fileBase64(f: File): Promise<string> {
  const bytes = new Uint8Array(await f.arrayBuffer());
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

function valueLabel(d: LinkOrderDonor): string {
  const v = pricePerThousandVisits(d);
  if (v == null) return dash;
  return v < 10 ? v.toFixed(2) : Math.round(v).toLocaleString();
}
