// Customer records: people, companies and the record types a workspace
// adds (deals, subscriptions), with their identities (emails, phones, the
// product's own user ids, payment customers, domains) and links. The
// catalog is the one definition of every field, option list and product
// event: read it before writing records, and reuse a field before adding
// one, so the same fact is never stored twice.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const id = z.string().uuid();
const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: false } as const;
export const brandRef = z.string().min(1).describe("The brand's handle (such as repocket) or id, from list_brands.");
const typeKey = z.string().min(1).describe("A record type's key: person, account, or one from get_catalog.");
const valueTypes = z.enum(["text", "number", "currency", "date", "timestamp", "checkbox", "select", "status", "rating", "email", "phone", "url", "location", "record_reference", "actor_reference"]);
const sources = z.enum(["manual", "first_party", "crm_sync", "enrichment", "import"]);
const origins = z.enum(["first_party", "prospect", "crm_sync", "import", "manual"]);
const sensitivity = z.enum(["public", "internal", "confidential", "restricted"]);
// Labels only (who owns it, how long it's kept, where it may go): nothing is blocked or deleted because of them.
const labels = {
  owner_id: id.nullable().optional().describe("Label: the team member who looks after it (a user id), or null for nobody."),
  retention_days: z.number().int().min(1).max(36500).nullable().optional().describe("Label: how long its data is meant to be kept, in days; null for forever."),
  destinations: z
    .array(z.enum(["webhooks", "exports", "ai", "published_queries", "email"]))
    .nullable()
    .optional()
    .describe("Label: where its data may be sent; null for everywhere."),
};
const writePolicy = z.enum(["source_owned", "platform_owned", "manual_override_allowed", "computed"]);
const options = z
  .array(z.object({ key: z.string().min(1).max(63), label: z.string().min(1).max(80), archived: z.boolean().optional() }))
  .max(500);
const identity = z.object({
  namespace: z.string().min(1).max(60).describe("email, phone, device_token, user_id, anonymous_id, domain, or a source's own such as stripe.customer."),
  value: z.string().min(1).max(500),
  verified: z
    .boolean()
    .optional()
    .describe("Only a verified identity claims its value. Default false for email and phone (set true only when the person proved they own it), true for the rest."),
  properties: z.record(z.string(), z.unknown()).optional().describe("device_token needs platform: ios, android or web."),
  source: z.string().max(60).optional(),
});
const attributes = z
  .record(z.string(), z.unknown())
  .describe("Values by field key, as get_catalog defines them; null clears one. Unknown keys are refused: add the field with create_field first.");
const eventProperties = z.array(z.object({ key: z.string().min(1), value_type: valueTypes, description: z.string().optional() })).max(100);

// brandCaller runs a call on a brand route (/v1/brands/{brand}/...), which
// takes the brand's id: a handle is looked up first.
export function brandCaller(gtm: MissionClient, workspace: () => string) {
  const uuidShape = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
  return async <T extends { data?: unknown; error?: unknown; response: Response }>(
    brand: string,
    fn: (id: string) => Promise<T>,
  ): Promise<{ data?: unknown; error?: unknown; response: Response }> => {
    if (uuidShape.test(brand)) return fn(brand);
    const found = await gtm.GET("/v1/workspaces/{ws}/brands/{brand}", { params: { path: { ws: workspace(), brand } } });
    if (!found.data) return found;
    return fn(found.data.id);
  };
}

