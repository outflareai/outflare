import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Brand, ConnectionAccount, ConnectionKind, Integration, UnassignedConnection } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, ErrorText, field, toast } from "../ui";
import { BrandsPicker, brandsLabel } from "./brand-picker";
import { ConnectorLogo } from "./connector-logo";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";

// Who a connection's data belongs to (plans/brands/README.md, "Connections
// and brands"): a brand connection is one brand's, a split one has its
// sites and ad accounts assigned to brands one by one, and a shared one is
// a tool every brand (or some) uses. No brand, no data: what nobody
// assigned shows in no brand until someone does.
export const kindLabels: Record<ConnectionKind, string> = { brand: "One brand", split: "Split by account", shared: "Shared tool" };

export function usedBy(i: Integration, brands: Brand[]): string {
  if (i.brand_id) return `${brands.find((b) => b.id === i.brand_id)?.name ?? "One brand"} only`;
  if (i.brand_kind === "brand") return "No brand yet";
  if (i.brand_kind === "split") return "Split by account";
  return brandsLabel(i.brand_ids, brands);
}

// UsedByBadge says where a connection comes from on a page narrowed to one
// brand: the brand's own, or shared from the workspace.
export function UsedByBadge({ i, brands, brand }: { i: Integration; brands: Brand[]; brand?: Brand }) {
  if (brands.length === 0) return null;
  if (i.brand_kind === "brand" && !i.brand_id) return <Badge tone="amber" title="Its data shows in no brand until you assign it">No brand yet</Badge>;
  if (!brand) return <span className="text-[12px] text-muted-foreground">{usedBy(i, brands)}</span>;
  return i.brand_id ? <Badge title={`Only ${brand.name} uses this connection`}>{brand.name}'s own</Badge> : <span className="text-[12px] text-muted-foreground" title={`Connected once for the workspace; ${usedBy(i, brands).toLowerCase()} use it`}>Shared from workspace</span>;
}

// ConnectionBrandsDialog picks a connection's kind and its brands: the
// brand of a brand connection, the brand of each site or ad account of a
// split one, the brands a shared one serves.
export function ConnectionBrandsDialog({ ws, integration: i, brands, onClose, onDone }: { ws: string; integration: Integration; brands: Brand[]; onClose: () => void; onDone: () => void }) {
  const active = brands.filter((b) => b.status === "active" || b.id === i.brand_id);
  const [mode, setMode] = useState<ConnectionKind>(i.brand_kind);
  const [shared, setShared] = useState<string[]>(i.brand_ids ?? []);
  const [own, setOwn] = useState(i.brand_id ?? active[0]?.id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const body = mode === "brand" ? { brand_kind: mode, brand_id: own } : mode === "shared" ? { brand_kind: mode, brand_id: null, brand_ids: shared } : { brand_kind: mode, brand_id: null, brand_ids: [] };
    const { error } = await api.PATCH("/v1/workspaces/{ws}/integrations/{id}", { params: { path: { ws, id: i.id } }, body });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(mode === "brand" ? `${i.name} belongs to ${active.find((b) => b.id === own)?.name ?? "one brand"}.` : mode === "shared" ? `${i.name} is shared with ${brandsLabel(shared, active).toLowerCase()}.` : `${i.name} is split: assign each of its accounts to a brand.`);
    onDone();
    if (mode !== "split") onClose();
  }

  const option = (m: ConnectionKind, help: string) => (
    <label className={`flex cursor-pointer items-start gap-3 rounded-lg border p-3 ${mode === m ? "border-foreground/30 bg-accent/40" : ""}`}>
      <input type="radio" name="kind" className="mt-1" checked={mode === m} onChange={() => setMode(m)} />
      <span className="min-w-0 flex-1 space-y-2 text-[13px]">
        <span className="block font-medium">{kindLabels[m]}</span>
        <span className="block text-[12px] text-muted-foreground">{help}</span>
        {m === "shared" && mode === "shared" && <BrandsPicker value={shared} brands={active} onChange={setShared} label="Brands that use it" heading="Used by" />}
        {m === "brand" && mode === "brand" && (
          <select aria-label="Brand" className={field} value={own} onChange={(e) => setOwn(e.target.value)}>
            {active.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
          </select>
        )}
      </span>
    </label>
  );

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={save} className="space-y-4">
          <DialogHeader className="text-left">
            <DialogTitle className="flex items-center gap-2.5 text-[15px]"><ConnectorLogo id={i.connector} />Whose is "{i.name}"</DialogTitle>
            <DialogDescription className="text-[13px]">Data from a connection, or one of its accounts, that belongs to no brand shows in no brand.</DialogDescription>
          </DialogHeader>
          {option("brand", "The whole connection belongs to one brand, such as Stripe for Acme.")}
          {option("split", "One sign-in whose sites, properties or ad accounts belong to different brands. Assign each below.")}
          {option("shared", "A tool every brand uses, such as DataForSEO or the AI key. What it produces belongs to the brand that used it.")}
          {mode === "split" && i.brand_kind === "split" && <ConnectionAccounts ws={ws} integration={i} brands={active} />}
          <ErrorText>{error}</ErrorText>
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>{mode === "split" && i.brand_kind === "split" ? "Done" : "Cancel"}</Button>
            {!(mode === "split" && i.brand_kind === "split") && <Button disabled={busy || (mode === "brand" && !own)}>{busy ? "Saving…" : "Save"}</Button>}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ConnectionAccounts lists a split connection's sites and ad accounts,
// each with a brand to pick.
export function ConnectionAccounts({ ws, integration: i, brands }: { ws: string; integration: Integration; brands: Brand[] }) {
  const [accounts, setAccounts] = useState<ConnectionAccount[] | null>(null);
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/integrations/{id}/accounts", { params: { path: { ws, id: i.id } } });
    setAccounts(data?.data ?? []);
  }, [ws, i.id]);
  useEffect(() => {
    load();
  }, [load]);
  if (accounts === null) return null;
  if (accounts.length === 0) return <p className="text-[12px] text-muted-foreground">No sites or ad accounts read yet. They show here after the first sync.</p>;
  return (
    <ul className="max-h-64 space-y-1.5 overflow-y-auto">
      {accounts.map((a) => (
        <li key={`${a.kind}:${a.account}`} className="flex items-center justify-between gap-3 text-[13px]">
          <span className="min-w-0 truncate" title={a.account}>{a.name || a.host || a.account}</span>
          <AssignAccount ws={ws} account={a} brands={brands} onDone={load} />
        </li>
      ))}
    </ul>
  );
}

