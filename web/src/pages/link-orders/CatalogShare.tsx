import { useState } from "react";
import { api, errorMessage } from "../../api";
import { Button, Card, ErrorText, field, toast } from "../../ui";
import { useBrand } from "../../lib/brand";
import type { PageData } from "./shared";

/**
 * copyCatalog copies one brand's whole donor catalog into another, page by
 * page (500 donors a call), and returns how many donors it wrote.
 */
export async function copyCatalog(from: string, to: string, onPage?: (written: number) => void): Promise<number> {
  let cursor: string | undefined;
  let written = 0;
  for (;;) {
    const { data, error } = await api.POST("/v1/brands/{brand}/link-orders/catalog/copy", {
      params: { path: { brand: to } },
      body: { from_brand: from, cursor },
    });
    if (error || !data) throw error ?? new Error("The copy failed.");
    written += data.created + data.updated;
    onPage?.(written);
    if (!data.next_cursor) return written;
    cursor = data.next_cursor;
  }
}

/**
 * CatalogShare keeps one donor catalog for every brand: a broker's
 * database is the same for all of them, so it is imported once and copied.
 * Each brand's copy, publishers and orders stay its own.
 */
export function CatalogShare({ data }: { data: PageData }) {
  const { active } = useBrand(data.ws);
  const others = active.filter((b) => b.id !== data.brand);
  const [from, setFrom] = useState("");
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const [err, setErr] = useState("");
  if (!data.canManage || !others.length) return null;

  const run = async (pairs: { from: string; to: string; name: string }[]) => {
    setBusy(true);
    setErr("");
    let total = 0;
    try {
      for (const [i, p] of pairs.entries()) {
        const n = await copyCatalog(p.from, p.to, (w) => setStatus(`${p.name} (${i + 1} of ${pairs.length}): ${w.toLocaleString()} sites…`));
        total += n;
      }
      toast(`Copied ${total.toLocaleString()} site${total === 1 ? "" : "s"}.`);
      setStatus("");
      data.reload();
    } catch (e) {
      setErr(`${errorMessage(e)} Run it again to finish: sites already copied are updated, not duplicated.`);
    } finally {
      setBusy(false);
    }
  };
  const nameOf = (id: string) => active.find((b) => b.id === id)?.name ?? "brand";

  return (
    <Card title="One catalog for every brand" description="A broker's database is the same for every brand: import it once, then copy it. Sites, prices, metrics, PBN and Best link flags carry over; each brand's orders and contacts stay its own. Copying again updates the sites already copied.">
      <div className="flex flex-wrap items-end gap-2">
        <Button disabled={busy || !data.donors.length} onClick={() => void run(others.map((b) => ({ from: data.brand, to: b.id, name: b.name })))}>
          Copy this catalog to every other brand ({others.length})
        </Button>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Or copy here from
          <select className={field} value={from} onChange={(e) => setFrom(e.target.value)} disabled={busy}>
            <option value="">Choose a brand</option>
            {others.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
          </select>
        </label>
        <Button variant="ghost" disabled={busy || !from} onClick={() => void run([{ from, to: data.brand, name: nameOf(data.brand) }])}>Copy here</Button>
      </div>
      {status ? <p className="mt-2 text-[12px] text-muted-foreground" role="status">{status}</p> : null}
      {err ? <ErrorText>{err}</ErrorText> : null}
    </Card>
  );
}
