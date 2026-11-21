# Error tracking

Outflare shows what breaks on a brand's website or app: every error, grouped so the same bug is one row, with how often it happens, on which pages, and to whom. It is under Analytics, **Errors**.

Errors are ordinary events. Each one is a track event named **Error Caught**, so the same data also works in trends, funnels (did people who hit this error still check out?), alerts and the Query page. Nothing else is stored.

## Turn it on

Errors are off until you turn them on, per site:

Open **Errors** and flip the **Error capture** switch at the top. The line beside it says what is happening: off, capturing errors on your site (with the script's last visit and the last error), or on but the script has sent no visit lately. The same switch, **Capture errors**, is on the script under Settings, **Website tracking**, in "What the script does on its own". It saves at once. A brand without the website script gets one button instead: **Add the website script**.

The script on your site stays the same: it learns the setting from Outflare on a visitor's first page. In the API the switch is `capture_errors` on the site key (`PATCH /v1/brands/{brand}/site-keys/{id}`; agents: `update_site_key`).

With the switch on, the script sends every error nothing caught and every rejected promise nothing handled.

## Send errors you caught yourself

`outflare.captureException` sends an error your own code caught. It works with or without the switch:

```js
try {
  await pay(order);
} catch (err) {
  outflare.captureException(err, { step: "checkout" });
}
```

The second argument is optional: extra properties to keep with the error. In React, call it from an error boundary:

```jsx
class Boundary extends React.Component {
  componentDidCatch(error, info) {
    window.outflare?.captureException(error, { component_stack: info.componentStack });
  }
  render() { return this.props.children; }
}
```

From a server, send a track event named `Error Caught` with the same properties, through `POST /v1/brands/{brand}/customer-events` or any of the libraries:

```json
{ "type": "track", "event": "Error Caught", "userId": "u_123",
  "properties": { "type": "TimeoutError", "message": "billing service timed out", "stack": "...", "handled": true } }
```

The libraries do it in one call. `captureException` (`capture_exception` in Python) reads the type, message and stack from the error, takes email addresses and key-shaped strings out, and adds a fingerprint that stays the same when only numbers, ids or line numbers change, so one bug is one issue:

```ts
import { Tracker } from "@outflare/sdk";
const tracker = new Tracker({ token, brand });

try { await charge(order); }
catch (err) {
  tracker.captureException(err, { userId: user.id, properties: { route: "/checkout" } });
  throw err;
}
```

```python
try:
    charge(order)
except Exception as err:
    tracker.capture_exception(err, user.id, {"route": "/checkout"})
    raise
```

Pass `handled: false` (`handled=False`) from a crash handler. An error with no `userId` or `anonymousId` is about no one, and is filed under the anonymous id `server`.

## What an error carries

| Property | What it is |
| --- | --- |
| `type` | The error's name, such as `TypeError`. |
| `message` | Its message, at most 500 characters. |
| `stack` | Its stack, at most 4,000 characters, with query strings taken out of addresses. |
| `handled` | `true` when your code caught it and sent it, `false` for an uncaught one. |
| `fingerprint` | What groups errors into one issue (below). |
| `source`, `line`, `column` | For an uncaught error: the file and place it was thrown. |
| `rejection` | `true` for a rejected promise. |
| `path` | The page. |
| `release` | The version of your site that was running, when you say it (below). |
| `viewport`, `color_scheme`, `reduced_motion` | The window's size (`1440x900`), `dark` or `light`, and `reduce` or `no-preference`: settings some bugs depend on. |
| `component_stack` | The React components it happened in, when you pass them (below). |

Outflare adds the rest when it stores the error: the site (the host of the page), the browser and operating system with their versions, the raw user agent, the browser's language and the visit's country. None of it is anything a visitor typed or chose on the page.

### Say which release is running

Add `data-release` to the Outflare script tag, filled in by your build with a version, a date or a commit, or call `outflare.setRelease("2026.11.18")` once the script has loaded. Every error then carries it, and an issue shows the release it first appeared in. Use the same value as `release` when you upload source maps.

```html
<script async src="https://app.outflare.ai/site.js" data-key="ofk_..." data-release="2026.11.18"></script>
```

### React: send the components it happened in

React 19 and newer tells you which components an error happened inside. Pass that on where your app starts:

```js
const report = (handled) => (error, info) =>
  window.outflare?.captureException(error, { component_stack: info.componentStack, handled });

createRoot(document.getElementById("root"), {
  onUncaughtError: report(false),
  onCaughtError: report(true),
  onRecoverableError: report(true),
}).render(<App />);
```

A production build of React or Redux Toolkit shortens its own errors to a code ("Minified React error #418"). Outflare spells these out without any setup: the issue and each error carry `message_full`, the full message with the details the build left in the link.

**Errors → Set up** shows, for the last 7 days, whether source maps are uploaded, how many errors carry a release and whether component names arrive, with these snippets to copy.

**Private data is taken out in the browser.** Before anything is sent, the script replaces email addresses with `[email]` and long strings shaped like keys or tokens with `[key]`, in the message and the stack. It never reads what a visitor typed. An error from another site's script that the browser hides ("Script error.") is not sent, because it says nothing.

A page sends at most 20 errors, and the same issue at most 5 times, so an error in a loop can't fill your events. `data-autocapture="off"` on the snippet stops uncaught errors for a site.

## Issues

An issue is the errors that are the same bug. Errors from the website script are grouped on the server, from the error's type, its message with numbers and ids taken out, and the file it was thrown in without its build hash, so "plan 42 not found" and "plan 7 not found" from the same file are one issue in Chrome, Firefox and Safari alike, and a new build (`app-3f9a1c.js` becoming `app-9b2e77.js`) doesn't split it. Every minified React or Redux error with the same code is one issue, and so is every failure to load a chunk of your app after a deploy. A server's errors keep the `fingerprint` they are sent with (lowercase letters and digits, at most 64), or are grouped by type and message.

**Merging by hand.** When the same bug still lands in two issues, tick them on the list and choose **Merge into one**: they become the one that happened most, with its status, and new errors of either join it. The issue's page lists what was merged into it, with **Split apart** to take a merge back. Nothing stored changes either way.

**Issues from before this grouping** were regrouped once, in the background. Their old links still open the issue they are now part of, and a status someone had set carried over.

The Errors page lists issues, most frequent first, for the last day, 7, 28 or 90 days, with a tab for each status and its count:

- **Open**: nobody has dealt with it. **New** marks one first seen in the period.
- **Fixed** (`resolved` in the API): you fixed it. If it happens again after that, it opens again by itself, marked **Back again**, and alerts again.
- **Ignored**: stays out of the open list and never alerts, even when it happens again. For noise you won't fix, such as a browser extension's errors.
- **Archived**: out of the way until it happens again, when it opens again quietly (not as Back again). For something that may have stopped.
- **Filtered**: noise, kept out of the open list and never alerting (below).
- **Delete** removes the issue and every error in it, so they also leave trends and funnels. It can't be undone; if the error happens again it is a new issue.

Search finds an error by its text or page, and the filters show only new ones, ones back again, uncaught ones, or the website's or the server's. Tick rows (or the box in the header for every row shown) to mark them fixed, ignore, archive, reopen or delete them at once; anything but Delete can be undone from the message that follows. Each row's **Actions** menu does the same for one issue.

**How much it matters.** Each row shows how many times and to how many visitors the error happened, the share of your site's visitors that is, the page where it hits the largest share of that page's visitors, and a bar for each day. Above the list, **Visitors who hit an error** is the share of all your visitors any error happened to. Shares need the website script's page views to compare with; without them they show a dash. While error capture is younger than the period you are looking at, nothing is marked **New**, because every error would be.

**Sites.** With errors from more than one site (`example.com`, `app.example.com`, `docs.example.com`), the list has a **Site** column and a site filter; in the API, `host` on each issue and `?host=`.

Open an issue to see how often it happened each day, the pages it happened on with the share of each page's views and visitors it reached, who changed its status and when, and every time it happened, 20 at a time. Each error opens to its stack, the React components, the release, the site, the browser and system with versions, language, country, window size and colour and motion settings, the user agent, **what the visitor did before** (their page views and events in the 30 minutes before it) and **Watch the recording** when the visit was recorded. When the visitor is a known person, **Open this person** goes to their record, so support can tell them it is fixed.

### Noise: filters and ignore rules

Some errors aren't your site's. These are filtered for every brand, always: errors thrown inside a browser extension (`chrome-extension://`, `moz-extension://`, `webkit-masked-url://`), an email link scanner opening your page ("Object Not Found Matching Id"), code a crypto wallet or an in-app browser injects (`window.ethereum`, `__firefox__`), a copy of your page saved to a file, and a copy served from a bare IP address. The script drops the first and the fourth before sending; the rest are stored, shown under **Filtered** with the reason, and never alert.

**Ignore rules** are your own filters, under **Errors → Ignore rules**. A rule looks in one part of an error (its message, type, the files in its stack, the page's address or the site) for a piece of text, in any case. Matching errors, stored and new, move to **Filtered** and stop alerting; delete the rule and they come back. **Ignore errors like this** in an error's menu starts a rule from it. Use a rule on the site to keep a staging site's errors out.

To refuse errors from sites that aren't yours before they arrive, list your own sites on the script under **Website tracking** ("Sites it records"). Left empty, any site that copies your script can send errors with it.

### Fix it with your AI coding assistant

**Copy fix prompt for AI** copies a prompt for Claude Code, Cursor or any assistant working in your code: the error with its full message, how often and to what share of visitors it happened, the site and pages, browsers with versions, window size and settings, the release it first appeared in, the stack (in your own files when a source map reads it), the React components, and what to do (find the cause, fix it rather than hide it, add a test). **Show prompt** shows exactly what is copied. Select several issues on the list to copy one prompt for up to 10 of them. **Copy error** copies the error as plain text for a ticket or chat. Prompts carry no visitor ids or names.

## Alerts

The first error of an issue nobody has seen before raises an [alert](alerts.md) (`error.new_issue`), once for the issue however often it happens. An issue you marked fixed raises one more when it comes back; an issue you ignored or archived raises none. The alert shows the error's type and message and opens the issue. A release that breaks many things at once stops at ten alerts an hour for a brand: the rest are on the Errors page, and alert with their next error once the hour has passed.

Filtered errors never alert.

**Emails are off until you switch them on.** A site that has just started capturing errors has many new ones, so nobody is emailed by default: new errors show on the Errors page and under Alerts. The **Emails about new errors** switch on the Errors page turns the email on for the brand's owners and admins, and off again. Under **Alerts** you can also send them to Slack or Telegram through your own connection, and each person can mute them for themselves. Every alert email ends with a **Manage notifications** link to that page.

## API and agents

- `GET /v1/brands/{brand}/errors?days=7&status=open` lists issues (agents: `list_error_issues`). `status` is `open`, `resolved`, `ignored`, `archived`, `filtered` or `all`; `host` keeps one site.
- `GET /v1/brands/{brand}/errors/{fingerprint}` is one issue with its days, pages, status changes, merges and latest errors (`get_error_issue`). A merged issue's fingerprint answers with the issue it is part of.
- `GET /v1/brands/{brand}/errors/{fingerprint}/occurrences?cursor=` pages through every error of an issue (`list_error_occurrences`), and `.../occurrences/{id}/before` is what the visitor did before one (`get_error_before`).
- `POST /v1/brands/{brand}/errors/merge` with `{"into": "...", "fingerprints": [...]}` merges issues (`merge_error_issues`), and `DELETE /v1/brands/{brand}/errors/{fingerprint}/merged/{merged}` splits one off again (`split_error_issue`). Recorded as `error_issue.merged` and `error_issue.split`.
- `GET`, `POST /v1/brands/{brand}/error-rules` and `PATCH`, `DELETE /v1/brands/{brand}/error-rules/{id}` are the ignore rules (`list_error_rules`, `create_error_rule`, `update_error_rule`, `delete_error_rule`), recorded as `error_rule.created`, `error_rule.updated` and `error_rule.deleted`.
- `PATCH /v1/brands/{brand}/errors/{fingerprint}` with `{"status": "resolved"}`, `ignored`, `archived` or `open` (`update_error_issue`). It needs `analytics.manage` and is recorded as `error_issue.updated`.
- `DELETE /v1/brands/{brand}/errors/{fingerprint}` deletes the issue and its errors (`delete_error_issue`), recorded as `error_issue.deleted`.
- `POST /v1/brands/{brand}/errors/bulk` with `{"fingerprints": [...], "action": "ignored"}` does one of those to up to 200 issues (`bulk_error_issues`).

Errors count toward the site key's daily event limits like any other event, and are kept as long as your events are.

## Source maps

A site's built code is minified, so a stack from it names functions `a` and `t` in `app-3f9a1c.js`. Upload the source map your build made for each file and the Errors page shows every stack in your own files, lines and function names, with **Show as sent** one press away. Without a map for a file, its lines stay as they were sent.

Upload from your build or deploy script, once per `.map` file, with an API token that has `analytics.manage`:

```sh
jq -n --arg file /assets/app-3f9a1c.js --slurpfile map dist/assets/app-3f9a1c.js.map '{file: $file, map: $map[0]}' \
  | curl -X POST https://app.outflare.ai/v1/brands/$BRAND/source-maps \
    -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" --data-binary @-
```

- `file` is the built file as your site serves it: its path or its full address. A bare file name (`app-3f9a1c.js`) matches that file in any folder. Uploading the same file again replaces its map.
- `release` (optional) is your own name for the build, such as a version or a commit, to keep one map per build of a file whose name doesn't change. A stack is read through the newest map for its file.
- `map` is the contents of the `.map` file, as an object or as its text. The body may be up to 64 MB.

Your source code is never kept: the map's `sourcesContent` is dropped before it is stored, so only names and positions remain, and a map can't be downloaded again. Stacks are read through the maps when an issue is opened, so a map uploaded after an error still explains it, and deleting a map changes nothing that was stored. A brand keeps its 300 newest maps.

In the app, **Errors → Source maps** lists what was uploaded, with the command to copy and a Delete on each row. `GET /v1/brands/{brand}/source-maps` lists them and `DELETE /v1/brands/{brand}/source-maps/{id}` removes one (agents: `list_source_maps`, `delete_source_map`). An issue's errors carry `stack_original` beside `stack`.

Don't serve `.map` files from your site unless you mean to publish your source: upload them here and leave them out of the deploy.
