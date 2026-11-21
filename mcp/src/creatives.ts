// Ad creatives: a brand's skills (saved recipes for one kind of ad, with
// versioned prompts) and the creatives made from them, which wait for a
// person's yes. Every picture and video is made on the brand's own AI
// account and paid by the brand.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";
import { brandCaller, brandRef } from "./records.js";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: false } as const;
const skillId = z.string().uuid().describe("The skill's id, from list_creative_skills.");
const creativeId = z.string().uuid().describe("The creative's id, from list_creatives.");
const status = z.enum(["making", "failed", "needs_review", "approved", "rejected"]);

const variant = z.object({
  name: z.string().max(100).optional().describe("A short name for the prompt, such as: Problem first."),
  picture: z
    .string()
    .min(1)
    .max(8000)
    .describe("What the AI model is asked to make. Words in curly brackets, such as {persona}, {pain}, {hook}, {brand} or one of your own like {proof}, are filled in for each creative."),
  copy: z.string().max(4000).optional().describe("How to write the headline, second line and button that are laid over the picture."),
  share: z.number().int().min(0).max(100).optional().describe("This prompt's part of new creatives against the others, such as 50 and 50. 0 takes it out of the test."),
});
const recipe = {
  provider: z.enum(["", "openai", "fal"]).optional().describe("Which of the brand's accounts makes it. Left out, the first one connected."),
  options: z.record(z.string(), z.unknown()).optional().describe("The model's own settings, such as size and quality."),
  sizes: z.array(z.string()).max(8).optional().describe("The shapes the ad is wanted in, such as 1:1, 4:5, 9:16, 16:9."),
  rules: z.string().max(4000).optional().describe("Added to every prompt, such as what must never be in the picture."),
};
const words = {
  name: z.string().max(200).optional(),
  persona: z.string().max(200).optional().describe("Who the ad is for."),
  pain: z.string().max(200).optional().describe("The one problem it is about."),
  hook: z.string().max(200).optional().describe("How it opens, in a few words."),
  tags: z.array(z.string().min(1).max(50)).max(20).optional(),
  headline: z.string().max(200).optional().describe("Laid over the picture: write it as the prompt's copy says."),
  subline: z.string().max(300).optional().describe("The second line."),
  cta: z.string().max(60).optional().describe("The words on the button."),
};

