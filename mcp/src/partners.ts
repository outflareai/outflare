// Tools for partner programs: customer referrals, affiliates and
// influencers on one platform. Programs hold the rules, partners are
// customer records with a ref code and links, the workspace's backend
// reports conversions, and rewards and payouts are a ledger. Outflare never
// sends money: payouts are marked paid after the workspace pays them.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";
import { brandCaller, brandRef } from "./records.js";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const id = z.string().uuid();
const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: false } as const;

const rule = z.object({
  group_id: z.string().uuid().nullable().optional().describe("Only partners in this group."),
  partner_id: z.string().uuid().nullable().optional().describe("Only this partner (from list_partners): their own rate, which beats their group's and the program's. Not with group_id."),
  trigger: z.string().max(80).describe("signup, sale, or a custom conversion's event_name such as trial_started."),
  side: z.enum(["partner", "referred"]).optional().describe("referred: the customer they brought (two-sided rewards), always an action."),
  kind: z.enum(["percent", "fixed", "action", "group"]).describe("group: move the partner to target_group_id."),
  percent_bp: z.number().int().min(1).max(10000).optional().describe("percent: basis points of the sale (2500 is 25%)."),
  amount_cents: z.number().int().min(1).optional().describe("fixed: per lead or sale."),
  action: z.string().max(60).optional().describe("action: what the workspace's system does, such as grant_credit."),
  action_data: z.record(z.string(), z.unknown()).optional().describe('action: its details, such as {"gb": 10}.'),
  recurrence: z.enum(["first", "every", "months"]).optional(),
  months: z.number().int().min(1).max(120).optional(),
  min_sales: z.number().int().min(0).optional().describe("A tier: applies once the partner has made this many sales."),
  product_ids: z.array(z.string()).max(50).optional(),
  name: z.string().max(120).optional().describe("What the flow is for, such as Launch bonus."),
  conditions: z
    .object({
      match: z.enum(["all", "any"]).optional(),
      groups: z
        .array(
          z.object({
            match: z.enum(["all", "any"]).optional(),
            conditions: z
              .array(z.object({ field: z.string(), op: z.enum(["eq", "neq", "gt", "gte", "lt", "lte", "in", "not_in", "within", "not_within"]), value: z.unknown() }))
              .max(20),
          }),
        )
        .max(10),
    })
    .optional()
    .describe(
      "AND/OR groups of conditions. Fields: partner.id, partner.country, partner.transactions, partner.customers, partner.leads, " +
        "partner.revenue_cents, partner.earnings_cents, customer.transactions, customer.total_paid_cents, customer.first_sale_at, " +
        'customer.signed_up_at, transaction.amount_cents, transaction.product_id, transaction.recurring. Dates take within {"amount": 3, "unit": "months"}.',
    ),
  approval: z.enum(["program", "auto", "after_days", "manual"]).optional().describe("program (the default) follows the program's hold and auto-approve."),
  approval_days: z.number().int().min(0).max(365).optional().describe("With approval after_days."),
  stacking: z.enum(["exclusive", "adds"]).optional().describe("Cash flows: exclusive ones compete and one pays; adds pays on top."),
  overrides: z
    .array(z.object({ partner_id: z.string().uuid().optional(), product_id: z.string().optional(), percent_bp: z.number().int().optional(), amount_cents: z.number().int().optional() }))
    .max(50)
    .optional()
    .describe("Another rate inside the flow for one partner or product."),
  target_group_id: z.string().uuid().optional().describe("kind group: the group the partner moves to."),
});

const promoRules = z.object({
  disabled: z.boolean().optional(),
  starts_at: z.string().nullable().optional(),
  ends_at: z.string().nullable().optional(),
  percent_bp: z.number().int().min(0).max(10000).optional(),
  amount_cents: z.number().int().min(0).optional(),
  currency: z.string().length(3).optional(),
  max_redemptions: z.number().int().min(0).optional(),
  first_order_only: z.boolean().optional(),
  min_order_cents: z.number().int().min(0).optional(),
  duration: z.enum(["once", "repeating", "forever"]).optional(),
  months: z.number().int().optional(),
  product_ids: z.array(z.string()).max(50).optional(),
});

const proposalConfig = z.object({
 partner_site_name:z.string().max(160).optional(),partner_site_url:z.string().optional(),partner_site_icon_url:z.string().optional(),
 title:z.string().min(1).max(160),brief:z.string().min(1).max(10000),reply_to:z.string(),request_quote:z.boolean().optional(),brand_kit_url:z.string().optional(),product_url:z.string().optional(),terms:z.string().max(10000).optional(),commission_terms:z.string().max(2000).optional(),
 deliverables:z.array(z.object({id:z.string(),title:z.string(),channel:z.string().optional(),format:z.string().optional(),placement_url:z.string().optional(),requirements:z.string().optional(),deadline:z.string().optional(),live_days:z.number().int().min(0).max(3650).optional(),link_url:z.string().optional(),link_text:z.string().optional(),link_attribute:z.string().optional()})).min(1).max(30),
 milestones:z.array(z.object({id:z.string(),title:z.string(),deliverable_id:z.string(),amount_cents:z.number().int().min(0).max(100000000).optional(),benefit:z.string().optional(),wait_days:z.number().int().min(0).max(3650).optional()})).max(30),
});

