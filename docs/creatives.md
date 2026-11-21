# Skills and creatives

In the app: **Paid ads**, the **Creatives** tab. Its pages are **Library** (every creative), **Talking videos**, **Skills and prompts** and **Brand kit**, each one click from the others.

**Start here** leads those pages for a brand's first ad. It shows four steps and which is next (add brand files, connect an AI account that makes pictures, pick a skill, make a creative), and **starter skills** that are added with one click: Problem first, One number, and Before and after. Each has two prompts sharing the creatives between them and rules that keep pictures from looking AI-made. Type the picture model's id once, as your AI account names it; Outflare has no default. A starter skill is an ordinary skill once added, so everything below applies to it. Ideas for the first creative come from the personas in the [brand profile](brand-profile.md).

### Say what you want

Under the starter skills is a box for an ad described in your own words, such as "an ad for hikers tired of heavy packs; lead with our weight". **Write the prompt** has the workspace's own AI model (AI tool **Creative prompts**, on the workspace's own key) draft a skill from it: a name, the prompt for the picture and how to write the words over it. It reads the description and what the brand says about itself (its context and personas), never customers. Where it needs a number or proof it wasn't given, it leaves a fill-in in curly brackets instead of inventing one. Read the draft, change anything and press **Save as a skill**; until then nothing is saved, and no picture is made or paid for. Without an AI model connected, the box says so and links to Connectors.

Over the API: `POST /v1/brands/{brand}/creative-skill-drafts` with `description` queues a draft, `GET /v1/brands/{brand}/creative-skill-drafts/{draft}` reads it until its status is `ready` or `failed`, and `POST /v1/brands/{brand}/creative-skills` saves it with the picture model you choose. The MCP tools are `draft_creative_skill` and `get_creative_skill_draft`. Drafts are in [Query](query.md) as `creative_skill_drafts`.

A brand makes its ads from **skills**: saved recipes for one kind of ad each. A **creative** is one ad made from a skill, for one persona and one pain. Every creative records the skill, the version and the prompt that made it, plus its tags, so results can later be compared by any of them: which prompt, which persona, which opening pays.

The picture or video is made on the brand's own AI account ([Pictures, video and voice](brand-generations.md)) and kept with the [brand's files](brand-assets.md). Outflare has no AI account of its own for this: without a connected provider nothing is made, and the answer names what to connect.

## Skills

A skill has a name, what it makes (`image` or `video`), an On/Off switch, and a recipe:

- **variants**: one to four prompts. Each has a `picture` (what the AI model is asked to make), a `copy` (how to write the headline, second line and button that are laid over the picture) and a `share`.
- **model** and **provider**: the provider's own id for the AI model, and which of the brand's accounts makes it. There is no default model: providers retire and rename models every few months, so the model is always a setting of the skill.
- **options**: the model's own settings, passed on as they are.
- **sizes**: the shapes the ad is wanted in, such as `1:1`, `4:5`, `9:16`, `16:9`.
- **rules**: added to every prompt, such as what must never be in the picture.

Words in curly brackets are filled in for each creative. `{persona}`, `{pain}`, `{hook}` and `{brand}` come from the creative itself; any other, such as `{proof}` or `{brand fact}`, is given when the creative is made. A skill lists the brackets each prompt needs as `fill_ins`.

```json
{
  "name": "Static ad",
  "kind": "image",
  "model": "your-image-model",
  "sizes": ["1:1", "4:5"],
  "rules": "Never: stock-photo look, extra text in the picture, fake logos.",
  "variants": [
    { "name": "Problem first", "picture": "A {persona} at their desk, stuck on {pain}.", "copy": "Name {pain} as a question. At most 6 words.", "share": 50 },
    { "name": "One number", "picture": "One picture that shows {proof} at a glance.", "copy": "Lead with one number. At most 6 words.", "share": 50 }
  ]
}
```

### Versions

