import { useEffect, useState, type FormEvent } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Button, ErrorText, field, toast } from "../ui";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { track } from "@/lib/analytics";
import { plural } from "../format";

// Adding and changing customer records: the add and edit dialog, an input
// for each kind of field in the catalog, erasing a record, and the forms
// for emails and ids and for consent. People.tsx shows them.

type Rec = Schemas["Record"];
type Field = Schemas["Field"];
type Option = Schemas["CatalogOption"];

export type Catalog = {
  fields: (type: string) => Field[];
  options: (f: Field) => Option[];
};

// Fields a person can set here: computed ones are Outflare's, and a
// source-owned one only its source writes.
export const editable = (f: Field) => f.write_policy !== "computed" && f.write_policy !== "source_owned";

// The fields the add dialog starts with; the rest are a click away.
const starters: Record<string, string[]> = {
  person: ["first_name", "last_name", "job_title", "companies", "lifecycle_stage"],
  account: ["website", "industry", "company_size", "lifecycle_stage"],
};

const empty = (v: unknown) => v === null || v === undefined || v === "" || (Array.isArray(v) && v.length === 0);
const same = (a: unknown, b: unknown) => (empty(a) && empty(b)) || JSON.stringify(a) === JSON.stringify(b);

// A timestamp as the browser's datetime-local input wants it, in local time.
function localInput(iso: unknown): string {
  if (typeof iso !== "string" || !iso) return "";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}

// FieldInput edits one value the way its field's type is written
// (FieldValueType in api/openapi.yaml).
export function FieldInput({ ws, f, value, names, cat, onChange }: { ws: string; f: Field; value: unknown; names: Record<string, string>; cat: Catalog; onChange: (v: unknown) => void }) {
  const id = `field-${f.key}`;
  const cls = `${field} w-full`;
  switch (f.value_type) {
    case "select":
    case "status": {
      const opts = cat.options(f);
      if (f.multiple) {
        const list = Array.isArray(value) ? (value as string[]) : [];
        return (
          <select id={id} multiple className={`${cls} h-24`} value={list} onChange={(e) => onChange([...e.target.selectedOptions].map((o) => o.value))}>
            {opts.map((o) => <option key={o.key} value={o.key}>{o.label}</option>)}
          </select>
        );
      }
      return (
        <select id={id} className={cls} value={(value as string) ?? ""} onChange={(e) => onChange(e.target.value || null)}>
          <option value="">–</option>
          {opts.map((o) => <option key={o.key} value={o.key}>{o.label}</option>)}
        </select>
      );
    }
    case "checkbox":
      return <input id={id} type="checkbox" className="size-4" checked={value === true} onChange={(e) => onChange(e.target.checked)} />;
    case "number":
      return <input id={id} type="number" step="any" className={cls} value={typeof value === "number" ? value : ""} onChange={(e) => onChange(e.target.value === "" ? null : Number(e.target.value))} />;
    case "rating":
      return (
        <select id={id} className={cls} value={typeof value === "number" ? String(value) : ""} onChange={(e) => onChange(e.target.value === "" ? null : Number(e.target.value))}>
          <option value="">–</option>
          {[0, 1, 2, 3, 4, 5].map((n) => <option key={n} value={n}>{"★".repeat(n) || "0"}</option>)}
        </select>
      );
    case "date":
      return <input id={id} type="date" className={cls} value={(value as string) ?? ""} onChange={(e) => onChange(e.target.value || null)} />;
    case "timestamp":
      return <input id={id} type="datetime-local" className={cls} value={localInput(value)} onChange={(e) => onChange(e.target.value ? new Date(e.target.value).toISOString() : null)} />;
    case "currency": {
      const c = (value ?? {}) as { amount_minor?: number; currency?: string };
      const set = (amount: string, currency: string) => onChange(amount === "" ? null : { amount_minor: Math.round(Number(amount) * 100), currency: currency.toUpperCase() || "USD" });
      return (
        <div className="flex gap-2">
          <input id={id} type="number" step="0.01" aria-label={`${f.label} amount`} className={`${field} min-w-0 flex-1`} value={typeof c.amount_minor === "number" ? c.amount_minor / 100 : ""} onChange={(e) => set(e.target.value, c.currency ?? "USD")} />
          <input aria-label={`${f.label} currency`} maxLength={3} className={`${field} w-16 uppercase`} value={c.currency ?? "USD"} onChange={(e) => typeof c.amount_minor === "number" && set(String(c.amount_minor / 100), e.target.value)} />
        </div>
      );
    }
    case "location": {
      const l = (value ?? {}) as Record<string, string>;
      const set = (k: string, v: string) => {
        const next = { ...l, [k]: v };
        onChange(Object.values(next).some(Boolean) ? next : null);
      };
      return (
        <div className="grid grid-cols-3 gap-2">
          {(["city", "region", "country"] as const).map((k) => <input key={k} aria-label={`${f.label} ${k}`} placeholder={k[0].toUpperCase() + k.slice(1)} className={`${field} min-w-0`} value={l[k] ?? ""} onChange={(e) => set(k, e.target.value)} />)}
        </div>
      );
    }
    case "record_reference":
      return <RecordPicker ws={ws} type={f.target_type ?? "account"} multiple={f.multiple} value={value} names={names} onChange={onChange} />;
    case "actor_reference":
      return <MemberPicker ws={ws} id={id} value={value} onChange={onChange} />;
  }
  return <input id={id} type={f.value_type === "email" ? "email" : f.value_type === "url" ? "url" : f.value_type === "phone" ? "tel" : "text"} className={cls} value={(value as string) ?? ""} onChange={(e) => onChange(e.target.value)} />;
}

