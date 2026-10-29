<a href="https://outflare.ai"><picture><source media="(prefers-color-scheme: dark)" srcset="https://outflare.ai/brand/svg/outflare-lockup-white.svg"><img src="https://outflare.ai/brand/svg/outflare-lockup-black.svg" alt="Outflare" width="140" height="28"></picture></a>

# outflare (Python)

The Python client for the [Outflare](https://outflare.ai) API. Every API operation is a method, generated from the same API document as the server, the CLI and the TypeScript SDK, so it never falls behind. Apache-2.0.

```sh
pip install /path/to/outflare/sdk/python   # not on PyPI yet
```

```python
from outflare import Outflare

client = Outflare()  # reads OUTFLARE_TOKEN, and OUTFLARE_URL for a self-hosted server

lead = client.upsert_lead(email="ana@acme-com.example", first_name="Ana", company="Acme")

for mailbox in client.paginate(client.list_mailboxes, status="active"):
    print(mailbox["email"], mailbox["daily_limit"])
```

- **Names.** Methods are the operationIds in snake case: `listLeads` is `list_leads`, `createWebhook` is `create_webhook`. Path IDs are positional (`client.get_lead(lead_id)`), and query parameters and body fields are keywords. `help(client.create_webhook)` shows the same text as the [API reference](https://outflare.ai/docs/api/reference/), including the scope it needs. Python keywords get a trailing underscore (`from_=`).
- **The workspace** is the token's own, found with `GET /v1/whoami` on first use. Pass `workspace=` (an ID or slug) to name it yourself, or `client.with_options(workspace="acme")` for a copy.
- **Answers** are the JSON the API returns, as dicts and lists. Exports come back as text.
- **Errors** raise `OutflareError` with `status`, `code` (every code is listed at `GET /v1/error-codes`), `param` (the field a 400 is about), `message` and `request_id`.
- **Retries.** Network errors, `429` and `502` to `504` are retried up to `max_retries` times (3 by default), waiting as long as `Retry-After` asks. Every write carries an `Idempotency-Key`, the same one on each retry, so a retry never does the work twice. Pass `idempotency_key=` to choose it yourself.
- **Paging.** `client.paginate(method, **params)` yields every item, following `next_cursor`.
- **Uploads.** CSV endpoints take `content=`: `client.import_mailboxes(content=open("mailboxes.csv").read(), pool="Warm")`.
- **New fields.** `extra_body={...}` sends body fields this version doesn't know yet, and `client.request("GET", "/v1/...")` calls any path.
- **Missions.** `client.with_options(mission=mission_id)` acts inside an agent mission. A held write answers a dict for which `is_held(answer)` is true.

## Webhooks

```python
from outflare import verify_webhook, WebhookVerificationError

event = verify_webhook(raw_body, request.headers, os.environ["OUTFLARE_WEBHOOK_SECRET"])
if event["type"] == "lead.replied":
    ...
```

Pass the raw body, before any JSON parsing. It raises `WebhookVerificationError` when no signature matches or the delivery is more than five minutes old. To try a receiver on your machine, run `outflare listen --forward-to http://localhost:8000/webhooks`.

## Develop

`src/outflare/_operations.py` is generated: after changing `api/openapi.yaml`, run `go run ./engine/cmd/sdkgen` from the repository root. The engine's tests fail when it is out of date. Run this package's tests with `pip install -e '.[test]' && pytest`.
