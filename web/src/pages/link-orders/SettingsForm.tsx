import { useState } from "react";
import { api, errorMessage } from "../../api";
import { Button, Card, ErrorText, field, toast } from "../../ui";
import { dash, idemp, memberName, type PageData } from "./shared";

export function SettingsForm({ data, onClose }: { data: PageData; onClose: () => void }) {
  const [approver, setApprover] = useState(data.settings?.approver_user_id ?? "");
  const [band, setBand] = useState(String(data.settings?.equivalent_dr_band ?? 10));
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const canEdit = data.can("coverage.approve");

  const save = async () => {
    if (!canEdit) return;
    const n = Number(band);
    if (!Number.isInteger(n) || n < 1 || n > 50) {
      setErr("The DR band must be a whole number from 1 to 50.");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      const { error } = await api.PATCH("/v1/brands/{brand}/link-orders/settings", {
        params: { path: { brand: data.brand }, header: idemp() },
        body: { approver_user_id: approver || null, equivalent_dr_band: n },
      });
      if (error) throw error;
      toast("Settings saved.");
      data.reload();
      onClose();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Link order settings" description="Each brand sets its own approver. Approve and Paid stay blocked until one is set. The DR band is used to suggest an equivalent donor when a link is lost.">
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Approver
          <select className={field} value={approver} disabled={!canEdit} onChange={(e) => setApprover(e.target.value)} aria-label="Brand approver">
            <option value="">No approver (Approve and Paid blocked)</option>
            {data.members.map((m) => (
              <option key={m.user_id} value={m.user_id}>
                {m.name || m.email || m.user_id}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-[12px] text-muted-foreground">
          Equivalent DR band
          <input className={field} type="number" min={1} max={50} value={band} disabled={!canEdit} onChange={(e) => setBand(e.target.value)} />
        </label>
      </div>
      <p className="mt-2 text-[12px] text-muted-foreground">
        Current approver: {data.settings?.approver_user_id ? memberName(data.members, data.settings.approver_user_id) : dash}. A band of {data.settings?.equivalent_dr_band ?? 10} means a donor rated 54 is matched with 50-59.
      </p>
      {err ? <ErrorText>{err}</ErrorText> : null}
      <div className="mt-3 flex gap-2">
        {canEdit ? <Button disabled={busy} onClick={() => void save()}>Save settings</Button> : <p className="text-[13px] text-muted-foreground">Changing these settings needs the coverage.approve right.</p>}
        <Button variant="ghost" onClick={onClose}>Close</Button>
      </div>
    </Card>
  );
}