export function registerCreativeTools(server: McpServer, gtm: MissionClient, workspace: () => string, readOnly: boolean, call: Call): void {
  const onBrand = brandCaller(gtm, workspace);

  server.registerTool(
    "list_creative_skills",
    {
      title: "List the brand's skills for making ads",
      description:
        "A skill is a saved recipe for one kind of ad: its prompts, the AI model and the sizes. Each comes with its current version, whether a split test between prompts is running (split_test) and how many creatives it made. " +
        "Use a skill to make a creative; don't write a prompt from nothing when a skill fits.",
      inputSchema: { brand: brandRef, archived: z.boolean().optional() },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/creative-skills", { params: { path: { brand: b }, query } }))),
  );

  server.registerTool(
    "get_creative_skill",
    {
      title: "Get a skill and its prompts",
      description: "One skill with its current version: each prompt (picture and copy), the curly brackets it needs filled in (fill_ins), the AI model, sizes and rules.",
      inputSchema: { brand: brandRef, skill_id: skillId },
      annotations: read,
    },
    ({ brand, skill_id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/creative-skills/{skill}", { params: { path: { brand: b, skill: skill_id } } }))),
  );

  server.registerTool(
    "list_creative_skill_versions",
    {
      title: "List a skill's versions",
      description: "Every saved version of a skill, newest first: what changed, its prompts and how many creatives it made.",
      inputSchema: { brand: brandRef, skill_id: skillId },
      annotations: read,
    },
    ({ brand, skill_id }) =>
      call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/creative-skills/{skill}/versions", { params: { path: { brand: b, skill: skill_id } } }))),
  );

  server.registerTool(
    "list_creatives",
    {
      title: "List the brand's creatives",
      description:
        "The ads the brand made from its skills, newest first: where each stands (making, failed, needs_review, approved, rejected), who and what it is for, the skill, version and prompt that made it, and its file (asset_id).",
      inputSchema: {
        brand: brandRef,
        status: status.optional(),
        skill_id: z.string().uuid().optional(),
        kind: z.enum(["image", "video"]).optional(),
        persona: z.string().optional(),
        pain: z.string().optional(),
        tag: z.string().optional(),
        q: z.string().optional().describe("Part of a name or headline."),
      },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/creatives", { params: { path: { brand: b }, query } }))),
  );

  server.registerTool(
    "get_creative",
    {
      title: "Get a creative and where it stands",
      description:
        "Read a creative until its status is needs_review (asset_id is its file; get_brand_asset has the details) or failed (error says why). A picture takes seconds to a few minutes, a video up to many minutes: wait between reads.",
      inputSchema: { brand: brandRef, creative_id: creativeId },
      annotations: read,
    },
    ({ brand, creative_id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/creatives/{creative}", { params: { path: { brand: b, creative: creative_id } } }))),
  );

  server.registerTool(
    "get_creative_history",
    {
      title: "Read the history of a creative",
      description: "Everything that happened to a creative, newest first: made (with the note given), its words changed, approved, rejected with the reason, liked or not, each with who did it.",
      inputSchema: { brand: brandRef, creative_id: creativeId },
      annotations: read,
    },
    ({ brand, creative_id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/creatives/{creative}/history", { params: { path: { brand: b, creative: creative_id } } }))),
  );

  server.registerTool(
    "get_creative_skill_draft",
    {
      title: "Get a skill drafted from a description",
      description: "A draft_creative_skill result: queued or running, then ready with a name, summary, picture prompt and copy instructions, or failed with why. Words in curly brackets are fill-ins left for the person.",
      inputSchema: { brand: brandRef, draft_id: z.string().uuid() },
      annotations: read,
    },
    ({ brand, draft_id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/creative-skill-drafts/{draft}", { params: { path: { brand: b, draft: draft_id } } }))),
  );

  if (readOnly) return;

  server.registerTool(
    "draft_creative_skill",
    {
      title: "Draft a skill from an ad described in words",
      description:
        "Turns a description of an ad into the draft of a skill (a name, the picture prompt and how to write the words), on the workspace's own AI model, from what the brand says about itself. Queued: read it with get_creative_skill_draft until ready, show it to the person, then save it with create_creative_skill. Nothing is made or saved until then.",
      inputSchema: { brand: brandRef, description: z.string().min(1).max(1000).describe("The ad wanted, in plain words: who it is for, what it shows, what it leads with.") },
      annotations: { ...write, idempotentHint: false, openWorldHint: true },
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/creative-skill-drafts", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "create_creative_skill",
    {
      title: "Add a skill",
      description:
        "Save a recipe for one kind of ad. Give one prompt, or two to four to split-test them. Ask the person which AI model to use when they haven't said: there is no default. Nothing is made or paid for here.",
      inputSchema: {
        brand: brandRef,
        name: z.string().min(1).max(100),
        description: z.string().max(500).optional().describe("What the ad is, in one line."),
        kind: z.enum(["image", "video"]),
        model: z.string().min(1).max(200).describe("The provider's own id for the AI model: an OpenAI image model or a fal.ai endpoint (owner/app/path)."),
        variants: z.array(variant).min(1).max(4),
        ...recipe,
      },
      annotations: { ...write, idempotentHint: false },
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/creative-skills", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "update_creative_skill",
    {
      title: "Change a skill, or save a new version of it",
      description:
        "name, description, enabled and archived change in place. A change to the prompts (variants, which replaces them all), model, provider, options, sizes or rules is saved as a new version with your note; earlier versions stay as they were. " +
        "restore_version goes back to an earlier version by saving it as a new one. To end a split test, send variants with only the winning prompt.",
      inputSchema: {
        brand: brandRef,
        skill_id: skillId,
        name: z.string().min(1).max(100).optional(),
        description: z.string().max(500).optional(),
        enabled: z.boolean().optional().describe("Off, the skill makes nothing."),
        archived: z.boolean().optional(),
        note: z.string().max(500).optional().describe("What changed and why, for the list of versions."),
        restore_version: z.number().int().optional(),
        version: z.number().int().optional().describe("The version you read; the change is refused if the skill has moved on."),
        model: z.string().min(1).max(200).optional(),
        variants: z.array(variant).min(1).max(4).optional(),
        ...recipe,
      },
      annotations: { ...write, idempotentHint: false },
    },
    ({ brand, skill_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/creative-skills/{skill}", { params: { path: { brand: b, skill: skill_id } }, body }))),
  );

  server.registerTool(
    "create_creative",
    {
      title: "Make a creative from a skill",
      description:
        "Fill a skill's prompt in for one persona and one pain and ask the brand's own AI account for the picture or video. " +
        "It costs the brand money at its provider every time, so make only what the person asked for. " +
        "Read the skill first (get_creative_skill): give a word for every curly bracket in fill_ins, and write headline, subline and cta as the prompt's copy says. " +
        "Leave variant out so a split test fills evenly. The creative then waits for a person: read it with get_creative until needs_review, and show it to them.",
      inputSchema: {
        brand: brandRef,
        skill_id: skillId,
        variant: z.string().max(1).optional().describe("One of the skill's prompts, A to D. Left out, the one furthest behind its share."),
        fill_ins: z.record(z.string(), z.string().max(1000)).optional().describe("Words for the prompt's curly brackets beyond persona, pain, hook and brand, such as { proof: \"99.9% of requests succeed\" }."),
        note: z.string().max(2000).optional().describe("Added to the prompt for this creative only."),
        ...words,
      },
      annotations: { readOnlyHint: false, destructiveHint: false, idempotentHint: false, openWorldHint: true },
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/creatives", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "update_creative",
    {
      title: "Rename a creative or change its words and tags",
      description: "Change a creative's name, the persona, pain and hook it is filed under, its tags, or the headline, second line and button. No new picture is made or paid for.",
      inputSchema: { brand: brandRef, creative_id: creativeId, ...words, version: z.number().int().optional().describe("The version you read.") },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, creative_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/creatives/{creative}", { params: { path: { brand: b, creative: creative_id } }, body }))),
  );

  server.registerTool(
    "approve_creative",
    {
      title: "Approve a creative",
      description: "Record a person's yes, so the creative may be used. Never approve on your own: only when the person looked at it and said so.",
      inputSchema: { brand: brandRef, creative_id: creativeId },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, creative_id }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/creatives/{creative}/approve", { params: { path: { brand: b, creative: creative_id } } }))),
  );

  server.registerTool(
    "reject_creative",
    {
      title: "Reject a creative",
      description: "Record a person's no, with their reason. The creative and its file are kept, so what was turned down and why can be learned from.",
      inputSchema: { brand: brandRef, creative_id: creativeId, reason: z.string().max(500).optional() },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, creative_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/creatives/{creative}/reject", { params: { path: { brand: b, creative: creative_id } }, body }))),
  );

  server.registerTool(
    "rate_creative",
    {
      title: "Like a creative, or say not this",
      description:
        "Record a person's opinion of a creative with their reason: liked marks what to make more of, not_this what to stay away from. Separate from approving. An empty rating takes it back.",
      inputSchema: { brand: brandRef, creative_id: creativeId, rating: z.enum(["", "liked", "not_this"]), reason: z.string().max(500).optional() },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, creative_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/creatives/{creative}/rating", { params: { path: { brand: b, creative: creative_id } }, body }))),
  );
}
