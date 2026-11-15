// Assigning a site: who on the team manages it, so nobody else reaches
// out. It is the owner of the site's company record, so it shows here, in
// the CRM and on the relationship check.
import { useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { confirmAction, toast } from "../../ui";
import { Avatar, TeammatePicker, type Teammate } from "@/components/teammate-picker";

export { Avatar };

export type SiteOwner = { owner_id?: string | null; owner_name?: string | null; owner_avatar_url?: string | null };
type Member = components["schemas"]["Member"];

type ErrorBody = { error?: { code?: string; message?: string } };

/** The members who can be assigned a site of this brand. */
export function useAssignees(ws: string, brand: string): Member[] {
  const [members, setMembers] = useState<Member[]>([]);
  useEffect(() => {
    let live = true;
    void api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }).then(({ data }) => {
      if (!live) return;
      const all = (data?.data ?? []).filter((m) => !!m.user_id);
      // Owners and admins see every brand; others only the brands they work on.
      setMembers(all.filter((m) => m.role === "owner" || m.role === "admin" || !m.brand_ids?.length || m.brand_ids.includes(brand)));
    });
    return () => {
      live = false;
    };
  }, [ws, brand]);
  return members;
}

/** Assigns a site to someone (or no one), asking first when it takes it from a teammate. */
export async function assignSite(
  brand: string,
  host: string,
  to: string | null,
  setError: (e: string) => void,
): Promise<boolean> {
  const send = (take_over: boolean) =>
    api.POST("/v1/brands/{brand}/coverage/sites/claim", {
      params: { path: { brand } },
      body: { host, owner_id: to, ...(take_over ? { take_over } : {}) },
    });
  let { error } = await send(false);
  const e = error as ErrorBody | undefined;
  if (e?.error?.code === "site_claimed") {
    const ok = await confirmAction(
      to ? "Reassign a teammate's site?" : "Unassign a teammate's site?",
      <>{e.error.message?.replace(/ Check with them first.*$/, "")} Check with them first, so you don't both reach out.</>,
      to ? "Reassign" : "Unassign",
    );
    if (!ok) return false;
    ({ error } = await send(true));
  }
  if (error) {
    setError(errorMessage(error));
    return false;
  }
  toast(to ? "Assigned: the team can see who is managing it" : "Unassigned: anyone on the team can take it");
  return true;
}

/** Who manages a site, as a dropdown of the team with their photos. */
export function SiteAssignee({
  brand,
  host,
  owner,
  members,
  myId,
  manage,
  onChanged,
  setError,
}: {
  brand: string;
  host: string;
  owner: SiteOwner;
  members: Member[];
  myId: string;
  manage: boolean;
  onChanged: () => void;
  setError: (e: string) => void;
}) {
  const [busy, setBusy] = useState(false);
  const current = owner.owner_id ?? null;
  const name = owner.owner_name || members.find((m) => m.user_id === current)?.name || "A teammate";
  const pick = async (to: string | null) => {
    if (to === current) return;
    setBusy(true);
    const ok = await assignSite(brand, host, to, setError);
    setBusy(false);
    if (ok) onChanged();
  };
  const people = members.map((m): Teammate => ({ id: m.user_id!, name: m.name || m.email || "Team member", email: m.email ?? "", avatar_url: m.avatar_url, signIn: true }));
  return (
    <TeammatePicker
      value={current}
      people={people}
      me={myId}
      canChange={manage}
      disabled={busy}
      fallbackName={name}
      fallbackAvatar={owner.owner_avatar_url}
      className="max-w-48"
      onPick={(to) => void pick(to)}
    />
  );
}

/** Mine / Unassigned / Everyone. */
export type OwnerFilter = "" | "me" | "none" | "others";
export function OwnerSelect({ value, onChange, className }: { value: OwnerFilter; onChange: (v: OwnerFilter) => void; className?: string }) {
  return (
    <select className={className} aria-label="Who it's assigned to" value={value} onChange={(e) => onChange(e.target.value as OwnerFilter)}>
      <option value="">Everyone's</option>
      <option value="me">Assigned to me</option>
      <option value="none">Unassigned</option>
      <option value="others">Assigned to teammates</option>
    </select>
  );
}
