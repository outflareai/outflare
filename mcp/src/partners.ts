// Tools for partner programs: customer referrals, affiliates and
// influencers on one platform. Programs hold the rules, partners are
// customer records with a ref code and links, the workspace's backend
// reports conversions, and rewards and payouts are a ledger. Outflare never
// sends money: payouts are marked paid after the workspace pays them.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const id = z.string().uuid();
const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: false } as const;

const rule = z.object({
  group_id: z.string().uuid().nullable().optional().describe("Only partners in this group."),
  trigger: z.string().max(80).describe("signup, sale, or a custom conversion's event_name such as trial_started."),
  side: z.enum(["partner", "referred"]).optional().describe("referred: the customer they brought (two-sided rewards), always an action."),
  kind: z.enum(["percent", "fixed", "action"]),
  percent_bp: z.number().int().min(1).max(10000).optional().describe("percent: basis points of the sale (2500 is 25%)."),
  amount_cents: z.number().int().min(1).optional().describe("fixed: per lead or sale."),
  action: z.string().max(60).optional().describe("action: what the workspace's system does, such as grant_credit."),
  action_data: z.record(z.string(), z.unknown()).optional().describe('action: its details, such as {"gb": 10}.'),
  recurrence: z.enum(["first", "every", "months"]).optional(),
  months: z.number().int().min(1).max(120).optional(),
  min_sales: z.number().int().min(0).optional().describe("A tier: applies once the partner has made this many sales."),
  product_ids: z.array(z.string()).max(50).optional(),
});

const programSettings = {
  name: z.string().max(120).optional(),
  brand: z.string().optional().describe("A brand's id or handle."),
  landing_url: z.string().max(2000).optional().describe("Where partner links land."),
  currency: z.string().length(3).optional(),
  credit_window_days: z.number().int().min(1).max(365).optional(),
  credit_model: z.enum(["first", "last"]).optional(),
  codes_win: z.boolean().optional().describe("A promo code beats a link when a conversion carries both."),
  hold_days: z.number().int().min(0).max(365).optional(),
  auto_approve: z.boolean().optional(),
  auto_accept: z.boolean().optional(),
  min_payout_cents: z.number().int().min(0).optional(),
  terms_url: z.string().max(2000).optional(),
  sales_source: z.string().optional().describe('A billing connector (stripe) whose synced payments and refunds are also partner sales; "" for none.'),
};

const page = {
  limit: z.number().int().min(1).max(500).optional(),
  cursor: z.string().optional().describe("next_cursor from the previous page."),
};

