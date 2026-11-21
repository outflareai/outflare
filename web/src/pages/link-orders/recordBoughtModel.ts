import { api, errorMessage } from "../../api";
import { csvRecords } from "./csvBatches";
import { idemp, type LinkOrderDonor } from "./shared";

// Recording a link that was already bought outside Outflare, as one form
// (or one CSV row): the same steps a new order goes through, run one after
// another with the person's own permissions. Add the site to the catalog
// if it is missing, open the order, save the quote, approve it (only the
// brand's designated approver can; for anyone else the order waits at
// Quoted), then record the publication, which queues a live-link check.
// Paid stays a separate step after that check passes, as for every order.

export type InvoiceCurrency = "USD" | "EUR" | "GBP" | "PLN" | "CZK" | "UAH" | "CHF" | "CAD" | "AUD" | "NZD" | "JPY" | "SEK" | "NOK" | "DKK" | "RON" | "HUF" | "BGN" | "TRY" | "INR" | "SGD" | "HKD" | "MXN" | "BRL" | "ZAR";

export const invoiceCurrencies: InvoiceCurrency[] = ["USD", "EUR", "GBP", "PLN", "CZK", "UAH", "CHF", "CAD", "AUD", "NZD", "JPY", "SEK", "NOK", "DKK", "RON", "HUF", "BGN", "TRY", "INR", "SGD", "HKD", "MXN", "BRL", "ZAR"];

export type BoughtLink = {
  domain: string;
  intermediary: string;
  targetURL: string;
  anchor: string;
  anchor2: string;
  publishedURL: string;
  proofURL: string;
  /** In major units, as typed ("150" or "150.50"). */
  price: string;
  currency: InvoiceCurrency;
  /** YYYY-MM-DD; the ECB rate for a non-USD/EUR currency is looked up for it. Empty for today. */
  invoiceDate: string;
  notes: string;
};

export const emptyBoughtLink: BoughtLink = {
  domain: "", intermediary: "", targetURL: "", anchor: "", anchor2: "", publishedURL: "", proofURL: "", price: "", currency: "USD", invoiceDate: "", notes: "",
};

export type RecordResult = {
  orderId?: string;
  /** The last step that worked: the order's status now. */
  reached: "none" | "requested" | "quoted" | "approved" | "published";
  /** Why it stopped before published, in plain words. */
  stopped?: string;
};

