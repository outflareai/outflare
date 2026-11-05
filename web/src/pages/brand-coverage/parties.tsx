// The CRM companies and people on a placement (the agency or partner that
// delivered it, the creator, author or editor), and its CRM deal.
import { useEffect, useState } from "react";
import { ArrowUpRight, X } from "lucide-react";
import { api, errorMessage } from "../../api";
import { appPath } from "../../lib/paths";
import { Button, ErrorText, field, toast } from "../../ui";
import { label, list, save, type Row } from "./client";

const roles = ["agency", "partner", "creator", "author", "editor", "owner", "account_manager", "other"];

type Found = { id: string; name: string; type: string };

export function PlacementParties({
  ws,
  brand,
  placement,
  manage,
  readCRM,
}: {
  ws: string;
  brand: string;
  placement: Row;
  manage: boolean;
  readCRM: boolean;
}) {
  const [rows, setRows] = useState<Row[]>([]),
    [names, setNames] = useState<Record<string, string>>({}),
    [error, setError] = useState(""),
    [q, setQ] = useState(""),
    [found, setFound] = useState<Found[]>([]),
    [record, setRecord] = useState(""),
    [role, setRole] = useState("agency"),
    [busy, setBusy] = useState(false),
    [reload, setReload] = useState(0);

  useEffect(() => {
    let active = true;
    list(brand, "parties", { placement_id: placement.id, limit: 200 })
      .then(async (r) => {
        if (!active) return;
        setRows(r.data);
        if (!readCRM) return;
        const named: Record<string, string> = {};
        for (const p of r.data) {
          const id = String(p.record_id);
          const { data } = await api.GET("/v1/brands/{brand}/records/{record}", { params: { path: { brand, record: id } } });
          named[id] = data?.name || "Unnamed record";
        }
        if (active) setNames(named);
      })
      .catch((e) => active && setError(String(e)));
    return () => {
      active = false;
    };
  }, [brand, placement.id, readCRM, reload]);

  // Companies and people both: an agency is a company, a creator a person.
  useEffect(() => {
    if (!manage || !readCRM) return;
    let active = true;
    const timer = setTimeout(async () => {
      try {
        const out: Found[] = [];
        for (const type of ["account", "person"] as const) {
          const { data, error } = await api.GET("/v1/brands/{brand}/records", {
            params: { path: { brand }, query: { type, q, limit: 25 } },
          });
          if (error) throw new Error(errorMessage(error));
          for (const r of data?.data ?? []) out.push({ id: r.id, name: r.name || r.id, type });
        }
        if (active) setFound(out);
      } catch (e) {
        if (active) setError(String(e));
      }
    }, 200);
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [brand, q, manage, readCRM]);

  const add = async () => {
    if (!record) return;
    setBusy(true);
    setError("");
    try {
      await save(brand, "parties", { placement_id: placement.id, record_id: record, role });
      setRecord("");
      setReload((n) => n + 1);
      toast("Added");
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  const remove = async (id: string) => {
    setBusy(true);
    setError("");
    const { error } = await api.DELETE("/v1/brands/{brand}/coverage/parties/{id}", { params: { path: { brand, id } } });
    setBusy(false);
    if (error) setError(errorMessage(error));
    else {
      setReload((n) => n + 1);
      toast("Removed");
    }
  };

  const deal = placement.deal_id ? String(placement.deal_id) : "";
  return (
    <div className="space-y-4 rounded-lg border p-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h3 className="font-medium">Pipeline</h3>
          <p className="mt-1 text-sm text-muted-foreground">
            {deal
              ? "This placement is worked as a CRM deal. Its stage here and the deal are the same piece of work."
              : "Link a CRM deal by editing the placement, so the partnerships team works it in the CRM pipeline."}
          </p>
        </div>
        {deal && (
          <a className="inline-flex items-center gap-1 text-sm text-brand underline" href={appPath(ws, "people", deal)} target="_blank" rel="noreferrer">
            Open deal
            <ArrowUpRight className="size-3" />
          </a>
        )}
      </div>
      <div>
        <h3 className="font-medium">People and companies</h3>
        <p className="mt-1 text-sm text-muted-foreground">
          Who delivered it: {label(String(placement.delivered_by ?? "unknown")).toLowerCase()}. Add the agency or partner, the creator, author or editor from the CRM.
        </p>
      </div>
      <ErrorText>{error}</ErrorText>
      {!readCRM ? (
        <p className="text-sm text-muted-foreground">Seeing CRM records needs CRM read access.</p>
      ) : rows.length ? (
        <ul className="divide-y text-sm">
          {rows.map((p) => (
            <li key={p.id} className="flex items-center justify-between gap-2 py-2">
              <a className="min-w-0 truncate text-brand underline" href={appPath(ws, "people", String(p.record_id))}>
                {names[String(p.record_id)] ?? "Loading…"}
              </a>
              <span className="flex items-center gap-2 text-muted-foreground">
                {label(String(p.role))}
                {manage && (
                  <button className="rounded p-1 hover:bg-muted" aria-label="Remove" disabled={busy} onClick={() => remove(p.id)}>
                    <X className="size-3.5" />
                  </button>
                )}
              </span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-muted-foreground">No one added yet.</p>
      )}
      {manage && readCRM && (
        <div className="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
          <div className="space-y-2">
            <input className={field + " w-full"} aria-label="Search the CRM" placeholder="Search companies and people…" value={q} onChange={(e) => setQ(e.target.value)} />
            <select className={field + " w-full"} aria-label="CRM record" value={record} onChange={(e) => setRecord(e.target.value)}>
              <option value="">Choose a company or person</option>
              {found.map((f) => (
                <option key={f.id} value={f.id}>
                  {f.name} ({f.type === "account" ? "company" : "person"})
                </option>
              ))}
            </select>
          </div>
          <select className={field + " self-end"} aria-label="Role" value={role} onChange={(e) => setRole(e.target.value)}>
            {roles.map((r) => (
              <option key={r} value={r}>
                {label(r)}
              </option>
            ))}
          </select>
          <Button className="self-end" disabled={busy || !record} onClick={add}>
            Add
          </Button>
        </div>
      )}
    </div>
  );
}
