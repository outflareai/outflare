# Talking videos

In the app: **Paid ads**, the **Creatives** tab, **Talking videos**.

A talking video is a person in a scene saying a script to the camera: the kind of ad that looks filmed on a phone. Outflare makes one stage by stage on the brand's own accounts, keeps what every stage made, and lets you fix or remove a word afterwards without making the whole video again.

It builds on [pictures, video and voice](brand-generations.md): every stage that calls a provider is one of those requests, on the brand's own connection, paid by the workspace to the provider. Outflare has no account of its own for any of it.

## What you need

- **ElevenLabs** connected: it reads the script and times every word. The key needs Text to Speech and Speech to Text.
- **fal.ai** connected: it renders the video, and upscales it.
- **Object storage** on the server (`OUTFLARE_S3_*`), where the files are kept.
- **ffmpeg** on the server, for the steps Outflare does itself: closing pauses, the phone-camera look, captions and edits. It is optional. Without it videos are still made, as the model made them, and each video's `notes` says what was left out. The engine looks for `ffmpeg` and `ffprobe` on its `PATH`, with the H.264 (libx264) and AAC encoders, when it starts.

## The stages

`POST /v1/brands/{brand}/videos` with the script, the picture to start from and the model of every stage:

```json
{
  "name": "Kitchen ad",
  "script": "Meet Northpeak, the fastest way to ship.",
  "direction": "A woman in a kitchen talks to her phone camera, handheld, morning light.",
  "scene_asset_id": "…",
  "settings": {
    "voice": { "voice": "<a voice id>", "model": "<an ElevenLabs speech model>" },
    "words": { "model": "<an ElevenLabs speech to text model>" },
    "video": { "model": "<a fal.ai video model that takes a picture and a sound file>", "options": { "resolution": "480p", "aspect_ratio": "9:16" } },
    "upscale": { "model": "<a fal.ai video upscaler>", "options": { "target_resolution": "1080p" } },
    "finish": { "tighten": true, "phone_look": true, "captions": true }
  }
}
```

