import { useEffect, useMemo, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { plural } from "../format";

// Shared by the People pages: the catalog of fields, and how a record and
// its values read.

export type Rec = Schemas["Record"];
type Field = Schemas["Field"];

export const origins: Record<Rec["origin"], string> = {
  first_party: "Your product",
  prospect: "Prospect",
  crm_sync: "CRM",
  import: "Import",
  manual: "Added by hand",
};

// Where a record came from, as the end of "Person …".
export const originPhrases: Record<Rec["origin"], string> = {
  first_party: "from your product",
  prospect: "found as a prospect",
  crm_sync: "from your CRM",
  import: "imported",
  manual: "added by hand",
};

export const namespaces: Record<string, string> = {
  email: "Email",
  phone: "Phone",
  user_id: "User id",
  anonymous_id: "Anonymous id",
  device_token: "Device",
  domain: "Domain",
  "outflare.lead": "Lead",
};

// The catalog's fields and option labels, read once per page.
export function useCatalog(ws: string) {
  const [catalog, setCatalog] = useState<Schemas["Catalog"] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/catalog", { params: { path: { ws } } }).then(({ data, error }) => {
      if (data) setCatalog(data);
      else setError(errorMessage(error));
    });
  }, [ws]);
  return useMemo(() => {
    const labels = new Map<string, Map<string, string>>();
    for (const s of catalog?.option_sets ?? []) labels.set(s.key, new Map(s.options.map((o) => [o.key, o.label])));
    const fields = (type: string) => catalog?.types.find((t) => t.key === type)?.fields.filter((f) => !f.archived) ?? [];
    const options = (f: Field) => catalog?.option_sets.find((s) => s.key === f.option_set)?.options.filter((o) => !o.archived) ?? [];
    return { catalog, error, labels, fields, options };
  }, [catalog, error]);
}
export type CatalogInfo = ReturnType<typeof useCatalog>;

// The record's name, or what else says who it is.
// The name for the top bar's path, for a page that isn't one record.
export function nameItem(id: string, name: string) {
  window.dispatchEvent(new CustomEvent("outflare:item-name", { detail: { id, name } }));
}

export function displayName(r: Rec) {
  if (r.name) return r.name;
  const id = r.identities.find((i) => i.namespace === "email") ?? r.identities.find((i) => i.namespace === "domain") ?? r.identities[0];
  return id?.value ?? "Unnamed";
}

export const identity = (r: Rec, ns: string) => (r.identities.find((i) => i.namespace === ns && i.verified) ?? r.identities.find((i) => i.namespace === ns))?.value;

// A value as text, for tables and filters: option labels instead of keys.
export function fieldLabelValue(cat: CatalogInfo, type: string, key: string, v: unknown, names: Record<string, string> = {}): string {
  if (v === null || v === undefined || v === "") return "";
  const f = cat.fields(type).find((x) => x.key === key);
  const opts = f?.option_set ? cat.labels.get(f.option_set) : undefined;
  const list = Array.isArray(v) ? v : [v];
  // References read as names where known, else as how many there are.
  if (f?.value_type === "record_reference" && list.some((x) => !names[String(x)])) {
    return f.target_type === "account" ? plural(list.length, "company", "companies") : plural(list.length, "record");
  }
  const one = (x: unknown) => opts?.get(String(x)) ?? names[String(x)] ?? (typeof x === "object" ? JSON.stringify(x) : String(x));
  return list.map(one).join(", ");
}
