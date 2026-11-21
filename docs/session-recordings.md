# Session recordings and heatmaps

Recordings let you watch a visit to your website back, as the visitor saw it: where they moved, what they clicked and where they got stuck. Heatmaps show, for each page, where visitors click and how far down they scroll. Both come from the [website snippet](website-tracking.md) you already have, so there is nothing new to install, and both are off until you turn them on for a brand.

Find them under **Analytics → Recordings** and **Analytics → Heatmaps**.

## Turning them on

Open **Analytics → Recordings → Settings**. Each setting saves as you change it, and your website picks the change up within a minute.

| Setting | What it does |
| --- | --- |
| Record visits | Keeps a recording of visits to watch back. |
| Heatmaps | Counts where each page is clicked and how far down it is scrolled. |
| Share of visits recorded | Every visit, or a random share of them on a busy site. A visit is picked once, when it starts. |
| Hide all text | Also hides every piece of text on the page, for sites that show personal details. |
| Hide your own keys | The shapes of keys your product shows its users, hidden wherever they appear. Type a made-up key that looks like yours, and Outflare keeps only its shape (`xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`: `x` is any letter or digit). |
| Keep recordings for | 7 to 90 days. Older recordings are deleted for good. |
| Only these pages | Record and count only the pages you list, one per line: `/pricing`, or `/blog/*` for every page under it. Empty means every page. |
| Never these pages | Pages left out of recordings and heatmaps, written the same way. A visit that passes through one is recorded on its other pages. |
| Shortest visit kept | Recordings of visits shorter than this are deleted about half an hour after they end. |
| Most recordings a day | Stops recording new visits once the day's count (UTC) is reached. No limit by default. |

Changing these needs the `customers.manage` permission; watching needs `customers.read`.

## Deleting recordings and heatmaps

Recordings delete themselves after **Keep recordings for**, and heatmap counts after 90 days. To delete sooner:

- **Some recordings**: tick them in the list and choose **Delete selected**.
- **Old recordings**: **Delete old recordings…** above the list deletes every recording older than 7, 30 or 60 days, or every recording.
- **A heatmap**: open the page's heatmap and choose **Clear**, such as after a redesign. **Clear all** on the Heatmaps page clears every page. Counting carries on from then.

Deleted recordings and cleared heatmaps are gone for good. Over the API: `POST /v1/brands/{brand}/recordings/delete` and `POST /v1/brands/{brand}/heatmaps/clear`.

## What is and is not recorded

- **Nothing a visitor types is ever recorded.** Every field is hidden before anything leaves the visitor's browser, and there is no setting that turns this off.
- **Keys, tokens and passwords shown on a page are hidden too**, before anything leaves the visitor's browser, in recordings and in heatmap pictures alike: known key formats (such as `sk-…`, `ghp_…`, AWS and Google keys, sign-in tokens), `user:password@host` addresses, whatever follows a label such as "API key", "Token" or "Password", secrets in a link's parameters, and any run of 16 or more mixed letters and digits, which covers UUIDs and other ids as well. They show as stars. The server checks the known formats again before storing. **Hide your own keys** (Settings, `hide_shapes`) adds the shapes of your own product's keys: whatever has that shape and stands on its own is hidden in text, attributes and addresses, in the browser and again on the server. This is matching, so treat it as a safety net: for a page that shows secrets, add `data-of-mask` to that part, list the page under **Never these pages**, or turn on **Hide all text**.
- To hide one part of a page, add `data-of-mask` to it: its text is replaced before it is sent. `data-of-block` leaves the part out altogether and shows an empty box its size.
- Where consent is needed (see [the consent banner](consent-banner.md)), a visitor is recorded and counted only after they allow analytics, and recording stops if they withdraw it.
- `data-replay="off"` on the snippet keeps both off for that site, whatever the brand's settings say.
- A heatmap is counts only: a page, a day, a kind of device and a place on the page. It never names a visitor. To draw the page behind the counts, the snippet takes one picture of each page as a visitor's browser showed it, with the same hiding as a recording and no visitor attached. Turn on **Hide all text** if your pages show things about the person looking at them.
- Canvas drawings and the contents of iframes from other sites are not recorded.

Say in your privacy notice that visits to your site may be recorded.

## Watching a recording

The list shows each visit's visitor, when it started, how long it was, the pages seen, clicks, device and country. A dot marks the ones nobody has watched yet. Filter by page, device, length and period, by **Known people** or **Anonymous visitors**, and by website when your snippet is on more than one (your site and your app, say). Once a visitor is known (they signed up or signed in), the list shows their name and links to their person; until then it says **Anonymous visitor**.

