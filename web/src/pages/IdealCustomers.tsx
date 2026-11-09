// Ideal customers: who the workspace sells to, kept once. Each profile is a
// description in words plus filters in the same vocabulary as lead
// searches. Lead list fit scores and the lead finder read it.

import { useCallback, useEffect, useState, type FormEvent } from "react";
import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import type { CustomerProfile, ICP } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, toast } from "../ui";
import { ago, plural } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { emptyICP, icpFields, TagInput } from "@/components/icp";

type Props = { ws: string; canManage: boolean };

const filterCount = (f: ICP) => Object.values(f).reduce((n, v) => n + v.length, 0);

// A profile is one brand's: the page shows the picked brand's.
export function IdealCustomers(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Ideal customer profiles" brands={active.length} />;
  return <IdealCustomersPage key={brand.id} {...props} brandId={brand.id} />;
}

function IdealCustomersPage({ canManage, brandId }: Props & { brandId: string }) {
  const [profiles, setProfiles] = useState<CustomerProfile[] | null>(null);
  const [status, setStatus] = useState<"active" | "archived">("active");
  const [editing, setEditing] = useState<CustomerProfile | "new" | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/ideal-customer-profiles", { params: { path: { brand: brandId }, query: { status } } });
    if (error) setError(errorMessage(error));
    setProfiles(data?.data ?? []);
  }, [brandId, status]);
  useEffect(() => {
    load();
  }, [load]);

  async function setArchived(p: CustomerProfile, archived: boolean) {
    const { error } = await api.PATCH("/v1/brands/{brand}/ideal-customer-profiles/{id}", { params: { path: { brand: brandId, id: p.id } }, body: { status: archived ? "archived" : "active" } });
    if (error) return toast(errorMessage(error), "error");
    toast(archived ? `Archived ${p.name}. Lists that use it keep scoring against it.` : `Restored ${p.name}.`);
    load();
  }

  const columns: ColumnDef<CustomerProfile>[] = [
    {
      id: "name", header: "Profile", accessorFn: (p) => p.name,
      cell: ({ row: { original: p } }) => (
        <span className="block min-w-0">
          <span className="block truncate font-medium">{p.name}</span>
          {p.description && <span className="block truncate text-[12px] text-muted-foreground">{p.description}</span>}
        </span>
      ),
    },
    { id: "filters", header: "Filters", accessorFn: (p) => filterCount(p.filters) },
    { id: "updated", header: "Changed", accessorFn: (p) => p.updated_at, cell: ({ row: { original: p } }) => ago(p.updated_at) },
  ];

  if (!profiles) return <Loading />;
  const none = profiles.length === 0 && status === "active";
  return (
    <div className="space-y-6">
      <PageHeader
        title="Ideal customers"
        description="Describe who you sell to once. Lead list fit scores and the lead finder use the same profile."
        action={canManage && <Button onClick={() => setEditing("new")}>New profile</Button>}
      />
      <ErrorText>{error}</ErrorText>
      {none ? (
        <Empty title="No ideal customer profiles yet" action={canManage && <Button onClick={() => setEditing("new")}>New profile</Button>}>
          Say who buys from you in a sentence, then add the job titles, industries, company sizes and places that fit.
        </Empty>
      ) : (
        <DataTable
          columns={canManage ? [...columns, {
            id: "actions", header: "", enableSorting: false,
            cell: ({ row: { original: p } }) => (
              <Menu aria={`Actions for ${p.name}`} items={[
                { label: "Edit", onClick: () => setEditing(p) },
                p.status === "active" ? { label: "Archive", onClick: () => setArchived(p, true) } : { label: "Restore", onClick: () => setArchived(p, false) },
              ]} />
            ),
          }] : columns}
          data={profiles}
          rowId={(p) => p.id}
          onRowClick={(p) => setEditing(p)}
          searchPlaceholder="Search profiles"
          toolbar={
            <select aria-label="Show" className={field} value={status} onChange={(e) => setStatus(e.target.value as "active" | "archived")}>
              <option value="active">Active</option>
              <option value="archived">Archived</option>
            </select>
          }
          empty={status === "archived" ? "No archived profiles." : "No profiles match."}
          mobile={(p) => (
            <span className="block min-w-0">
              <span className="block truncate text-[13px] font-medium">{p.name}</span>
              <span className="block truncate text-[12px] text-muted-foreground">{plural(filterCount(p.filters), "filter")}</span>
            </span>
          )}
        />
      )}
      {editing && (
        <ProfileDialog brandId={brandId} profile={editing === "new" ? null : editing} readOnly={!canManage}
          onClose={() => setEditing(null)} onSaved={() => { setEditing(null); load(); }} />
      )}
    </div>
  );
}

function ProfileDialog({ brandId, profile, readOnly, onClose, onSaved }: { brandId: string; profile: CustomerProfile | null; readOnly: boolean; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(profile?.name ?? "");
  const [description, setDescription] = useState(profile?.description ?? "");
  const [filters, setFilters] = useState<ICP>({ ...emptyICP(), ...profile?.filters });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const body = { name: name.trim(), description: description.trim(), filters };
    const { error } = profile
      ? await api.PATCH("/v1/brands/{brand}/ideal-customer-profiles/{id}", { params: { path: { brand: brandId, id: profile.id } }, body })
      : await api.POST("/v1/brands/{brand}/ideal-customer-profiles", { params: { path: { brand: brandId } }, body });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(profile ? `Saved ${body.name}.` : `Made ${body.name}.`);
    onSaved();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{profile ? profile.name : "New ideal customer profile"}</DialogTitle>
          <DialogDescription>
            {profile
              ? <>Lists that use it score against the changes on their next run. <Badge tone="grey">v{profile.version}</Badge></>
              : "Who you sell to, in words and as filters lead searches can run."}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={save} className="space-y-3">
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Series A fintech, AU" maxLength={100} required autoFocus={!profile} readOnly={readOnly} />
          <label className="block text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">Who they are and why they buy</span>
            <textarea className={`${field} w-full resize-y leading-relaxed`} rows={3} maxLength={4000} value={description} readOnly={readOnly}
              onChange={(e) => setDescription(e.target.value)} placeholder="Fintechs that raised a Series A in the last year and need pipeline before their next round." />
          </label>
          {icpFields.map((f) => (
            <TagInput key={f.key} label={f.label} placeholder={f.placeholder} values={filters[f.key] ?? []} readOnly={readOnly} onChange={(v) => setFilters((x) => ({ ...x, [f.key]: v }))} />
          ))}
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>{readOnly ? "Close" : "Cancel"}</Button>
            {!readOnly && <Button disabled={busy || !name.trim()}>{profile ? "Save" : "Make profile"}</Button>}
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
