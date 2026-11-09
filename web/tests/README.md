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

Partner program settings page:

```sh
pnpm --filter @outflare/web test:partner-settings
```

Opens a program's settings at `/partners/settings` with every API request mocked: the page fills the main area, each tab (with its own URL) shows only its sections, edits survive switching tabs, the save bar shows only after a change and sends what was edited, Discard and the archive question work, and it fits a phone in light and dark. `OUTFLARE_TEST_SHOTS=<dir>` saves screenshots. CI runs it in the web-and-sdk job.

Reload after a deploy browser regression:

```sh
pnpm --filter @outflare/web test:stale-build-reload
```

Builds the app into a temporary folder and serves it, with the API mocked.
A page whose entry script answers 404 once reloads once and runs; one whose entry stays gone reloads once and stops; a failed lazy chunk reloads once and not again soon after.
CI runs it in the web-and-sdk job.
