// Who owns a person, company or deal: one control to see it and change
// it, so giving a record to a teammate never needs the edit form.

import { useState } from "react";
import { track } from "@/lib/analytics";
import { api, errorMessage } from "../api";
import { toast } from "../ui";
import { TeammatePicker, useTeammates } from "./teammate-picker";

export function OwnerPicker({ ws, brand, record, what, canManage, onChanged }: { ws: string; brand: string; record: { id: string; version?: number; attributes: Record<string, unknown> }; what: string; canManage: boolean; onChanged: () => void }) {
  const { me, people } = useTeammates(ws, brand);
  const [busy, setBusy] = useState(false);
  const owner = typeof record.attributes.owner === "string" ? record.attributes.owner : "";
  const name = (id: string) => people.find((x) => x.id === id || x.aka === id)?.name ?? "A former teammate";

  async function assign(to: string) {
    if (busy || to === owner) return;
    setBusy(true);
    try {
      const { data, error } = await api.PATCH("/v1/brands/{brand}/records/{record}", { params: { path: { brand, record: record.id } }, body: { attributes: { owner: to || null } } });
      if (error || !data) return toast(errorMessage(error), "error");
      if ("id" in data) {
        toast(to ? `${to === me ? "You own" : `${name(to)} owns`} this ${what} now.` : `This ${what} has no owner now.`);
        track("ui.record_owner_assigned", { record_type: what, to: !to ? "nobody" : to === me ? "me" : "teammate" });
        onChanged();
      } else toast("Sent for approval.");
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-w-0 items-center gap-2 text-[13px] text-muted-foreground">
      Owner
      <span className="min-w-0 text-foreground">
        <TeammatePicker
          value={owner || null}
          people={people}
          me={me}
          canChange={canManage}
          disabled={busy || people.length === 0}
          noneLabel="Nobody yet"
          label="Owner"
          onPick={(to) => void assign(to ?? "")}
        />
      </span>
    </div>
  );
}