// workspace is read on every call: with an agency, use_client changes it.
export function registerPartnerTools(server: McpServer, gtm: MissionClient, workspace: () => string, readOnly: boolean, call: Call): void {
  const program = z.string().describe("The program's id or slug.");
  const partner = id.describe("The partner's id (their membership in one program), from list_partners.");
  const path = () => ({ ws: workspace() });

  server.registerTool(
    "list_partner_programs",
    {
      title: "List partner programs",
      description: "Referral, affiliate and influencer programs with their credit rule, hold, minimum payout and reward rules.",
      inputSchema: { status: z.enum(["active", "paused", "archived"]).optional() },
      annotations: read,
    },
    (query) => call(() => gtm.GET("/v1/workspaces/{ws}/partner-programs", { params: { path: path(), query } })),
  );

  server.registerTool(
    "get_partner_program",
    {
      title: "Get a partner program",
      description: "One program with its reward rules and groups.",
      inputSchema: { program },
      annotations: read,
    },
    ({ program }) => call(() => gtm.GET("/v1/workspaces/{ws}/partner-programs/{id}", { params: { path: { ws: workspace(), id: program } } })),
  );

  server.registerTool(
    "list_partners",
    {
      title: "List partners",
      description:
        "Partners newest first, each with their program, ref code, short link, campaign link, promo codes and totals: clicks, referrals, " +
        "customers, sales, and cash pending, approved, paid and in their balance.",
      inputSchema: {
        program: z.string().optional().describe("A program's id or slug."),
        status: z.enum(["pending", "active", "suspended", "rejected"]).optional(),
        record_id: id.optional().describe("The partner's customer record."),
        ...page,
      },
      annotations: read,
    },
    (query) => call(() => gtm.GET("/v1/workspaces/{ws}/partners", { params: { path: path(), query } })),
  );

  server.registerTool(
    "get_partner",
    { title: "Get a partner", description: "One partner with their links, promo codes and totals.", inputSchema: { partner_id: partner }, annotations: read },
    ({ partner_id }) => call(() => gtm.GET("/v1/workspaces/{ws}/partners/{id}", { params: { path: { ws: workspace(), id: partner_id } } })),
  );

  server.registerTool(
    "list_partner_conversions",
    {
      title: "List partner conversions",
      description: "Sign-ups, sales, refunds and custom conversions credited to partners, newest first.",
      inputSchema: { type: z.enum(["signup", "sale", "refund", "custom"]).optional(), partner_id: id.optional(), program: z.string().optional(), ...page },
      annotations: read,
    },
    (query) => call(() => gtm.GET("/v1/workspaces/{ws}/partner-conversions", { params: { path: path(), query } })),
  );

  server.registerTool(
    "list_partner_referrals",
    {
      title: "List referred customers",
      description: "Each customer a partner was credited with (once per program), how (ref, click, promo_code, visit), and their net sales.",
      inputSchema: {
        partner_id: id.optional(),
        record_id: id.optional().describe("The referred customer."),
        status: z.enum(["lead", "customer", "refunded", "rejected"]).optional(),
        ...page,
      },
      annotations: read,
    },
    (query) => call(() => gtm.GET("/v1/workspaces/{ws}/partner-referrals", { params: { path: path(), query } })),
  );

  server.registerTool(
    "list_partner_rewards",
    {
      title: "List the partner reward ledger",
      description:
        "Cash, fees, actions and adjustments, newest first. pending is on hold or waiting for a person; approved cash counts toward the next " +
        "payout; an approved action waits for the workspace's system to carry it out.",
      inputSchema: {
        status: z.enum(["pending", "approved", "rejected", "paid", "fulfilled"]).optional(),
        kind: z.enum(["cash", "fee", "action", "adjustment"]).optional(),
        partner_id: id.optional(),
        payout_id: id.optional(),
        program: z.string().optional(),
        ...page,
      },
      annotations: read,
    },
    (query) => call(() => gtm.GET("/v1/workspaces/{ws}/partner-rewards", { params: { path: path(), query } })),
  );

  server.registerTool(
    "list_partner_payouts",
    {
      title: "List partner payouts",
      description: "Payouts newest first: due ones are waiting to be paid from the workspace's own account.",
      inputSchema: { status: z.enum(["due", "paid", "canceled"]).optional(), partner_id: id.optional(), ...page },
      annotations: read,
    },
    (query) => call(() => gtm.GET("/v1/workspaces/{ws}/partner-payouts", { params: { path: path(), query } })),
  );

  server.registerTool(
    "list_partner_postbacks",
    {
      title: "List a partner's postbacks to their tracker",
      description: "The last 50 calls to the partner's postback URL: queued, sent, or failed with the tracker's answer.",
      inputSchema: { partner_id: partner },
      annotations: read,
    },
    ({ partner_id }) => call(() => gtm.GET("/v1/workspaces/{ws}/partners/{id}/postbacks", { params: { path: { ws: workspace(), id: partner_id } } })),
  );

  server.registerTool(
    "export_partner_payouts",
    {
      title: "Export partner payouts as CSV",
      description:
        "Due payouts (or paid, canceled, all) as CSV, with each partner's payout details, for paying them from the workspace's own account " +
        "or a payout tool. Needs partners.manage; the export is recorded in the audit log. Never paste the payout details anywhere public.",
      inputSchema: { status: z.enum(["due", "paid", "canceled", "all"]).optional(), program: z.string().max(100).optional() },
      annotations: read,
    },
    (query) => call(() => gtm.GET("/v1/workspaces/{ws}/partner-payouts/export", { params: { path: path(), query }, parseAs: "text" })),
  );

  server.registerTool(
    "list_partner_assets",
    { title: "List a program's assets for partners", description: "Banners, copy and links shown on the portal.", inputSchema: { program }, annotations: read },
    ({ program }) => call(() => gtm.GET("/v1/workspaces/{ws}/partner-programs/{id}/assets", { params: { path: { ws: workspace(), id: program } } })),
  );

  server.registerTool(
    "list_partner_imports",
    { title: "List a program's imports from Tolt", description: "Newest first: status, counts per kind (created, updated, unchanged, skipped) and issues by Tolt id and reason.", inputSchema: { program }, annotations: read },
    ({ program }) => call(() => gtm.GET("/v1/workspaces/{ws}/partner-programs/{id}/imports", { params: { path: { ws: workspace(), id: program } } })),
  );

  server.registerTool(
    "get_partner_import",
    { title: "Get an import from Tolt", description: "Its status, counts per kind and issues.", inputSchema: { program, import_id: id }, annotations: read },
    ({ program, import_id }) => call(() => gtm.GET("/v1/workspaces/{ws}/partner-programs/{id}/imports/{import}", { params: { path: { ws: workspace(), id: program, import: import_id } } })),
  );

  if (readOnly) return;

  server.registerTool(
    "start_partner_import",
    {
      title: "Import a partner program from Tolt",
      description:
        "Brings the workspace's Tolt program into this one, with its own Tolt connection: partners with their link codes, payout details, " +
        "promo codes, referred customers, transactions and commissions, paid ones as paid payouts. Safe to run again until switch-over day: " +
        "each run adds only what is new and brings over status changes. Sends no reward webhooks. Poll get_partner_import for the result. " +
        "source_program is the Tolt program id, needed only when the account has more than one.",
      inputSchema: { program, source_program: z.string().max(200).optional(), integration_id: id.optional() },
      annotations: write,
    },
    ({ program, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/partner-programs/{id}/imports", { params: { path: { ws: workspace(), id: program } }, body })),
  );

  server.registerTool(
    "create_partner_program",
    {
      title: "Create a partner program",
      description:
        "kind picks a preset, and every setting it fills in can change. referral: two-sided action rewards on the first sale that the " +
        "workspace's own system grants (such as credit), partners accepted at once. affiliate: 20% of every sale, 30-day hold, $50 minimum " +
        "payout. influencer: track only; log fees with log_partner_fee. The credit rule is the program's own and never follows the " +
        "workspace's attribution settings.",
      inputSchema: { ...programSettings, name: z.string().max(120), kind: z.enum(["referral", "affiliate", "influencer"]), slug: z.string().max(60).optional(), rules: z.array(rule).max(50).optional() },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/partner-programs", { params: { path: path() }, body })),
  );

  server.registerTool(
    "update_partner_program",
    {
      title: "Change a partner program",
      description: "Change settings, or pause, archive or restore it. Send only what changes. A paused program credits nothing new.",
      inputSchema: { program, ...programSettings, status: z.enum(["active", "paused", "archived"]).optional() },
      annotations: write,
    },
    ({ program, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/partner-programs/{id}", { params: { path: { ws: workspace(), id: program } }, body })),
  );

  server.registerTool(
    "set_partner_rules",
    {
      title: "Replace a program's reward rules",
      description:
        "Rules stack: each rule a conversion matches gives a reward, except that only the best cash rule counts (a group's own, then the " +
        "highest tier reached). [] makes the program track only. Rewards already earned keep their amounts.",
      inputSchema: { program, rules: z.array(rule).max(50) },
      annotations: write,
    },
    ({ program, rules }) => call(() => gtm.PUT("/v1/workspaces/{ws}/partner-programs/{id}/rules", { params: { path: { ws: workspace(), id: program } }, body: { rules } })),
  );

  server.registerTool(
    "create_partner_group",
    {
      title: "Add a partner group",
      description: "A group (tier) with its own rates: give it rules with set_partner_rules (group_id) and move partners into it with update_partner.",
      inputSchema: { program, name: z.string().max(80) },
      annotations: write,
    },
    ({ program, name }) => call(() => gtm.POST("/v1/workspaces/{ws}/partner-programs/{id}/groups", { params: { path: { ws: workspace(), id: program } }, body: { name } })),
  );

  server.registerTool(
    "add_partner",
    {
      title: "Add a partner to a program",
      description:
        "A partner is a customer record: pass record_id, or an email (the person with it, made when nobody has it). Returns their ref code, " +
        "short link and campaign link to share.",
      inputSchema: {
        program,
        record_id: id.optional(),
        email: z.string().optional(),
        name: z.string().max(200).optional(),
        code: z.string().max(64).optional().describe("Their ref code; made from their name when left out. It never changes."),
        status: z.enum(["pending", "active"]).optional(),
        group_id: id.optional(),
        payout_method: z.string().max(500).optional().describe("Free text, never bank details."),
        note: z.string().max(2000).optional(),
      },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/partners", { params: { path: path() }, body })),
  );

  server.registerTool(
    "update_partner",
    {
      title: "Approve, suspend or change a partner",
      description: "status active approves a pending partner; suspended or rejected stops new credit (their links still land). Or change their group, payout method or note.",
      inputSchema: {
        partner_id: partner,
        status: z.enum(["pending", "active", "suspended", "rejected"]).optional(),
        group_id: z.string().optional().describe('A group of the program; "" for none.'),
        payout_method: z.string().max(500).optional(),
        postback_url: z.string().max(2000).optional().describe(
          "The partner's own tracker (https), called once per conversion credited to them and per refund, with placeholders such as {sub_id}, {commission}, {status}, {type}, {amount} and {currency}; \"\" turns it off.",
        ),
        note: z.string().max(2000).optional(),
      },
      annotations: write,
    },
    ({ partner_id, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/partners/{id}", { params: { path: { ws: workspace(), id: partner_id } }, body })),
  );

  server.registerTool(
    "add_partner_promo_code",
    {
      title: "Tie a promo code to a partner",
      description: "The code as customers type it at checkout; conversions reporting it credit the partner. With coupon, Outflare makes it in the connected Stripe account; otherwise make it there yourself.",
      inputSchema: {
        partner_id: partner,
        code: z.string().max(64),
        coupon: z.string().max(200).optional().describe("A coupon id in the connected Stripe account: Outflare makes the code there too."),
      },
      annotations: write,
    },
    ({ partner_id, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/partners/{id}/promo-codes", { params: { path: { ws: workspace(), id: partner_id } }, body })),
  );

  server.registerTool(
    "log_partner_fee",
    {
      title: "Log a fixed fee for a partner",
      description: "Such as an influencer's fee for a post. Approved at once, counted toward their next payout and logged as a cost on the affiliates channel.",
      inputSchema: { partner_id: partner, amount_cents: z.number().int().min(1), note: z.string().max(2000).optional() },
      annotations: write,
    },
    ({ partner_id, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/partners/{id}/fees", { params: { path: { ws: workspace(), id: partner_id } }, body })),
  );

  server.registerTool(
    "adjust_partner_balance",
    {
      title: "Correct what a partner is owed",
      description: "Positive adds, negative takes away; it nets in their next payout. Say why in note.",
      inputSchema: { partner_id: partner, amount_cents: z.number().int(), note: z.string().min(1).max(2000) },
      annotations: write,
    },
    ({ partner_id, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/partners/{id}/adjustments", { params: { path: { ws: workspace(), id: partner_id } }, body })),
  );

  server.registerTool(
    "report_partner_conversion",
    {
      title: "Report a sign-up, sale, refund or custom conversion",
      description:
        "What the workspace's backend normally sends. Each becomes a customer event attribution reads (signed_up, payment.succeeded, " +
        "payment.refunded or the custom event_name). The partner is found from promo_code, ref or click_id, then a referral already bound " +
        "to the customer, then the customer's own visits with a ref inside the program's window. Not credited is not an error: credited " +
        "false comes with a reason.",
      inputSchema: {
        type: z.enum(["signup", "sale", "refund", "custom"]),
        event_name: z.string().max(80).optional().describe("custom only, such as trial_started."),
        recurring: z.boolean().optional().describe("Sales: a subscription renewal."),
        program: z.string().optional(),
        ref: z.string().optional(),
        click_id: id.optional(),
        promo_code: z.string().optional(),
        customer_id: z.string().max(200).optional().describe("The workspace's own id for the customer."),
        email: z.string().optional(),
        record_id: id.optional(),
        external_id: z.string().max(200).optional().describe("The charge or order id; makes a retry safe."),
        refund_of: z.string().max(200).optional().describe("Refunds: the sale's external_id."),
        amount_cents: z.number().int().min(0).optional(),
        currency: z.string().length(3).optional(),
        product_id: z.string().max(200).optional(),
      },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/partner-conversions", { params: { path: path() }, body })),
  );

  server.registerTool(
    "approve_partner_reward",
    {
      title: "Approve a held partner reward",
      description: "Approve now, even before its hold ends. Cash counts toward the next payout and is logged as a cost; an action is sent to the workspace's system.",
      inputSchema: { reward_id: id },
      annotations: write,
    },
    ({ reward_id }) => call(() => gtm.POST("/v1/workspaces/{ws}/partner-rewards/{id}/approve", { params: { path: { ws: workspace(), id: reward_id } }, body: {} })),
  );

  server.registerTool(
    "reject_partner_reward",
    {
      title: "Reject a held partner reward",
      description: "Only a pending one. Say why in note; the partner sees it.",
      inputSchema: { reward_id: id, note: z.string().max(2000).optional() },
      annotations: write,
    },
    ({ reward_id, note }) => call(() => gtm.POST("/v1/workspaces/{ws}/partner-rewards/{id}/reject", { params: { path: { ws: workspace(), id: reward_id } }, body: { note } })),
  );

  server.registerTool(
    "fulfil_partner_reward",
    {
      title: "Mark an action reward done",
      description: "After the workspace's system carried out an approved action reward (granted the credit).",
      inputSchema: { reward_id: id, note: z.string().max(2000).optional() },
      annotations: write,
    },
    ({ reward_id, note }) => call(() => gtm.POST("/v1/workspaces/{ws}/partner-rewards/{id}/fulfil", { params: { path: { ws: workspace(), id: reward_id } }, body: { note } })),
  );

  server.registerTool(
    "create_partner_payouts",
    {
      title: "Make the partner payouts that are due",
      description:
        "One payout per partner whose approved balance reaches the program's minimum (one partner, one program, or all). Outflare sends no " +
        "money: the workspace pays from its own account, then mark_partner_payout_paid.",
      inputSchema: { partner_id: id.optional(), program: z.string().optional() },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/partner-payouts", { params: { path: path() }, body })),
  );

  server.registerTool(
    "mark_partner_payout_paid",
    {
      title: "Mark a partner payout paid",
      description: "Only once the workspace has actually paid it. reference is the payment's id (a transfer id or invoice number).",
      inputSchema: { payout_id: id, note: z.string().max(2000).optional(), reference: z.string().max(200).optional() },
      annotations: write,
    },
    ({ payout_id, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/partner-payouts/{id}/paid", { params: { path: { ws: workspace(), id: payout_id } }, body })),
  );

  server.registerTool(
    "set_partner_portal",
    {
      title: "Open or set up a program's partner portal",
      description:
        "The portal is where partners apply, sign in with a link emailed through the workspace's own email provider (email_from, on a " +
        "verified sending domain) and see their links, masked referred customers, rewards and payouts. settings replace the stored ones; " +
        "left-out fields take the kind's defaults. portal_url in the answer is its address.",
      inputSchema: {
        program,
        enabled: z.boolean().optional(),
        subdomain: z.string().max(63).optional().describe("Its subdomain of the server's partners domain; \"\" for none."),
        settings: z.object({
          title: z.string().max(120).optional(),
          logo_url: z.string().optional(),
          color: z.string().optional().describe("Hex, such as #0f766e."),
          welcome: z.string().max(4000).optional(),
          pages: z.array(z.enum(["dashboard", "links", "assets", "referrals", "payouts", "settings"])).optional(),
          signup_open: z.boolean().optional(),
          signup_fields: z.array(z.object({ key: z.string(), label: z.string(), type: z.enum(["text", "textarea", "url", "select"]), required: z.boolean().optional(), options: z.array(z.string()).optional() })).max(20).optional(),
          email_from: z.string().optional(),
          sign_in_template: z.string().optional(),
        }).optional(),
      },
      annotations: write,
    },
    ({ program, ...body }) => call(() => gtm.PUT("/v1/workspaces/{ws}/partner-programs/{id}/portal", { params: { path: { ws: workspace(), id: program } }, body })),
  );

  server.registerTool(
    "add_partner_asset",
    {
      title: "Add an asset for partners",
      description: "Shown on the portal's Assets page. image: a banner's https address. text: copy or an email swipe. link: a page to promote.",
      inputSchema: { program, kind: z.enum(["image", "text", "link"]), title: z.string().max(200), body: z.string().max(10000).optional(), url: z.string().max(2000).optional() },
      annotations: write,
    },
    ({ program, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/partner-programs/{id}/assets", { params: { path: { ws: workspace(), id: program } }, body })),
  );

  server.registerTool(
    "cancel_partner_payout",
    {
      title: "Cancel a due partner payout",
      description: "Its rewards go back into the partner's balance.",
      inputSchema: { payout_id: id, note: z.string().max(2000).optional() },
      annotations: write,
    },
    ({ payout_id, note }) => call(() => gtm.POST("/v1/workspaces/{ws}/partner-payouts/{id}/cancel", { params: { path: { ws: workspace(), id: payout_id } }, body: { note } })),
  );
}