/** A typed price in major units: "1,200", "1 200.50" and "120,50" all read as people mean them. Null when unreadable. */
export function parseAmount(s: string): number | null {
  let t = s.replace(/[\s\u00a0']/g, "").replace(/[^\d.,]/g, "");
  if (!t) return null;
  const comma = t.lastIndexOf(","), dot = t.lastIndexOf(".");
  if (comma >= 0 && dot >= 0) t = comma > dot ? t.replace(/\./g, "").replace(",", ".") : t.replace(/,/g, "");
  else if (comma >= 0) t = /,\d{1,2}$/.test(t) && (t.match(/,/g) ?? []).length === 1 ? t.replace(",", ".") : t.replace(/,/g, "");
  const n = Number(t);
  return Number.isFinite(n) && n >= 0 ? n : null;
}

export function hostOf(s: string): string {
  const t = s.trim().toLowerCase();
  try {
    return new URL(/^https?:\/\//.test(t) ? t : `https://${t}`).hostname.replace(/^www\./, "");
  } catch {
    return t.replace(/^www\./, "").split("/")[0];
  }
}

/** Problems with the form, before anything is written; empty when it can be recorded. */
export function checkBoughtLink(b: BoughtLink): string[] {
  const out: string[] = [];
  const domain = hostOf(b.domain || b.publishedURL);
  if (!domain || !domain.includes(".")) out.push("Donor site is missing.");
  if (!/^https?:\/\/\S+\.\S+/i.test(b.targetURL.trim())) out.push("The page it links to must be a full address (https://…).");
  if (!/^https?:\/\/\S+\.\S+/i.test(b.publishedURL.trim())) out.push("The published article must be a full address (https://…).");
  if (parseAmount(b.price) == null) out.push("Price must be a number, such as 150 or 150.50.");
  if (b.invoiceDate && !/^\d{4}-\d{2}-\d{2}$/.test(b.invoiceDate)) out.push("Invoice date must be YYYY-MM-DD.");
  return out;
}

export async function recordBoughtLink(brand: string, donors: LinkOrderDonor[], b: BoughtLink, canApprove: boolean): Promise<RecordResult> {
  const domain = hostOf(b.domain || b.publishedURL);
  const params = { path: { brand } };
  let donor = donors.find((d) => d.domain.toLowerCase() === domain);
  if (!donor) {
    const { data, error } = await api.POST("/v1/brands/{brand}/link-orders/catalog", {
      params,
      body: { domain, intermediary: b.intermediary.trim() || undefined } as never,
    });
    if (error || !data) return { reached: "none", stopped: `Adding ${domain} to the catalog: ${errorMessage(error)}` };
    donor = data as LinkOrderDonor;
  }
  const created = await api.POST("/v1/brands/{brand}/link-orders", {
    params: { ...params, header: idemp() },
    body: {
      donor_profile_id: donor.id,
      target_url: b.targetURL.trim(),
      target_choice: "manual",
      anchor_primary: b.anchor.trim() || undefined,
      anchor_primary_source: b.anchor.trim() ? "manual" : undefined,
      anchor_secondary: b.anchor2.trim() || undefined,
      anchor_secondary_type: b.anchor2.trim() ? "keyword" : undefined,
      notes: ["Recorded as an already bought link.", b.notes.trim()].filter(Boolean).join(" "),
    },
  });
  if (created.error || !created.data) return { reached: "none", stopped: `Opening the order: ${errorMessage(created.error)}` };
  const id = created.data.id;
  const orderParams = { path: { brand, id } };
  const amount = Math.round((parseAmount(b.price) ?? 0) * 100);
  const quoted = await api.POST("/v1/brands/{brand}/link-orders/{id}/quote", {
    params: orderParams,
    body: { amount_minor: amount, currency: b.currency, ...(b.invoiceDate ? { invoice_date: b.invoiceDate } : {}) },
  });
  if (quoted.error) return { orderId: id, reached: "requested", stopped: `Saving the price: ${errorMessage(quoted.error)}` };
  if (!canApprove) {
    return { orderId: id, reached: "quoted", stopped: "Waiting for the brand's approver to Approve it; then record the publication on the order." };
  }
  const approved = await api.POST("/v1/brands/{brand}/link-orders/{id}/approve", { params: orderParams });
  if (approved.error) return { orderId: id, reached: "quoted", stopped: `Approving: ${errorMessage(approved.error)}` };
  const published = await api.POST("/v1/brands/{brand}/link-orders/{id}/publish", {
    params: orderParams,
    body: { published_url: b.publishedURL.trim(), proof_url: b.proofURL.trim() || undefined },
  });
  if (published.error) return { orderId: id, reached: "approved", stopped: `Recording the publication: ${errorMessage(published.error)}` };
  return { orderId: id, reached: "published" };
}

// Past orders from a CSV file: one bought link per row. Headers are
// matched ignoring case and punctuation.
const columnAliases: Record<keyof BoughtLink, string[]> = {
  domain: ["domain", "donor", "site", "website", "donor_domain", "donor_site"],
  intermediary: ["intermediary", "broker", "vendor", "seller", "agency"],
  targetURL: ["target_url", "target", "target_page", "our_page", "page", "landing_page", "link_to", "url_to"],
  anchor: ["anchor", "anchor_primary", "primary_anchor", "anchor_text", "anchor_1"],
  anchor2: ["anchor_secondary", "second_anchor", "anchor_2", "secondary_anchor"],
  publishedURL: ["published_url", "article_url", "article", "post_url", "placement_url", "live_url", "link_url", "url"],
  proofURL: ["proof_url", "proof", "screenshot"],
  price: ["price", "amount", "cost", "paid", "price_usd", "price_eur"],
  currency: ["currency", "cur", "ccy"],
  invoiceDate: ["invoice_date", "date", "paid_at", "paid_on", "published_at", "order_date"],
  notes: ["notes", "note", "comment", "comments"],
};

function norm(h: string): string {
  return h.toLowerCase().replace(/^﻿/, "").replace(/[^\p{L}\p{N}]+/gu, "_").replace(/^_|_$/g, "");
}

function splitRecord(rec: string): string[] {
  const out: string[] = [];
  let cur = "";
  let q = false;
  for (let i = 0; i < rec.length; i++) {
    const c = rec[i];
    if (q) {
      if (c === '"' && rec[i + 1] === '"') {
        cur += '"';
        i++;
      } else if (c === '"') q = false;
      else cur += c;
    } else if (c === '"') q = true;
    else if (c === ",") {
      out.push(cur);
      cur = "";
    } else cur += c;
  }
  out.push(cur);
  return out.map((s) => s.trim());
}

/** Reads past orders from a CSV; each row is a BoughtLink, with the column names it did not use. */
export function parsePastOrders(text: string): { rows: BoughtLink[]; unused: string[]; missing: string[] } {
  const recs = csvRecords(text.replace(/^﻿/, ""));
  if (!recs.length) return { rows: [], unused: [], missing: ["the header row"] };
  const header = splitRecord(recs[0]);
  const idx: Partial<Record<keyof BoughtLink, number>> = {};
  const used = new Set<number>();
  for (const key of Object.keys(columnAliases) as (keyof BoughtLink)[]) {
    const i = header.findIndex((h, j) => !used.has(j) && columnAliases[key].includes(norm(h)));
    if (i >= 0) {
      idx[key] = i;
      used.add(i);
    }
  }
  const currencyFromHeader = (i: number | undefined): InvoiceCurrency | undefined => {
    if (i == null) return undefined;
    const h = norm(header[i]);
    return h.endsWith("eur") ? "EUR" : h.endsWith("usd") ? "USD" : undefined;
  };
  const missing = (["targetURL", "publishedURL", "price"] as (keyof BoughtLink)[]).filter((k) => idx[k] == null).map((k) => columnAliases[k][0]);
  const rows = recs.slice(1).map((r) => {
    const cells = splitRecord(r);
    const get = (k: keyof BoughtLink) => (idx[k] != null ? cells[idx[k]!] ?? "" : "");
    const cur = get("currency").toUpperCase();
    const currency = (invoiceCurrencies as string[]).includes(cur) ? (cur as InvoiceCurrency) : currencyFromHeader(idx.price) ?? "USD";
    return {
      domain: get("domain"), intermediary: get("intermediary"), targetURL: get("targetURL"), anchor: get("anchor"), anchor2: get("anchor2"),
      publishedURL: get("publishedURL"), proofURL: get("proofURL"), price: get("price"), currency,
      invoiceDate: get("invoiceDate").slice(0, 10), notes: get("notes"),
    };
  });
  return { rows, unused: header.filter((_, i) => !used.has(i) && header[i].trim()), missing };
}

export const pastOrdersExampleCSV = `domain,intermediary,target_url,anchor,published_url,price,currency,invoice_date,notes
donor.example,Acme Links,https://northpeak.example/pricing,northpeak pricing,https://donor.example/blog/best-tools,150,USD,2026-10-01,Guest post
`;