`PATCH /v1/brands/{brand}/creative-skills/{skill}` changes the name, description and switch in place. A change to the prompts, model, provider, options, sizes or rules is saved as a **new version**, with a `note` saying what changed. Earlier versions are never edited, so a creative made by version 3 still shows exactly what version 3 was. `restore_version` goes back to an earlier version by saving it as a new one. `GET .../versions` lists them all, with how many creatives each made.

### Split tests

A version with more than one prompt taking a share is a split test (`split_test: true`). New creatives are shared between the prompts by their shares: each one is made by the prompt furthest behind its share, unless you name a `variant`. To end a test, save a version with only the winning prompt.

## Creatives

`POST /v1/brands/{brand}/creatives` with a `skill_id`, the `persona` and `pain` it is for, the words for any other curly brackets in `fill_ins`, and the words to lay over the picture (`headline`, `subline`, `cta`):

```json
{
  "skill_id": "…",
  "persona": "Agency owner",
  "pain": "unclear pricing",
  "fill_ins": { "proof": "one price per GB, no contract" },
  "headline": "Pay per GB. No contract.",
  "cta": "Start free",
  "tags": ["pricing"]
}
```

The prompt is filled in and sent to the brand's AI account once. A bracket with no word is refused before anything is sent, so nothing is paid for a prompt with a hole in it. The creative gets a short code within the brand (`CR-0142`) and a status:

| Status | Meaning |
|---|---|
| `making` | The AI account is working on it. |
| `failed` | It ended without a file; `error_code` and `error` say why. |
| `needs_review` | The file is there (`asset_id`) and it waits for a person. |
| `approved`, `rejected` | A person's answer. A rejection keeps its reason. |

`POST .../creatives/{creative}/approve` and `.../reject` record the answer. Nothing reaches an ad network without a yes. `PATCH` renames a creative or changes its tags and words; no new picture is made or paid for.

`POST .../creatives/{creative}/rating` with `{"rating": "liked", "reason": "..."}` or `"not_this"` is a person's opinion, separate from the yes or no: an approved creative can still be one not to make more of. `GET .../creatives/{creative}/history` is everything that happened to it, newest first, with who did it and what they said: made (with the note given), words changed, approved, rejected, liked.

Rules the brand taught its creatives (`POST /v1/brands/{brand}/creative-lessons` with `kind: rule`, for `image` or `all`; see [talking videos](brand-videos.md#lessons)) are added to the prompt of every creative made after.

The list filters by `status`, `skill_id`, `kind`, `persona`, `pain`, `tag` and `q` (part of a name or headline).

## The finished ad

The picture the AI account makes carries no words. Outflare lays the headline, second line, button and the brand's logo over it:

`GET /v1/brands/{brand}/creatives/{creative}/ad?size=4:5&width=1080`

- **size**: the shape, as width:height (`1:1`, `4:5`, `9:16`, `16:9`, or any other up to three times as wide as tall or as tall as wide). The picture is cropped around its middle to fit. Left out, the picture's own shape.
- **width**: 240 to 2160 pixels, 1080 when left out. **format**: `jpeg` (default) or `png`. **logo=false** leaves the logo out; **download=true** saves it as a file.

So one picture gives the ad in every size, and changing the words costs nothing: the ad is drawn from the creative as it is now, and no copy of it is kept. It uses the newest logo (PNG, JPEG, WebP or GIF) and font (TTF or OTF) among the [brand's files](brand-assets.md), a built-in font when the brand has none, and the brand's primary colour for the button. Only pictures are drawn; a video creative is its own file.

## Limits

A brand keeps at most 100 skills, each with up to 4 prompts and 500 versions. Creatives count toward the brand's 200 pictures, videos and recordings in 24 hours. Finished files need object storage (`OUTFLARE_S3_*`).

## For agents

The MCP server has a tool for each call: `list_creative_skills`, `get_creative_skill`, `list_creative_skill_versions`, `create_creative_skill`, `update_creative_skill`, `list_creatives`, `get_creative`, `create_creative`, `update_creative`, `approve_creative`, `reject_creative`, `rate_creative` and `get_creative_history`. An agent writes the headline as the prompt's `copy` says, makes only what the person asked for, and approves only when the person said so.