const programSettings = {
  name: z.string().max(120).optional(),
  landing_url: z.string().max(2000).optional().describe("Where partner links land."),
  currency: z.string().length(3).optional(),
  credit_window_days: z.number().int().min(1).max(365).optional(),
  credit_model: z.enum(["first", "last"]).optional(),
  codes_win: z.boolean().optional().describe("A promo code beats a link when a conversion carries both."),
  block_self_referrals: z.boolean().optional().describe("A partner's own sign-ups and sales earn them nothing. On by default."),
  hold_days: z.number().int().min(0).max(365).optional(),
  auto_approve: z.boolean().optional(),
  auto_accept: z.boolean().optional(),
  pay_browser_signups: z.boolean().optional(),
  min_payout_cents: z.number().int().min(0).optional(),
  payout_term_days: z.number().int().min(0).max(90).nullable().optional().describe("Net N: a reward joins a payout N days after the month it was earned in ends. null pays it once approved."),
  payout_methods: z.array(z.enum(["paypal", "wise", "bank_transfer", "crypto", "other"])).optional().describe("What partners choose from on the portal; empty allows any."),
  payout_tokens: z.array(z.string().max(40)).max(20).optional().describe("Crypto tokens partners choose from, such as USDC on Base."),
  lead_expiry_days: z.number().int().min(1).max(3650).nullable().optional().describe("Days a referred lead has to buy before it stops crediting the partner; null never expires."),
  url_params: z.array(z.string().max(32)).max(5).optional().describe("Query parameters besides ref and via that carry a partner's code on the website, such as aff for links made elsewhere."),
  block_ad_clicks: z.boolean().optional().describe("Website visits from paid ad clicks (gclid and the like) don't credit partners."),
  block_social_clicks: z.boolean().optional().describe("Website visits from social media (fbclid, or a social network as referrer) don't credit partners."),
  blocked_countries: z.array(z.string().length(2)).max(250).optional().describe("Two-letter country codes whose website visits and new customers don't credit partners, such as BR. [] for none."),
  terms_url: z.string().max(2000).optional(),
  sales_source: z.string().optional().describe('A billing connector (stripe) whose synced payments and refunds are also partner sales; "" for none.'),
};

const page = {
  limit: z.number().int().min(1).max(500).optional(),
  cursor: z.string().optional().describe("next_cursor from the previous page."),
};

