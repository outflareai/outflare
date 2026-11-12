import { useEffect, useMemo, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { plural } from "../format";
import { useBrand } from "./brand";

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

// catalogWrite sends a catalog change to the brand whose catalog the page
// shows (useCatalog): the brand picked in the switcher, or, with all
// brands picked, the workspace's first brand. Each brand has its own
// catalog, so a change must never land on another brand's.
export function useCatalogWrite(ws: string) {
  const { brand: picked, brands } = useBrand(ws);
  const brand = picked?.id ?? brands[0]?.id ?? "";
  return useMemo(() => ({
    createType: (body: Schemas["RecordTypeInput"]) =>
      api.POST("/v1/brands/{brand}/catalog/types", { params: { path: { brand } }, body }),
    createOptionSet: (body: Schemas["OptionSetInput"]) =>
      api.POST("/v1/brands/{brand}/catalog/option-sets", { params: { path: { brand } }, body }),
    updateOptionSet: (set: string, body: Schemas["OptionSetInput"]) =>
      api.PATCH("/v1/brands/{brand}/catalog/option-sets/{set}", { params: { path: { brand, set } }, body }),
    createField: (type: string, body: Schemas["FieldInput"]) =>
      api.POST("/v1/brands/{brand}/catalog/types/{type}/fields", { params: { path: { brand, type } }, body }),
    updateField: (type: string, field: string, body: Schemas["FieldInput"]) =>
      api.PATCH("/v1/brands/{brand}/catalog/types/{type}/fields/{field}", { params: { path: { brand, type, field } }, body }),
    updateEvent: (event: string, body: Schemas["EventDefinitionInput"]) =>
      api.PATCH("/v1/brands/{brand}/catalog/events/{event}", { params: { path: { brand, event } }, body }),
  }), [brand]);
}

// The catalog's fields and option labels, read once per page. Each brand
// has its own catalog: brandId's (a record's brand), else the brand picked
// in the switcher, else, with all brands picked, the workspace's first
// brand's.
export function useCatalog(ws: string, brandId?: string | null) {
  const { brand: picked, brands } = useBrand(ws);
  const brand = brandId || picked?.id || brands[0]?.id || "";
  const [catalog, setCatalog] = useState<Schemas["Catalog"] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    setCatalog(null);
    setError("");
    if (!brand) return () => controller.abort();
    api.GET("/v1/brands/{brand}/catalog", { params: { path: { brand }, query: { archived: true } }, signal: controller.signal }).then(({ data, error }) => {
      if (controller.signal.aborted) return;
      if (data) setCatalog(data);
      else setError(errorMessage(error));
    }).catch((error) => { if (!controller.signal.aborted) setError(errorMessage(error)); });
    return () => controller.abort();
  }, [ws, brand]);
  return useMemo(() => {
    const labels = new Map<string, Map<string, string>>();
    for (const s of catalog?.option_sets ?? [])
      labels.set(s.key, new Map(s.options.map((o) => [o.key, o.label])));
    const fields = (type: string) =>
      catalog?.types
        .find((t) => t.key === type)
        ?.fields.filter((f) => !f.archived) ?? [];
    const options = (f: Field) =>
      catalog?.option_sets
        .find((s) => s.key === f.option_set)
        ?.options.filter((o) => !o.archived) ?? [];
    return { catalog, error, labels, fields, options };
  }, [catalog, error]);
}
export type CatalogInfo = ReturnType<typeof useCatalog>;

// The record's name, or what else says who it is.
// The name for the top bar's path, for a page that isn't one record.
export function nameItem(id: string, name: string) {
  window.dispatchEvent(
    new CustomEvent("outflare:item-name", { detail: { id, name } }),
  );
}

export function displayName(r: Rec) {
  if (r.name) return r.name;
  const id =
    r.identities.find((i) => i.namespace === "email") ??
    r.identities.find((i) => i.namespace === "domain") ??
    r.identities[0];
  return id?.value ?? "Unnamed";
}

export const identity = (r: Rec, ns: string) =>
  (
    r.identities.find((i) => i.namespace === ns && i.verified) ??
    r.identities.find((i) => i.namespace === ns)
  )?.value;

// A value as text, for tables and filters: option labels instead of keys.
export function fieldLabelValue(
  cat: CatalogInfo,
  type: string,
  key: string,
  v: unknown,
  names: Record<string, string> = {},
): string {
  if (v === null || v === undefined || v === "") return "";
  const f = cat.fields(type).find((x) => x.key === key);
  const opts = f?.option_set ? cat.labels.get(f.option_set) : undefined;
  const list = Array.isArray(v) ? v : [v];
  // References read as names where known, else as how many there are.
  if (
    f?.value_type === "record_reference" &&
    list.some((x) => !names[String(x)])
  ) {
    return f.target_type === "account"
      ? plural(list.length, "company", "companies")
      : plural(list.length, "record");
  }
  const one = (x: unknown) =>
    opts?.get(String(x)) ??
    names[String(x)] ??
    (typeof x === "object" ? JSON.stringify(x) : String(x));
  return list.map(one).join(", ");
}
