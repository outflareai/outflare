import { useState, type FormEvent } from "react";
import type { Brand, Integration } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, ErrorText, field, toast } from "../ui";
import { BrandsPicker, brandsLabel } from "./brand-picker";
import { ConnectorLogo } from "./connector-logo";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";

// Who uses a connection: a workspace connection is shared with every brand
// or some of them; a brand's own connection is used by that brand alone,
// in place of the workspace's connection to the same tool.
export function usedBy(i: Integration, brands: Brand[]): string {
  if (i.brand_id) return `${brands.find((b) => b.id === i.brand_id)?.name ?? "One brand"} only`;
  return brandsLabel(i.brand_ids, brands);
}

// UsedByBadge says where a connection comes from on a page narrowed to one
// brand: the brand's own, or shared from the workspace.
export function UsedByBadge({ i, brands, brand }: { i: Integration; brands: Brand[]; brand?: Brand }) {
  if (brands.length === 0) return null;
  if (!brand) return <span className="text-[12px] text-muted-foreground">{usedBy(i, brands)}</span>;
  return i.brand_id ? <Badge title={`Only ${brand.name} uses this connection`}>{brand.name}'s own</Badge> : <span className="text-[12px] text-muted-foreground" title={`Connected once for the workspace; ${usedBy(i, brands).toLowerCase()} use it`}>Shared from workspace</span>;
}

type Mode = "shared" | "own";

// ConnectionBrandsDialog picks who uses a connection.
export function ConnectionBrandsDialog({ ws, integration: i, brands, onClose, onDone }: { ws: string; integration: Integration; brands: Brand[]; onClose: () => void; onDone: () => void }) {
  const active = brands.filter((b) => b.status === "active" || b.id === i.brand_id);
  const [mode, setMode] = useState<Mode>(i.brand_id ? "own" : "shared");
  const [shared, setShared] = useState<string[]>(i.brand_ids ?? []);
  const [own, setOwn] = useState(i.brand_id ?? active[0]?.id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const body = mode === "own" ? { brand_id: own } : { brand_id: null, brand_ids: shared };
    const { error } = await api.PATCH("/v1/workspaces/{ws}/integrations/{id}", { params: { path: { ws, id: i.id } }, body });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(mode === "own" ? `${i.name} is now used by ${active.find((b) => b.id === own)?.name ?? "one brand"} only.` : `${i.name} is shared with ${brandsLabel(shared, active).toLowerCase()}.`);
    onClose();
    onDone();
  }

  const option = (m: Mode, title: string, help: string) => (
    <label className={`flex cursor-pointer items-start gap-3 rounded-lg border p-3 ${mode === m ? "border-foreground/30 bg-accent/40" : ""}`}>
      <input type="radio" name="used-by" className="mt-1" checked={mode === m} onChange={() => setMode(m)} />
      <span className="min-w-0 flex-1 space-y-2 text-[13px]">
        <span className="block font-medium">{title}</span>
        <span className="block text-[12px] text-muted-foreground">{help}</span>
        {m === "shared" && mode === "shared" && <BrandsPicker value={shared} brands={active} onChange={setShared} label="Brands that use it" heading="Used by" />}
        {m === "own" && mode === "own" && (
          <select aria-label="Brand" className={field} value={own} onChange={(e) => setOwn(e.target.value)}>
            {active.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
          </select>
        )}
      </span>
    </label>
  );

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={save} className="space-y-4">
          <DialogHeader className="text-left">
            <DialogTitle className="flex items-center gap-2.5 text-[15px]"><ConnectorLogo id={i.connector} />Who uses "{i.name}"</DialogTitle>
            <DialogDescription className="text-[13px]">Connect a tool once and share it, or keep it for one brand with an account of its own.</DialogDescription>
          </DialogHeader>
          {option("shared", "Shared from the workspace", "Every brand you pick uses this one connection. Each brand still reads its own sites and accounts on it.")}
          {option("own", "One brand's own", `Only this brand uses it, in place of any shared ${i.connector_name} connection.`)}
          <ErrorText>{error}</ErrorText>
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={busy || (mode === "own" && !own)}>{busy ? "Saving…" : "Save"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
