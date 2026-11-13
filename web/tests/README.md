Subscription brand assignment browser regression:

```sh
pnpm --filter @outflare/web exec playwright install chromium --only-shell
pnpm --filter @outflare/web test:ai-account-brands
```

The test starts its own local Vite server and mocks every API request. It needs no backend or account credentials. It checks empty initial brand choices despite a remembered brand, brand-only saves, workspace-wide reads, deferring assignment during polling, and the first rendered frame when switching workspaces, and the onboarding redirect to subscription assignment. CI runs it in the web-and-sdk job.

Desktop app sign-in browser regression:

```sh
pnpm --filter @outflare/web test:app-signin
```

The same kind of test for the Outflare desktop app (docs/desktop-app.md): the approve page in a browser (signed in, signed out, expired), the sign-in screen inside the app's window with a fake shell, a failed completion's message, and links that leave Outflare opening through the shell. CI runs it in the web-and-sdk job.

Paid ads page browser regression:

```sh
pnpm --filter @outflare/web test:ads
```

The Paid ads page (plans/paid-ads/plan.md, "Web experience") for a brand with a Google Ads account, against mocked API answers: the cost per new paying customer against the target and its sentence, the header's customers and attribution window switches changing the performance query, the decision inbox (a saving approved without a question, a raise confirmed first, Undo, Acknowledge and the shadow section), the set-up path with the offer sheet reviewed in place, and the results, customers and verdicts screens at phone width without sideways scrolling. Set `OUTFLARE_TEST_SHOTS` to a folder to save a screenshot of each screen. CI runs it in the web-and-sdk job.
Tracker report page browser regression:

```sh
pnpm --filter @outflare/web test:tracker-report
```

The tracker's Report page (plans/ad-tracker-ui.md B1) against mocked drill-down answers: the default dates in the brand's time zone, the cost note, expanding a row of the tree, sorting largest first on the first click, drilling into a row (it becomes a filter and the grouping moves down), a custom column, saving a view with its settings, the CSV download, and one card per row at phone width. CI runs it in the web-and-sdk job.

Tracker campaigns page browser regression:

```sh
pnpm --filter @outflare/web test:tracker-campaigns
```

The tracker's Campaigns page (plans/ad-tracker-ui.md B2) against mocked API answers: the list of tracker campaigns with the last 7 days from the report, a new campaign opening its editor, General (name and redirect mode), Destination (each offer's share of the clicks, suggested weights applied by a click, Equalise, a path with two rules written out as a sentence, a lander and an offer on it, and a flow instead), Tracking (the source's parameters and postback), and the read-only editor at phone width without sideways scrolling. CI runs it in the web-and-sdk job.

Tracker offers, landers and flows browser regression:

```sh
pnpm --filter @outflare/web test:tracker-library
```

The tracker's Offers, Landers and Flows pages (plans/ad-tracker-ui.md B3) against mocked API answers: a capped offer in the list, a new offer's token picker inserting at the cursor, the missing click id warning, Preview with test values, a fixed payout with a payout by country, a cap and a fallback offer, archiving after a question; a new lander staying open with its click URLs and script; a new flow, and a flow's paths saved only after the "used by 3 links" question, then renamed; and the read-only Offers list at phone width without sideways scrolling. CI runs it in the web-and-sdk job.

Tracker traffic sources and affiliate networks browser regression:

```sh
pnpm --filter @outflare/web test:tracker-sources
```

The tracker's Traffic sources and Affiliate networks pages (plans/ad-tracker-ui.md B4) against mocked API answers: the template gallery with Custom first, Connected on a network Outflare syncs, category chips and search; a source made from a template whose postback holds REPLACE (the save waits until it is filled), a parameter added in the next free field with the campaign URL preview following, and the send rules posted; an existing source's recent postbacks with a failure's reason; an affiliate network made from a platform template, its postback URL shown after it is made and again after a rotation; and the read-only list at phone width without sideways scrolling. CI runs it in the web-and-sdk job.

Tracker setup browser regression:

```sh
pnpm --filter @outflare/web test:tracker-setup
```

The tracker's Setup page (plans/ad-tracker-ui.md B5) against mocked API answers: the checklist ticked from the brand's data (4 of 6), Skip remembered after a reload, the postback URL built in Everflow's own tokens with a parameter added and p1 set, its secret from a rotation in a masked field, the image pixel and the server call built from it, a test conversion refused with its reason and then accepted on the latest click (Use my latest click) with its campaign shown, and the read-only page at phone width without sideways scrolling. CI runs it in the web-and-sdk job.

Tracker logs browser regression:

```sh
pnpm --filter @outflare/web test:tracker-logs
```

The tracker's Logs page (plans/ad-tracker-ui.md B6) against mocked API answers: clicks with their campaign and offer by name and the suspect badge, the time window, campaign and likely-bots filters in the request, Load more following the cursor, a click's detail with its id; lander clicks, conversions (one outside the window left out), postbacks in and out; errors grouped by why, most first, with Fix it opening the right tab; Live refreshing until paused; and the page at phone width without sideways scrolling. CI runs it in the web-and-sdk job.

Partner program settings page:

```sh
pnpm --filter @outflare/web test:partner-settings
```

Opens a program's settings at `/partners/settings` with every API request mocked: the page fills the main area, each tab (with its own URL) shows only its sections, edits survive switching tabs, the save bar shows only after a change and sends what was edited, Discard and the archive question work, and it fits a phone in light and dark. `OUTFLARE_TEST_SHOTS=<dir>` saves screenshots. CI runs it in the web-and-sdk job.

Link orders page:

```sh
pnpm --filter @outflare/web test:link-orders
```

Opens the Link orders page in the SEO section with every API request mocked, including fixture catalog, candidates, orders and impact. It checks Approve and Paid gating, candidate traffic as an em dash, that impact does not claim causality, and that the page never starts a paid SEO read. `OUTFLARE_TEST_SHOTS=<dir>` saves labeled screenshots at desktop (dark and light) and phone widths. CI runs it in the web-and-sdk job.

Reload after a deploy browser regression:

```sh
pnpm --filter @outflare/web test:stale-build-reload
```

Builds the app into a temporary folder and serves it, with the API mocked.
A page whose entry script answers 404 once reloads once and runs; one whose entry stays gone reloads once and stops; a failed lazy chunk reloads once and not again soon after.
CI runs it in the web-and-sdk job.

Importing leads into a list:

```sh
pnpm --filter @outflare/web test:lead-import
```

With every API request mocked: a list's page imports a CSV in a dialog with that list fixed and stays on the list, and the Leads import picks a list from a menu (search lists left out), starts on the filtered list, names a new list before importing, and sends no list for **No list**. `OUTFLARE_TEST_SHOTS=<dir>` saves screenshots. CI runs it in the web-and-sdk job.
