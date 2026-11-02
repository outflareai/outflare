import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Button, confirmAction, ErrorText, field, toast } from "../ui";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { track } from "@/lib/analytics";
import {
  Check,
  ChevronsUpDown,
  Loader2,
  Plus,
  X,
  Building2,
} from "lucide-react";
import {
  Command,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { useBrand } from "@/lib/brand";
import { useCatalog, displayName, identity } from "@/lib/records";
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
export const editable = (f: Field) =>
  f.write_policy !== "computed" && f.write_policy !== "source_owned";

// The fields the add dialog starts with; the rest are a click away.
const starters: Record<string, string[]> = {
  person: [
    "first_name",
    "last_name",
    "job_title",
    "companies",
    "lifecycle_stage",
  ],
  account: ["website", "industry", "company_size", "lifecycle_stage"],
  deal: [
    "associated_company",
    "associated_people",
    "stage",
    "value",
    "close_date",
    "owner",
  ],
};

const empty = (v: unknown) =>
  v === null ||
  v === undefined ||
  v === "" ||
  (Array.isArray(v) && v.length === 0);
const same = (a: unknown, b: unknown) =>
  (empty(a) && empty(b)) || JSON.stringify(a) === JSON.stringify(b);

// A timestamp as the browser's datetime-local input wants it, in local time.
function localInput(iso: unknown): string {
  if (typeof iso !== "string" || !iso) return "";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
}

// FieldInput edits one value the way its field's type is written
// (FieldValueType in api/openapi.yaml).
export function FieldInput({ ws, brand, f, value, names, cat, onChange }: { ws: string; brand?: string; f: Field; value: unknown; names: Record<string, string>; cat: Catalog; onChange: (v: unknown) => void }) {
  const id = `field-${f.key}`;
  const cls = `${field} w-full`;
  switch (f.value_type) {
    case "select":
    case "status":
      return (
        <ChoicePicker
          id={id}
          label={f.label}
          options={cat.options(f).map((o) => ({ key: o.key, label: o.label }))}
          value={value}
          multiple={f.multiple}
          onChange={onChange}
        />
      );
    case "checkbox":
      return (
        <input
          id={id}
          type="checkbox"
          className="size-4"
          checked={value === true}
          onChange={(e) => onChange(e.target.checked)}
        />
      );
    case "number":
      return (
        <input
          id={id}
          type="number"
          step="any"
          className={cls}
          value={typeof value === "number" ? value : ""}
          onChange={(e) =>
            onChange(e.target.value === "" ? null : Number(e.target.value))
          }
        />
      );
    case "rating":
      return (
        <select
          id={id}
          className={cls}
          value={typeof value === "number" ? String(value) : ""}
          onChange={(e) =>
            onChange(e.target.value === "" ? null : Number(e.target.value))
          }
        >
          <option value="">–</option>
          {[0, 1, 2, 3, 4, 5].map((n) => (
            <option key={n} value={n}>
              {"★".repeat(n) || "0"}
            </option>
          ))}
        </select>
      );
    case "date":
      return (
        <input
          id={id}
          type="date"
          className={cls}
          value={(value as string) ?? ""}
          onChange={(e) => onChange(e.target.value || null)}
        />
      );
    case "timestamp":
      return (
        <input
          id={id}
          type="datetime-local"
          className={cls}
          value={localInput(value)}
          onChange={(e) =>
            onChange(
              e.target.value ? new Date(e.target.value).toISOString() : null,
            )
          }
        />
      );
    case "currency": {
      const c = (value ?? {}) as { amount_minor?: number; currency?: string };
      const set = (amount: string, currency: string) =>
        onChange(
          amount === ""
            ? null
            : {
                amount_minor: Math.round(Number(amount) * 100),
                currency: currency.toUpperCase() || "USD",
              },
        );
      return (
        <div className="flex gap-2">
          <input
            id={id}
            type="number"
            step="0.01"
            aria-label={`${f.label} amount`}
            className={`${field} min-w-0 flex-1`}
            value={
              typeof c.amount_minor === "number" ? c.amount_minor / 100 : ""
            }
            onChange={(e) => set(e.target.value, c.currency ?? "USD")}
          />
          <input
            aria-label={`${f.label} currency`}
            maxLength={3}
            className={`${field} w-16 uppercase`}
            value={c.currency ?? "USD"}
            onChange={(e) =>
              typeof c.amount_minor === "number" &&
              set(String(c.amount_minor / 100), e.target.value)
            }
          />
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
          {(["city", "region", "country"] as const).map((k) => (
            <input
              key={k}
              aria-label={`${f.label} ${k}`}
              placeholder={k[0].toUpperCase() + k.slice(1)}
              className={`${field} min-w-0`}
              value={l[k] ?? ""}
              onChange={(e) => set(k, e.target.value)}
            />
          ))}
        </div>
      );
    }
    case "record_reference":
      return (
        <RecordPicker
          ws={ws}
          brand={brand}
          id={id}
          type={f.target_type ?? "account"}
          multiple={f.multiple}
          value={value}
          names={names}
          onChange={onChange}
          canCreate
        />
      );
    case "actor_reference":
      return (
        <MemberPicker
          ws={ws}
          id={id}
          value={value}
          multiple={f.multiple}
          onChange={onChange}
        />
      );
  }
  if (f.multiple)
    return (
      <MultiValueInput
        id={id}
        label={f.label}
        value={value}
        onChange={onChange}
      />
    );
  return (
    <input
      id={id}
      type={
        f.value_type === "email"
          ? "email"
          : f.value_type === "url"
            ? "url"
            : f.value_type === "phone"
              ? "tel"
              : "text"
      }
      className={cls}
      value={(value as string) ?? ""}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}

// RecordPicker sets a reference to other records, such as a person's
// companies: what is set, and a search to add one.
export function RecordPicker({
  ws,
  brand,
  id,
  type,
  multiple = false,
  value,
  names = {},
  onChange,
  onSelect,
  canCreate = false,
  placeholder,
}: {
  ws: string;
  brand?: string;
  id?: string;
  type: string;
  multiple?: boolean;
  value: unknown;
  names?: Record<string, string>;
  onChange: (v: unknown) => void;
  onSelect?: (r: Rec) => void;
  canCreate?: boolean;
  placeholder?: string;
}) {
  const ids = (Array.isArray(value) ? value : value ? [value] : []) as string[];
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [found, setFound] = useState<Rec[]>([]);
  const [known, setKnown] = useState<Record<string, Rec>>({});
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const { brand: picked } = useBrand(ws);
  const recordBrand = brand || picked?.id || "";
  const cat = useCatalog(ws, recordBrand);
  const [listId, setListId] = useState<string>();
  const listRef = useCallback((element: HTMLDivElement | null) => {
    if (element) setListId(element.id);
  }, []);
  const idsKey = ids.join(",");
  useEffect(() => {
    let active = true;
    for (const record of ids.filter((key) => !known[key])) {
      api
        .GET("/v1/brands/{brand}/records/{record}", {
          params: { path: { brand: recordBrand, record } },
        })
        .then(({ data }) => {
          if (active && data)
            setKnown((prev) => ({ ...prev, [data.id]: data }));
        })
        .catch(() => {});
    }
    return () => {
      active = false;
    };
  }, [recordBrand, idsKey]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    setLoading(true);
    setError("");
    setFound([]);
    const timer = setTimeout(async () => {
      try {
        const { data, error } = await api.GET("/v1/brands/{brand}/records", {
          params: {
            path: { brand: recordBrand },
            query: { type, q: q.trim(), limit: 20 },
          },
          signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        if (!data) setError(errorMessage(error));
        else setFound(data.data);
      } catch (e) {
        if (!controller.signal.aborted) setError(errorMessage(e));
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    }, 180);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [recordBrand, type, q, open]);
  const name = (key: string) =>
    known[key] ? displayName(known[key]) : (names[key] ?? "Loading record…");
  const set = (next: string[]) => onChange(multiple ? next : (next[0] ?? null));
  const choose = (r: Rec) => {
    setKnown((prev) => ({ ...prev, [r.id]: r }));
    set(multiple ? [...ids.filter((key) => key !== r.id), r.id] : [r.id]);
    onSelect?.(r);
    setQ("");
    setOpen(false);
  };
  const label =
    cat.catalog?.types.find((t) => t.key === type)?.name.toLowerCase() ??
    "record";
  return (
    <div className="space-y-2">
      {multiple && ids.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {ids.map((key) => (
            <span
              key={key}
              className="inline-flex items-center gap-1.5 rounded-md border bg-muted/40 py-1 pl-2 text-xs"
            >
              {name(key)}
              <button
                type="button"
                className="rounded p-1 hover:bg-muted"
                aria-label={`Remove ${name(key)}`}
                onClick={() => set(ids.filter((x) => x !== key))}
              >
                <X className="size-3" />
              </button>
            </span>
          ))}
        </div>
      )}
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <button
            id={id}
            type="button"
            role="combobox"
            aria-haspopup="listbox"
            aria-expanded={open}
            aria-controls={listId}
            className={`${field} flex w-full items-center gap-2 text-left`}
          >
            {type === "account" && (
              <Building2 className="size-4 shrink-0 text-muted-foreground" />
            )}
            <span className="min-w-0 flex-1 truncate">
              {!multiple && ids.length
                ? name(ids[0])
                : (placeholder ??
                  `Find ${label === "person" ? "a person" : `a ${label}`}…`)}
            </span>
            <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" />
          </button>
        </PopoverTrigger>
        <PopoverContent className="w-[min(24rem,85vw)] p-0" align="start">
          <Command shouldFilter={false}>
            <CommandInput
              placeholder={`Search ${label === "company" ? "companies" : "records"}…`}
              value={q}
              onValueChange={setQ}
            />
            <CommandList ref={listRef}>
              {loading && (
                <div
                  role="status"
                  className="flex items-center gap-2 p-3 text-sm text-muted-foreground"
                >
                  <Loader2 className="size-3.5 animate-spin" />
                  Searching…
                </div>
              )}
              {error && (
                <div role="alert" className="p-3 text-sm text-destructive">
                  {error}
                  <button
                    type="button"
                    className="ml-2 underline"
                    onClick={() => {
                      setOpen(false);
                      setTimeout(() => setOpen(true), 0);
                    }}
                  >
                    Retry
                  </button>
                </div>
              )}
              {!loading && !error && found.length === 0 && (
                <p className="p-3 text-sm text-muted-foreground">
                  {q ? "No matching records." : "No records yet."}
                </p>
              )}
              {found.map((r) => (
                <CommandItem key={r.id} value={r.id} onSelect={() => choose(r)}>
                  <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-xs font-medium">
                    {displayName(r).slice(0, 2).toUpperCase()}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate font-medium">
                      {displayName(r)}
                    </span>
                    <span className="block truncate text-xs text-muted-foreground">
                      {identity(r, r.type === "account" ? "domain" : "email") ??
                        r.type}
                    </span>
                  </span>
                  {ids.includes(r.id) && <Check className="size-4" />}
                </CommandItem>
              ))}
              {canCreate && cat.catalog && (
                <CommandItem
                  value="create-record"
                  onSelect={() => {
                    setOpen(false);
                    setCreating(true);
                  }}
                  className="border-t"
                >
                  <Plus className="size-4" />
                  Create a new {label}
                </CommandItem>
              )}
              {!multiple && ids.length > 0 && (
                <CommandItem
                  value="clear-record"
                  onSelect={() => {
                    set([]);
                    setOpen(false);
                  }}
                >
                  <X className="size-4" />
                  Clear selection
                </CommandItem>
              )}
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
      {creating && (
        <RecordDialog
          brand={recordBrand}
          ws={ws}
          type={type}
          rec={null}
          cat={cat}
          names={names}
          onClose={() => setCreating(false)}
          onSaved={(r) => {
            choose(r);
            setCreating(false);
          }}
        />
      )}
    </div>
  );
}

function ChoicePicker({
  id,
  label,
  options,
  value,
  multiple,
  onChange,
}: {
  id: string;
  label: string;
  options: { key: string; label: string }[];
  value: unknown;
  multiple: boolean;
  onChange: (v: unknown) => void;
}) {
  const [open, setOpen] = useState(false);
  const ids = (Array.isArray(value) ? value : value ? [value] : []).map(String);
  const selected = options.filter((o) => ids.includes(o.key));
  const [listId, setListId] = useState<string>();
  const listRef = useCallback((element: HTMLDivElement | null) => {
    if (element) setListId(element.id);
  }, []);
  return (
    <div className="space-y-2">
      {multiple && selected.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {selected.map((o) => (
            <span
              key={o.key}
              className="inline-flex items-center gap-1 rounded-md border bg-muted/40 pl-2 text-xs"
            >
              {o.label}
              <button
                type="button"
                className="p-1.5"
                aria-label={`Remove ${o.label}`}
                onClick={() => onChange(ids.filter((v) => v !== o.key))}
              >
                <X className="size-3" />
              </button>
            </span>
          ))}
        </div>
      )}
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <button
            type="button"
            id={id}
            role="combobox"
            aria-haspopup="listbox"
            aria-expanded={open}
            aria-controls={listId}
            className={`${field} flex w-full items-center justify-between gap-2 text-left`}
          >
            <span
              className={
                selected.length && !multiple ? "" : "text-muted-foreground"
              }
            >
              {multiple
                ? `Choose ${label.toLowerCase()}…`
                : (selected[0]?.label ?? `Choose ${label.toLowerCase()}…`)}
            </span>
            <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" />
          </button>
        </PopoverTrigger>
        <PopoverContent className="w-[min(22rem,85vw)] p-0" align="start">
          <Command>
            <CommandInput placeholder={`Search ${label.toLowerCase()}…`} />
            <CommandList ref={listRef}>
              {options.map((o) => (
                <CommandItem
                  key={o.key}
                  value={o.key}
                  keywords={[o.label]}
                  onSelect={() => {
                    if (multiple)
                      onChange(
                        ids.includes(o.key)
                          ? ids.filter((v) => v !== o.key)
                          : [...ids, o.key],
                      );
                    else {
                      onChange(o.key);
                      setOpen(false);
                    }
                  }}
                >
                  <span className="min-w-0 flex-1">{o.label}</span>
                  {ids.includes(o.key) && <Check className="size-3.5" />}
                </CommandItem>
              ))}
              {ids.length > 0 && (
                <CommandItem
                  value="__clear"
                  onSelect={() => {
                    onChange(multiple ? [] : null);
                    setOpen(false);
                  }}
                >
                  <X className="size-3.5" />
                  Clear selection
                </CommandItem>
              )}
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
    </div>
  );
}
function MultiValueInput({
  id,
  label,
  value,
  onChange,
}: {
  id: string;
  label: string;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const values = Array.isArray(value) ? value.map(String) : [];
  const [draft, setDraft] = useState("");
  const add = () => {
    const next = draft
      .split(/[,;\n]/)
      .map((v) => v.trim())
      .filter(Boolean);
    if (next.length) onChange([...new Set([...values, ...next])]);
    setDraft("");
  };
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-1">
        {values.map((v) => (
          <span
            key={v}
            className="inline-flex items-center gap-1 rounded-md border bg-muted/40 pl-2 text-xs"
          >
            {v}
            <button
              type="button"
              className="p-1.5"
              aria-label={`Remove ${v}`}
              onClick={() => onChange(values.filter((x) => x !== v))}
            >
              <X className="size-3" />
            </button>
          </span>
        ))}
      </div>
      <input
        id={id}
        className={`${field} w-full`}
        value={draft}
        placeholder={`Add ${label.toLowerCase()}, then press Enter`}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={add}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            add();
          }
        }}
      />
    </div>
  );
}
export function MemberPicker({
  ws,
  id,
  value,
  multiple,
  onChange,
}: {
  ws: string;
  id: string;
  value: unknown;
  multiple: boolean;
  onChange: (v: unknown) => void;
}) {
  const [members, setMembers] = useState<Schemas["Member"][]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    api
      .GET("/v1/workspaces/{ws}/members", {
        params: { path: { ws } },
        signal: controller.signal,
      })
      .then(({ data, error }) => {
        if (controller.signal.aborted) return;
        if (data) setMembers(data.data ?? []);
        else setError(errorMessage(error));
        setLoading(false);
      })
      .catch((e) => {
        if (!controller.signal.aborted) {
          setError(errorMessage(e));
          setLoading(false);
        }
      });
    return () => controller.abort();
  }, [ws]);
  if (loading)
    return (
      <p role="status" className="py-2 text-xs text-muted-foreground">
        Loading team…
      </p>
    );
  if (error) return <ErrorText>{error}</ErrorText>;
  return (
    <ChoicePicker
      id={id}
      label="team member"
      options={members
        .filter((m) => !!m.user_id)
        .map((m) => ({
          key: m.user_id!,
          label:
            m.name && m.email
              ? `${m.name} · ${m.email}`
              : m.name || m.email || "Team member",
        }))}
      value={value}
      multiple={multiple}
      onChange={onChange}
    />
  );
}

