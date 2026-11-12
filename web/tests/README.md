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
