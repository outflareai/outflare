import type { paths } from "@outflare/sdk";
import { api, errorMessage } from "../../api";

export const resources = [
  "publishers",
  "placements",
  "parties",
  "agreements",
  "deliverables",
  "invoices",
  "payments",
  "rates",
  "budgets",
  "tasks",
  "candidates",
  "searches",
  "views",
  "rules",
  "finance_tasks",
  "documents",
  "delivery_reports",
] as const;
export type Resource = (typeof resources)[number];
export type Row = {
  id: string;
  version: number;
  created_at: string;
  [key: string]: unknown;
};
export type Field = {
  name: string;
  kind: string;
  required: boolean;
  nullable: boolean;
  choices: string[];
  reference: string;
  default: unknown;
};
export type Definition = { name: Resource; finance: boolean; fields: Field[] };
type CollectionPath = Extract<
  keyof paths,
  `/v1/brands/{brand}/coverage/${Resource}`
>;
type DetailPath = Extract<
  keyof paths,
  `/v1/brands/{brand}/coverage/${Exclude<Resource, "payments">}/{id}`
>;
// Names people use for values whose code names read badly.
const names: Record<string, string> = {
  display_ad: "Banner",
  youtube: "YouTube video",
  influencer: "Influencer post",
  forum: "Forum thread",
  mcp_directory: "MCP directory",
  app_listing: "App store listing",
  organic: "Organic (free, unprompted)",
  in_house: "In-house",
  delivered_by: "Who delivered it",
  deal: "CRM deal",
  deal_id: "CRM deal",
  account_manager: "Account manager",
  parties: "People and companies",
  ai_citations: "AI answers",
  web_search: "Web search",
  social: "Social watch",
  link_prospects: "Link prospects",
  connector_needed: "Needs a search results connection",
  budget_reached: "Connection budget reached",
  link_status: "Link",
  mention_status: "Mention",
  mention_missing: "Mention removed",
  offline_since: "Offline since",
};
export const label = (s: string) =>
  names[s] ?? s.replaceAll("_", " ").replace(/\b\w/g, (c) => c.toUpperCase());
export const nameOf = (r: Row) => String(r.title || r.domain || r.id);
export async function list(
  brand: string,
  resource: Resource,
  query: Record<string, string | number> = {},
) {
  const { data, error } = await api.GET(
    `/v1/brands/{brand}/coverage/${resource}` as CollectionPath,
    { params: { path: { brand }, query } },
  );
  if (error || !data) throw new Error(errorMessage(error));
  return data as unknown as { data: Row[]; total: number; next_cursor: string | null };
}
export async function save(
  brand: string,
  resource: Resource,
  body: Record<string, unknown>,
  row?: Row,
) {
  const response = row
    ? await api.PATCH(
        `/v1/brands/{brand}/coverage/${resource}/{id}` as DetailPath,
        {
          params: { path: { brand, id: row.id } },
          body: { ...body, version: row.version } as never,
        },
      )
    : await api.POST(
        `/v1/brands/{brand}/coverage/${resource}` as CollectionPath,
        { params: { path: { brand } }, body: body as never },
      );
  if (response.error || !response.data)
    throw new Error(errorMessage(response.error));
  if (response.response.status === 202)
    throw new Error(
      "This change is waiting for mission approval. It has not been saved yet.",
    );
  return response.data as unknown as Row;
}
// RFC 4180 quoting, including embedded newlines. Formula-like text stays text
// when exported to spreadsheets; import never evaluates cell content.
export function csv(rows: Record<string, unknown>[], columns: string[]) {
  const quote = (v: unknown) =>
    '"' +
    String(v ?? "")
      .replace(/^[=+@\-\t\r]/, (c) => "'" + c)
      .replaceAll('"', '""') +
    '"';
  return [
    columns.map(quote).join(","),
    ...rows.map((r) => columns.map((k) => quote(r[k])).join(",")),
  ].join("\r\n");
}
export function parseCSV(text: string): Record<string, string>[] {
  const rows: string[][] = [];
  let row: string[] = [],
    cell = "",
    quoted = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (c === '"') {
      if (quoted && text[i + 1] === '"') {
        cell += '"';
        i++;
      } else quoted = !quoted;
    } else if (!quoted && (c === "," || c === "\n" || c === "\r")) {
      row.push(cell);
      cell = "";
      if (c !== ",") {
        if (c === "\r" && text[i + 1] === "\n") i++;
        rows.push(row);
        row = [];
      }
    } else cell += c;
  }
  if (quoted) throw new Error("An imported field has an unclosed quote.");
  if (cell || row.length) {
    row.push(cell);
    rows.push(row);
  }
  const headers = rows.shift()?.map((h) => h.trim()) ?? [];
  if (!headers.length || new Set(headers).size !== headers.length)
    throw new Error("CSV needs unique column headers.");
  return rows
    .filter((r) => r.some(Boolean))
    .map((r) => Object.fromEntries(headers.map((h, i) => [h, r[i] ?? ""])));
}