| Stage | What happens | Where |
|---|---|---|
| `voice` | The script is read out in the voice you name. The voice comes first because a video model given only the text slurs words. | ElevenLabs |
| `words` | Long pauses are closed (`finish.tighten`), then every word is timed. A word the voice read differently from the script is listed in `notes` with its time. | ffmpeg, ElevenLabs |
| `render` | The video is made to that voice-over, from the scene picture, your direction and the script. | fal.ai |
| `upscale` | The render is made large. Rendering small and upscaling costs far less than rendering large. Left out when `settings.upscale` is. | fal.ai |
| `finish` | The look of a phone camera (30 frames a second, a little grain and sharpening, a phone's compression) and captions burned in as the words are said. | ffmpeg |

The answer is the video with `status: working`. Read `GET /v1/brands/{brand}/videos/{video}` until it is `ready` (`final_asset_id` is the finished file among the [brand's files](brand-assets.md)) or `failed` (`error_code` and `error` say why).

Every model is yours to name. Outflare has no defaults, because providers rename and retire models often. `scene_as`, `audio_as` and `video_as` are the models' own names for their inputs, for a model that doesn't use `image_urls`, `audio_urls` and `video_url`.

The voice-over may run as long as the video model takes in one request: 30 seconds unless `settings.video.max_seconds` says otherwise. A longer one fails with `video_too_long` before anything is rendered.

## The scene picture

The picture decides how real the video looks. A good way to make one: take a frame of a real video shot where you want yours to be, upload it as a brand file, and ask for a picture with that frame as a reference and a prompt for a different person in the same place (`POST /v1/brands/{brand}/generations` with `references`). Keep the picture: used again as `scene_asset_id`, or as a reference for the next picture, it keeps the person and the place the same across videos.

A picture of a real person, or a cloned voice, is used only with that person's agreement: a brand file kept as a `person` or a `voice` can't be used until [consent is confirmed](brand-assets.md#voices-and-people-need-consent).

## What each stage kept

`steps` on a video lists what every stage did: the file it made (`asset_id`) and, for a provider, the request it ran (`generation_id`), which holds its recipe: exactly what was sent and what came back. A stage is sent to a provider once. When a video fails, `POST /v1/brands/{brand}/videos/{video}/retry` starts it again at the stage that failed; the stages before it are kept and not paid for again.

## Editing by the words

A ready video lists every word with its time in `words`. `POST /v1/brands/{brand}/videos/{video}/edits` picks words by position (`from` and `to`, counted from 0):

- `{"action": "remove", "from": 4, "to": 6}` cuts those words out of the video and closes the gap.
- `{"action": "redo", "from": 4, "to": 4, "text": "Northpeak", "say": "north-peek"}` has them said again and stitched in, for a word the voice read wrong or words you want changed. `text` is what to show in their place (left out: the same words); `say` is how to spell them for the voice. The voice reads only that stretch, the video model renders only that stretch starting on the frame the old one started on, it is upscaled like the rest, and stitched in.

A stretch shorter than two seconds takes in the words next to it, since a video model renders no less. Both edits end with the finish again, so the captions follow the new words. One edit at a time. An edit that fails leaves the video ready as it was, with `error` saying why. Edits need ffmpeg on the server (`409 not_configured` without it).

A redo costs a short voice-over, its word timings and a few seconds of video: a fraction of the whole.

Add `"note"` to say what is wrong in your own words ("she says too, not to"). The video model is told it for that stretch, and it is kept in the video's history.

## Keep the fix, or go back

What an edit makes never replaces the video by itself. It waits as a new version: the video has `waiting_version`, and stays as it was, until a person answers. The video's `versions` hold both, each with its own finished file, so they can be played side by side.

- `POST .../videos/{video}/fix/keep` is yes: the fix becomes the video, and the version it replaces is kept. With `{"remember": true}` the way the fix spelled its words for the voice is saved as a lesson (below).
- `POST .../videos/{video}/fix/go-back` is no: the video stays as it was. `reason` and `note` say why, and stay with the fix, which is kept among the versions. With `"try_again": true` the same words are fixed again, the video model is told the note, and `say` can spell the words differently for the voice; what that makes waits for an answer in turn.
- `POST .../videos/{video}/versions/{number}/restore` goes back to any earlier version, or to a fix that was turned down. The version it replaces is kept, so going back loses nothing.

No other edit is taken while a fix waits (`409`).

## Like it, and make more of it

`POST .../videos/{video}/rating` with `{"rating": "liked", "reason": "..."}` or `"not_this"` records a person's opinion and why; an empty rating takes it back.

`POST .../videos/{video}/variations` makes more of a video that works: one new video per value, the same except for one thing.

| `change` | Each value is |
| --- | --- |
| `first_line` | A new first sentence for the script. |
| `direction` | A new description of how the video looks and moves. |
| `scene` | The id of another scene picture among the brand's files. |
| `voice` | The id of another voice. |

Each variation is a video of its own, made from the start on your accounts, with `variation_of` pointing at the first video of the family and `variation_change` saying what differs. `GET .../videos?family={video}` lists a video with its variations. Up to five at a time; they count toward the daily limit.

## Lessons

Something said once should hold from then on. `POST /v1/brands/{brand}/creative-lessons` saves it:

- `{"kind": "say", "word": "Northpeak", "say": "north-peek"}`: every later voice-over of the brand reads the word that way. Teaching the same word again replaces the earlier spelling.
- `{"kind": "rule", "rule": "No logos on clothing", "applies_to": "video"}`: a line added to every prompt, for `image`, `video` or `all`. [Creatives](creatives.md) made from skills get the rules for pictures too.

`GET` lists them, `DELETE .../creative-lessons/{lesson}` stops using one. A brand keeps up to 200.

## One history

`GET .../videos/{video}/history` is everything that happened to the video, newest first: made, each fix asked for with what was said, each fix that was ready, kept, turned down or failed, versions gone back to, likes and variations, each with who did it and when. Nothing in it is changed or removed while the video exists.

## For agents

The MCP server has `create_brand_video`, `get_brand_video`, `list_brand_videos`, `edit_brand_video`, `retry_brand_video`, `update_brand_video` and `delete_brand_video`, and for what comes after: `keep_brand_video_fix`, `go_back_brand_video_fix`, `restore_brand_video_version`, `rate_brand_video`, `create_brand_video_variations`, `get_brand_video_history`, `list_creative_lessons`, `add_creative_lesson` and `remove_creative_lesson`. An agent shows a fix and asks before keeping it. An agent is told that every stage costs the brand money, to make only what was asked for, and to ask which models to use.

## Events and monitoring

`video.requested`, `video.completed`, `video.failed`, `video.edit_requested`, `video.edit_failed`, `video.retried`, `video.renamed`, `video.deleted`, `video.fix_kept`, `video.fix_discarded`, `video.version_restored`, `video.rated`, `video.variations_requested`, `creative_lesson.added` and `creative_lesson.removed` are audit actions and product events, with the stage, the number of words and seconds and which finish steps are on; never the script, a name, a reason or a lesson's words. The Grafana dashboard's Brand files row shows videos by outcome, how long one takes and how long each stage takes on the server, with alerts for failures and a slow finish ([Monitoring](monitoring.md)).