export function registerRecordTools(server: McpServer, gtm: MissionClient, workspace: () => string, readOnly: boolean, call: Call): void {
  const path = () => ({ ws: workspace() });
  const onBrand = brandCaller(gtm, workspace);
  // findRecordBrand is the brand a record is in: each brand's records are
  // only under its own route, so it asks each brand in turn.
  const findRecordBrand = async (record: string): Promise<string | undefined> => {
    const brands = await gtm.GET("/v1/workspaces/{ws}/brands", { params: { path: path() } });
    for (const b of brands.data?.data ?? []) {
      const found = await gtm.GET("/v1/brands/{brand}/records/{record}", { params: { path: { brand: b.id, record } } });
      if (found.data) return b.id;
    }
    return undefined;
  };

  server.registerTool(
    "get_catalog",
    {
      title: "Read a brand's catalog",
      description:
        "Everything a brand's customer records can hold: record types (person and account built in, plus the brand's own such as deal), each type's fields " +
        "with their value types, the option lists (country, industry, company_size, seniority, lifecycle_stage and the brand's own) and " +
        "the product events. Each brand has its own catalog. Call before writing a brand's records or adding fields, and reuse what exists.",
      inputSchema: { brand: brandRef, archived: z.boolean().optional().describe("Include archived types, fields and options.") },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/catalog", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "list_records",
    {
      title: "List customer records",
      description:
        "People, companies or another record type, newest first. q matches the name or any identity, such as part of an email. " +
        "Records held for review (people a visitor identification tool named) are left out unless review is needs_review or all.",
      inputSchema: {
        brand: brandRef,
        type: typeKey.optional(),
        q: z.string().optional(),
        attribute: z
          .array(z.string())
          .max(20)
          .optional()
          .describe("Field filters as key:value, such as lifecycle_stage:customer. Values of one field are alternatives; different fields must all match. companies takes a company's record id."),
        origin: origins.optional(),
        archived: z.enum(["false", "true", "all"]).optional(),
        review: z.enum(["approved", "needs_review", "all"]).optional(),
        acquired_via: z.string().optional().describe("Only records from these tools, comma-separated, such as rb2b,vector."),
        limit: z.number().int().min(1).max(200).optional(),
        sort: z.enum(["name", "created_at", "updated_at"]).optional(),
        direction: z.enum(["asc", "desc"]).optional(), offset: z.number().int().min(0).max(100000).optional(),
        empty_attribute: z.string().optional(),
        cursor: z.string().optional().describe("next_cursor from the previous page."),
      },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/records", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "get_record",
    {
      title: "Read a customer record",
      description: "One record with its attributes, which source set each one, and its active identities.",
      inputSchema: { brand: brandRef, record_id: id },
      annotations: read,
    },
    ({ brand, record_id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/records/{record}", { params: { path: { brand: b, record: record_id } } }))),
  );

  server.registerTool(
    "lookup_record",
    {
      title: "Find records by identity",
      description:
        "The records an email, phone, user id or other identity points at. owner is the one record holding it verified; the others hold it " +
        "unverified, such as a sign-up that never confirmed its email. Each brand has its own people: pass brand to look in one (the same email " +
        "in another brand is another person). Use before create_record to avoid duplicates.",
      inputSchema: { namespace: z.string().min(1), value: z.string().min(1), brand: z.string().optional() },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/records/lookup", { params: { path: path(), query: q } })),
  );

  server.registerTool(
    "get_record_history",
    {
      title: "A record's change history",
      description: "Every change to a record's name and attributes, newest first, with who made it and from which source.",
      inputSchema: { brand: brandRef, record_id: id, field: z.string().optional(), limit: z.number().int().min(1).max(200).optional(), cursor: z.string().optional() },
      annotations: read,
    },
    ({ brand, record_id, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/records/{record}/history", { params: { path: { brand: b, record: record_id }, query: q } }))),
  );

  server.registerTool(
    "list_transactions",
    {
      title: "List transactions",
      description:
        "A brand's payments, refunds, chargebacks and credits, newest first, from billing connectors and record_transaction: all, a person's or company's " +
        "(record_id), or some kinds. Amounts are in minor units (cents), in their currency and converted to the brand's (brand_amount_minor).",
      inputSchema: {
        brand: brandRef,
        record_id: id.optional(),
        kind: z.string().optional().describe("payment, refund, chargeback, credit, or several separated by commas."),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional(),
      },
      annotations: read,
    },
    ({ brand, record_id, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/transactions", { params: { path: { brand: b }, query: { ...q, ...(record_id ? { record: record_id } : {}) } } }))),
  );

  server.registerTool(
    "list_record_field_values",
    {
      title: "Every source's value of a record's fields",
      description:
        "What each source (a person, first-party data, a CRM sync, enrichment) last said about each field, whether or not it won, and the hand edits behind " +
        "the record's values: when each stops winning (the brand's or field's manual_edit_days, 180 by default) and whether it is pinned. To use another " +
        "source's value, update_record with it: that is a hand edit.",
      inputSchema: { brand: brandRef, record_id: id },
      annotations: read,
    },
    ({ brand, record_id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/records/{record}/field-values", { params: { path: { brand: b, record: record_id } } }))),
  );

  server.registerTool(
    "list_record_identities",
    {
      title: "A record's identities",
      description: "Its emails, phones, ids and domains; closed: true adds closed ones, such as an email the person changed.",
      inputSchema: { brand: brandRef, record_id: id, closed: z.boolean().optional() },
      annotations: read,
    },
    ({ brand, record_id, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/records/{record}/identities", { params: { path: { brand: b, record: record_id }, query: q } }))),
  );

  server.registerTool(
    "list_record_links",
    {
      title: "A record's links",
      description: "Links both ways, such as the companies a person belongs to (field links) and partner_of links.",
      inputSchema: { brand: brandRef, record_id: id, ended: z.boolean().optional() },
      annotations: read,
    },
    ({ brand, record_id, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/records/{record}/relationships", { params: { path: { brand: b, record: record_id }, query: q } }))),
  );

  server.registerTool(
    "get_record_timeline",
    {
      title: "A record's timeline",
      description:
        "One timeline per person or company, newest first, across every module: product events (for a company, the group calls that named it), " +
        "attribute changes, identities added and closed, consent given or withdrawn, cold email sent and replies, meetings, deals, website visits, " +
        "opt-out list entries and journeys. A company's timeline includes its people's; each item's record_id says whose it is.",
      inputSchema: {
        brand: brandRef,
        record_id: id,
        kinds: z
          .string()
          .optional()
          .describe("Comma-separated: event, attribute_change, identity, consent, message (cold email and replies), meeting, deal, visit, opt_out, journey. Default all."),
        people: z.enum(["true", "false"]).optional().describe("For a company: false shows only the company's own items, without its people's."),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional(),
      },
      annotations: read,
    },
    ({ brand, record_id, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/records/{record}/timeline", { params: { path: { brand: b, record: record_id }, query: q } }))),
  );

  server.registerTool(
    "list_record_consents",
    {
      title: "A person's consents",
      description:
        "The current consent for each channel, topic and purpose (the latest grant or withdrawal); history: true lists every one. " +
        "Check it before any marketing message: no grant means do not send.",
      inputSchema: { brand: brandRef, record_id: id, history: z.boolean().optional() },
      annotations: read,
    },
    ({ brand, record_id, history }) =>
      call(() =>
        onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/records/{record}/consents", {
          params: { path: { brand: b, record: record_id }, query: history === undefined ? {} : { history: history ? "true" : "false" } },
        }),
      )),
  );

  server.registerTool(
    "list_customer_events",
    {
      title: "List product events",
      description: "Product events, newest first. matched: false lists anonymous visitors' events not yet linked to a person.",
      inputSchema: {
        brand: brandRef,
        name: z.string().optional().describe("A catalog name, such as order_completed or page.viewed."),
        kind: z.enum(["track", "identify", "page", "screen", "group", "alias"]).optional(),
        user_id: z.string().optional(),
        anonymous_id: z.string().optional(),
        matched: z.enum(["true", "false", "all"]).optional(),
        since: z.string().datetime().optional(),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional(),
      },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/customer-events", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "list_record_duplicates",
    {
      title: "List possible duplicates",
      description:
        "Pairs of records that may be the same human because they share an email or phone, with both records. open pairs can be merged " +
        "(merge_records), linked or dismissed; blocked pairs both have their own user id (two accounts in the product) and are never merged. " +
        "Compare the two before deciding, and ask the user when unsure.",
      inputSchema: {
        brand: brandRef,
        status: z.string().optional().describe("Comma-separated: open, blocked, merged, linked, dismissed; or all. Default open,blocked."),
        type: typeKey.optional(),
        record: id.optional().describe("Only pairs with this record."),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional(),
      },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/records/duplicates", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "list_brand_events",
    {
      title: "List a brand's received events",
      description:
        "Product events sent to a brand with send_brand_events, newest first, with what happened to each: pending, accepted, duplicate, " +
        "rejected (code and reason say what to fix) or held (a track name the catalog hasn't approved). expires_at is when it is deleted " +
        "(30 days accepted, 90 rejected or held). Use get_brand_event for one event as sent.",
      inputSchema: {
        brand: brandRef,
        status: z.enum(["pending", "accepted", "duplicate", "rejected", "held"]).optional(),
        code: z.string().optional().describe("A rejection code, such as missing_id or property_type."),
        source: z.string().optional(),
        batch_id: id.optional().describe("The batch_id send_brand_events answered."),
        name: z.string().optional().describe("A catalog name, such as order_completed."),
        received_after: z.string().datetime().optional(),
        received_before: z.string().datetime().optional(),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional(),
      },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/events", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "get_brand_event",
    {
      title: "Read a received event",
      description: "One event sent to a brand, with its status, what to fix when rejected, and raw: the event exactly as sent.",
      inputSchema: { brand: brandRef, event: id.describe("The event's receipt id, from send_brand_events or list_brand_events.") },
      annotations: read,
    },
    ({ brand, event }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/events/{event}", { params: { path: { brand: b, event } } }))),
  );

  server.registerTool(
    "get_brand_events_summary",
    {
      title: "Summarise a brand's received events",
      description:
        "How many events a brand received in the last 24 hours and 7 days by status, how many wait, the rejection codes still kept with " +
        "the latest reason, and the held names waiting for approval (approve with approve_event_name, then replay_brand_events).",
      inputSchema: { brand: brandRef },
      annotations: read,
    },
    ({ brand }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/events/summary", { params: { path: { brand: b } } }))),
  );

  server.registerTool(
    "get_privacy_export",
    {
      title: "Read a privacy export",
      description:
        "A privacy export's status (pending, ready, failed with error_code, or expired after 7 days), its rows by table and, once ready, " +
        "download_url for a person with customers.manage to download the file. Start one with request_privacy_export.",
      inputSchema: { brand: brandRef, export: id.describe("The export's id, from request_privacy_export or list_privacy_exports.") },
      annotations: read,
    },
    ({ brand, export: x }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/privacy-exports/{export}", { params: { path: { brand: b, export: x } } }))),
  );

  server.registerTool(
    "list_privacy_exports",
    {
      title: "List a brand's privacy exports",
      description: "A brand's privacy exports, newest first, with their status and expiry; record narrows it to one person or company.",
      inputSchema: {
        brand: brandRef,
        record: id.optional().describe("Only this record's exports."),
        status: z.enum(["pending", "ready", "failed", "expired"]).optional(),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional(),
      },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/privacy-exports", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "list_recently_deleted",
    {
      title: "List what was deleted in the last 7 days",
      description:
        "A brand's Recently deleted: what each delete removed (a record with its identities, links and events, or one field value, " +
        "identity, link or product event), newest first, with who deleted it and when it is purged for good (purge_after, 7 days later). " +
        "Restore one with restore_deletion.",
      inputSchema: {
        brand: brandRef,
        kind: z.enum(["record", "field_value", "identity", "relationship", "event", "batch", "transaction"]).optional(),
        record: id.optional().describe("Only deletions about this record."),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional(),
      },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/recently-deleted", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "get_deletion",
    {
      title: "Read a deletion",
      description: "One deletion from Recently deleted: what it holds, who deleted it, when it is purged, and what a restore couldn't bring back and why.",
      inputSchema: { brand: brandRef, deletion: id.describe("The deletion's id, from list_recently_deleted or a delete tool.") },
      annotations: read,
    },
    ({ brand, deletion }) =>
      call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/recently-deleted/{deletion}", { params: { path: { brand: b, deletion } } }))),
  );

  server.registerTool(
    "list_merges",
    {
      title: "List merges",
      description:
        "A brand's merges, newest first: which record was kept (into_id), which was merged into it (from_id) and what moved. undoable " +
        "says whether undo_merge would undo it now.",
      inputSchema: {
        brand: brandRef,
        record: id.optional().describe("Only merges this record was kept or merged in."),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional(),
      },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/merges", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "list_batches",
    {
      title: "List import and ingest batches",
      description:
        "A brand's batches, newest first: CSV imports of leads (csv_import), CRM imports (crm_import) and sends of product events (events), " +
        "with what each made that is still here and whether it was undone. A bad one can be undone in one step: preview_batch_undo, then undo_batch.",
      inputSchema: {
        brand: brandRef,
        kind: z.enum(["csv_import", "crm_import", "events"]).optional(),
        limit: z.number().int().min(1).max(100).optional(),
        cursor: z.string().optional(),
      },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/batches", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "list_bulk_edits",
    {
      title: "List bulk edits",
      description:
        "A brand's bulk edits and their undos, newest first: who ran each, where its records came from, what it changed and how many records " +
        "it changed. Every change is also in each record's history (get_record_history, bulk_edit_id).",
      inputSchema: { brand: brandRef, limit: z.number().int().min(1).max(100).optional(), cursor: z.string().optional() },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/bulk-edits", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "get_bulk_edit",
    {
      title: "Read a bulk edit",
      description: "One bulk edit or undo: its status (queued, running, done, failed), counts and until when it can be undone.",
      inputSchema: { brand: brandRef, bulk_edit: id.describe("The bulk edit's id, from list_bulk_edits.") },
      annotations: read,
    },
    ({ brand, bulk_edit }) =>
      call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/bulk-edits/{bulk_edit}", { params: { path: { brand: b, bulk_edit } } }))),
  );

  server.registerTool(
    "preview_bulk_edit",
    {
      title: "Preview a bulk edit",
      description:
        "Check a change to many people or companies at once and see what it would do, without changing anything: how many records and the " +
        "first 20 with each change. Records come from exactly one of smart_list_id, saved_query_id (its record_id or id column) or record_ids. " +
        "Only the workspace's owners can run it, signed in to the app: show the user the preview and ask an owner to run it there.",
      inputSchema: {
        brand: brandRef,
        smart_list_id: id.optional(),
        saved_query_id: id.optional(),
        record_ids: z.array(id).max(10000).optional(),
        changes: z.object({
          set: z.record(z.string(), z.unknown()).optional().describe("Fields to set by key (owner is a field); null clears one."),
          add_tags: z.array(z.string()).optional(),
          remove_tags: z.array(z.string()).optional(),
          add_to_list: id.optional().describe("A static list of the brand (people only)."),
          remove_from_list: id.optional().describe("A static list of the brand (people only)."),
          archive: z.boolean().optional(),
        }),
      },
      annotations: read,
    },
    ({ brand, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/bulk-edits/preview", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "preview_batch_undo",
    {
      title: "Preview undoing a batch",
      description:
        "What a batch made, what changed since (records someone edited, emailed, paid or had other activity on; records merged since; fields " +
        "changed since; leads already in a sequence) with those items, and what each mode would undo: untouched (only what nobody changed) or " +
        "everything (changed records too). Show this to the user and let them choose before undo_batch.",
      inputSchema: { brand: brandRef, batch: id.describe("The batch's id, from list_batches, an import's batch_id or a send's batch_id.") },
      annotations: read,
    },
    ({ brand, batch }) =>
      call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/batches/{batch}/undo-preview", { params: { path: { brand: b, batch } } }))),
  );

  server.registerTool("get_crm_summary",{title:"CRM sales overview",description:"Actual relationship counts, outstanding tasks, per-currency pipeline values and stale open deals.",inputSchema:{brand_id:id},annotations:read},({brand_id})=>call(()=>gtm.GET("/v1/brands/{brand}/crm/summary",{params:{path:{brand:brand_id}}})));
  server.registerTool("list_crm_activities", {
    title: "List CRM tasks and notes", description: "Record-linked tasks and notes for one brand. Page with next_cursor.",
    inputSchema: { brand_id: id, record_id: id.optional(), kind: z.enum(["task", "note"]).optional(), status: z.enum(["open", "done"]).optional(), assigned_to: id.optional(), cursor: z.string().optional(), limit: z.number().int().min(1).max(500).optional() }, annotations: read,
  }, ({ brand_id, ...query }) => call(() => gtm.GET("/v1/brands/{brand}/crm/activities", { params: { path: { brand: brand_id }, query } })));
  server.registerTool("list_crm_views", {
    title: "List CRM saved views", description: "The caller's personal views and the team's shared views in one brand.",
    inputSchema: { brand_id: id, record_type: typeKey.optional(), limit: z.number().int().min(1).max(500).optional() }, annotations: read,
  }, ({ brand_id, ...query }) => call(() => gtm.GET("/v1/brands/{brand}/crm/views", { params: { path: { brand: brand_id }, query } })));

  if (readOnly) return;

  server.registerTool(
    "record_transaction",
    {
      title: "Record a transaction",
      description:
        "Records a payment, refund, chargeback or credit from the brand's own billing. source_id is its id there: sending it again never adds it twice and " +
        "updates its status. Amounts are in the currency's minor unit (cents), always 0 or more. A succeeded payment with a person (record_id or user_id) " +
        "counts as a payment for revenue, attribution and ad conversions, once: the same payment already in from an Order Completed event " +
        "(same order_id as source_id) or a billing connector is answered as that one.",
      inputSchema: {
        brand: brandRef,
        kind: z.enum(["payment", "refund", "chargeback", "credit"]),
        amount_minor: z.number().int().min(0),
        currency: z.string().length(3),
        source_id: z.string().min(1).max(200),
        status: z.enum(["succeeded", "pending", "failed", "reversed"]).optional(),
        record_id: id.optional(),
        user_id: z.string().max(200).optional().describe("Your own id for who paid (the userId your events send), when you don't have record_id."),
        product: z.string().optional().describe("One of the brand's products, by id or key."),
        refund_of: z
          .string()
          .max(200)
          .optional()
          .describe("A refund or chargeback: the payment it gives back, by its source_id (an order's order_id), its order's messageId or Outflare's transaction id."),
        description: z.string().max(500).optional(),
        occurred_at: z.string().optional().describe("When it happened (RFC 3339); now by default."),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/transactions", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "get_developer_setup",
    {
      title: "Check a brand's developer setup",
      description:
        "A live checklist of what the brand's developer sends: sign-ups and orders from the server, payments counted once with a person, orders carrying " +
        "their payment's id, payments sent two ways sharing an id and amount, payments saved twice, ids reused for renewals, retries under new ids, amounts " +
        "in cents, refunds finding their payment, the website's copies matching the server's, and refunds reaching ad networks. Each check is ok, warning " +
        "or missing, with what it found and what to send to fix it. unmatched_refunds are refunds whose payment isn't known: link them with link_refund. " +
        "Payments saved twice are merged with merge_duplicate_payments.",
      inputSchema: { brand: brandRef },
      annotations: read,
    },
    ({ brand }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/developer-setup", { params: { path: { brand: b } } }))),
  );

  server.registerTool(
    "link_refund",
    {
      title: "Link a refund to its payment",
      description:
        "For a refund whose refund_of named a payment Outflare doesn't have (get_developer_setup lists them): names its payment by any id it has (its " +
        "order_id or source_id, its order's messageId, or Outflare's transaction id). The refund then counts and comes off that payment and its ad conversion.",
      inputSchema: { brand: brandRef, transaction_id: id, payment: z.string().min(1).max(200) },
      annotations: write,
    },
    ({ brand, transaction_id, payment }) =>
      call(() =>
        onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/transactions/{transaction}/refund-of", { params: { path: { brand: b, transaction: transaction_id } }, body: { payment } })),
      ),
  );

  server.registerTool(
    "merge_duplicate_payments",
    {
      title: "Merge payments saved twice",
      description:
        "Keeps once each payment or refund the brand has twice, from an order event and a transaction with the same id (get_developer_setup's duplicates " +
        "check counts them). The copy already counted stays, with its person; the other goes to Recently deleted for 7 days.",
      inputSchema: { brand: brandRef },
      annotations: write,
    },
    ({ brand }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/developer-setup/merge-duplicates", { params: { path: { brand: b } } }))),
  );

  server.registerTool(
    "pin_record_field",
    {
      title: "Pin or unpin a hand edit",
      description: "pinned true keeps a value set by hand for good, over every other source; false starts its time again from now. Only a value set by hand can be pinned.",
      inputSchema: { brand: brandRef, record_id: id, field: z.string().min(1), pinned: z.boolean() },
      annotations: write,
    },
    ({ brand, record_id, field, pinned }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/records/{record}/fields/{field}/pin", { params: { path: { brand: b, record: record_id, field } }, body: { pinned } }))),
  );

  const activity = {
    title: z.string().min(1).max(300), body: z.string().max(20000).optional(),
    priority: z.enum(["low", "normal", "high"]).optional(), status: z.enum(["open", "done"]).optional(),
    assigned_to: id.nullable().optional(), due_at: z.string().datetime().nullable().optional(),
  };
  server.registerTool("create_crm_activity", {
    title: "Create a CRM task or note", description: "Attach a task or note to a person, company, deal or custom record in this brand.",
    inputSchema: { brand_id: id, record_id: id, kind: z.enum(["task", "note"]), ...activity }, annotations: write,
  }, ({ brand_id, ...body }) => call(() => gtm.POST("/v1/brands/{brand}/crm/activities", { params: { path: { brand: brand_id } }, body })));
  server.registerTool("update_crm_activity", {
    title: "Update a CRM task or note", description: "Edit an activity or complete a task. Send the version read from list_crm_activities; stale writes are refused.",
    inputSchema: { brand_id: id, id, version: z.number().int().min(1), ...z.object(activity).partial().shape }, annotations: write,
  }, ({ brand_id, id: activity_id, ...body }) => call(() => gtm.PATCH("/v1/brands/{brand}/crm/activities/{id}", { params: { path: { brand: brand_id, id: activity_id } }, body })));
  server.registerTool("delete_crm_activity", {
    title: "Delete a CRM task or note", description: "Permanently remove a task or note from a brand's CRM.",
    inputSchema: { brand_id: id, id }, annotations: { ...write, destructiveHint: true },
  }, ({ brand_id, id: activity_id }) => call(() => gtm.DELETE("/v1/brands/{brand}/crm/activities/{id}", { params: { path: { brand: brand_id, id: activity_id } } })));
  server.registerTool("create_crm_view", {
    title: "Save a CRM view", description: "Save filters, sorting, columns and layout as a personal or shared view.",
    inputSchema: { brand_id: id, name: z.string().min(1).max(100), record_type: typeKey, shared: z.boolean().optional(), config: z.record(z.string(), z.unknown()).optional() }, annotations: write,
  }, ({ brand_id, ...body }) => call(() => gtm.POST("/v1/brands/{brand}/crm/views", { params: { path: { brand: brand_id } }, body })));
  server.registerTool("update_crm_view", {
    title: "Update your saved CRM view", description: "Only the view's creator can change it. Send its latest version.",
    inputSchema: { brand_id: id, id, version: z.number().int().min(1), name: z.string().min(1).max(100).optional(), shared: z.boolean().optional(), config: z.record(z.string(), z.unknown()).optional() }, annotations: write,
  }, ({ brand_id, id: view_id, ...body }) => call(() => gtm.PATCH("/v1/brands/{brand}/crm/views/{id}", { params: { path: { brand: brand_id, id: view_id } }, body })));
  server.registerTool("delete_crm_view", {
    title: "Delete your saved CRM view", description: "Remove a saved view. The underlying records stay.",
    inputSchema: { brand_id: id, id }, annotations: { ...write, destructiveHint: true },
  }, ({ brand_id, id: view_id }) => call(() => gtm.DELETE("/v1/brands/{brand}/crm/views/{id}", { params: { path: { brand: brand_id, id: view_id } } })));


  server.registerTool(
    "review_record",
    {
      title: "Approve or reject a record held for review",
      description:
        "For a record with needs_review true, such as a person RB2B, Vector or Warmly named on the website. approve lists it and lets cold " +
        "email reach it (never marketing without their consent); reject archives it. Ask the user before deciding for them.",
      inputSchema: { brand: brandRef, record_id: id, decision: z.enum(["approve", "reject"]) },
      annotations: write,
    },
    ({ brand, record_id, decision }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/records/{record}/review", { params: { path: { brand: b, record: record_id } }, body: { decision } }))),
  );

  server.registerTool(
    "create_record",
    {
      title: "Add a customer record",
      description:
        "Add a person, company or other record. Look it up first with lookup_record. A verified identity another record holds is refused " +
        "with identity_taken (the message names that record): change that one instead.",
      inputSchema: {
        brand: brandRef,
        type: typeKey,
        name: z.string().max(500).optional().describe("A person with no name takes first_name and last_name."),
        attributes: attributes.optional(),
        identities: z.array(identity).max(50).optional(),
        origin: origins.optional().describe("Where it came from. Default manual."),
        source: sources.optional().describe("Who the values come from, for precedence. Default manual."),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/records", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "update_record",
    {
      title: "Change or archive a customer record",
      description:
        "attributes merges: null clears a value, keys left out stay. A value set by a higher-precedence source is kept and listed in skipped. " +
        "version is required: send the version from get_record. If someone changed the record since (record_changed), read it again, " +
        "check your change still makes sense and send the new version.",
      inputSchema: {
        brand: brandRef,
        record_id: id,
        name: z.string().max(500).optional(),
        attributes: attributes.optional(),
        source: sources.optional(),
        origin: origins.optional(),
        archived: z.boolean().optional(),
        version: z.number().int().describe("The record's version, from get_record."),
      },
      annotations: write,
    },
    ({ brand, record_id, ...body }) => call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/records/{record}", { params: { path: { brand: b, record: record_id } }, body }))),
  );

  server.registerTool(
    "delete_record",
    {
      title: "Erase a customer record",
      description:
        "Deletes a person or company for good, such as for an erasure request: the record, records merged into it, and for a person their leads, " +
        "cold email and replies, meetings, product events and visits. Deals, payments and opt-outs stay. To hide a record and keep its history, " +
        "archive it with update_record instead. suppress adds their emails (a company's domains) to the opt-out list. Only when the user asked " +
        "for this record to be deleted. version is required: send the version from get_record.",
      inputSchema: {
        brand: brandRef,
        record_id: id,
        suppress: z.boolean().optional().describe("Also never contact them again."),
        version: z.number().int().describe("The record's version, from get_record."),
      },
      annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: false },
    },
    ({ brand, record_id, ...query }) => call(() => onBrand(brand, (b) => gtm.DELETE("/v1/brands/{brand}/records/{record}", { params: { path: { brand: b, record: record_id }, query } }))),
  );

  server.registerTool(
    "add_record_identity",
    {
      title: "Add, verify or change an identity",
      description:
        "Add an email, phone, id or domain to a record, or verify one it holds unverified. When a person changes their email, pass the old " +
        "identity's id as replaces: it is closed and kept in the history. Never verify an email or phone the person has not proved they own.",
      inputSchema: { brand: brandRef, record_id: id, ...identity.shape, replaces: id.optional() },
      annotations: write,
    },
    ({ brand, record_id, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/records/{record}/identities", { params: { path: { brand: b, record: record_id } }, body }))),
  );

  server.registerTool(
    "close_record_identity",
    {
      title: "Remove an identity",
      description: "It stops pointing at the record and stays in its history.",
      inputSchema: { brand: brandRef, record_id: id, identity_id: id },
      annotations: write,
    },
    ({ brand, record_id, identity_id }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/records/{record}/identities/{identity}/close", { params: { path: { brand: b, record: record_id, identity: identity_id } } }))),
  );

  server.registerTool(
    "link_records",
    {
      title: "Link two records",
      description: "A typed link such as partner_of. Record reference fields, such as a person's companies, are set with update_record instead.",
      inputSchema: { brand: brandRef, record_id: id, to: id, kind: z.string().regex(/^[a-z][a-z0-9_]{1,62}$/), properties: z.record(z.string(), z.unknown()).optional() },
      annotations: write,
    },
    ({ brand, record_id, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/records/{record}/relationships", { params: { path: { brand: b, record: record_id } }, body }))),
  );

  server.registerTool(
    "end_record_link",
    {
      title: "End a link",
      description: "The link is kept with its end time.",
      inputSchema: { brand: brandRef, record_id: id, link_id: id },
      annotations: write,
    },
    ({ brand, record_id, link_id }) =>
      call(() =>
        onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/records/{record}/relationships/{relationship}/end", { params: { path: { brand: b, record: record_id, relationship: link_id } } }),
      )),
  );

  server.registerTool(
    "create_record_type",
    {
      title: "Add a record type",
      description: "A kind of record a brand tracks beyond people and companies, such as subscription or partner. Then add its fields with create_field.",
      inputSchema: {
        brand: brandRef,
        name: z.string().min(1).max(60),
        plural: z.string().max(60).optional(),
        key: z.string().optional(),
        description: z.string().max(1000).optional(),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/catalog/types", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "update_record_type",
    {
      title: "Rename or archive a record type",
      description: "Built-in types never change. Archiving is how a type is deleted; it can be restored. Other brands' types are never touched.",
      inputSchema: {
        brand: brandRef,
        type: typeKey,
        name: z.string().max(60).optional(),
        plural: z.string().max(60).optional(),
        description: z.string().max(1000).optional(),
        archived: z.boolean().optional(),
      },
      annotations: write,
    },
    ({ brand, type, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/catalog/types/{type}", { params: { path: { brand: b, type } }, body }))),
  );

  server.registerTool(
    "create_field",
    {
      title: "Add a field",
      description:
        "One definition of a fact the brand's features share. Check get_catalog first so the same fact is never added twice. select and status " +
        "take an option_set; record_reference takes a target_type. A field's key, value type, option list and target never change. Other brands don't see it.",
      inputSchema: {
        brand: brandRef,
        type: typeKey,
        label: z.string().min(1).max(80),
        value_type: valueTypes,
        key: z.string().optional(),
        description: z.string().max(1000).optional(),
        option_set: z.string().optional(),
        multiple: z.boolean().optional(),
        target_type: z.string().optional(),
        write_policy: writePolicy.optional(),
        precedence: z.array(sources).optional(),
        sensitivity: sensitivity.optional(),
        filterable: z.boolean().optional(),
        ...labels,
      },
      annotations: write,
    },
    ({ brand, type, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/catalog/types/{type}/fields", { params: { path: { brand: b, type } }, body }))),
  );

  server.registerTool(
    "update_field",
    {
      title: "Change or archive a field",
      description:
        "Label, description, write policy, precedence, sensitivity, filterable, archived (how a field is deleted), and the labels owner_id, " +
        "retention_days and destinations. Of a built-in field only sensitivity and those labels change. Labels block nothing.",
      inputSchema: {
        brand: brandRef,
        type: typeKey,
        field: z.string().min(1),
        label: z.string().max(80).optional(),
        description: z.string().max(1000).optional(),
        write_policy: writePolicy.optional(),
        precedence: z.array(sources).optional(),
        sensitivity: sensitivity.optional(),
        filterable: z.boolean().optional(),
        archived: z.boolean().optional(),
        ...labels,
      },
      annotations: write,
    },
    ({ brand, type, field, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/catalog/types/{type}/fields/{field}", { params: { path: { brand: b, type, field } }, body }))),
  );

  server.registerTool(
    "create_option_set",
    {
      title: "Add an option list",
      description: "Choices a brand's select and status fields share, such as partner tiers.",
      inputSchema: { brand: brandRef, name: z.string().min(1), key: z.string().optional(), options },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/catalog/option-sets", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "update_option_set",
    {
      title: "Change an option list",
      description: "options sets the order and labels. An option left out is archived, never deleted. Built-in lists never change.",
      inputSchema: { brand: brandRef, set: z.string().min(1), name: z.string().optional(), options: options.optional() },
      annotations: write,
    },
    ({ brand, set, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/catalog/option-sets/{set}", { params: { path: { brand: b, set } }, body }))),
  );

  server.registerTool(
    "define_event",
    {
      title: "Define a product event",
      description:
        "An event a brand's product sends, such as product.activated, with its properties. Only approved events drive segments, rules and journeys. " +
        "For now a name is defined in one brand of a workspace at a time (409 event_name_in_other_brand).",
      inputSchema: {
        brand: brandRef,
        name: z.string().min(1).max(100),
        label: z.string().max(80).optional(),
        description: z.string().max(1000).optional(),
        properties: eventProperties.optional(),
        status: z.enum(["quarantined", "approved", "deprecated"]).optional(),
        sensitivity: sensitivity.optional(),
        ...labels,
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/catalog/events", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "update_event_definition",
    {
      title: "Change, approve or deprecate an event",
      description: "The name never changes. To only approve a held name, approve_event_name needs less access.",
      inputSchema: {
        brand: brandRef,
        name: z.string().min(1),
        label: z.string().max(80).optional(),
        description: z.string().max(1000).optional(),
        properties: eventProperties.optional(),
        status: z.enum(["quarantined", "approved", "deprecated"]).optional(),
        sensitivity: sensitivity.optional(),
        ...labels,
      },
      annotations: write,
    },
    ({ brand, name, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/catalog/events/{event}", { params: { path: { brand: b, event: name } }, body }))),
  );

  server.registerTool(
    "approve_event_name",
    {
      title: "Approve a held event name",
      description:
        "Approves a track name a brand's events are held for (get_brand_events_summary lists them), so replay_brand_events can process them. " +
        "Needs customers.manage only; it changes nothing else about the event.",
      inputSchema: { brand: brandRef, name: z.string().min(1).max(100).describe("The catalog name, such as order_completed.") },
      annotations: write,
    },
    ({ brand, name }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/catalog/events/{event}/approve", { params: { path: { brand: b, event: name } } }))),
  );

  server.registerTool(
    "track_customer_events",
    {
      title: "Send product events",
      description:
        "Send what people did in the product, in Segment's format (track, identify, page, screen, group, alias), up to 500 at a time. " +
        "Each is matched to a person by userId, then a verified email, then anonymousId; a new userId makes a new person. Two userIds are " +
        "never merged. identify traits that match catalog fields are written (first_party); the rest come back in ignored_traits. " +
        "Use a messageId so a retry is stored once. Only send events the workspace's own systems reported, never guesses.",
      inputSchema: {
        batch: z
          .array(
            z
              .object({
                type: z.enum(["track", "identify", "page", "screen", "group", "alias"]),
                userId: z.string().max(200).optional(),
                anonymousId: z.string().max(200).optional(),
                event: z.string().max(200).optional().describe("track: what happened, such as Order Completed."),
                name: z.string().max(200).optional().describe("page or screen: its name."),
                properties: z.record(z.string(), z.unknown()).optional(),
                traits: z
                  .record(z.string(), z.unknown())
                  .optional()
                  .describe("identify: the person; group: the company. email_verified: true only when they proved the email."),
                groupId: z.string().max(200).optional(),
                previousId: z.string().max(200).optional(),
                messageId: z.string().max(200).optional(),
                timestamp: z.string().datetime().optional(),
                context: z.record(z.string(), z.unknown()).optional(),
              })
              .passthrough(),
          )
          .min(1)
          .max(500),
        source: z.string().max(60).optional().describe("A label for where they came from, such as segment. Default api."),
        brand_id: z.string().nullable().optional().describe("The brand new records belong to; leave out for shared."),
      },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/customer-events", { params: { path: path() }, body })),
  );

  server.registerTool(
    "send_brand_events",
    {
      title: "Send product events to a brand",
      description:
        "Send what people did in the brand's product, in Segment's format (track, identify, page, screen, group, alias), up to 500 at a time. " +
        "Every event is saved and answered at once with a receipt id; a job then matches each to a person in this brand (userId, then a " +
        "verified email, then anonymousId) and marks it accepted, duplicate, rejected (with what to fix) or held (a new track name waiting " +
        "for approval). Follow them with list_brand_events. Use a messageId so a retry is stored once. Only send events the brand's own " +
        "systems reported, never guesses.",
      inputSchema: {
        brand: brandRef,
        batch: z
          .array(
            z
              .object({
                type: z.enum(["track", "identify", "page", "screen", "group", "alias"]),
                userId: z.string().max(200).optional(),
                anonymousId: z.string().max(200).optional(),
                event: z.string().max(200).optional().describe("track: what happened, such as Order Completed."),
                name: z.string().max(200).optional().describe("page or screen: its name."),
                properties: z.record(z.string(), z.unknown()).optional(),
                traits: z
                  .record(z.string(), z.unknown())
                  .optional()
                  .describe("identify: the person; group: the company. email_verified: true only when they proved the email."),
                groupId: z.string().max(200).optional(),
                previousId: z.string().max(200).optional(),
                messageId: z.string().max(200).optional(),
                timestamp: z.string().datetime().optional(),
                context: z.record(z.string(), z.unknown()).optional(),
              })
              .passthrough(),
          )
          .min(1)
          .max(500),
        source: z.string().max(60).optional().describe("A label for where they came from, such as segment. Default api."),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/events", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "replay_brand_events",
    {
      title: "Replay rejected or held events",
      description:
        "Process rejected or held events again once what they needed is fixed (the name approved with approve_event_name, a property's " +
        "type changed, an identity freed): either ids, or a filter (status, code, source, batch_id, name), up to 10,000 a call. Accepted and " +
        "duplicate events are skipped. Check the result with list_brand_events.",
      inputSchema: {
        brand: brandRef,
        ids: z.array(id).max(1000).optional(),
        status: z.enum(["rejected", "held"]).optional(),
        code: z.string().optional(),
        source: z.string().optional(),
        batch_id: id.optional(),
        name: z.string().optional(),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/events/replay", { params: { path: { brand: b } }, body }))),
  );

  // The 7-day recovery window: these deletes hide the data everywhere at
  // once and can be undone with restore_deletion for 7 days.
  const recoverable = { readOnlyHint: false, destructiveHint: true, idempotentHint: false, openWorldHint: false } as const;
  const version = z.number().int().optional().describe("The record's version, from get_record; required for agents.");
  const recordBrand = async (record: string, brand: string | undefined): Promise<string | CallToolResult> =>
    brand ?? (await findRecordBrand(record)) ?? { isError: true, content: [{ type: "text" as const, text: "No brand has this record; pass brand." }] };
  const brandOpt = brandRef.optional().describe("The record's brand (handle or id). Leave out to use the brand the record belongs to.");

  server.registerTool(
    "soft_delete_record",
    {
      title: "Delete a person or company (restorable for 7 days)",
      description:
        "Deletes a record with its identities, links and product events: hidden everywhere at once (lists, search, Query, smart lists, " +
        "rules, journeys, sends), its journeys end, and it can be restored with restore_deletion for 7 days, then it is purged. Reports " +
        "that counted its events change. Not for an erasure request: that is delete_record, permanent at once. Only when the user asked.",
      inputSchema: { record_id: id, brand: brandOpt, version },
      annotations: recoverable,
    },
    async ({ record_id, brand, version }) => {
      const ref = await recordBrand(record_id, brand);
      if (typeof ref !== "string") return ref;
      return call(() =>
        onBrand(ref, (b) => gtm.POST("/v1/brands/{brand}/records/{record}/delete", { params: { path: { brand: b, record: record_id } }, body: { version } })),
      );
    },
  );

  server.registerTool(
    "delete_field_value",
    {
      title: "Delete a field's value (restorable for 7 days)",
      description:
        "Removes one wrong value from a record (a reference field's links too) without firing rules or journeys; restore_deletion puts it " +
        "back within 7 days unless the field was set again. Computed fields can't be deleted. To change a value, use update_record instead.",
      inputSchema: { record_id: id, field: z.string().min(1).describe("The field's key."), brand: brandOpt, version },
      annotations: recoverable,
    },
    async ({ record_id, field, brand, version }) => {
      const ref = await recordBrand(record_id, brand);
      if (typeof ref !== "string") return ref;
      return call(() =>
        onBrand(ref, (b) =>
          gtm.POST("/v1/brands/{brand}/records/{record}/fields/{field}/delete", { params: { path: { brand: b, record: record_id, field } }, body: { version } }),
        ),
      );
    },
  );

  server.registerTool(
    "delete_record_identity",
    {
      title: "Delete a wrong identity (restorable for 7 days)",
      description:
        "Deletes an email, phone or id that was wrong, leaving no history (unlike close_record_identity, for one that changed). Its value is " +
        "free for another record at once; restore_deletion brings it back within 7 days unless another record verified it since.",
      inputSchema: { record_id: id, identity: id.describe("The identity's id, from get_record or list_record_identities."), brand: brandOpt, version },
      annotations: recoverable,
    },
    async ({ record_id, identity, brand, version }) => {
      const ref = await recordBrand(record_id, brand);
      if (typeof ref !== "string") return ref;
      return call(() =>
        onBrand(ref, (b) =>
          gtm.POST("/v1/brands/{brand}/records/{record}/identities/{identity}/delete", {
            params: { path: { brand: b, record: record_id, identity } },
            body: { version },
          }),
        ),
      );
    },
  );

  server.registerTool(
    "delete_record_link",
    {
      title: "Delete a wrong link (restorable for 7 days)",
      description:
        "Deletes a link between two records that was wrong, leaving no history (unlike end_record_link, for one that ended). A link that is a " +
        "reference field's value goes with delete_field_value instead. restore_deletion brings it back within 7 days.",
      inputSchema: { record_id: id, relationship: id.describe("The link's id, from list_record_links."), brand: brandOpt, version },
      annotations: recoverable,
    },
    async ({ record_id, relationship, brand, version }) => {
      const ref = await recordBrand(record_id, brand);
      if (typeof ref !== "string") return ref;
      return call(() =>
        onBrand(ref, (b) =>
          gtm.POST("/v1/brands/{brand}/records/{record}/relationships/{relationship}/delete", {
            params: { path: { brand: b, record: record_id, relationship } },
            body: { version },
          }),
        ),
      );
    },
  );

  server.registerTool(
    "delete_customer_event",
    {
      title: "Delete a product event or payment (restorable for 7 days)",
      description:
        "Deletes one product event of a brand, such as a test payment, with its attribution and ad conversion sends. Reports, attribution " +
        "and rule history that counted it change; a payment's billing fields are worked out again. Nothing already sent is taken back. " +
        "restore_deletion brings it back within 7 days without firing rules or journeys. Only when the user asked.",
      inputSchema: { brand: brandRef, event: id.describe("The event's id, from list_customer_events or get_record_timeline.") },
      annotations: recoverable,
    },
    ({ brand, event }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/customer-events/{event}/delete", { params: { path: { brand: b, event } } }))),
  );

  server.registerTool(
    "delete_transaction",
    {
      title: "Delete a transaction (restorable for 7 days)",
      description:
        "Moves one of a brand's transactions, such as a test payment, into Recently deleted: lists, query and the record card leave it out, and " +
        "the same source_id sent again isn't added back. restore_deletion brings it back within 7 days without sending transaction.created again. " +
        "Only when the user asked.",
      inputSchema: { brand: brandRef, transaction: id.describe("The transaction's id, from list_transactions.") },
      annotations: recoverable,
    },
    ({ brand, transaction }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/transactions/{transaction}/delete", { params: { path: { brand: b, transaction } } }))),
  );

  server.registerTool(
    "restore_deletion",
    {
      title: "Restore something deleted",
      description:
        "Puts back everything a deletion holds (from list_recently_deleted), as it was, within its 7 days. Nothing fires or is sent again: " +
        "no rule, journey, webhook or email. What can't come back stays in the deletion with a reason (taken: its email or id belongs to " +
        "another record now; gone: the other end was erased; changed: the field was set again; record_deleted: restore the record first).",
      inputSchema: { brand: brandRef, deletion: id },
      annotations: write,
    },
    ({ brand, deletion }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/recently-deleted/{deletion}/restore", { params: { path: { brand: b, deletion } } }))),
  );

  server.registerTool(
    "undo_merge",
    {
      title: "Undo a merge",
      description:
        "Unmerges (merge id from list_merges): the merged record is its own again and every row the merge moved goes back to it; what came " +
        "to the kept record later stays there, and a field changed since keeps its new value. Nothing fires (no rule, journey or email). " +
        "Undo chained merges newest first (merge_not_latest otherwise); merges from before Outflare kept what merges move can't be undone " +
        "(merge_not_reversible). Only when the user asked.",
      inputSchema: { brand: brandRef, merge: id },
      annotations: recoverable,
    },
    ({ brand, merge }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/merges/{merge}/undo", { params: { path: { brand: b, merge } } }))),
  );

  server.registerTool(
    "undo_batch",
    {
      title: "Undo an import or ingest batch",
      description:
        "Rolls back a batch in the mode the user chose after preview_batch_undo: untouched (only what nobody changed since; changed records " +
        "stay) or everything (changed records go too). Records it created go with their identities, links and events, what it added to other " +
        "records is removed, fields it set go back, and leads no sequence used go. All of it is one deletion in Recently deleted, restorable " +
        "with restore_deletion for 7 days. Emails already sent stay. A large batch runs in the background: follow undo.status with list_batches.",
      inputSchema: { brand: brandRef, batch: id, mode: z.enum(["untouched", "everything"]) },
      annotations: recoverable,
    },
    ({ brand, batch, mode }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/batches/{batch}/undo", { params: { path: { brand: b, batch } }, body: { mode } }))),
  );

  server.registerTool(
    "request_privacy_export",
    {
      title: "Export what a brand holds about a person",
      description:
        "For a person's request to see their data: builds one file of everything the record's brand holds about a person or company " +
        "(fields, history, ids, links, product events, consent, emails, replies, meetings, deals, payments and the rest), never another " +
        "brand's data and never secrets. Answers at once, pending; follow it with get_privacy_export until it is ready, then share its " +
        "download_url with someone who has customers.manage. The file can be downloaded for 7 days. Only when someone asked for it.",
      inputSchema: {
        record: id.describe("The person's or company's record id."),
        brand: brandRef.optional().describe("The record's brand (handle or id). Leave out to use the brand the record belongs to."),
      },
      annotations: write,
    },
    async ({ record, brand }) => {
      const ref = brand ?? (await findRecordBrand(record));
      if (!ref) return { isError: true, content: [{ type: "text" as const, text: "No brand has this record; pass brand." }] };
      return call(() =>
        onBrand(ref!, (b) => gtm.POST("/v1/brands/{brand}/records/{record}/privacy-exports", { params: { path: { brand: b, record } } })),
      );
    },
  );

  server.registerTool(
    "record_consent",
    {
      title: "Record consent",
      description:
        "Record that a person granted or withdrew consent on a channel, with where it happened and what proves it. Nothing is overwritten; " +
        "the latest counts. Only record what the person actually did (a form, a preference centre, an unsubscribe), never assume consent.",
      inputSchema: {
        brand: brandRef,
        record_id: id,
        channel: z.enum(["email", "sms", "push", "whatsapp", "in_app"]),
        status: z.enum(["granted", "withdrawn"]),
        topic: z.string().max(63).optional().describe("A subscription topic's key, such as product_updates. Leave out for the whole channel."),
        purpose: z.string().max(40).optional().describe("Default marketing."),
        source: z.string().max(60).optional().describe("signup_form, preference_center, unsubscribe_link, import. Default api."),
        evidence: z.record(z.string(), z.unknown()).optional().describe("What proves it, such as the form and the wording's version."),
        occurred_at: z.string().datetime().optional(),
      },
      annotations: write,
    },
    ({ brand, record_id, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/records/{record}/consents", { params: { path: { brand: b, record: record_id } }, body }))),
  );

  server.registerTool(
    "merge_records",
    {
      title: "Merge a duplicate",
      description:
        "Merge from into record_id, which is kept: identities, links, product events and consents move to it, and from's attributes fill in " +
        "what it lacks. from is archived with merged_into set. Two records with their own user ids are refused (merge_refused): use " +
        "link_record_duplicate. Merge only when the records are clearly the same human.",
      inputSchema: { brand: brandRef, record_id: id.describe("The record to keep."), from: id.describe("The duplicate merged into it."), version: z.number().int().describe("The kept record's version, from get_record.") },
      annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: false },
    },
    ({ brand, record_id, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/records/{record}/merge", { params: { path: { brand: b, record: record_id } }, body }))),
  );

  server.registerTool(
    "dismiss_record_duplicate",
    {
      title: "Not the same human",
      description: "Answer a possible duplicate: these are different people. The pair is not asked about again.",
      inputSchema: { brand: brandRef, candidate_id: id },
      annotations: write,
    },
    ({ brand, candidate_id }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/records/duplicates/{candidate}/dismiss", { params: { path: { brand: b, candidate: candidate_id } } }))),
  );

  server.registerTool(
    "link_record_duplicate",
    {
      title: "The same human, kept apart",
      description: "Link a possible duplicate as the same human without merging, such as one person with two accounts in the product.",
      inputSchema: { brand: brandRef, candidate_id: id },
      annotations: write,
    },
    ({ brand, candidate_id }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/records/duplicates/{candidate}/link", { params: { path: { brand: b, candidate: candidate_id } } }))),
  );
}
