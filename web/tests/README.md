Subscription brand assignment browser regression:

```sh
pnpm --filter @outflare/web exec playwright install chromium --only-shell
pnpm --filter @outflare/web test:ai-account-brands
```

The test starts its own local Vite server and mocks every API request. It needs no backend or account credentials. It checks empty initial brand choices despite a remembered brand, brand-only saves, workspace-wide reads, deferring assignment during polling, and the first rendered frame when switching workspaces, and the onboarding redirect to subscription assignment. CI runs it in the web-and-sdk job.
