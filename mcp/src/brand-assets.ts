// Brand files: the logos, fonts, product shots, screenshots, example ads,
// voices and pictures of people a brand keeps, which ads and other content
// are built from. Agents look here before making anything visual, and add
// files the person hands them.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";
import { brandCaller, brandRef } from "./records.js";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const id = z.string().uuid().describe("The file's id, from list_brand_assets.");
const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: false } as const;
const kind = z
  .enum(["logo", "font", "product_shot", "screenshot", "example_ad", "voice", "person", "recording", "other"])
  .describe(
    "What the file is for: logo and screenshot are pictures; font is a font; product_shot, example_ad and person are pictures or videos; voice is a sound file; " +
      "recording is a screen recording of the product; other is any of them.",
  );
const generationKind = z.enum(["image", "video", "voice"]).describe("image is a picture, video a video, voice a script read out in a voice.");
const consent = z
  .object({
    confirmed: z.boolean().optional().describe("true only when the person told you the one shown or heard agreed to this use. false takes it back."),
    note: z.string().max(2000).optional().describe("How they agreed, such as a signed release and its date."),
  })
  .describe("For a voice or person only. Never confirm consent on your own: ask the person first.");
const details = {
  name: z.string().min(1).max(200).optional(),
  note: z.string().max(2000).optional().describe("When to use it, such as: on dark backgrounds only."),
  tags: z.array(z.string().min(1).max(50)).max(20).optional(),
  consent: consent.optional(),
};