Open a row to watch the visit full screen. It starts playing by itself. The progress bar shows what happened when: a red dot for each click, an amber bar for typing (never what was typed), a tick where a new page opened, and grey where the visitor paused. Pick a mark to jump to it. While it plays, each click shows as a ring where it landed, and a label says what the visitor is doing. Below, **The visit, page by page** lists each page with how long the visitor stayed, their clicks and how often they typed; pick a page to jump to it. You can also play at up to 8× speed and skip the pauses.

A visit is one browser tab, up to four hours. A recording appears a few seconds after the visit starts. Delete one from its page; its files are removed within ten minutes.

## Reading a heatmap

The Heatmaps list shows your pages with their page views and clicks. Open a page to see:

- **Clicks**: the warmer an area, the more clicks landed there. **Show** chooses how much is drawn over the page: **Busiest areas** (the default, which leaves quiet parts clear), **Every click**, **Top 10 spots, numbered**, or **The page only**.
- **Most clicked**: the ten spots with the most clicks, each named by what sits under it (a link or button with its text, or the text with "not a link or button"), with its clicks and share of all clicks. Pick one to see it on the page.
- **Scrolling**: the share of page views that got at least as far as each part of the page, and how far half of them got.

Pages are laid out differently on each kind of device, so a heatmap shows desktop, tablet or phone, one at a time. The page itself is drawn behind the heatmap. Heatmaps take their own picture of each page, the first time a visitor clicks on it and again once the picture is a week old, so they don't need recordings to be on. Until a page has its picture, its heatmap is drawn on a plain sheet the page's size. Heatmap counts and pictures are kept for 90 days.

**Pages with steps.** Some pages change what they show without changing their address: the steps of a sign-up, a wizard. Outflare tells the steps apart by the step marked as current (`aria-current="step"`) or, without one, the first heading in view, and gives each its own heatmap and picture. The Heatmaps list shows such a page once with its number of steps, and its heatmap has a **Step** picker, busiest step first. A page keeps at most 12 steps; past that (a heading that changes for every visitor) clicks count under the page itself. With **Hide all text** on, steps are named by a number instead of their heading. Over the API, `screen` picks one (`GET /v1/brands/{brand}/heatmaps?path=/onboarding&screen=Choose+a+plan`).

**Leaving pages out.** Tick pages in the Heatmaps list and choose **Exclude from tracking**: they are added to **Never these pages** and what was counted on them is deleted. On the Recordings list, tick visits and choose **Exclude their pages from tracking** to do the same for the pages those visits were on. **Delete all** on the Recordings page deletes every recording.

What to look for: a busy spot that is not a link or button is something visitors expect to work; a main button low on the list is being missed; and where scrolling drops sharply, move what matters above that point.

## Storage

Heatmaps and their page pictures are kept in the database. Recordings are files, kept in the server's S3-compatible bucket (Cloudflare R2, Amazon S3, MinIO), the same one [analytics storage](analytics-storage.md) describes:

```sh
OUTFLARE_S3_ENDPOINT=https://<account>.r2.cloudflarestorage.com
OUTFLARE_S3_BUCKET=outflare-files
OUTFLARE_S3_ACCESS_KEY_ID=...
OUTFLARE_S3_SECRET_ACCESS_KEY=...
```

Without a bucket, recordings stay off and the page says why; heatmaps still work. Files are at `replay/<workspace>/<brand>/<visit>/`, compressed.

Limits per recording: 4 hours, 60 MB and 600 parts; a workspace stores at most 5 GB of new recordings a day. A visit that passes a limit stops being recorded and keeps what it has.

## API and agents

| Call | What it does |
| --- | --- |
| `GET /v1/brands/{brand}/replay/settings`, `PATCH` | Read and change the settings |
| `GET /v1/brands/{brand}/recordings` | List recordings (`days`, `path`, `device`, `min_seconds`, `record_id`, `host`, `visitor`), and the websites recorded |
| `GET /v1/brands/{brand}/recordings/{recording}` and `/parts/{part}` | One recording and its parts, in order |
| `DELETE /v1/brands/{brand}/recordings/{recording}` | Delete one |
| `GET /v1/brands/{brand}/heatmaps/pages` | Pages with a heatmap |
| `GET /v1/brands/{brand}/heatmaps?path=/pricing` | One page's clicks and scroll depth |
| `GET /v1/brands/{brand}/heatmaps/snapshot?path=/pricing` | The picture of the page behind its heatmap |

MCP tools: `get_replay_settings`, `update_replay_settings`, `list_recordings`, `delete_recording`, `list_heatmap_pages` and `get_heatmap`.

The recorder is [rrweb](https://github.com/rrweb-io/rrweb) (MIT), served from your own Outflare address at `/v1/site/replay.js` and loaded only for visits that are recorded. `node scripts/vendor-rrweb.mjs` rebuilds it.
