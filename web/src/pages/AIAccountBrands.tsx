import { useEffect, useState } from "react";
import type { AIAccount } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, ErrorText, toast } from "../ui";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";

export function AccountBrandPicker({ ws, selected, onChange }: { ws: string; selected: string[]; onChange: (ids: string[]) => void }) {
  const { brands, loaded } = useBrand(ws);
  const available = brands.filter(b => b.status === "active" || selected.includes(b.id));
  return <fieldset className="space-y-2">
    <legend className="mb-2 font-medium">Which brands use this subscription?</legend>
    <p className="text-[12px] text-muted-foreground">Choose explicitly. The brand currently open in your browser is never selected for you.</p>
    {!loaded ? <p>Loading brands…</p> : available.length === 0 ? <p className="text-muted-foreground">Create a brand in workspace settings before assigning this subscription.</p> : available.map(b => <label key={b.id} className="flex items-center gap-2 rounded-md border p-3">
      <input type="checkbox" checked={selected.includes(b.id)} onChange={e => {
        onChange(e.target.checked ? [...selected, b.id] : selected.filter(id => id !== b.id));
        track("ui.ai_account_brand_selected", { brand_id: b.id, selected: e.target.checked });
      }} />
      <span>{b.name}{b.status !== "active" ? " (archived)" : ""}</span>
    </label>)}
  </fieldset>;
}

export function BrandAssignment({ ws, account, onClose, onSaved }: { ws: string; account: AIAccount; onClose: () => void; onSaved: () => void }) {
  const [selected, setSelected] = useState<string[]>(account.brand_ids ?? []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => { track("ui.ai_account_brand_assignment_opened", { account_id: account.id }); }, [account.id]);
  function defer() { track("ui.ai_account_brand_assignment_deferred", { account_id: account.id }); onClose(); }
  async function save() {
    if (!selected.length) { track("ui.error_shown", { error_code: "brand_required" }); setError("Choose at least one brand for this subscription."); return; }
    setBusy(true); setError("");
    const { error } = await api.PATCH("/v1/workspaces/{ws}/ai-accounts/{id}", { params: { path: { ws, id: account.id } }, body: { brand_ids: selected } });
    setBusy(false);
    if (error) { setError(errorMessage(error)); return; }
    toast("Brand assignment saved."); onSaved();
  }
  return <Sheet open onOpenChange={open => !open && defer()}><SheetContent className="w-full overflow-y-auto sm:max-w-lg">
    <SheetHeader><SheetTitle>Assign {account.name}</SheetTitle><SheetDescription>{account.account_email ?? "Subscription"} — choose the brands this account serves. It remains in the workspace report.</SheetDescription></SheetHeader>
    <div className="space-y-5 px-4 pb-6 text-[13px]"><ErrorText>{error}</ErrorText>
      <fieldset disabled={busy}><AccountBrandPicker ws={ws} selected={selected} onChange={setSelected} /></fieldset>
      <div className="flex gap-2"><Button onClick={save} disabled={busy || !selected.length}>{busy ? "Saving…" : "Save brands"}</Button><Button variant="ghost" onClick={defer} disabled={busy}>Assign later</Button></div>
    </div>
  </SheetContent></Sheet>;
}
