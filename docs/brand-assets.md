# Brand files

In the app: **Paid ads**, the **Creatives** tab, **Brand kit**.

Brand files are the things a brand keeps to build ads and other content from: logos, fonts, product shots, screenshots, example ads, voices, pictures of people and screen recordings of the product. Each brand has its own, and nothing of one brand's is seen from another.

Keeping them in one place means an agent, a person or a tool that makes a creative starts from the same logo and the same product shots, and never from a picture of someone who hasn't agreed to it.

## In the app

The **Brand kit** page lists a brand's files as a table or a gallery (the choice is remembered), filtered by what they are and searched by name or tag. A banner counts the files that can't be used yet. **Add files** takes several at once and asks what each one is before adding it. A file's own page shows it large, and is where it is renamed, noted, tagged, archived, deleted, and where consent is confirmed or taken back. The brand's colours (buttons, background, text) are set on the same page.

## Made by AI

A brand can also have a picture, a video or a voice recording made on its own AI accounts; the file lands here with `source: generated`. See [Pictures, video and voice](brand-generations.md).

## Storage

The files live in the server's object storage, the same bucket as [session recordings](session-recordings.md) (`OUTFLARE_S3_*`, see [Analytics storage](analytics-storage.md)). A server without a bucket keeps no files: the list says `storage: false` and an upload is refused with `not_configured`.

## What a file has

- **Kind**: what it is for. The kind decides which types of file fit.

  | Kind | Takes |
  |---|---|
  | `logo`, `screenshot` | A picture |
  | `product_shot`, `example_ad`, `person` | A picture or a video |
  | `recording` | A video |
  | `voice` | A sound file |
  | `font` | A font |
  | `other` | Any of them |

- **Name**, a **note** on when to use it ("on dark backgrounds only") and up to 20 **tags**.
- Its **type** and, for a picture, its **size**. These are worked out from the file itself, never from its name or what the uploader said, so a script renamed `logo.png` is refused.
- **Consent**, for a voice or a person (below), and **usable**: whether it may be used for something new.

Accepted types: pictures (PNG, JPEG, GIF, WebP, SVG), videos (MP4, WebM, MOV), sound (MP3, WAV, M4A, OGG) and fonts (TTF, OTF, WOFF, WOFF2), up to 50 MB each and 5,000 files per brand. Uploading a file the brand already has returns the one it has.

## Voices and people need consent

A `voice` or `person` file shows or sounds like a real person, so it is **not usable** until someone confirms that person agreed to the use, with a note on how (a signed release and its date). Outflare records who confirmed it and when. Consent can be taken back at any time, which makes the file unusable again, and deleting a file removes it and every copy from storage.

Agents are told never to confirm consent on their own: they ask the person first.

## Smaller copies

`GET /v1/brands/{brand}/assets/{asset}/file` returns the file. For a picture, `?width=240`, `480`, `960` or `1920` returns a copy no wider than that (PNG when it has see-through parts, JPEG otherwise), made on first ask and kept. Pictures are never enlarged, SVGs are served as they are, and a picture of more than 60 million pixels gets no copies. `?download=true` sends it as a download.

Files are served with headers that stop a browser running anything inside them, so an SVG with a script in it is only ever a picture.

## API and agents

| Call | Does |
|---|---|
| `GET /v1/brands/{brand}/assets` | Lists files, by `kind`, `q` (part of a name, or a tag), `archived` and `usable` |
| `POST /v1/brands/{brand}/assets` | Uploads one: `kind`, `filename`, `data` (base64) and optionally `name`, `note`, `tags`, `consent` |
| `GET`, `PATCH`, `DELETE /v1/brands/{brand}/assets/{asset}` | Reads, changes (name, note, tags, kind, consent) or deletes one |
| `POST .../{asset}/archive`, `.../restore` | Stops using a file for anything new while keeping it, and brings it back |

Reading needs the `content.read` scope and changing `content.manage`. Over [MCP](../mcp/README.md) the tools are `list_brand_assets`, `get_brand_asset`, `upload_brand_asset`, `update_brand_asset`, `archive_brand_asset`, `restore_brand_asset` and `delete_brand_asset`.

## Events and monitoring

Every change is an audit action and a product event: `asset.uploaded`, `asset.updated`, `asset.consent_confirmed`, `asset.consent_withdrawn`, `asset.archived`, `asset.restored` and `asset.deleted`. They carry the kind, the family of file and its size, never its name. The **Brand files** row of the Grafana dashboard shows uploads by outcome and bytes stored; see [Monitoring](monitoring.md) for its alerts.