export function registerBrandAssetTools(server: McpServer, gtm: MissionClient, workspace: () => string, readOnly: boolean, call: Call): void {
  const onBrand = brandCaller(gtm, workspace);

  server.registerTool(
    "list_brand_assets",
    {
      title: "List the brand's files",
      description:
        "The files a brand keeps to build ads and other content from: logos, fonts, product shots, screenshots, example ads, voices, pictures of people and screen recordings. " +
        "Look here before making anything visual, and use only files with usable true: a voice or person nobody has confirmed consent for is not usable. " +
        "storage false means this server has no file storage, so nothing can be uploaded.",
      inputSchema: {
        brand: brandRef,
        kind: kind.optional(),
        q: z.string().optional().describe("Part of a name, or a whole tag."),
        archived: z.boolean().optional(),
        usable: z.boolean().optional().describe("Only files that may be used."),
      },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/assets", { params: { path: { brand: b }, query } }))),
  );

  server.registerTool(
    "get_brand_asset",
    {
      title: "Get a file's details",
      description: "What one of the brand's files is: its kind, type, size, tags, note, consent and whether it is usable. Its bytes are at file_path.",
      inputSchema: { brand: brandRef, asset_id: id },
      annotations: read,
    },
    ({ brand, asset_id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/assets/{asset}", { params: { path: { brand: b, asset: asset_id } } }))),
  );

  server.registerTool(
    "list_generation_providers",
    {
      title: "List which AI accounts can make what",
      description:
        "For pictures, video and voice: which of the brand's own accounts (OpenAI, fal.ai, ElevenLabs) can make each, and whether it is connected. " +
        "Check this before create_brand_generation. When a kind isn't ready, tell the person which account to connect under Connectors: Outflare has none of its own.",
      inputSchema: { brand: brandRef },
      annotations: read,
    },
    ({ brand }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/generations/providers", { params: { path: { brand: b } } }))),
  );

  server.registerTool(
    "list_generation_voices",
    {
      title: "List the brand's ElevenLabs voices",
      description:
        "The voices the brand's own ElevenLabs account holds; a voice's id is what a voice request names. Use a cloned voice of a real person only when the person has agreed.",
      inputSchema: { brand: brandRef },
      annotations: { ...read, openWorldHint: true },
    },
    ({ brand }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/generations/voices", { params: { path: { brand: b } } }))),
  );

  server.registerTool(
    "list_brand_generations",
    {
      title: "List what the brand asked its AI accounts to make",
      description: "The pictures, videos and voice recordings the brand asked for, newest first: where each stands and, once done, the file it became (asset_id).",
      inputSchema: {
        brand: brandRef,
        kind: generationKind.optional(),
        status: z.enum(["queued", "running", "done", "failed"]).optional(),
        asset_id: id.optional().describe("Only the request that made this brand file: its recipe (request and response)."),
      },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/generations", { params: { path: { brand: b }, query } }))),
  );

  server.registerTool(
    "get_brand_generation",
    {
      title: "Get one request and where it stands",
      description:
        "Read a request until its status is done (asset_id is the brand file; get_brand_asset has its details) or failed (error says why). " +
        "A picture or recording takes seconds to a few minutes, a video up to many minutes: wait between reads.",
      inputSchema: { brand: brandRef, generation_id: z.string().uuid() },
      annotations: read,
    },
    ({ brand, generation_id }) =>
      call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/generations/{generation}", { params: { path: { brand: b, generation: generation_id } } }))),
  );

  server.registerTool(
    "list_brand_videos",
    {
      title: "List the brand's talking videos",
      description:
        "The talking videos the brand made or is making, newest first: where each stands and, once ready, its finished file (final_asset_id). " +
        "finishing says whether this server can add the phone-camera look and captions and edit videos (it needs ffmpeg).",
      inputSchema: {
        brand: brandRef,
        status: z.enum(["working", "ready", "failed"]).optional(),
        family: z.string().uuid().optional().describe("The id of a video: only that video and the variations made of it."),
      },
      annotations: read,
    },
    ({ brand, ...query }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/videos", { params: { path: { brand: b }, query } }))),
  );

  server.registerTool(
    "get_brand_video",
    {
      title: "Get a talking video, its words and its steps",
      description:
        "Read a talking video until its status is ready (final_asset_id is the finished file) or failed (error says why; retry_brand_video starts again at the stage that failed). " +
        "It lists every word with its time (words, counted from 0: what edit_brand_video picks from), notes on words the voice read differently from the script, and what each stage did. " +
        "A video takes several minutes to make: wait between reads.",
      inputSchema: { brand: brandRef, video_id: z.string().uuid() },
      annotations: read,
    },
    ({ brand, video_id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/videos/{video}", { params: { path: { brand: b, video: video_id } } }))),
  );

  server.registerTool(
    "get_brand_video_history",
    {
      title: "Read the history of a talking video",
      description:
        "Everything that happened to a talking video, newest first: made, each fix asked for and what the person said was wrong, each fix that was ready, kept, turned down (with why) or failed, versions gone back to, likes, and variations. " +
        "Read it before fixing a video again, so the same mistake isn't made twice.",
      inputSchema: { brand: brandRef, video_id: z.string().uuid() },
      annotations: read,
    },
    ({ brand, video_id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/videos/{video}/history", { params: { path: { brand: b, video: video_id } } }))),
  );

  server.registerTool(
    "list_creative_lessons",
    {
      title: "List what the brand taught its creatives",
      description:
        "The brand's lessons: how to say a word (given to every voice-over) and rules (added to every prompt for pictures, videos or both). They are applied by themselves; read them to know what the brand already said.",
      inputSchema: { brand: brandRef },
      annotations: read,
    },
    ({ brand }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/creative-lessons", { params: { path: { brand: b } } }))),
  );

  if (readOnly) return;

  const videoModel = (what: string) =>
    z.object({ model: z.string().min(1).max(200).describe(what), options: z.record(z.string(), z.unknown()).optional().describe("The model's own settings.") });

  server.registerTool(
    "create_brand_video",
    {
      title: "Make a talking video from a script",
      description:
        "Make a video of a person saying a script, stage by stage on the brand's own accounts: the voice-over (ElevenLabs), the time of every word (ElevenLabs), the video made to that voice from a picture of the person and scene (fal.ai), an upscale (fal.ai), and a finish with the look of a phone camera and captions (ffmpeg on the server). " +
        "Every stage costs the brand money at its provider, so make only what the person asked for. Ask which models to use when they haven't said: there is no default. " +
        "For the scene picture, make one first with create_brand_generation: a frame of a real video as a reference, and a prompt for a different person in the same place. " +
        "Render small (the video model's resolution option, such as 480p) and upscale, which costs far less than rendering large. " +
        "The answer is working: read it with get_brand_video until it is ready or failed.",
      inputSchema: {
        brand: brandRef,
        name: z.string().max(200).optional(),
        script: z.string().min(1).max(5000).describe("What the person says. About 60 words is 20 seconds."),
        direction: z.string().max(10000).optional().describe("How the video looks and moves: who, where, how they hold the camera."),
        scene_asset_id: id.optional().describe("A picture among the brand's files of the person in the scene."),
        settings: z.object({
          voice: z.object({
            model: z.string().min(1).max(200).describe("The ElevenLabs speech model."),
            voice: z.string().min(1).max(100).describe("The voice's id, from list_generation_voices."),
            options: z.record(z.string(), z.unknown()).optional(),
          }),
          words: z.object({ model: z.string().min(1).max(200).describe("The ElevenLabs speech to text model.") }),
          video: videoModel("The fal.ai video model's endpoint: one that takes a picture and a sound file as references.").extend({
            scene_as: z.string().max(60).optional().describe("The model's name for its picture input. Left out: image_urls."),
            audio_as: z.string().max(60).optional().describe("The model's name for its sound input. Left out: audio_urls."),
            max_seconds: z.number().min(0).max(600).optional().describe("The longest voice-over the model takes. Left out: 30."),
          }),
          upscale: videoModel("The fal.ai upscaler's endpoint.")
            .extend({ video_as: z.string().max(60).optional().describe("The upscaler's name for its video input. Left out: video_url.") })
            .optional(),
          finish: z
            .object({
              tighten: z.boolean().optional().describe("Close long pauses in the voice-over."),
              phone_look: z.boolean().optional().describe("The texture of a phone camera."),
              captions: z.boolean().optional().describe("Burn the words in as they are said."),
            })
            .optional(),
        }),
      },
      annotations: { readOnlyHint: false, destructiveHint: false, idempotentHint: false, openWorldHint: true },
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/videos", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "edit_brand_video",
    {
      title: "Edit a talking video by its words",
      description:
        "Change a ready talking video without making it all again. Pick words by position in the video's words (from get_brand_video, counted from 0). " +
        "remove cuts them out. redo has them said again and stitched in, for a word the voice read wrong or words to change: text is what to show in their place, say is how to spell them for the voice (\"north-peek\"). " +
        "A redo costs the brand a short voice-over and a few seconds of video, not the whole video. An edit that fails leaves the video as it was. " +
        "What the edit makes does not replace the video: it waits as a new version (waiting_version) until the person says keep (keep_brand_video_fix) or go back (go_back_brand_video_fix). Show them both and ask.",
      inputSchema: {
        brand: brandRef,
        video_id: z.string().uuid(),
        action: z.enum(["remove", "redo"]),
        from: z.number().int().min(0).describe("Position of the first word picked."),
        to: z.number().int().min(0).describe("Position of the last word picked."),
        text: z.string().max(1000).optional().describe("For redo: the words to show in place of the picked ones."),
        say: z.string().max(1000).optional().describe("For redo: how the voice should read them, spelled as they sound."),
        note: z.string().max(1000).optional().describe("What is wrong, in the person's own words. The video model is told it, and it is kept in the video's history."),
      },
      annotations: { readOnlyHint: false, destructiveHint: false, idempotentHint: false, openWorldHint: true },
    },
    ({ brand, video_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/videos/{video}/edits", { params: { path: { brand: b, video: video_id } }, body }))),
  );

  server.registerTool(
    "retry_brand_video",
    {
      title: "Try a failed talking video again",
      description: "Start a failed talking video again at the stage that failed. The stages already made are kept and not paid for again.",
      inputSchema: { brand: brandRef, video_id: z.string().uuid() },
      annotations: { readOnlyHint: false, destructiveHint: false, idempotentHint: false, openWorldHint: true },
    },
    ({ brand, video_id }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/videos/{video}/retry", { params: { path: { brand: b, video: video_id } } }))),
  );

  server.registerTool(
    "keep_brand_video_fix",
    {
      title: "Keep the fix to a talking video",
      description:
        "Record a person's yes to the fix that waits on a talking video: it becomes the video, and the version before it is kept to go back to. " +
        "remember saves how the fix spelled its words for the voice, so every later voice-over says them that way. Only when the person said to keep it.",
      inputSchema: { brand: brandRef, video_id: z.string().uuid(), remember: z.boolean().optional() },
      annotations: { ...write, idempotentHint: false },
    },
    ({ brand, video_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/videos/{video}/fix/keep", { params: { path: { brand: b, video: video_id } }, body }))),
  );

  server.registerTool(
    "go_back_brand_video_fix",
    {
      title: "Go back from the fix to a talking video",
      description:
        "Record a person's no to the fix that waits: the video stays as it was before. The fix is kept among the video's versions with the reason. " +
        "try_again makes the same fix again with note told to the video model (and say to the voice), which costs the brand another short voice-over and stretch of video.",
      inputSchema: {
        brand: brandRef,
        video_id: z.string().uuid(),
        reason: z.string().max(200).optional().describe("Why, in a few words."),
        note: z.string().max(1000).optional().describe("What is wrong, in the person's own words."),
        try_again: z.boolean().optional(),
        say: z.string().max(1000).optional().describe("For try_again on a redo: how to spell the words for the voice this time."),
      },
      annotations: { readOnlyHint: false, destructiveHint: false, idempotentHint: false, openWorldHint: true },
    },
    ({ brand, video_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/videos/{video}/fix/go-back", { params: { path: { brand: b, video: video_id } }, body }))),
  );

  server.registerTool(
    "restore_brand_video_version",
    {
      title: "Go back to a version of a talking video",
      description: "Make an earlier version of a talking video, or a fix that was turned down, the video again. The version it replaces is kept. Versions are on get_brand_video.",
      inputSchema: { brand: brandRef, video_id: z.string().uuid(), number: z.number().int().min(1) },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, video_id, number }) =>
      call(() =>
        onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/videos/{video}/versions/{number}/restore", { params: { path: { brand: b, video: video_id, number } } })),
      ),
  );

  server.registerTool(
    "rate_brand_video",
    {
      title: "Like a talking video, or say not this",
      description: "Record a person's opinion of a talking video with their reason: liked marks what to make more of, not_this what to stay away from. An empty rating takes it back.",
      inputSchema: { brand: brandRef, video_id: z.string().uuid(), rating: z.enum(["", "liked", "not_this"]), reason: z.string().max(500).optional() },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, video_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/videos/{video}/rating", { params: { path: { brand: b, video: video_id } }, body }))),
  );

  server.registerTool(
    "create_brand_video_variations",
    {
      title: "Make variations of a talking video",
      description:
        "Make more of a talking video that works: one new video per value, the same except for one thing. first_line replaces the script's first sentence, direction how it looks and moves, scene the scene picture (a file id), voice the voice (a voice id). " +
        "Each is a whole new video and costs the brand what a video costs, so make only as many as the person asked for. list_brand_videos with family shows them side by side.",
      inputSchema: {
        brand: brandRef,
        video_id: z.string().uuid(),
        change: z.enum(["first_line", "direction", "scene", "voice"]),
        values: z.array(z.string().min(1).max(10000)).min(1).max(5).describe("One variation per value."),
      },
      annotations: { readOnlyHint: false, destructiveHint: false, idempotentHint: false, openWorldHint: true },
    },
    ({ brand, video_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/videos/{video}/variations", { params: { path: { brand: b, video: video_id } }, body }))),
  );

  server.registerTool(
    "add_creative_lesson",
    {
      title: "Teach the brand's creatives something",
      description:
        "Save something the person said once so it holds for every later creative. kind say: word (as written) and say (spelled as it sounds), read that way by every voice-over. " +
        "kind rule: rule, a line added to every prompt, for applies_to all, image or video. Use the person's own words, and only what they said should always hold.",
      inputSchema: {
        brand: brandRef,
        kind: z.enum(["say", "rule"]),
        word: z.string().max(100).optional(),
        say: z.string().max(200).optional(),
        rule: z.string().max(500).optional(),
        applies_to: z.enum(["all", "image", "video"]).optional(),
        source_type: z.enum(["video", "creative"]).optional().describe("What it was learned on."),
        source_id: z.string().uuid().optional(),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/creative-lessons", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "remove_creative_lesson",
    {
      title: "Remove a lesson",
      description: "Stop using a lesson for new creatives. What was made with it stays as it is. Only when the person asked for it.",
      inputSchema: { brand: brandRef, lesson_id: z.string().uuid() },
      annotations: { readOnlyHint: false, destructiveHint: true, idempotentHint: true, openWorldHint: false },
    },
    ({ brand, lesson_id }) =>
      call(() => onBrand(brand, (b) => gtm.DELETE("/v1/brands/{brand}/creative-lessons/{lesson}", { params: { path: { brand: b, lesson: lesson_id } } }))),
  );

  server.registerTool(
    "update_brand_video",
    {
      title: "Rename a talking video",
      description: "Change a talking video's name. To change what is said, use edit_brand_video.",
      inputSchema: { brand: brandRef, video_id: z.string().uuid(), name: z.string().max(200) },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, video_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/videos/{video}", { params: { path: { brand: b, video: video_id } }, body }))),
  );

  server.registerTool(
    "delete_brand_video",
    {
      title: "Delete a talking video",
      description: "Remove a talking video and its steps. The files it made stay among the brand's files. Only when the person asked for it.",
      inputSchema: { brand: brandRef, video_id: z.string().uuid() },
      annotations: { readOnlyHint: false, destructiveHint: true, idempotentHint: true, openWorldHint: false },
    },
    ({ brand, video_id }) => call(() => onBrand(brand, (b) => gtm.DELETE("/v1/brands/{brand}/videos/{video}", { params: { path: { brand: b, video: video_id } } }))),
  );

  const reference = z.object({
    asset_id: id.describe("A usable file of this brand: a picture, a video or a sound file."),
    as: z.string().max(60).optional().describe("The model's own name for the input the file goes in, such as image_urls, audio_urls or video_url. Left out: image_urls, video_urls or audio_urls by the file's type."),
  });

  server.registerTool(
    "reuse_brand_generation",
    {
      title: "Make another file from the recipe of an earlier one",
      description:
        "Queue a new request with the same provider, model, prompt, settings and references as an earlier one, changing only what you pass: the liked picture again with new words in the prompt, the same person in another scene. " +
        "Find the recipe of a file with list_brand_generations and asset_id. It costs the brand money like any request. Read it with get_brand_generation until it is done.",
      inputSchema: {
        brand: brandRef,
        generation_id: z.string().uuid(),
        prompt: z.string().max(32000).optional(),
        model: z.string().max(200).optional(),
        voice: z.string().max(100).optional(),
        options: z.record(z.string(), z.unknown()).optional(),
        references: z.array(reference).max(12).optional(),
        asset_kind: kind.optional(),
        name: z.string().max(200).optional(),
      },
      annotations: { readOnlyHint: false, destructiveHint: false, idempotentHint: false, openWorldHint: true },
    },
    ({ brand, generation_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/generations/{generation}/reuse", { params: { path: { brand: b, generation: generation_id } }, body }))),
  );

  server.registerTool(
    "create_brand_generation",
    {
      title: "Make a picture, video or voice recording",
      description:
        "Ask one of the brand's own AI accounts for a file and keep it with the brand's files: a picture (OpenAI or fal.ai), a video (fal.ai) or a voice recording (ElevenLabs). " +
        "It costs the brand money at its provider every time, so make only what the person asked for, and never repeat a request to see if it comes out better without asking. " +
        "The answer is queued: read it with get_brand_generation until it is done or failed. " +
        "To keep a person or a scene the same across files, pass the earlier file in references. " +
        "Ask the person which model to use when they haven't said: there is no default, since providers retire models often.",
      inputSchema: {
        brand: brandRef,
        kind: generationKind,
        provider: z.enum(["openai", "fal", "elevenlabs"]).optional().describe("Left out, the first one the brand has connected for the kind."),
        model: z.string().min(1).max(200).describe("The provider's own id for the model: an OpenAI image model, a fal.ai endpoint (owner/app/path) or an ElevenLabs model id."),
        prompt: z.string().max(32000).optional().describe("What to make; for a voice, the script to read out. Needed unless the model only works on a reference file, as an upscaler does."),
        references: z
          .array(reference)
          .max(12)
          .optional()
          .describe("Brand files to make it from, so a person, a scene or a style stays the same: a picture of a person, a frame of a real video, an ad the brand liked, a video to upscale. Not for a voice."),
        voice: z.string().max(100).optional().describe("For a voice: the id of the voice, from list_generation_voices."),
        options: z.record(z.string(), z.unknown()).optional().describe("The model's own settings, such as size and quality for a picture or duration and aspect_ratio for a video."),
        asset_kind: kind.optional().describe("What the file is kept as among the brand's files. Left out: other."),
        name: z.string().max(200).optional(),
      },
      annotations: { readOnlyHint: false, destructiveHint: false, idempotentHint: false, openWorldHint: true },
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/generations", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "upload_brand_asset",
    {
      title: "Upload a file to the brand",
      description:
        "Add a file the person gave you to the brand's files, up to 50 MB: pictures (PNG, JPEG, GIF, WebP, SVG), videos (MP4, WebM, MOV), sound (MP3, WAV, M4A, OGG) and fonts (TTF, OTF, WOFF, WOFF2). " +
        "The type is worked out from the file itself and has to fit the kind. A file the brand already has comes back as it is, not added twice. " +
        "A voice or person stays unusable until consent is confirmed.",
      inputSchema: {
        brand: brandRef,
        kind,
        filename: z.string().min(1).max(255).describe("The file's name, such as logo.png."),
        data: z.string().min(1).describe("The file, base64-encoded."),
        ...details,
      },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/assets", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "update_brand_asset",
    {
      title: "Change a file's details",
      description:
        "Rename one of the brand's files, change its note, tags or kind, or confirm or take back consent for a voice or person. The file itself never changes: upload a new one instead.",
      inputSchema: {
        brand: brandRef,
        asset_id: id,
        kind: kind.optional(),
        ...details,
        version: z.number().int().optional().describe("The version you read; the change is refused if the file changed since."),
      },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, asset_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/assets/{asset}", { params: { path: { brand: b, asset: asset_id } }, body }))),
  );

  server.registerTool(
    "archive_brand_asset",
    {
      title: "Archive a file",
      description: "Stop using one of the brand's files for anything new, keeping it and its history. restore_brand_asset brings it back.",
      inputSchema: { brand: brandRef, asset_id: id },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, asset_id }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/assets/{asset}/archive", { params: { path: { brand: b, asset: asset_id } } }))),
  );

  server.registerTool(
    "restore_brand_asset",
    {
      title: "Restore an archived file",
      description: "Bring back a file that was archived, so it can be used again.",
      inputSchema: { brand: brandRef, asset_id: id },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, asset_id }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/assets/{asset}/restore", { params: { path: { brand: b, asset: asset_id } } }))),
  );

  server.registerTool(
    "delete_brand_asset",
    {
      title: "Delete a file for good",
      description:
        "Remove one of the brand's files and every copy of it from storage. It can't be undone, so do it only when the person asks, for example when someone no longer agrees to their picture or voice being used. To only stop using a file, archive it.",
      inputSchema: { brand: brandRef, asset_id: id },
      annotations: { readOnlyHint: false, destructiveHint: true, idempotentHint: true, openWorldHint: false },
    },
    ({ brand, asset_id }) => call(() => onBrand(brand, (b) => gtm.DELETE("/v1/brands/{brand}/assets/{asset}", { params: { path: { brand: b, asset: asset_id } } }))),
  );
}