// RecordPicker sets a reference to other records, such as a person's
// companies: what is set, and a search to add one.
function RecordPicker({ ws, type, multiple, value, names, onChange }: { ws: string; type: string; multiple: boolean; value: unknown; names: Record<string, string>; onChange: (v: unknown) => void }) {
  const ids = (Array.isArray(value) ? value : value ? [value] : []) as string[];
  const [q, setQ] = useState("");
  const [found, setFound] = useState<Rec[]>([]);
  const [known, setKnown] = useState<Record<string, string>>({});
  useEffect(() => {
    if (!q.trim()) return setFound([]);
    const t = setTimeout(() => {
      api.GET("/v1/workspaces/{ws}/records", { params: { path: { ws }, query: { type, q: q.trim(), limit: 8 } } }).then(({ data }) => setFound(data?.data ?? []));
    }, 200);
    return () => clearTimeout(t);
  }, [ws, type, q]);
  const name = (id: string) => known[id] ?? names[id] ?? "A record";
  const set = (next: string[]) => onChange(multiple ? next : (next[0] ?? null));
  return (
    <div className="space-y-1.5">
      {ids.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {ids.map((id) => (
            <span key={id} className="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[12px]">
              {name(id)}
              <button type="button" aria-label={`Remove ${name(id)}`} className="text-muted-foreground hover:text-foreground" onClick={() => set(ids.filter((x) => x !== id))}>×</button>
            </span>
          ))}
        </div>
      )}
      {(multiple || ids.length === 0) && (
        <div className="relative">
          <input type="search" className={`${field} w-full`} placeholder={type === "account" ? "Find a company" : "Find a record"} value={q} onChange={(e) => setQ(e.target.value)} />
          {found.length > 0 && (
            <ul className="absolute z-10 mt-1 w-full rounded-md border bg-popover p-1 text-sm shadow-md">
              {found.filter((r) => !ids.includes(r.id)).map((r) => (
                <li key={r.id}>
                  <button
                    type="button"
                    className="w-full rounded px-2 py-1 text-left hover:bg-muted"
                    onClick={() => {
                      setKnown({ ...known, [r.id]: r.name || r.identities[0]?.value || "Unnamed" });
                      set([...ids, r.id]);
                      setQ("");
                    }}
                  >
                    {r.name || r.identities[0]?.value || "Unnamed"}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

function MemberPicker({ ws, id, value, onChange }: { ws: string; id: string; value: unknown; onChange: (v: unknown) => void }) {
  const [members, setMembers] = useState<Schemas["Member"][]>([]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }).then(({ data }) => setMembers(data?.data ?? []));
  }, [ws]);
  return (
    <select id={id} className={`${field} w-full`} value={(value as string) ?? ""} onChange={(e) => onChange(e.target.value || null)}>
      <option value="">–</option>
      {members.map((m) => <option key={m.user_id} value={m.user_id}>{m.name || m.email}</option>)}
    </select>
  );
}

// RecordDialog adds a person or company, or changes one's name and fields.
export function RecordDialog({ ws, type, rec, cat, names, onClose, onSaved }: {
  ws: string;
  type: string;
  rec: Rec | null;
  cat: Catalog;
  names: Record<string, string>;
  onClose: () => void;
  onSaved: (r: Rec) => void;
}) {
  const person = type === "person";
  const fields = cat.fields(type).filter(editable);
  const [name, setName] = useState(rec?.name ?? "");
  const [values, setValues] = useState<Record<string, unknown>>(() => ({ ...(rec?.attributes ?? {}) }));
  const [ident, setIdent] = useState("");
  const [verified, setVerified] = useState(false);
  const [all, setAll] = useState(!!rec);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [stale, setStale] = useState(false);

  const changed = fields.filter((f) => !same(values[f.key], rec?.attributes[f.key])).map((f) => f.key);
  const shown = all ? fields : fields.filter((f) => starters[type]?.includes(f.key) || !empty(values[f.key]));

  function close() {
    if (changed.length || (!rec && (name || ident))) track("ui.record_dialog_closed", { mode: rec ? "edit" : "add", type, changed: changed.length });
    onClose();
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const attributes = Object.fromEntries(changed.map((k) => [k, empty(values[k]) ? null : values[k]]));
    let res;
    if (rec) {
      res = await api.PATCH("/v1/workspaces/{ws}/records/{record}", {
        params: { path: { ws, record: rec.id } },
        body: { version: rec.version, ...(name.trim() !== rec.name ? { name: name.trim() } : {}), ...(changed.length ? { attributes } : {}) },
      });
    } else {
      const value = ident.trim();
      const ns = person ? "email" : "domain";
      res = await api.POST("/v1/workspaces/{ws}/records", {
        params: { path: { ws } },
        body: {
          type,
          origin: "manual",
          ...(name.trim() ? { name: name.trim() } : {}),
          ...(changed.length ? { attributes: Object.fromEntries(Object.entries(attributes).filter(([, v]) => v !== null)) } : {}),
          ...(value ? { identities: [{ namespace: ns, value, source: "manual", ...(person ? { verified } : {}) }] } : {}),
        },
      });
    }
    setBusy(false);
    if (res.error) {
      setStale((res.error as Schemas["Error"]).error?.code === "record_changed");
      return setError(errorMessage(res.error));
    }
    if (!res.data || !("id" in res.data)) return onClose(); // held for a mission's approval
    const saved = res.data as Rec;
    const kept = saved.skipped ?? [];
    toast(
      kept.length
        ? `Saved. ${kept.map((s) => fields.find((f) => f.key === s.field)?.label ?? s.field).join(", ")} kept as ${kept[0].kept_source} set ${kept.length === 1 ? "it" : "them"}.`
        : rec ? "Saved." : person ? "Added the person." : "Added the company.",
    );
    onSaved(saved);
    onClose();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && close()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <form className="space-y-3 text-sm" onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>{rec ? `Edit ${rec.name || "record"}` : person ? "Add a person" : "Add a company"}</DialogTitle>
            <DialogDescription>
              {rec
                ? "What you type here wins over values from enrichment, a CRM or your product, unless a field says otherwise."
                : person
                  ? "Look them up first if they may already be here: someone with the same verified email can't be added twice."
                  : "The domain says which people work there, from their email addresses."}
            </DialogDescription>
          </DialogHeader>
          {(!person || rec) && (
            <label className="block">
              <span className="mb-1 block font-medium">Name</span>
              <input required={!rec && !person} autoFocus className={`${field} w-full`} value={name} onChange={(e) => setName(e.target.value)} />
              {person && rec && <span className="mt-1 block text-xs text-muted-foreground">Leave it empty to use their first and last name.</span>}
            </label>
          )}
          {!rec && (
            <div>
              <label className="block">
                <span className="mb-1 block font-medium">{person ? "Email" : "Domain"} <span className="font-normal text-muted-foreground">(optional)</span></span>
                <input type={person ? "email" : "text"} autoFocus={person} className={`${field} w-full`} placeholder={person ? "ana@acme.example" : "acme.example"} value={ident} onChange={(e) => setIdent(e.target.value)} />
              </label>
              {person && ident && (
                <label className="mt-1.5 flex items-center gap-1.5 text-xs">
                  <input type="checkbox" checked={verified} onChange={(e) => setVerified(e.target.checked)} /> They proved it's theirs (it then claims the address)
                </label>
              )}
            </div>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            {shown.map((f) => (
              <div key={f.key} className={f.value_type === "record_reference" || f.value_type === "location" || f.value_type === "url" ? "sm:col-span-2" : ""}>
                <label htmlFor={`field-${f.key}`} className="mb-1 block font-medium" title={f.description || undefined}>{f.label}</label>
                <FieldInput ws={ws} f={f} value={values[f.key]} names={names} cat={cat} onChange={(v) => setValues({ ...values, [f.key]: v })} />
              </div>
            ))}
          </div>
          {!all && <button type="button" className="text-xs underline" onClick={() => setAll(true)}>Show every field</button>}
          <ErrorText>{error}</ErrorText>
          {stale && <p className="text-xs text-muted-foreground">Close this and open the record again to see the change, then make yours.</p>}
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" onClick={close}>Cancel</Button>
            <Button disabled={busy}>{busy ? "Saving…" : rec ? "Save" : "Add"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// eraseRecord asks, then deletes a record for good. True when it went.
export async function eraseRecord(ws: string, rec: Rec, label: string): Promise<boolean> {
  const person = rec.type === "person";
  const r = await ask({
    title: `Delete ${label} for good?`,
    body: (
      <>
        <p>
          {person
            ? "This erases them: their record and emails and ids, their leads, the emails sent to them and their replies, meetings, product events, website visits and consent. Deals and payments stay, without their name."
            : "This erases the company's record, its fields and history. Its people and deals stay, no longer linked to it."}{" "}
          It can't be undone. To hide {person ? "them" : "it"} and keep the history, archive instead.
        </p>
        <p>{person ? "If they asked never to be contacted, also add their emails to the do-not-contact list, so a later import can't email them again." : "Adding its domains to the do-not-contact list stops email to anyone there."}</p>
      </>
    ),
    choices: [
      { label: "Cancel", value: "cancel", variant: "ghost" },
      { label: "Delete", value: "delete", variant: "ghost" },
      { label: "Delete and never contact", value: "suppress", variant: "danger-solid" },
    ],
  });
  if (!r || r.choice === "cancel") return false;
  const { data, error } = await api.DELETE("/v1/workspaces/{ws}/records/{record}", {
    params: { path: { ws, record: rec.id }, query: { version: rec.version, suppress: r.choice === "suppress" } },
  });
  if (error) {
    toast(errorMessage(error), "error");
    return false;
  }
  if (!data || !("erased" in data)) return true; // held for a mission's approval
  const activity = Object.entries(data.erased).filter(([t]) => t !== "records").reduce((n, [, c]) => n + c, 0);
  toast(`Deleted ${label}${activity ? ` and ${plural(activity, "thing")} about ${person ? "them" : "it"}` : ""}${data.suppressed ? `; ${(person ? plural(data.suppressed, "address", "addresses") : plural(data.suppressed, "domain"))} added to do not contact` : ""}.`);
  return true;
}

// AddIdentity adds an email, phone or id to a record.
export function AddIdentity({ ws, rec, onAdded }: { ws: string; rec: Rec; onAdded: () => void }) {
  const [open, setOpen] = useState(false);
  const [ns, setNs] = useState(rec.type === "person" ? "email" : "domain");
  const [value, setValue] = useState("");
  const [verified, setVerified] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  if (!open) return <button className="mt-3 text-xs underline" onClick={() => setOpen(true)}>Add an email or id</button>;
  const checks = ns === "email" || ns === "phone";

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/records/{record}/identities", {
      params: { path: { ws, record: rec.id } },
      body: { namespace: ns, value: value.trim(), source: "manual", ...(checks ? { verified } : {}) },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setValue("");
    setError("");
    setOpen(false);
    onAdded();
  }

  return (
    <form className="mt-3 space-y-2 border-t pt-3 text-sm" onSubmit={submit}>
      <div className="flex gap-2">
        <select aria-label="Kind" className={field} value={ns} onChange={(e) => setNs(e.target.value)}>
          {(rec.type === "person" ? ["email", "phone", "user_id"] : ["domain"]).map((n) => <option key={n} value={n}>{({ email: "Email", phone: "Phone", user_id: "User id", domain: "Domain" } as Record<string, string>)[n]}</option>)}
        </select>
        <input required autoFocus aria-label="Value" className={`${field} min-w-0 flex-1`} placeholder={ns === "phone" ? "+61 412 345 678" : ns === "domain" ? "acme.example" : ns === "email" ? "ana@acme.example" : ""} value={value} onChange={(e) => setValue(e.target.value)} />
      </div>
      {checks && (
        <label className="flex items-center gap-1.5 text-xs">
          <input type="checkbox" checked={verified} onChange={(e) => setVerified(e.target.checked)} /> They proved it's theirs
        </label>
      )}
      <ErrorText>{error}</ErrorText>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={() => setOpen(false)}>Cancel</Button>
        <Button disabled={busy}>{busy ? "Adding…" : "Add"}</Button>
      </div>
    </form>
  );
}

const channels = ["email", "sms", "push", "whatsapp", "in_app", "ads"] as const;

// RecordConsent notes that a person agreed to something or took it back,
// such as on a call.
export function RecordConsent({ ws, id, onAdded }: { ws: string; id: string; onAdded: () => void }) {
  const [open, setOpen] = useState(false);
  const [channel, setChannel] = useState<(typeof channels)[number]>("email");
  const [topic, setTopic] = useState("");
  const [status, setStatus] = useState<"granted" | "withdrawn">("granted");
  const [error, setError] = useState("");
  if (!open) return <button className="mt-3 text-xs underline" onClick={() => setOpen(true)}>Record consent</button>;

  async function submit(e: FormEvent) {
    e.preventDefault();
    const { error } = await api.POST("/v1/workspaces/{ws}/records/{record}/consents", {
      params: { path: { ws, record: id } },
      body: { channel, status, source: "manual", ...(topic.trim() ? { topic: topic.trim() } : {}) },
    });
    if (error) return setError(errorMessage(error));
    setOpen(false);
    setTopic("");
    setError("");
    onAdded();
  }

  return (
    <form className="mt-3 space-y-2 border-t pt-3 text-sm" onSubmit={submit}>
      <div className="grid grid-cols-2 gap-2">
        <select aria-label="Channel" className={field} value={channel} onChange={(e) => setChannel(e.target.value as typeof channel)}>
          {channels.map((c) => <option key={c} value={c}>{c.replace("_", " ")}</option>)}
        </select>
        <select aria-label="Status" className={field} value={status} onChange={(e) => setStatus(e.target.value as typeof status)}>
          <option value="granted">Agreed</option>
          <option value="withdrawn">Took it back</option>
        </select>
      </div>
      <input aria-label="Topic" className={`${field} w-full`} placeholder="Topic key, such as product_updates (empty for the whole channel)" value={topic} onChange={(e) => setTopic(e.target.value)} />
      <ErrorText>{error}</ErrorText>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={() => setOpen(false)}>Cancel</Button>
        <Button>Save</Button>
      </div>
    </form>
  );
}