// workspace is read on every call.
export function registerPartnerTools(server: McpServer, gtm: MissionClient, workspace: () => string, readOnly: boolean, call: Call): void {
  const program = z.string().describe("The program's id or slug.");
  const partner = id.describe("The partner's id (their membership in one program), from list_partners.");
  const path = () => ({ ws: workspace() });
  const onBrand = brandCaller(gtm, workspace);

  server.registerTool("list_partner_proposals",{title:"List one-to-one partnership proposals",inputSchema: { brand: brandRef,program},annotations:read},({ brand, program:id})=>call(()=>onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-programs/{id}/proposals",{params:{path: { brand: b, id}}}))));
  if(!readOnly){
    server.registerTool("create_partner_proposal",{title:"Draft a private partnership proposal",description:"One named recipient, cross-platform deliverables and manually approved milestones. No outreach is sent. Use invite after reviewing the draft.",inputSchema: { brand: brandRef,program,partner_id:id.optional(),email:z.string().optional(),name:z.string().optional(),config:proposalConfig},annotations:write},({ brand, program:id,...body})=>call(()=>onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-programs/{id}/proposals",{params:{path: { brand: b, id}},body}))));
    server.registerTool("update_partner_proposal",{title:"Manage a partnership proposal",description:"Revise before acceptance, generate an invitation link, explicitly verify submitted work, or approve an eligible milestone. Time never automatically releases payment.",inputSchema: { brand: brandRef,program,proposal:id,version:z.number().int(),action:z.enum(["revise","invite","verify","approve","complete"]),config:proposalConfig.optional(),deliverable_id:z.string().optional(),milestone_id:z.string().optional(),note:z.string().max(4000).optional()},annotations:write},({ brand, program:id,proposal,...body})=>call(()=>onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-programs/{id}/proposals/{proposal}",{params:{path: { brand: b, id,proposal}},body}))));
  }
  server.registerTool("list_partner_tasks", {
    title: "List partner tasks", description: "Paid and voluntary opportunities, private website proposals and their assignments.",
    inputSchema: { brand: brandRef, program }, annotations: read,
  }, ({ brand, program: id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-programs/{id}/tasks", { params: { path: { brand: b, id } } }))));
  server.registerTool("list_partner_audiences", {
    title: "List partner audience profiles", description: "Partner websites, channels and communities, with reach sources and website ownership status. Scraped reach is not proof of ownership.",
    inputSchema: { brand: brandRef, program }, annotations: read,
  }, ({ brand, program: id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-programs/{id}/audiences", { params: { path: { brand: b, id } } }))));
  if (!readOnly) {
    server.registerTool("create_partner_task", {
      title: "Create a partner task", description: "Create a paid or voluntary opportunity. target_host makes it a private proposal unlocked by verified website ownership. Ranged/custom prices require an accepted offer. Automatic verification checks a required link on a verified website using the workspace's own page reader.",
      inputSchema: { brand: brandRef,
        program, title: z.string().min(1).max(160), description: z.string().min(1).max(6000),
        category: z.enum(["blog","video","social","newsletter","banner","community","other"]).optional(),
        reward_kind: z.enum(["free","cash","action"]).optional(), pricing: z.enum(["fixed","range","custom"]).optional(),
        amount_cents: z.number().int().min(0).max(100000000).optional(), max_amount_cents: z.number().int().min(0).max(100000000).optional(),
        reward_label: z.string().max(200).optional(), action: z.string().max(60).optional(), action_data: z.record(z.string(),z.unknown()).optional(),
        claim_mode: z.enum(["instant","application"]).optional(), verification: z.enum(["human","auto","auto_hold"]).optional(),
        hold_days: z.number().int().min(0).max(365).optional(), asset_kind: z.enum(["","website","youtube","x","linkedin","instagram","tiktok","newsletter","reddit","facebook","discord","podcast","other"]).optional(),
        min_reach: z.number().int().min(0).optional(), target_host: z.string().max(253).optional(), required_link: z.string().max(2000).optional(), slots: z.number().int().min(1).max(10000).optional(),
      }, annotations: write,
    }, ({ brand, program: id, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-programs/{id}/tasks", { params: { path: { brand: b, id } }, body }))));
    server.registerTool("update_partner_task", {
      title: "Pause or resume a partner task", inputSchema: { brand: brandRef, program, task: id, action: z.enum(["pause","resume"]) }, annotations: write,
    }, ({ brand, program: id, task, action }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-programs/{id}/tasks/{task}", { params: { path: { brand: b, id, task } }, body: { action } }))));
    server.registerTool("review_partner_task", {
      title: "Review partner work or offer a price", description: "Offer a fixed price for an application, approve submitted work into the reward ledger, or reject it. Partners accept offered prices before doing the work.",
      inputSchema: { brand: brandRef, program, submission: id, action: z.enum(["offer","approve","reject"]), amount_cents: z.number().int().min(0).max(100000000).optional(), note: z.string().max(2000).optional() }, annotations: write,
    }, ({ brand, program: id, submission, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-programs/{id}/task-submissions/{submission}", { params: { path: { brand: b, id, submission } }, body }))));
  }

  server.registerTool(
    "list_partner_programs",
    {
      title: "List partner programs",
      description: "Referral, affiliate and influencer programs with their credit rule, hold, minimum payout and reward rules.",
      inputSchema: { brand: brandRef, status: z.enum(["active", "paused", "archived"]).optional() },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-programs", { params: { path: { brand: b }, query } }))),
  );

  server.registerTool(
    "get_partner_settings",
    {
      title: "Get partner settings",
      description: "The partner rules for every program in the workspace: whether erasing a partner's record also clears their payouts' notes and references.",
      inputSchema: {},
      annotations: read,
    },
    () => call(() => gtm.GET("/v1/workspaces/{ws}/partner-settings", { params: { path: path() } })),
  );

  server.registerTool(
    "get_partner_program_stats",
    {
      title: "Get a partner program's totals",
      description: "Partners by status, and clicks, referred customers, sales, rewards on hold, due and paid across all of the program's partners.",
      inputSchema: { brand: brandRef, program },
      annotations: read,
    },
    ({ brand, program }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-programs/{id}/stats", { params: { path: { brand: b, id: program } } }))),
  );

  server.registerTool(
    "get_partner_program",
    {
      title: "Get a partner program",
      description: "One program with its reward rules and groups.",
      inputSchema: { brand: brandRef, program },
      annotations: read,
    },
    ({ brand, program }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-programs/{id}", { params: { path: { brand: b, id: program } } }))),
  );

  server.registerTool(
    "list_partners",
    {
      title: "Find partners",
      description:
        "Partners, each with their program, ref code, short link, campaign link, promo codes and totals: clicks, referrals, " +
        "customers, sales, and cash pending, approved, paid and in their balance. q and sort cover every partner, not only one page.",
      inputSchema: { brand: brandRef,
        q: z.string().max(200).optional().describe("Part of a partner's code, promo code, name or email, or a partner's or their record's whole id."),
        sort: z.enum(["newest", "oldest", "name", "code", "clicks", "customers", "sales", "balance", "paid"]).optional()
          .describe("newest (default) and oldest by when they joined; name and code A to Z; the totals largest first."),
        program: z.string().optional().describe("A program's id or slug."),
        status: z.enum(["pending", "active", "suspended", "rejected"]).optional(),
        record_id: id.optional().describe("The partner's customer record."),
        payout_details: z.enum(["missing", "set"]).optional().describe("missing: only partners who haven't given payout details; set: only those who have."),
        ...page,
      },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partners", { params: { path: { brand: b }, query } }))),
  );

  server.registerTool(
    "get_partner",
    { title: "Get a partner", description: "One partner with their links, promo codes and totals.", inputSchema: { brand: brandRef, partner_id: partner }, annotations: read },
    ({ brand, partner_id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partners/{id}", { params: { path: { brand: b, id: partner_id } } }))),
  );

  server.registerTool(
    "list_partner_activity",
    {
      title: "List a partner's history",
      description:
        "What happened to one partner and their money, newest first, from the audit log: joining, status, group and payout detail changes " +
        "(field names only, never the details), promo codes, referrals, rewards made, approved, rejected or reversed, and payouts made, paid " +
        "or canceled, each with who did it. Use it to answer why a partner was paid what they were.",
      inputSchema: { brand: brandRef, partner_id: partner, ...page },
      annotations: read,
    },
    ({ brand, partner_id, ...query }) =>
      call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partners/{id}/activity", { params: { path: { brand: b, id: partner_id }, query } }))),
  );

  server.registerTool(
    "list_partner_conversions",
    {
      title: "List partner conversions",
      description: "Sign-ups, sales, refunds and custom conversions credited to partners, newest first, each with the partner's code and name and the customer's name and email.",
      inputSchema: { brand: brandRef,
        q: z.string().max(200).optional().describe("Part of the partner's code or email, the customer's email, or the sale's external_id."),
        type: z.enum(["signup", "sale", "refund", "custom"]).optional(),
        partner_id: id.optional(),
        program: z.string().optional(),
        ...page,
      },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-conversions", { params: { path: { brand: b }, query } }))),
  );

  server.registerTool(
    "list_partner_conversion_log",
    {
      title: "List inbound conversion calls",
      description:
        "Every conversion the workspace's backend or tracker reported in the last 30 days (API or postback URL), newest first, " +
        "with what it sent, the answer and the outcome (credited, not_credited with its reason, duplicate, rejected, error). " +
        "Use it to find out why a sale wasn't credited. q finds a call by external_id, customer_id, ref, promo code, email or request id.",
      inputSchema: { brand: brandRef,
        program: z.string().optional(),
        source: z.enum(["api", "postback"]).optional(),
        outcome: z.enum(["credited", "not_credited", "duplicate", "rejected", "error"]).optional(),
        q: z.string().max(200).optional(),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional().describe("next_cursor from the previous page."),
      },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-conversion-log", { params: { path: { brand: b }, query } }))),
  );

  server.registerTool(
    "list_partner_referrals",
    {
      title: "List referred customers",
      description: "Each customer a partner was credited with (once per program), how (ref, click, promo_code, visit), and their net sales.",
      inputSchema: { brand: brandRef,
        partner_id: id.optional(),
        record_id: id.optional().describe("The referred customer."),
        status: z.enum(["lead", "customer", "refunded", "rejected"]).optional(),
        ...page,
      },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-referrals", { params: { path: { brand: b }, query } }))),
  );

  server.registerTool(
    "list_partner_rewards",
    {
      title: "List the partner reward ledger",
      description:
        "Cash, fees, actions and adjustments, newest first. pending is on hold or waiting for a person; approved cash counts toward the next " +
        "payout; an approved action waits for the workspace's system to carry it out. Each has its partner, customer, sale, rule, source " +
        "(native or imported, and held_at_source while the old platform still pays it), who decided it, the entries that reverse it " +
        "(reversed_by) and net_cents after them.",
      inputSchema: { brand: brandRef,
        q: z.string().max(200).optional().describe("Part of the partner's code or email, the customer's email, or the sale's external_id."),
        source: z.enum(["native", "imported", "held_at_source"]).optional(),
        status: z.enum(["pending", "approved", "rejected", "paid", "fulfilled"]).optional(),
        kind: z.enum(["cash", "fee", "action", "adjustment"]).optional(),
        partner_id: id.optional(),
        payout_id: id.optional(),
        program: z.string().optional(),
        ...page,
      },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-rewards", { params: { path: { brand: b }, query } }))),
  );

  server.registerTool(
    "get_partner_reward_proof",
    {
      title: "Show why a partner earned a reward",
      description:
        "The chain behind one reward: the click or website visit that credited the partner, the referral, the customer, the sale (with " +
        "the brand's own id for it), the rule that priced it, who approved or rejected it, and the payout that paid it. A link with no " +
        "record is null and gaps says why, such as credited by a promo code. Use it to answer a partner's question or check a reward before paying.",
      inputSchema: { brand: brandRef, reward_id: id.describe("From list_partner_rewards.") },
      annotations: read,
    },
    ({ brand, reward_id }) =>
      call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-rewards/{id}/proof", { params: { path: { brand: b, id: reward_id } } }))),
  );

  server.registerTool(
    "export_partner_proofs",
    {
      title: "Export the proof behind a payout or a partner's rewards",
      description:
        "The proof of every reward in one payout (payout_id), or one partner's rewards made from `from` up to `to` (partner_id), as " +
        "get_partner_reward_proof gives it, at most 10,000. It holds customers' names and emails but no payout details, and is recorded in the audit log.",
      inputSchema: { brand: brandRef,
        payout_id: id.optional(),
        partner_id: id.optional(),
        from: z.string().datetime({ offset: true }).optional().describe("With partner_id: rewards made at or after this time."),
        to: z.string().datetime({ offset: true }).optional().describe("With partner_id: rewards made before this time."),
      },
      annotations: read,
    },
    ({ brand, ...query }) =>
      call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-proofs/export", { params: { path: { brand: b }, query: { ...query, format: "json" } } }))),
  );

  server.registerTool(
    "list_partner_payouts",
    {
      title: "List partner payouts",
      description: "Payouts newest first: due ones are waiting to be paid from the workspace's own account.",
      inputSchema: { brand: brandRef, status: z.enum(["due", "paid", "canceled"]).optional(), partner_id: id.optional(), program: z.string().optional(), ...page },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-payouts", { params: { path: { brand: b }, query } }))),
  );

  server.registerTool(
    "list_partner_postbacks",
    {
      title: "List a partner's postbacks to their tracker",
      description: "The last 50 calls to the partner's postback URL: queued, sent, or failed with the tracker's answer.",
      inputSchema: { brand: brandRef, partner_id: partner },
      annotations: read,
    },
    ({ brand, partner_id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partners/{id}/postbacks", { params: { path: { brand: b, id: partner_id } } }))),
  );

  server.registerTool(
    "export_partner_payouts",
    {
      title: "Export partner payouts as CSV",
      description:
        "Due payouts (or paid, canceled, all) as CSV, with each partner's payout details, for paying them from the workspace's own account " +
        "or a payout tool. Needs partners.manage; the export is recorded in the audit log. Never paste the payout details anywhere public.",
      inputSchema: { brand: brandRef, status: z.enum(["due", "paid", "canceled", "all"]).optional(), program: z.string().max(100).optional() },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-payouts/export", { params: { path: { brand: b }, query }, parseAs: "text" }))),
  );

  server.registerTool(
    "list_partner_assets",
    { title: "List a program's assets for partners", description: "Banners, copy and links shown on the portal.", inputSchema: { brand: brandRef, program }, annotations: read },
    ({ brand, program }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-programs/{id}/assets", { params: { path: { brand: b, id: program } } }))),
  );

  server.registerTool(
    "list_partner_imports",
    { title: "List a program's imports from Tolt", description: "Newest first: status, counts per kind (created, updated, unchanged, skipped) and issues by Tolt id and reason.", inputSchema: { brand: brandRef, program }, annotations: read },
    ({ brand, program }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-programs/{id}/imports", { params: { path: { brand: b, id: program } } }))),
  );

  server.registerTool(
    "get_partner_import",
    { title: "Get an import from Tolt", description: "Its status, counts per kind and issues.", inputSchema: { brand: brandRef, program, import_id: id }, annotations: read },
    ({ brand, program, import_id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-programs/{id}/imports/{import}", { params: { path: { brand: b, id: program, import: import_id } } }))),
  );

  if (readOnly) return;

  server.registerTool(
    "list_partner_import_sources",
    {
      title: "List the Tolt programs an import can read",
      description:
        "Reads the programs in the workspace's connected Tolt account (id, name, currency, active or inactive), to pass one as " +
        "start_partner_import's source_program. suggested is the one an import takes when none is named. Needs partners.manage; changes nothing.",
      inputSchema: { brand: brandRef, program, integration_id: id.optional() },
      annotations: read,
    },
    ({ brand, program, integration_id }) =>
      call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-programs/{id}/import-sources", { params: { path: { brand: b, id: program }, query: { integration_id } } }))),
  );

  server.registerTool(
    "switch_over_partner_program",
    {
      title: "Switch a program over from Tolt",
      description:
        "Only once the platform the program was imported from (Tolt) no longer pays it. Until then its imported commissions keep Tolt's " +
        "status and are never approved or paid here, so nobody is paid twice; afterwards they are approved and paid here like any other.",
      inputSchema: { brand: brandRef, program },
      annotations: write,
    },
    ({ brand, program }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-programs/{id}/switch-over", { params: { path: { brand: b, id: program } } }))),
  );

  server.registerTool(
    "start_partner_import",
    {
      title: "Import a partner program from Tolt",
      description:
        "Brings the workspace's Tolt program into this one, with its own Tolt connection: partners with their link codes, payout details, " +
        "promo codes, referred customers, transactions and commissions, paid ones as paid payouts. Safe to run again until switch-over day: " +
        "each run adds only what is new and brings over status changes. Sends no reward webhooks. Poll get_partner_import for the result. " +
        "source_program is the Tolt program id (list_partner_import_sources), needed only when the account has more than one active program.",
      inputSchema: { brand: brandRef, program, source_program: z.string().max(200).optional(), integration_id: id.optional() },
      annotations: write,
    },
    ({ brand, program, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-programs/{id}/imports", { params: { path: { brand: b, id: program } }, body }))),
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
      inputSchema: { brand: brandRef, ...programSettings, name: z.string().max(120), kind: z.enum(["referral", "affiliate", "influencer"]), slug: z.string().max(60).optional(), rules: z.array(rule).max(50).optional() },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-programs", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "update_partner_program",
    {
      title: "Change a partner program",
      description: "Change settings, or pause, archive or restore it. Send only what changes. A paused program credits nothing new.",
      inputSchema: { brand: brandRef, program, ...programSettings, status: z.enum(["active", "paused", "archived"]).optional() },
      annotations: write,
    },
    ({ brand, program, ...body }) => call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/partner-programs/{id}", { params: { path: { brand: b, id: program } }, body }))),
  );

  server.registerTool(
    "update_partner_settings",
    {
      title: "Change partner settings",
      description: "For every program: whether erasing a partner's record also clears the note and reference on their payouts, which can hold their name or email. Payouts and links always stay.",
      inputSchema: { erase_payout_notes: z.boolean() },
      annotations: write,
    },
    (body) => call(() => gtm.PATCH("/v1/workspaces/{ws}/partner-settings", { params: { path: path() }, body })),
  );

  server.registerTool(
    "set_partner_rules",
    {
      title: "Replace a program's reward rules",
      description:
        "Each action rule a conversion matches gives a reward, but only one cash rule pays, whatever the order: a partner's own rate, then " +
        "their group's, then the highest tier reached, then the narrower recurrence (first sale, then months, then every sale), then " +
        "product-specific, then the higher payout. Send every rule: this replaces them all, so read the program first. [] makes the program " +
        "track only. Rewards already earned keep their amounts.",
      inputSchema: { brand: brandRef, program, rules: z.array(rule).max(50) },
      annotations: write,
    },
    ({ brand, program, rules }) => call(() => onBrand(brand, (b) => gtm.PUT("/v1/brands/{brand}/partner-programs/{id}/rules", { params: { path: { brand: b, id: program } }, body: { rules } }))),
  );

  server.registerTool(
    "create_partner_group",
    {
      title: "Add a partner group",
      description:
        "A group (tier) with its own rates: give it rules with set_partner_rules (group_id) and move partners into it with " +
        "assign_partner_group. default: true makes it the group new partners join.",
      inputSchema: { brand: brandRef, program, name: z.string().max(80), default: z.boolean().optional() },
      annotations: write,
    },
    ({ brand, program, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-programs/{id}/groups", { params: { path: { brand: b, id: program } }, body }))),
  );

  const group = z.string().uuid().describe("A group's id, from get_partner_program.");
  server.registerTool(
    "update_partner_group",
    {
      title: "Rename a partner group or make it the default",
      description: "default: true makes it the group new partners join; false stops that. Returns the program.",
      inputSchema: { brand: brandRef, program, group_id: group, name: z.string().min(1).max(80).optional(), default: z.boolean().optional() },
      annotations: write,
    },
    ({ brand, program, group_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/partner-programs/{id}/groups/{group}", { params: { path: { brand: b, id: program, group: group_id } }, body }))),
  );

  server.registerTool(
    "assign_partner_group",
    {
      title: "Move partners into a group",
      description: "A partner is in one group at a time, and its rules apply to them. group_id none takes them out of every group.",
      inputSchema: { brand: brandRef, program, group_id: z.string().describe('A group\'s id, or "none".'), partner_ids: z.array(id).min(1).max(1000) },
      annotations: write,
    },
    ({ brand, program, group_id, partner_ids }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-programs/{id}/groups/{group}/partners", { params: { path: { brand: b, id: program, group: group_id } }, body: { partner_ids } }))),
  );

  server.registerTool(
    "delete_partner_group",
    {
      title: "Remove a partner group",
      description: "Its own rules go with it; its partners move to no group.",
      inputSchema: { brand: brandRef, program, group_id: group },
      annotations: { ...write, destructiveHint: true },
    },
    ({ brand, program, group_id }) =>
      call(() => onBrand(brand, (b) => gtm.DELETE("/v1/brands/{brand}/partner-programs/{id}/groups/{group}", { params: { path: { brand: b, id: program, group: group_id } } }))),
  );

  server.registerTool(
    "add_partner",
    {
      title: "Add a partner to a program",
      description:
        "A partner is a customer record: pass record_id, or an email (the person with it, made when nobody has it). Returns their ref code, " +
        "short link and campaign link to share.",
      inputSchema: { brand: brandRef,
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
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partners", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "update_partner",
    {
      title: "Approve, suspend or change a partner",
      description: "status active approves a pending partner; suspended or rejected stops new credit (their links still land). Or change their group, payout method or note.",
      inputSchema: { brand: brandRef,
        partner_id: partner,
        status: z.enum(["pending", "active", "suspended", "rejected"]).optional(),
        group_id: z.string().optional().describe('A group of the program; "" for none.'),
        payout_method: z.string().max(500).optional(),
        payout_type: z.enum(["paypal", "wise", "bank_transfer", "crypto", "other"]).nullable().optional().describe("The payout method they chose; null clears it."),
        payout_token: z.string().max(40).nullable().optional().describe("The crypto token, with payout_type crypto."),
        postback_url: z.string().max(2000).optional().describe(
          "The partner's own tracker (https), called once per conversion credited to them and per refund, with placeholders such as {sub_id}, {commission}, {status}, {type}, {amount} and {currency}; \"\" turns it off.",
        ),
        note: z.string().max(2000).optional(),
      },
      annotations: write,
    },
    ({ brand, partner_id, ...body }) => call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/partners/{id}", { params: { path: { brand: b, id: partner_id } }, body }))),
  );

  server.registerTool(
    "add_partner_promo_code",
    {
      title: "Tie a promo code to a partner",
      description: "The code as customers type it at checkout; conversions reporting it credit the partner. With coupon, Outflare makes it in the connected Stripe account; otherwise make it there yourself.",
      inputSchema: { brand: brandRef,
        rules: promoRules.optional(),
        partner_id: partner,
        code: z.string().max(64),
        coupon: z.string().max(200).optional().describe("A coupon id in the connected Stripe account: Outflare makes the code there too."),
      },
      annotations: write,
    },
    ({ brand, partner_id, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partners/{id}/promo-codes", { params: { path: { brand: b, id: partner_id } }, body }))),
  );

  server.registerTool(
    "log_partner_fee",
    {
      title: "Log a fixed fee for a partner",
      description: "Such as an influencer's fee for a post. Approved at once, counted toward their next payout and logged as a cost on the affiliates channel.",
      inputSchema: { brand: brandRef, partner_id: partner, amount_cents: z.number().int().min(1), note: z.string().max(2000).optional() },
      annotations: write,
    },
    ({ brand, partner_id, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partners/{id}/fees", { params: { path: { brand: b, id: partner_id } }, body }))),
  );

  server.registerTool(
    "adjust_partner_balance",
    {
      title: "Correct what a partner is owed",
      description: "Positive adds, negative takes away; it nets in their next payout. Say why in note.",
      inputSchema: { brand: brandRef, partner_id: partner, amount_cents: z.number().int(), note: z.string().min(1).max(2000) },
      annotations: write,
    },
    ({ brand, partner_id, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partners/{id}/adjustments", { params: { path: { brand: b, id: partner_id } }, body }))),
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
      inputSchema: { brand: brandRef,
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
        country: z.string().length(2).optional().describe("The customer's country, such as DE; a program that blocks it doesn't take them as a new referral."),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-conversions", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "list_partner_leads",
    {
      title: "List leads partners submitted",
      description: "People partners say they referred, submitted on the portal: pending ones wait for approval, approved ones are referrals via manual, rejected ones say why.",
      inputSchema: { brand: brandRef, program: z.string().optional(), status: z.enum(["pending", "approved", "rejected"]).optional(), ...page },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/partner-leads", { params: { path: { brand: b }, query } }))),
  );

  server.registerTool(
    "approve_partner_lead",
    {
      title: "Approve a submitted lead",
      description: "Make the person the partner's referred customer (via manual), under the program's rules. If another partner has them already, the lead is rejected with that reason.",
      inputSchema: { brand: brandRef, lead_id: id },
      annotations: write,
    },
    ({ brand, lead_id }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-leads/{id}/approve", { params: { path: { brand: b, id: lead_id } }, body: {} }))),
  );

  server.registerTool(
    "reject_partner_lead",
    {
      title: "Reject a submitted lead",
      description: "Turn down a lead a partner submitted; the partner sees the reason.",
      inputSchema: { brand: brandRef, lead_id: id, reason: z.string().max(500).optional() },
      annotations: write,
    },
    ({ brand, lead_id, reason }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-leads/{id}/reject", { params: { path: { brand: b, id: lead_id } }, body: { reason } }))),
  );

  server.registerTool(
    "approve_partner_reward",
    {
      title: "Approve a held partner reward",
      description: "Approve now, even before its hold ends. Cash counts toward the next payout and is logged as a cost; an action is sent to the workspace's system.",
      inputSchema: { brand: brandRef, reward_id: id },
      annotations: write,
    },
    ({ brand, reward_id }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-rewards/{id}/approve", { params: { path: { brand: b, id: reward_id } }, body: {} }))),
  );

  server.registerTool(
    "reject_partner_reward",
    {
      title: "Reject a held partner reward",
      description: "Only a pending one. Say why in note; the partner sees it.",
      inputSchema: { brand: brandRef, reward_id: id, note: z.string().max(2000).optional() },
      annotations: write,
    },
    ({ brand, reward_id, note }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-rewards/{id}/reject", { params: { path: { brand: b, id: reward_id } }, body: { note } }))),
  );

  server.registerTool(
    "bulk_partner_rewards",
    {
      title: "Approve or reject many held partner rewards",
      description:
        "Approve or reject pending rewards: those in ids, or every pending one of a program or partner (optionally one kind). Each is " +
        "recorded as if done alone. Up to 1,000 a call; more: true means call again. skipped lists any that weren't pending or visible, with why. " +
        "Approving leaves out imported rewards the old platform still pays (skipped as held_at_source) unless include_held_at_source. " +
        "counts says what it touches by source; call with dry_run first to see that and what it would skip.",
      inputSchema: { brand: brandRef,
        action: z.enum(["approve", "reject"]),
        ids: z.array(z.string()).max(1000).optional().describe("Reward ids, from list_partner_rewards."),
        program: z.string().optional().describe("A program's id or slug: all its pending rewards."),
        partner_id: id.optional().describe("A partner: all their pending rewards."),
        kind: z.enum(["cash", "fee", "action", "adjustment"]).optional(),
        note: z.string().max(2000).optional().describe("Reject only: why, for the partner."),
        include_held_at_source: z.boolean().optional().describe("Also approve imported rewards the old platform still pays."),
        dry_run: z.boolean().optional().describe("Change nothing; answer what would change."),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-rewards/bulk", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "fulfil_partner_reward",
    {
      title: "Mark an action reward done",
      description: "After the workspace's system carried out an approved action reward (granted the credit).",
      inputSchema: { brand: brandRef, reward_id: id, note: z.string().max(2000).optional() },
      annotations: write,
    },
    ({ brand, reward_id, note }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-rewards/{id}/fulfil", { params: { path: { brand: b, id: reward_id } }, body: { note } }))),
  );

  server.registerTool(
    "create_partner_payouts",
    {
      title: "Make the partner payouts that are due",
      description:
        "One payout per partner whose approved balance reaches the program's minimum (one partner, one program, or all). Outflare sends no " +
        "money: the workspace pays from its own account, then mark_partner_payout_paid.",
      inputSchema: { brand: brandRef, partner_id: id.optional(), program: z.string().optional() },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-payouts", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "mark_partner_payout_paid",
    {
      title: "Mark a partner payout paid",
      description: "Only once the workspace has actually paid it. reference is the payment's id (a transfer id or invoice number).",
      inputSchema: { brand: brandRef, payout_id: id, note: z.string().max(2000).optional(), reference: z.string().max(200).optional() },
      annotations: write,
    },
    ({ brand, payout_id, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-payouts/{id}/paid", { params: { path: { brand: b, id: payout_id } }, body }))),
  );

  server.registerTool("create_partner_portal_session", {
    title: "Open a hosted partner portal session", description: "Backend-only one-use sign-in URL valid for 60 seconds. Provisions a brand-authenticated customer using their stable customer_id. Terms acceptance still applies. Never expose workspace tokens in a browser.",
    inputSchema: { brand: brandRef, program, customer_id: z.string().min(1), email: z.string().optional(), name: z.string().optional() }, annotations: write,
  }, ({ brand, program: id, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-programs/{id}/portal-sessions", { params: { path: { brand: b, id } }, body }))));
  server.registerTool("update_partner_promo_code", {
    title: "Enable or disable a partner coupon", inputSchema: { brand: brandRef, partner, code: z.string(), enabled: z.boolean() }, annotations: write,
  }, ({ brand, partner: id, code, ...body }) => call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/partners/{id}/promo-codes/{code}", { params: { path: { brand: b, id, code } }, body }))));

  server.registerTool(
    "set_partner_portal",
    {
      title: "Open or set up a program's partner portal",
      description:
        "The portal is where partners apply, sign in with a link emailed through the workspace's own email provider (email_from, on a " +
        "verified sending domain) and see their links, masked referred customers, rewards and payouts. settings replace the stored ones; " +
        "left-out fields take the kind's defaults. portal_url in the answer is its address.",
      inputSchema: { brand: brandRef,
        program,
        enabled: z.boolean().optional(),
        subdomain: z.string().max(63).optional().describe("Its subdomain of the server's partners domain; \"\" for none."),
        settings: z.object({
          home_url: z.string().optional(), support_url: z.string().optional(), support_email: z.string().optional(),
          terms_revision: z.string().max(100).optional(), payout_terms: z.string().max(6000).optional(),
          header_links: z.array(z.object({ label: z.string().max(80), url: z.string() })).max(12).optional(),
          footer_links: z.array(z.object({ label: z.string().max(80), url: z.string() })).max(12).optional(),
          landing_pages: z.array(z.object({ label: z.string().max(80), url: z.string() })).max(12).optional(),
          help_articles: z.array(z.object({ id: z.string(), title: z.string().max(160), category: z.enum(["getting_started", "promotion", "tracking", "payouts", "integrations", "rules"]), body: z.string().max(20000) })).max(40).optional(),
          coupon_templates: z.array(z.object({ id: z.string(), label: z.string().max(100), coupon: z.string().max(200), rules: promoRules })).max(12).optional(),
          title: z.string().max(120).optional(),
          logo_url: z.string().optional(),
          favicon_url: z.string().optional().describe("The browser tab's icon, an https image."),
          color: z.string().optional().describe("Hex, such as #0f766e."),
          welcome: z.string().max(4000).optional(),
          pages: z.array(z.enum(["dashboard", "tasks", "profiles", "links", "assets", "referrals", "payouts", "settings", "help", "proposals"])).optional(),
          signup_open: z.boolean().optional(),
          signup_fields: z.array(z.object({ key: z.string(), label: z.string(), type: z.enum(["text", "textarea", "url", "select"]), required: z.boolean().optional(), options: z.array(z.string()).optional() })).max(20).optional(),
          email_from: z.string().optional(),
          sign_in_template: z.string().optional(),
          hide_powered_by: z.boolean().optional().describe('Hide the "Powered by Outflare" line at the foot of the portal.'),
          business_email_only: z.boolean().optional().describe("Refuse applications from free, disposable and relay email addresses."),
          site_key_id: z.string().optional().describe("A site key id: the portal loads the workspace's website script with it, so partners' visits show in its analytics."),
          lead_submissions: z.enum(["", "review", "auto"]).optional().describe("Partners submit people they referred on the portal: review (you approve each), auto (taken at once unless another partner has them) or off."),
          ga_measurement_id: z.string().optional().describe("A Google Analytics 4 id (G-...), loaded through the website script once the visitor allows analytics. Needs site_key_id."),
        }).optional(),
      },
      annotations: write,
    },
    ({ brand, program, ...body }) => call(() => onBrand(brand, (b) => gtm.PUT("/v1/brands/{brand}/partner-programs/{id}/portal", { params: { path: { brand: b, id: program } }, body }))),
  );

  server.registerTool(
    "add_partner_asset",
    {
      title: "Add an asset for partners",
      description: "Shown on the portal's Assets page. image: a banner's https address. text: copy or an email swipe. link: a page to promote.",
      inputSchema: { brand: brandRef, program, kind: z.enum(["image", "text", "link"]), title: z.string().max(200), body: z.string().max(10000).optional(), url: z.string().max(2000).optional() },
      annotations: write,
    },
    ({ brand, program, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-programs/{id}/assets", { params: { path: { brand: b, id: program } }, body }))),
  );

  server.registerTool(
    "cancel_partner_payout",
    {
      title: "Cancel a due partner payout",
      description: "Its rewards go back into the partner's balance.",
      inputSchema: { brand: brandRef, payout_id: id, note: z.string().max(2000).optional() },
      annotations: write,
    },
    ({ brand, payout_id, note }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/partner-payouts/{id}/cancel", { params: { path: { brand: b, id: payout_id } }, body: { note } }))),
  );
}
