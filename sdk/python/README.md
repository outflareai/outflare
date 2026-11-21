<a href="https://outflare.ai"><picture><source media="(prefers-color-scheme: dark)" srcset="https://outflare.ai/brand/svg/outflare-lockup-white.svg"><img src="https://outflare.ai/brand/svg/outflare-lockup-black.svg" alt="Outflare" width="140" height="28"></picture></a>

# outflare (Python)

The Python client for the [Outflare](https://outflare.ai) API. Every API operation is a method, generated from the same API document as the server, the CLI and the TypeScript SDK, so it never falls behind. Apache-2.0.

```sh
pip install /path/to/outflare/sdk/python   # not on PyPI yet
```

```python
from outflare import Outflare

client = Outflare(brand="northpeak")  # reads OUTFLARE_TOKEN, and OUTFLARE_URL for a self-hosted server

lead = client.upsert_lead(email="ana@acme-com.example", first_name="Ana", company="Acme")

for mailbox in client.paginate(client.list_mailboxes, status="active"):
    print(mailbox["email"], mailbox["daily_limit"])
```

- **Names.** Methods are the operationIds in snake case: `listLeads` is `list_leads`, `createWebhook` is `create_webhook`. Path IDs are positional (`client.get_lead(lead_id)`), and query parameters and body fields are keywords. `help(client.create_webhook)` shows the same text as the [API reference](https://outflare.ai/docs/api/reference/), including the scope it needs. Python keywords get a trailing underscore (`from_=`).
- **The workspace** is the token's own, found with `GET /v1/whoami` on first use. Pass `workspace=` (an ID or slug) to name it yourself, or `client.with_options(workspace="acme")` for a copy.
- **The brand.** A brand's data (leads, records, tracking, webhooks) is under `/v1/brands/{brand}/`. A brand's own token goes to its brand by itself; with a workspace token, pass `brand=` (an ID or handle), set `OUTFLARE_BRAND`, or use `client.with_options(brand="northpeak")`. Each method also takes `brand=` for one call.
- **Answers** are the JSON the API returns, as dicts and lists. Exports come back as text.
- **Errors** raise `OutflareError` with `status`, `code` (every code is listed at `GET /v1/error-codes`), `param` (the field a 400 is about), `message` and `request_id`.
- **Retries.** Network errors, `429` and `502` to `504` are retried up to `max_retries` times (3 by default), waiting as long as `Retry-After` asks. Every write carries an `Idempotency-Key`, the same one on each retry, so a retry never does the work twice. Pass `idempotency_key=` to choose it yourself.
- **Paging.** `client.paginate(method, **params)` yields every item, following `next_cursor`.
- **Uploads.** CSV endpoints take `content=`: `client.import_mailboxes(content=open("mailboxes.csv").read(), pool="Warm")`.
- **New fields.** `extra_body={...}` sends body fields this version doesn't know yet, and `client.request("GET", "/v1/...")` calls any path.
- **Missions.** `client.with_options(mission=mission_id)` acts inside an agent mission. A held write answers a dict for which `is_held(answer)` is true.

## Send product events

`Tracker` sends what people do in your product from your server. It needs an API token with `customers.manage` (it reads `OUTFLARE_TOKEN`), so keep it on your servers.

```python
from outflare import Tracker

tracker = Tracker(brand="northpeak")

tracker.identify("u_123", {"email": "ana@acme.example", "plan": "pro"})
tracker.track("u_123", "Order Completed", {"total": 42, "currency": "USD"})
tracker.group("u_123", "org_7", {"name": "Acme"})

tracker.close()  # before the process exits; also runs at exit
```

Events are queued in memory and sent in batches (20 at a time, or every 10 seconds) to `POST /v1/brands/{brand}/events`. A batch the server can't take is tried again three times with a growing wait; a refusal such as a bad token is not. Every call returns the event's message id, which the server uses to store a retried event once. There are also `page`, `screen` and `alias`, and every call takes `anonymous_id=`, `timestamp=` and `message_id=`. `flush()` sends now; `on_error(error, lost)` hears about batches that were given up on. A background thread does the sending.

## Webhooks

```python
from outflare import verify_webhook, WebhookVerificationError

event = verify_webhook(raw_body, request.headers, os.environ["OUTFLARE_WEBHOOK_SECRET"])
if event["type"] == "lead.replied":
    ...
```

Pass the raw body, before any JSON parsing. It raises `WebhookVerificationError` when no signature matches or the delivery is more than five minutes old. To try a receiver on your machine, run `outflare listen --forward-to http://localhost:8000/webhooks`.

## Flags without a flash

`flags_bootstrap_tag()` turns your server's flag decisions into the tag the Outflare site script reads first, so a page you render shows the visitor's version in the first paint, escaped so a payload can't close the tag:

```python
from outflare import flags_bootstrap_tag

anonymous_id = request.cookies.get("of_aid") or str(uuid.uuid4())
decisions = client.evaluate_flags(anonymous_id=anonymous_id, url=str(request.url))
html = html.replace("</head>", flags_bootstrap_tag(decisions, anonymous_id) + "</head>")
```

## Flags decided in your server's memory

`LocalFlags` reads a brand's flag definitions once, then again every 30 seconds, and decides each visitor's flags in your process. A page costs no request to Outflare, and deciding carries on with the last definitions when Outflare can't be reached. The answers match the website script's and the evaluate API's:

```python
from outflare import LocalFlags, flags_bootstrap_tag

flags = LocalFlags(client, brand=brand)  # one per process; safe to share between threads

user_agent = request.headers.get("user-agent", "")
decisions = flags.decide(anonymous_id=anonymous_id, url=str(request.url), user_agent=user_agent)
html = html.replace("</head>", flags_bootstrap_tag(decisions, anonymous_id) + "</head>")
```

`flags.refresh()` at start-up moves the first read out of the first page. For flags limited to people, pass what your server knows about the visitor: `lists` (ids of the People lists they are in) and `person` (their fields); in memory nobody is looked up. `refresh_seconds` changes how often it asks (at least 5) and `on_error` hears about a failed refresh. `decide_flags(definitions, ...)` decides from definitions you hold yourself, and `flag_bucket(salt, subject)` is the hash. The token needs `analytics.read` and stays on your server.

For one flag, `flags.variant(key, anonymous_id=...)` gives the version's key or `False`, `flags.is_enabled(key, ...)` whether it is on, and `flags.payload(key, ...)` the version's content. When your server shows a version itself, `tracker.exposed(user_id, key, variant, anonymous_id=...)` sends Experiment Viewed so the flag's A/B test counts the visitor; it sends nothing when the variant is `False`.

## Errors

`tracker.capture_exception(err, user_id, {"route": "/checkout"})` sends an error your server caught as `Error Caught`, which the Errors page groups into issues. It reads the type, message and traceback from the exception, takes email addresses and key-shaped strings out, and adds a fingerprint that ignores numbers, ids and line numbers. Without a `user_id` or `anonymous_id` the error is filed under the anonymous id `server`. `describe_error(err)` returns the same properties without sending them.

`tracker.crawler_visit(user_agent, host)` counts requests from AI crawlers (GPTBot, ClaudeBot, PerplexityBot and others) for a site that isn't behind Cloudflare. Call it for every request from a middleware: it returns at once, counts in memory only when the user agent looks like a crawler, and sends the counts with the next flush. Outflare keeps only how often each AI crawler came per host and day, shown under AI visibility, Traffic.

## Develop

`src/outflare/_operations.py` is generated: after changing `api/openapi.yaml`, run `go run ./engine/cmd/sdkgen` from the repository root. The engine's tests fail when it is out of date. Run this package's tests with `pip install -e '.[test]' && pytest`.