// RecordDialog adds a person or company, or changes one's name and fields.
export function RecordDialog({ ws, brand: specifiedBrand, type, rec, cat, names, onClose, onSaved }: {
  ws: string;
  // The brand the record is made in, or is in.
  brand?: string;
  type: string;
  rec: Rec | null;
  cat: Catalog;
  names: Record<string, string>;
  onClose: () => void;
  onSaved: (r: Rec) => void;
}) {
  const person = type === "person";
  const { brand: pickedBrand } = useBrand(ws);
  const brand = rec?.brand_id || specifiedBrand || pickedBrand?.id || "";

  const typeLabel =
    type === "account" ? "company" : type === "person" ? "person" : "record";
  const fields = cat.fields(type).filter(editable);
  const listEntry =
    type.startsWith("list_") &&
    fields.some((f) => f.key === "outflare_parent_record");
  const [name, setName] = useState(rec?.name ?? "");
  const [values, setValues] = useState<Record<string, unknown>>(() => ({
    ...(rec?.attributes ?? {}),
    ...(!rec && (type === "deal" || listEntry)
      ? {
          stage:
            (fields.find((f) => f.key === "stage")
              ? cat.options(fields.find((f) => f.key === "stage")!)
              : []
            ).find((o) => o.key === "lead")?.key ??
            (fields.find((f) => f.key === "stage")
              ? cat.options(fields.find((f) => f.key === "stage")!)
              : [])[0]?.key ??
            null,
        }
      : {}),
  }));
  const [ident, setIdent] = useState("");
  const [verified, setVerified] = useState(false);
  const [all, setAll] = useState(!!rec || !starters[type]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [stale, setStale] = useState(false);

  const changed = fields
    .filter((f) => !same(values[f.key], rec?.attributes[f.key]))
    .map((f) => f.key);
  const shown = all
    ? fields
    : fields.filter(
        (f) => starters[type]?.includes(f.key) || !empty(values[f.key]),
      );

  const personRef =
    type === "deal" &&
    Array.isArray(values.associated_people) &&
    values.associated_people.length === 1
      ? String(values.associated_people[0])
      : "";
  useEffect(() => {
    if (!personRef || values.associated_company) return;
    const controller = new AbortController();
    api
      .GET("/v1/workspaces/{ws}/records/{record}", {
        params: { path: { ws, record: personRef } },
        signal: controller.signal,
      })
      .then(({ data }) => {
        if (controller.signal.aborted) return;
        const companies = data?.attributes.companies;
        if (Array.isArray(companies) && companies.length === 1)
          setValues((prev) =>
            prev.associated_company
              ? prev
              : { ...prev, associated_company: String(companies[0]) },
          );
      })
      .catch((error) => {
        if (!controller.signal.aborted) setError(errorMessage(error));
      });
    return () => controller.abort();
  }, [ws, personRef, values.associated_company]);

  const parentRecord =
    listEntry && typeof values.outflare_parent_record === "string"
      ? values.outflare_parent_record
      : "";
  useEffect(() => {
    if (!parentRecord || rec) return;
    const controller = new AbortController();
    api
      .GET("/v1/workspaces/{ws}/records/{record}", {
        params: { path: { ws, record: parentRecord } },
        signal: controller.signal,
      })
      .then(({ data }) => {
        if (!controller.signal.aborted && data) setName(displayName(data));
      })
      .catch((error) => {
        if (!controller.signal.aborted) setError(errorMessage(error));
      });
    return () => controller.abort();
  }, [ws, parentRecord, rec]);

  function close() {
    if (changed.length || (!rec && (name || ident)))
      track("ui.record_dialog_closed", {
        mode: rec ? "edit" : "add",
        type,
        changed: changed.length,
      });
    onClose();
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (listEntry && !values.outflare_parent_record) {
      setError("Choose the record to add to this list.");
      return;
    }
    setBusy(true);
    const attributes = Object.fromEntries(
      changed.map((k) => [k, empty(values[k]) ? null : values[k]]),
    );
    let res;
    if (rec) {
      res = await api.PATCH("/v1/brands/{brand}/records/{record}", {
        params: { path: { brand: rec.brand_id ?? brand, record: rec.id } },
        body: { version: rec.version, ...(name.trim() !== rec.name ? { name: name.trim() } : {}), ...(changed.length ? { attributes } : {}) },
      });
    } else {
      const value = ident.trim();
      const ns = person ? "email" : "domain";
      res = await api.POST("/v1/brands/{brand}/records", {
        params: { path: { brand } },
        body: {
          type,
          brand_id: brand,
          origin: "manual",
          ...(name.trim() ? { name: name.trim() } : {}),
          ...(changed.length
            ? {
                attributes: Object.fromEntries(
                  Object.entries(attributes).filter(([, v]) => v !== null),
                ),
              }
            : {}),
          ...(listEntry && values.outflare_parent_record
            ? {
                identities: [
                  {
                    namespace: "outflare.list_entry",
                    value: `${type}:${String(values.outflare_parent_record)}`,
                    source: "manual",
                    verified: true,
                  },
                ],
              }
            : {}),
          ...(value
            ? {
                identities: [
                  {
                    namespace: ns,
                    value,
                    source: "manual",
                    ...(person ? { verified } : {}),
                  },
                ],
              }
            : {}),
        },
      });
    }
    setBusy(false);
    if (res.error) {
      setStale(
        (res.error as Schemas["Error"]).error?.code === "record_changed",
      );
      return setError(errorMessage(res.error));
    }
    if (!res.data || !("id" in res.data)) return onClose(); // held for a mission's approval
    const saved = res.data as Rec;
    const kept = saved.skipped ?? [];
    toast(
      kept.length
        ? `Saved. ${kept.map((s) => fields.find((f) => f.key === s.field)?.label ?? s.field).join(", ")} kept as ${kept[0].kept_source} set ${kept.length === 1 ? "it" : "them"}.`
        : rec
          ? "Saved."
          : person
            ? "Added the person."
            : "Added the company.",
    );
    onSaved(saved);
    onClose();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && close()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <form className="space-y-3 text-sm" onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>
              {rec
                ? `Edit ${rec.name || "record"}`
                : `Add ${person ? "a person" : `a ${typeLabel}`}`}
            </DialogTitle>
            <DialogDescription>
              {rec
                ? "What you type here wins over values from enrichment, a CRM or your product, unless a field says otherwise."
                : person
                  ? "Look them up first if they may already be here: someone with the same verified email can't be added twice."
                  : "The domain says which people work there, from their email addresses."}
            </DialogDescription>
          </DialogHeader>
          {(!person || rec) && (!listEntry || rec) && (
            <label className="block">
              <span className="mb-1 block font-medium">Name</span>
              <input
                required={!rec && !person}
                autoFocus
                className={`${field} w-full`}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
              {person && rec && (
                <span className="mt-1 block text-xs text-muted-foreground">
                  Leave it empty to use their first and last name.
                </span>
              )}
            </label>
          )}
          {!rec && (person || type === "account") && (
            <div>
              <label className="block">
                <span className="mb-1 block font-medium">
                  {person ? "Email" : "Domain"}{" "}
                  <span className="font-normal text-muted-foreground">
                    (optional)
                  </span>
                </span>
                <input
                  type={person ? "email" : "text"}
                  autoFocus={person}
                  className={`${field} w-full`}
                  placeholder={person ? "ana@acme.example" : "acme.example"}
                  value={ident}
                  onChange={(e) => setIdent(e.target.value)}
                />
              </label>
              {person && ident && (
                <label className="mt-1.5 flex items-center gap-1.5 text-xs">
                  <input
                    type="checkbox"
                    checked={verified}
                    onChange={(e) => setVerified(e.target.checked)}
                  />{" "}
                  They proved it's theirs (it then claims the address)
                </label>
              )}
            </div>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            {shown.map((f) => (
              <div key={f.key} className={f.value_type === "record_reference" || f.value_type === "location" || f.value_type === "url" ? "sm:col-span-2" : ""}>
                <label htmlFor={`field-${f.key}`} className="mb-1 block font-medium" title={f.description || undefined}>{f.label}</label>
                <FieldInput ws={ws} brand={rec?.brand_id ?? brand} f={f} value={values[f.key]} names={names} cat={cat} onChange={(v) => setValues({ ...values, [f.key]: v })} />
              </div>
            ))}
          </div>
          {!all && (
            <button
              type="button"
              className="text-xs underline"
              onClick={() => setAll(true)}
            >
              Show every field
            </button>
          )}
          <ErrorText>{error}</ErrorText>
          {stale && (
            <p className="text-xs text-muted-foreground">
              Close this and open the record again to see the change, then make
              yours.
            </p>
          )}
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" onClick={close}>
              Cancel
            </Button>
            <Button disabled={busy || (listEntry && !name.trim())}>
              {busy ? "Saving…" : rec ? "Save" : "Add"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// softDeleteRecord asks, then deletes a record into Recently deleted: hidden
// everywhere now, restorable for 7 days. True when it went.
export async function softDeleteRecord(
  rec: Rec,
  label: string,
): Promise<boolean> {
  if (!rec.brand_id) {
    toast(
      "This record has no brand, so it can only be archived or erased.",
      "error",
    );
    return false;
  }
  const person = rec.type === "person";
  const ok = await confirmAction(
    `Delete ${label}?`,
    <>
      <p>
        {person ? "They are" : "It is"} hidden everywhere at once: lists,
        search, Query, smart lists, rules and journeys, with{" "}
        {person ? "their" : "its"} emails and ids, links and product events.{" "}
        {person ? "Their journeys stop, and nothing more is sent to them." : ""}
      </p>
      <p>
        Reports and rule history that counted {person ? "their" : "its"} events
        change while {person ? "they are" : "it is"} deleted. You can restore{" "}
        {person ? "them" : "it"}, as {person ? "they were" : "it was"} and
        without sending anything again, from People → Recently deleted for 7
        days; then {person ? "they are" : "it is"} gone for good.
      </p>
      <p>
        For a request to erase someone's data, use Erase permanently instead: it
        can't be undone.
      </p>
    </>,
    "Delete",
  );
  if (!ok) return false;
  const { error } = await api.POST(
    "/v1/brands/{brand}/records/{record}/delete",
    {
      params: { path: { brand: rec.brand_id, record: rec.id } },
      body: { version: rec.version },
    },
  );
  if (error) {
    toast(errorMessage(error), "error");
    return false;
  }
  toast(
    `Deleted ${label}. Restore ${person ? "them" : "it"} from Recently deleted within 7 days.`,
  );
  return true;
}

// softDeleteEvent asks, then deletes one product event (a payment is one)
// into Recently deleted. True when it went.
export async function softDeleteEvent(
  brand: string,
  event: string,
  name: string,
): Promise<boolean> {
  const payment = name.startsWith("payment.");
  const ok = await confirmAction(
    `Delete this ${payment ? "payment" : "event"}?`,
    <>
      <p>
        {name} is hidden everywhere at once. Reports, attribution and rule
        history that counted it change
        {payment ? ", and their lifetime revenue is worked out again" : ""}.
        Nothing already sent because of it is taken back.
      </p>
      <p>
        You can restore it from People → Recently deleted for 7 days, without
        firing rules or journeys again; then it is gone for good.
      </p>
    </>,
    "Delete",
  );
  if (!ok) return false;
  const { error } = await api.POST(
    "/v1/brands/{brand}/customer-events/{event}/delete",
    { params: { path: { brand, event } } },
  );
  if (error) {
    toast(errorMessage(error), "error");
    return false;
  }
  toast("Deleted. Restore it from Recently deleted within 7 days.");
  return true;
}

// eraseRecord asks, then deletes a record for good. True when it went.
export async function eraseRecord(rec: Rec, label: string): Promise<boolean> {
  const person = rec.type === "person";
  const r = await ask({
    title: `Erase ${label} permanently?`,
    body: (
      <>
        <p>
          {person
            ? "This erases them: their record and emails and ids, their leads, the emails sent to them and their replies, meetings, product events, website visits and consent. Deals and payments stay, without their name."
            : "This erases the company's record, its fields and history. Its people and deals stay, no longer linked to it."}{" "}
          It can't be undone, also not from Recently deleted: use it for a
          request to erase someone's data. To remove {person ? "them" : "it"}{" "}
          and keep 7 days to change your mind, use Delete instead.
        </p>
        <p>
          {person
            ? "If they asked never to be contacted, also add their emails to the do-not-contact list, so a later import can't email them again."
            : "Adding its domains to the do-not-contact list stops email to anyone there."}
        </p>
      </>
    ),
    choices: [
      { label: "Cancel", value: "cancel", variant: "ghost" },
      { label: "Erase", value: "delete", variant: "ghost" },
      {
        label: "Erase and never contact",
        value: "suppress",
        variant: "danger-solid",
      },
    ],
  });
  if (!r || r.choice === "cancel") return false;
  const { data, error } = await api.DELETE("/v1/brands/{brand}/records/{record}", {
    params: { path: { brand: rec.brand_id ?? "", record: rec.id }, query: { version: rec.version, suppress: r.choice === "suppress" } },
  });
  if (error) {
    toast(errorMessage(error), "error");
    return false;
  }
  if (!data || !("erased" in data)) return true; // held for a mission's approval
  const activity = Object.entries(data.erased)
    .filter(([t]) => t !== "records")
    .reduce((n, [, c]) => n + c, 0);
  toast(
    `Erased ${label}${activity ? ` and ${plural(activity, "thing")} about ${person ? "them" : "it"}` : ""}${data.suppressed ? `; ${person ? plural(data.suppressed, "address", "addresses") : plural(data.suppressed, "domain")} added to do not contact` : ""}.`,
  );
  return true;
}

// AddIdentity adds an email, phone or id to a record.
export function AddIdentity({ rec, onAdded }: { rec: Rec; onAdded: () => void }) {
  const [open, setOpen] = useState(false);
  const [ns, setNs] = useState(rec.type === "person" ? "email" : "domain");
  const [value, setValue] = useState("");
  const [verified, setVerified] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  if (!open)
    return (
      <button className="mt-3 text-xs underline" onClick={() => setOpen(true)}>
        Add an email or id
      </button>
    );
  const checks = ns === "email" || ns === "phone";

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/records/{record}/identities", {
      params: { path: { brand: rec.brand_id ?? "", record: rec.id } },
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
        <select
          aria-label="Kind"
          className={field}
          value={ns}
          onChange={(e) => setNs(e.target.value)}
        >
          {(rec.type === "person"
            ? ["email", "phone", "user_id"]
            : ["domain"]
          ).map((n) => (
            <option key={n} value={n}>
              {
                (
                  {
                    email: "Email",
                    phone: "Phone",
                    user_id: "User id",
                    domain: "Domain",
                  } as Record<string, string>
                )[n]
              }
            </option>
          ))}
        </select>
        <input
          required
          autoFocus
          aria-label="Value"
          className={`${field} min-w-0 flex-1`}
          placeholder={
            ns === "phone"
              ? "+61 412 345 678"
              : ns === "domain"
                ? "acme.example"
                : ns === "email"
                  ? "ana@acme.example"
                  : ""
          }
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
      </div>
      {checks && (
        <label className="flex items-center gap-1.5 text-xs">
          <input
            type="checkbox"
            checked={verified}
            onChange={(e) => setVerified(e.target.checked)}
          />{" "}
          They proved it's theirs
        </label>
      )}
      <ErrorText>{error}</ErrorText>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
          Cancel
        </Button>
        <Button disabled={busy}>{busy ? "Adding…" : "Add"}</Button>
      </div>
    </form>
  );
}

const channels = ["email", "sms", "push", "whatsapp", "in_app", "ads"] as const;

// RecordConsent notes that a person agreed to something or took it back,
// such as on a call.
export function RecordConsent({ brand, id, onAdded }: { brand: string; id: string; onAdded: () => void }) {
  const [open, setOpen] = useState(false);
  const [channel, setChannel] = useState<(typeof channels)[number]>("email");
  const [topic, setTopic] = useState("");
  const [status, setStatus] = useState<"granted" | "withdrawn">("granted");
  const [error, setError] = useState("");
  if (!open)
    return (
      <button className="mt-3 text-xs underline" onClick={() => setOpen(true)}>
        Record consent
      </button>
    );

  async function submit(e: FormEvent) {
    e.preventDefault();
    const { error } = await api.POST("/v1/brands/{brand}/records/{record}/consents", {
      params: { path: { brand, record: id } },
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
        <select
          aria-label="Channel"
          className={field}
          value={channel}
          onChange={(e) => setChannel(e.target.value as typeof channel)}
        >
          {channels.map((c) => (
            <option key={c} value={c}>
              {c.replace("_", " ")}
            </option>
          ))}
        </select>
        <select
          aria-label="Status"
          className={field}
          value={status}
          onChange={(e) => setStatus(e.target.value as typeof status)}
        >
          <option value="granted">Agreed</option>
          <option value="withdrawn">Took it back</option>
        </select>
      </div>
      <input
        aria-label="Topic"
        className={`${field} w-full`}
        placeholder="Topic key, such as product_updates (empty for the whole channel)"
        value={topic}
        onChange={(e) => setTopic(e.target.value)}
      />
      <ErrorText>{error}</ErrorText>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
          Cancel
        </Button>
        <Button>Save</Button>
      </div>
    </form>
  );
}
