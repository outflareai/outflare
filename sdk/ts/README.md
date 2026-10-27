<a href="https://outflare.ai"><picture><source media="(prefers-color-scheme: dark)" srcset="https://outflare.ai/brand/svg/outflare-lockup-white.svg"><img src="https://outflare.ai/brand/svg/outflare-lockup-black.svg" alt="Outflare" width="140" height="28"></picture></a>

# @outflare/sdk

[outflare.ai](https://outflare.ai) · [SDK docs](https://outflare.ai/docs/sdk/typescript/) · [API reference](https://outflare.ai/docs/api/reference/)

TypeScript client for the [Outflare](https://github.com/outflareai/outflare) API: the open-source cold-email sequencer and GTM control plane. Works in Node 18+, Deno, Bun, edge runtimes and the browser.

Every path, parameter and response is typed from the API's OpenAPI contract, so the client always matches the server.

Not published to a registry yet. Build it from this repository and install it from the folder:

```sh
git clone https://github.com/outflareai/outflare
cd outflare && pnpm install && pnpm --filter @outflare/sdk build
# then, in your project:
npm install /path/to/outflare/sdk/ts
```

## Use

Create an API token in the web app under API tokens, scoped to what your code needs.

```ts
import { createClient, ok } from "@outflare/sdk";

const gtm = createClient({ baseUrl: "https://outflare.example.com", token: process.env.OUTFLARE_TOKEN });
const ws = process.env.OUTFLARE_WORKSPACE!;

// How is outreach going?
const stats = ok(await gtm.GET("/v1/workspaces/{ws}/stats", { params: { path: { ws } } }));
console.log(`${stats.mailboxes.active} mailboxes, ${stats.sent_24h} sent today, ${stats.unread_replies} unread replies`);

// Import leads from a CSV into a list.
const imported = ok(
  await gtm.POST("/v1/workspaces/{ws}/leads/import", {
    params: { path: { ws }, query: { list: "Q4 founders" } },
    body: csvText,
    bodySerializer: (b: string) => b,
    headers: { "Content-Type": "text/csv" },
  }),
);

// Enroll the list in a sequence. Every send still goes through suppression
// and the policy check.
ok(
  await gtm.POST("/v1/workspaces/{ws}/sequences/{id}/enrollments", {
    params: { path: { ws, id: sequenceId } },
    body: { list_id: imported.list_id! }, // set because the import named a list
  }),
);

// Read replies.
const { data: replies } = ok(await gtm.GET("/v1/workspaces/{ws}/inbox", { params: { path: { ws }, query: { unread: true } } }));
```

Every call sends `Outflare-Client: sdk-ts/<version>`, so the server's product analytics can tell your integration's calls apart. Pass `clientName: "my-app/1.0"` to name it yourself (letters, digits and `._/@+-`, up to 64 characters).

## Errors

Each call returns `{ data, error, response }`, as [openapi-fetch](https://openapi-ts.dev/openapi-fetch/) does. Check `error`, or wrap the call in `ok()`. It returns the data, or throws an `OutflareError` that carries `status`, `code` (such as `conflict` or `invalid_request`) and `requestId`.

```ts
import { ok, OutflareError } from "@outflare/sdk";

try {
  ok(await gtm.DELETE("/v1/workspaces/{ws}/oauth-connections/{id}", { params: { path: { ws, id } } }));
} catch (e) {
  if (e instanceof OutflareError && e.code === "conflict") console.log(e.message);
  else throw e;
}
```

## Retries and paging

The client retries a call that failed on the network or was answered `429`, `502`, `503` or `504`, up to `maxRetries` times (3 by default), waiting as long as `Retry-After` asks (up to a minute). Every write carries an `Idempotency-Key`, the same one on each retry, so a retried write gets the first answer back instead of running twice. Pass your own key in `header: { "Idempotency-Key": key }` to make a whole job safe to run again, or `maxRetries: 0` to turn retries off.

`paginate()` yields every item of a paged list, following `next_cursor`:

```ts
import { paginate } from "@outflare/sdk";

for await (const lead of paginate((cursor) => gtm.GET("/v1/workspaces/{ws}/leads", { params: { path: { ws }, query: { q: "acme", cursor, fields: ["email"] } } }))) {
  console.log(lead.email);
}
```

`fields` trims each item to what you read (and `id`), which keeps long lists small.

## Missions

An agent acting toward a goal works inside a mission. Pass `mission`, and every call carries the `Outflare-Mission` header: the call is logged against the mission, and a write may come back `202` with a `MissionHeld` answer instead of running (a dry run, or held until a person approves it). Check with `isHeld()`:

```ts
import { createClient, isHeld, ok } from "@outflare/sdk";

const gtm = createClient({ baseUrl, token, mission: missionId });
const lead = ok(await gtm.POST("/v1/workspaces/{ws}/leads", { params: { path: { ws } }, body: { email } }));
if (isHeld(lead)) console.log(lead.message, lead.mission_action.id); // follow it with GET .../missions/{id}/actions/{action}
else console.log("created", lead.id);
```

A client made without `mission` is typed without those answers, since it never gets them. A client that sets the header itself, as the MCP server does, passes `missions: true` to get the same typing as one with `mission`.

## Webhooks

`verifyWebhook()` checks a delivery's signature and age and returns the typed event. Pass the raw body, before any JSON parsing. It uses Web Crypto, so it runs in Node 18+, Deno, Bun, Cloudflare Workers and browsers:

```ts
import { verifyWebhook, WebhookVerificationError } from "@outflare/sdk";

export default {
  async fetch(req: Request) {
    try {
      const event = await verifyWebhook(await req.text(), req.headers, env.OUTFLARE_WEBHOOK_SECRET);
      if (event.type === "lead.replied") await notifyRep(event.data);
      return new Response(null, { status: 204 });
    } catch (e) {
      if (e instanceof WebhookVerificationError) return new Response("bad signature", { status: 401 });
      throw e;
    }
  },
};
```

With Express, read the body with `express.text({ type: "application/json" })` and pass `req.headers`. Deliveries are retried, so use `event.id` to skip one you already handled. They follow [Standard Webhooks](https://www.standardwebhooks.com), so its libraries for other languages verify them too.

## Types

Schemas are exported by name, such as `Mailbox`, `Lead`, `Sequence`, `Enrollment`, `InboundMessage`, `WorkspaceStats` and `WebhookEvent`. `paths` and `components` are the full generated types.

## In the browser

Omit `token` on the same origin as the instance, and the session cookie is used. This is how the Outflare web app calls the API.

## Reference

- [API reference](https://github.com/outflareai/outflare/blob/main/api/openapi.yaml). Every instance also serves it at `/v1/openapi.yaml`.
- [Scopes and authentication](https://github.com/outflareai/outflare#api). Each operation in the document names the scope it needs in `x-outflare-access`.

## License

Apache-2.0, so it can be embedded in any project. The Outflare engine itself is AGPL-3.0.
