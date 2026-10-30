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
const brandRef = z.string().min(1).describe("The brand's handle (such as repocket) or id, from list_brands.");
const typeKey = z.string().min(1).describe("A record type's key: person, account, or one from get_catalog.");
const valueTypes = z.enum(["text", "number", "currency", "date", "timestamp", "checkbox", "select", "status", "rating", "email", "phone", "url", "location", "record_reference", "actor_reference"]);
const sources = z.enum(["manual", "first_party", "crm_sync", "enrichment", "import"]);
const origins = z.enum(["first_party", "prospect", "crm_sync", "import", "manual"]);
const sensitivity = z.enum(["public", "internal", "confidential", "restricted"]);
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

export function registerRecordTools(server: McpServer, gtm: MissionClient, workspace: () => string, readOnly: boolean, call: Call): void {
  const path = () => ({ ws: workspace() });
  const recordPath = (record: string) => ({ ws: workspace(), record });
  // Brand routes take the brand's id; a handle is looked up first.
  const uuidShape = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
  const onBrand = async <T extends { data?: unknown; error?: unknown; response: Response }>(
    brand: string,
    fn: (id: string) => Promise<T>,
  ): Promise<{ data?: unknown; error?: unknown; response: Response }> => {
    if (uuidShape.test(brand)) return fn(brand);
    const found = await gtm.GET("/v1/workspaces/{ws}/brands/{brand}", { params: { path: { ws: workspace(), brand } } });
    if (!found.data) return found;
    return fn(found.data.id);
  };

  server.registerTool(
    "get_catalog",
    {
      title: "Read the catalog",
      description:
        "Everything customer records can hold: record types (person and account built in, plus the workspace's own such as deal), each type's fields " +
        "with their value types, the shared option lists (country, industry, company_size, seniority, lifecycle_stage and the workspace's own) and " +
        "the product events. Call before writing records or adding fields, and reuse what exists.",
      inputSchema: { archived: z.boolean().optional().describe("Include archived types, fields and options.") },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/catalog", { params: { path: path(), query: q } })),
  );

  server.registerTool(
    "list_records",
    {
      title: "List customer records",
      description:
        "People, companies or another record type, newest first. q matches the name or any identity, such as part of an email. " +
        "Records held for review (people a visitor identification tool named) are left out unless review is needs_review or all.",
      inputSchema: {
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
        brand: z.string().optional().describe("A brand's handle or id, none for shared records, or all."),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional().describe("next_cursor from the previous page."),
      },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/records", { params: { path: path(), query: q } })),
  );

  server.registerTool(
    "get_record",
    {
      title: "Read a customer record",
      description: "One record with its attributes, which source set each one, and its active identities.",
      inputSchema: { record_id: id },
      annotations: read,
    },
    ({ record_id }) => call(() => gtm.GET("/v1/workspaces/{ws}/records/{record}", { params: { path: recordPath(record_id) } })),
  );

  server.registerTool(
    "lookup_record",
    {
      title: "Find records by identity",
      description:
        "The records an email, phone, user id or other identity points at. owner is the one record holding it verified; the others hold it " +
        "unverified, such as a sign-up that never confirmed its email. Use before create_record to avoid duplicates.",
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
      inputSchema: { record_id: id, field: z.string().optional(), limit: z.number().int().min(1).max(200).optional(), cursor: z.string().optional() },
      annotations: read,
    },
    ({ record_id, ...q }) => call(() => gtm.GET("/v1/workspaces/{ws}/records/{record}/history", { params: { path: recordPath(record_id), query: q } })),
  );

  server.registerTool(
    "list_record_identities",
    {
      title: "A record's identities",
      description: "Its emails, phones, ids and domains; closed: true adds closed ones, such as an email the person changed.",
      inputSchema: { record_id: id, closed: z.boolean().optional() },
      annotations: read,
    },
    ({ record_id, ...q }) => call(() => gtm.GET("/v1/workspaces/{ws}/records/{record}/identities", { params: { path: recordPath(record_id), query: q } })),
  );

  server.registerTool(
    "list_record_links",
    {
      title: "A record's links",
      description: "Links both ways, such as the companies a person belongs to (field links) and partner_of links.",
      inputSchema: { record_id: id, ended: z.boolean().optional() },
      annotations: read,
    },
    ({ record_id, ...q }) => call(() => gtm.GET("/v1/workspaces/{ws}/records/{record}/relationships", { params: { path: recordPath(record_id), query: q } })),
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
        record_id: id,
        kinds: z
          .string()
          .optional()
          .describe("Comma-separated: event, attribute_change, identity, consent, message (cold email and replies), meeting, deal, visit, opt_out, journey. Default all."),
        people: z.enum(["true", "false"]).optional().describe("For a company: false shows only the company's own items, without its people's."),
        brand: z.string().optional().describe("A brand's id or handle: its items and the ones no brand owns."),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional(),
      },
      annotations: read,
    },
    ({ record_id, ...q }) => call(() => gtm.GET("/v1/workspaces/{ws}/records/{record}/timeline", { params: { path: recordPath(record_id), query: q } })),
  );

  server.registerTool(
    "list_record_consents",
    {
      title: "A person's consents",
      description:
        "The current consent for each channel, topic and purpose (the latest grant or withdrawal); history: true lists every one. " +
        "Check it before any marketing message: no grant means do not send.",
      inputSchema: { record_id: id, history: z.boolean().optional() },
      annotations: read,
    },
    ({ record_id, history }) =>
      call(() =>
        gtm.GET("/v1/workspaces/{ws}/records/{record}/consents", {
          params: { path: recordPath(record_id), query: history === undefined ? {} : { history: history ? "true" : "false" } },
        }),
      ),
  );

  server.registerTool(
    "list_customer_events",
    {
      title: "List product events",
      description: "Product events, newest first. matched: false lists anonymous visitors' events not yet linked to a person.",
      inputSchema: {
        name: z.string().optional().describe("A catalog name, such as order_completed or page.viewed."),
        kind: z.enum(["track", "identify", "page", "screen", "group", "alias"]).optional(),
        user_id: z.string().optional(),
        anonymous_id: z.string().optional(),
        matched: z.enum(["true", "false", "all"]).optional(),
        since: z.string().datetime().optional(),
        brand: z.string().optional(),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional(),
      },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/customer-events", { params: { path: path(), query: q } })),
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
        status: z.string().optional().describe("Comma-separated: open, blocked, merged, linked, dismissed; or all. Default open,blocked."),
        type: typeKey.optional(),
        record: id.optional().describe("Only pairs with this record."),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional(),
      },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/records/duplicates", { params: { path: path(), query: q } })),
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
        "the latest reason, and the held names waiting for approval (approve with update_event_definition, then replay_brand_events).",
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

  if (readOnly) return;

  server.registerTool(
    "review_record",
    {
      title: "Approve or reject a record held for review",
      description:
        "For a record with needs_review true, such as a person RB2B, Vector or Warmly named on the website. approve lists it and lets cold " +
        "email reach it (never marketing without their consent); reject archives it. Ask the user before deciding for them.",
      inputSchema: { record_id: id, decision: z.enum(["approve", "reject"]) },
      annotations: write,
    },
    ({ record_id, decision }) =>
      call(() => gtm.POST("/v1/workspaces/{ws}/records/{record}/review", { params: { path: recordPath(record_id) }, body: { decision } })),
  );

  server.registerTool(
    "create_record",
    {
      title: "Add a customer record",
      description:
        "Add a person, company or other record. Look it up first with lookup_record. A verified identity another record holds is refused " +
        "with identity_taken (the message names that record): change that one instead.",
      inputSchema: {
        type: typeKey,
        name: z.string().max(500).optional().describe("A person with no name takes first_name and last_name."),
        attributes: attributes.optional(),
        identities: z.array(identity).max(50).optional(),
        origin: origins.optional().describe("Where it came from. Default manual."),
        source: sources.optional().describe("Who the values come from, for precedence. Default manual."),
        brand_id: z.string().nullable().optional().describe("A brand's handle or id; leave out or null for a record every brand shares."),
      },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/records", { params: { path: path() }, body })),
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
        record_id: id,
        name: z.string().max(500).optional(),
        attributes: attributes.optional(),
        source: sources.optional(),
        origin: origins.optional(),
        brand_id: z.string().nullable().optional(),
        archived: z.boolean().optional(),
        version: z.number().int().describe("The record's version, from get_record."),
      },
      annotations: write,
    },
    ({ record_id, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/records/{record}", { params: { path: recordPath(record_id) }, body })),
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
        record_id: id,
        suppress: z.boolean().optional().describe("Also never contact them again."),
        version: z.number().int().describe("The record's version, from get_record."),
      },
      annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: false },
    },
    ({ record_id, ...query }) => call(() => gtm.DELETE("/v1/workspaces/{ws}/records/{record}", { params: { path: recordPath(record_id), query } })),
  );

  server.registerTool(
    "add_record_identity",
    {
      title: "Add, verify or change an identity",
      description:
        "Add an email, phone, id or domain to a record, or verify one it holds unverified. When a person changes their email, pass the old " +
        "identity's id as replaces: it is closed and kept in the history. Never verify an email or phone the person has not proved they own.",
      inputSchema: { record_id: id, ...identity.shape, replaces: id.optional() },
      annotations: write,
    },
    ({ record_id, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/records/{record}/identities", { params: { path: recordPath(record_id) }, body })),
  );

  server.registerTool(
    "close_record_identity",
    {
      title: "Remove an identity",
      description: "It stops pointing at the record and stays in its history.",
      inputSchema: { record_id: id, identity_id: id },
      annotations: write,
    },
    ({ record_id, identity_id }) =>
      call(() => gtm.POST("/v1/workspaces/{ws}/records/{record}/identities/{identity}/close", { params: { path: { ...recordPath(record_id), identity: identity_id } } })),
  );

  server.registerTool(
    "link_records",
    {
      title: "Link two records",
      description: "A typed link such as partner_of. Record reference fields, such as a person's companies, are set with update_record instead.",
      inputSchema: { record_id: id, to: id, kind: z.string().regex(/^[a-z][a-z0-9_]{1,62}$/), properties: z.record(z.string(), z.unknown()).optional() },
      annotations: write,
    },
    ({ record_id, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/records/{record}/relationships", { params: { path: recordPath(record_id) }, body })),
  );

  server.registerTool(
    "end_record_link",
    {
      title: "End a link",
      description: "The link is kept with its end time.",
      inputSchema: { record_id: id, link_id: id },
      annotations: write,
    },
    ({ record_id, link_id }) =>
      call(() =>
        gtm.POST("/v1/workspaces/{ws}/records/{record}/relationships/{relationship}/end", { params: { path: { ...recordPath(record_id), relationship: link_id } } }),
      ),
  );

  server.registerTool(
    "create_record_type",
    {
      title: "Add a record type",
      description: "A kind of record beyond people and companies, such as deal, subscription or partner. Then add its fields with create_field.",
      inputSchema: { name: z.string().min(1).max(60), plural: z.string().max(60).optional(), key: z.string().optional(), description: z.string().max(1000).optional() },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/catalog/types", { params: { path: path() }, body })),
  );

  server.registerTool(
    "update_record_type",
    {
      title: "Rename or archive a record type",
      description: "Built-in types never change.",
      inputSchema: { type: typeKey, name: z.string().max(60).optional(), plural: z.string().max(60).optional(), description: z.string().max(1000).optional(), archived: z.boolean().optional() },
      annotations: write,
    },
    ({ type, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/catalog/types/{type}", { params: { path: { ws: workspace(), type } }, body })),
  );

  server.registerTool(
    "create_field",
    {
      title: "Add a field",
      description:
        "One definition of a fact every feature shares. Check get_catalog first so the same fact is never added twice. select and status " +
        "take an option_set; record_reference takes a target_type. A field's key, value type, option list and target never change.",
      inputSchema: {
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
      },
      annotations: write,
    },
    ({ type, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/catalog/types/{type}/fields", { params: { path: { ws: workspace(), type } }, body })),
  );

  server.registerTool(
    "update_field",
    {
      title: "Change or archive a field",
      description: "Label, description, write policy, precedence, sensitivity, filterable or archived. Built-in fields never change.",
      inputSchema: {
        type: typeKey,
        field: z.string().min(1),
        label: z.string().max(80).optional(),
        description: z.string().max(1000).optional(),
        write_policy: writePolicy.optional(),
        precedence: z.array(sources).optional(),
        sensitivity: sensitivity.optional(),
        filterable: z.boolean().optional(),
        archived: z.boolean().optional(),
      },
      annotations: write,
    },
    ({ type, field, ...body }) =>
      call(() => gtm.PATCH("/v1/workspaces/{ws}/catalog/types/{type}/fields/{field}", { params: { path: { ws: workspace(), type, field } }, body })),
  );

  server.registerTool(
    "create_option_set",
    {
      title: "Add an option list",
      description: "Choices that select and status fields share, such as deal stages or partner tiers.",
      inputSchema: { name: z.string().min(1), key: z.string().optional(), options },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/catalog/option-sets", { params: { path: path() }, body })),
  );

  server.registerTool(
    "update_option_set",
    {
      title: "Change an option list",
      description: "options sets the order and labels. An option left out is archived, never deleted. Built-in lists never change.",
      inputSchema: { set: z.string().min(1), name: z.string().optional(), options: options.optional() },
      annotations: write,
    },
    ({ set, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/catalog/option-sets/{set}", { params: { path: { ws: workspace(), set } }, body })),
  );

  server.registerTool(
    "define_event",
    {
      title: "Define a product event",
      description: "An event the product sends, such as product.activated, with its properties. Only approved events drive segments, rules and journeys.",
      inputSchema: {
        name: z.string().min(1).max(100),
        label: z.string().max(80).optional(),
        description: z.string().max(1000).optional(),
        properties: eventProperties.optional(),
        status: z.enum(["quarantined", "approved", "deprecated"]).optional(),
        sensitivity: sensitivity.optional(),
      },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/catalog/events", { params: { path: path() }, body })),
  );

  server.registerTool(
    "update_event_definition",
    {
      title: "Change, approve or deprecate an event",
      description: "The name never changes.",
      inputSchema: {
        name: z.string().min(1),
        label: z.string().max(80).optional(),
        description: z.string().max(1000).optional(),
        properties: eventProperties.optional(),
        status: z.enum(["quarantined", "approved", "deprecated"]).optional(),
        sensitivity: sensitivity.optional(),
      },
      annotations: write,
    },
    ({ name, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/catalog/events/{event}", { params: { path: { ws: workspace(), event: name } }, body })),
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
        "Process rejected or held events again once what they needed is fixed (the name approved with update_event_definition, a property's " +
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
      let ref = brand;
      if (!ref) {
        const found = await gtm.GET("/v1/workspaces/{ws}/records/{record}", { params: { path: recordPath(record) } });
        if (!found.data) return call(async () => found);
        ref = found.data.brand_id ?? undefined;
        if (!ref) return { isError: true, content: [{ type: "text" as const, text: "This record has no brand; pass brand." }] };
      }
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
    ({ record_id, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/records/{record}/consents", { params: { path: recordPath(record_id) }, body })),
  );

  server.registerTool(
    "merge_records",
    {
      title: "Merge a duplicate",
      description:
        "Merge from into record_id, which is kept: identities, links, product events and consents move to it, and from's attributes fill in " +
        "what it lacks. from is archived with merged_into set. Two records with their own user ids are refused (merge_refused): use " +
        "link_record_duplicate. Merge only when the records are clearly the same human.",
      inputSchema: { record_id: id.describe("The record to keep."), from: id.describe("The duplicate merged into it."), version: z.number().int().describe("The kept record's version, from get_record.") },
      annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: false },
    },
    ({ record_id, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/records/{record}/merge", { params: { path: recordPath(record_id) }, body })),
  );

  server.registerTool(
    "dismiss_record_duplicate",
    {
      title: "Not the same human",
      description: "Answer a possible duplicate: these are different people. The pair is not asked about again.",
      inputSchema: { candidate_id: id },
      annotations: write,
    },
    ({ candidate_id }) =>
      call(() => gtm.POST("/v1/workspaces/{ws}/records/duplicates/{candidate}/dismiss", { params: { path: { ws: workspace(), candidate: candidate_id } } })),
  );

  server.registerTool(
    "link_record_duplicate",
    {
      title: "The same human, kept apart",
      description: "Link a possible duplicate as the same human without merging, such as one person with two accounts in the product.",
      inputSchema: { candidate_id: id },
      annotations: write,
    },
    ({ candidate_id }) =>
      call(() => gtm.POST("/v1/workspaces/{ws}/records/duplicates/{candidate}/link", { params: { path: { ws: workspace(), candidate: candidate_id } } })),
  );
}