function AssignAccount({ ws, account: a, brands, onDone }: { ws: string; account: ConnectionAccount; brands: Brand[]; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  async function assign(brand: string) {
    setBusy(true);
    const { error } = await api.PUT("/v1/workspaces/{ws}/integrations/{id}/accounts/{account}", { params: { path: { ws, id: a.integration_id, account: a.account } }, body: { brand_id: brand || null } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(brand ? `${a.name || a.account} now belongs to ${brands.find((b) => b.id === brand)?.name ?? "that brand"}.` : `${a.name || a.account} no longer has a brand of its own.`);
    onDone();
  }
  const value = a.source === "assigned" ? a.brand_id ?? "" : "";
  return (
    <select aria-label={`Brand for ${a.name || a.account}`} className={`${field} w-44 shrink-0`} disabled={busy} value={value} onChange={(e) => assign(e.target.value)}>
      <option value="">{a.source === "none" ? "No brand" : `${brands.find((b) => b.id === a.brand_id)?.name ?? "Its connection's"} (default)`}</option>
      {a.suggested_brand_id && !value && <option value={a.suggested_brand_id}>{brands.find((b) => b.id === a.suggested_brand_id)?.name} (matches its domain)</option>}
      {brands.filter((b) => b.id !== a.suggested_brand_id || value).map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
    </select>
  );
}

// UnassignedConnections is the workspace's alert: connections and
// accounts whose data belongs to no brand, each with a way to assign it.
export function UnassignedConnections({ ws, brands, canManage, onOpen }: { ws: string; brands: Brand[]; canManage: boolean; onOpen?: (integrationId: string) => void }) {
  const [list, setList] = useState<UnassignedConnection[] | null>(null);
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/connections/unassigned", { params: { path: { ws } } });
    setList(data?.data ?? []);
  }, [ws]);
  useEffect(() => {
    load();
  }, [load]);
  if (!list || list.length === 0) return null;
  const active = brands.filter((b) => b.status === "active");
  return (
    <section role="status" className="space-y-2 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-[13px]">
      <div className="font-medium">{list.length === 1 ? "A connection has" : `${list.length} connections and accounts have`} no brand, so {list.length === 1 ? "its" : "their"} data shows in no brand.</div>
      <ul className="space-y-1.5">
        {list.map((u) => (
          <li key={`${u.integration_id}:${u.account?.kind ?? ""}:${u.account?.account ?? ""}`} className="flex flex-wrap items-center justify-between gap-2">
            <span className="flex min-w-0 items-center gap-2"><ConnectorLogo id={u.connector} /><span className="truncate">{u.name}{u.account ? ` · ${u.account.name || u.account.host || u.account.account}` : ""}</span></span>
            {canManage && (u.account ? <AssignAccount ws={ws} account={u.account} brands={active} onDone={load} /> : onOpen && <Button variant="ghost" onClick={() => onOpen(u.integration_id)}>Assign</Button>)}
          </li>
        ))}
      </ul>
    </section>
  );
}

// UnassignedNotice is the workspace-wide line above every page while
// something has no brand; it leads to Connectors, where it is assigned.
export function UnassignedNotice({ ws, onFix }: { ws: string; onFix: () => void }) {
  const [n, setN] = useState(0);
  useEffect(() => {
    let live = true;
    api.GET("/v1/workspaces/{ws}/connections/unassigned", { params: { path: { ws } } }).then(({ data }) => live && setN(data?.data.length ?? 0));
    return () => {
      live = false;
    };
  }, [ws]);
  if (n === 0) return null;
  return (
    <div role="status" className="mb-6 flex items-center justify-between gap-3 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-[13px] text-amber-800 dark:text-amber-200">
      <span>{n === 1 ? "A connection or account has" : `${n} connections or accounts have`} no brand, so {n === 1 ? "its" : "their"} data shows in no brand.</span>
      <button className="shrink-0 font-medium underline-offset-2 hover:underline" onClick={onFix}>Assign {n === 1 ? "it" : "them"}</button>
    </div>
  );
}
