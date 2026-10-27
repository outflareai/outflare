# Integrations

Add your service to Outflare without writing Go. Each folder under `connectors/` is one connector: a manifest that describes your API, a logo, and recorded answers that prove the manifest works. Once a pull request is merged, the connector ships with the next deploy of Outflare Cloud (app.outflare.ai) and the next release for self-hosted installs. It shows up in every workspace's Connectors page, in the API catalog (`GET /v1/connectors`), to agents through MCP, and on outflare.ai/integrations.

Everything here is Apache-2.0 (see [LICENSE](LICENSE)), so you can copy any manifest as a starting point. Contributions are covered by the repository's [CLA](../CLA.md).

## How it works

A manifest is data, not code. Outflare reads it and makes the calls itself:

- Every request goes to your one `base_url`, over https. A person's key is only ever sent there.
- Templates only fill in values: `{{api_key}}` (or any field you ask for) and the capability's inputs, such as `{{email}}`. There are no expressions or scripts.
- Answers are read with dotted paths (`domain.acceptAll`, `results.0.email`) and mapped onto Outflare's own types, so every feature that needs that capability can use your connector, exactly like one built into the engine.
- Every connection uses the workspace's own key for your service. Outflare never supplies one.

## Folder layout

```
connectors/<id>/
  manifest.yaml   what your API is and how to call it
  tests.yaml      recorded answers (fake data only) and what Outflare should make of them
  logo.svg        your mark, square, a bare <svg> with a viewBox, no scripts or links
  logo.dark.svg   optional: a variant for dark backgrounds
```

The folder name is the connector's id: 2–40 lowercase letters, digits or `_`.

## Capabilities

Manifests support these capabilities today. More are coming (enrichment, email finding, notifications, MCP servers).

| Capability | Category | Inputs | Maps onto |
| --- | --- | --- | --- |
| `verify` | `email_verification` | `{{email}}` | `status` (deliverable, undeliverable, risky, unknown), `reason`, `provider_status`, `catch_all`, `disposable`, `role`, `free`, `suggestion`, `score` (0–100) |

## manifest.yaml

```yaml
id: acmeverify
name: AcmeVerify
category: email_verification
summary: Email verification with catch-all detection.   # one sentence, at most 160 characters
description: Checks each address before it is emailed.  # optional, at most 600 characters
website: https://acme.example
docs_url: https://acme.example/docs/api
key_url: https://app.acme.example/settings/api           # where people find their key
pricing: Pay per credit
publisher:
  id: acme                       # your publisher id
  name: Acme                     # not needed once you are in publishers.yaml
  website: https://acme.example
  contact: dev@acme.example      # for maintainers, not shown

base_url: https://api.acme.example

auth:
  api_key:
    fields:
      - name: api_key            # used as {{api_key}}
        label: API key
        secret: true
        help: Acme → Settings → API → create a key.

test:                            # a GET that signs in without changing anything or spending credits
  request:
    method: GET
    path: /v1/account
    headers:
      Authorization: "Bearer {{api_key}}"
  errors:                        # optional: a 200 answer that says something went wrong
    - when: { path: status, equals: error }
      message: reason            # the answer's own words
      kinds:                     # optional: the first word found picks the kind
        - { contains: credit, kind: quota }
        - { contains: key, kind: auth }
  account:
    name: workspace.name
    email: owner_email
    credits: credits             # or a list of paths, added up
    details:
      plan: plan.name

capabilities:
  verify:
    request:
      method: GET
      path: /v1/verify
      query:
        email: "{{email}}"
      headers:
        Authorization: "Bearer {{api_key}}"
    result:
      status:
        from: result
        values:
          valid: deliverable
          invalid: undeliverable
          disposable: { status: undeliverable, disposable: true }
          catch_all: { status: risky, catch_all: true }
        default: unknown
      reason: sub_result
      provider_status: result
      catch_all: is_catch_all
      disposable: is_disposable
      role: is_role
      free: is_free
      suggestion: did_you_mean
      score: score
```

Rules the checks enforce:

- `base_url` is a plain https address on a public host name: no IP address, port, query or path tricks. Request `path`s start with `/` and stay on it.
- `method` is GET, POST, PUT, PATCH or DELETE; the test request is a GET. `body` (JSON) is allowed on the others.
- Headers can't set `Host`, `Cookie`, `Content-Length`, `Transfer-Encoding` or `Connection`. A value with a line break in it is refused when the request is built.
- Every `{{variable}}` must be one of your auth fields or the capability's inputs.
- Unknown keys are errors, so a typo never goes unnoticed.
- HTTP 401 and 403 are read as a refused key, 402 and 429 as out of credits or rate limited, 5xx as the service being down.

## tests.yaml

Record real answers from your API, replace every person, address and domain with fake ones (`ana@acme.example`, Northpeak, `.example` domains) and keys with obvious fakes (`test-not-a-real-key`), and say what Outflare should make of each. You need at least one case for `test` and one for every capability; cover each status word you map and each error.

```yaml
- name: a deliverable address
  run: verify                    # test, or a capability
  creds: { api_key: test-not-a-real-key }
  input: { email: ana@acme.example }
  request:                       # optional: what your API should receive
    method: GET
    path: /v1/verify
    query: { email: ana@acme.example }
    headers: { Authorization: Bearer test-not-a-real-key }
  answer:                        # your API's recorded answer
    status: 200
    body: { result: valid, is_catch_all: false, score: 97 }
  expect: { status: deliverable, catch_all: false, score: 97 }

- name: a wrong key
  run: test
  creds: { api_key: test-not-a-real-key }
  answer: { status: 401, body: { error: unauthorized } }
  expect_error: auth             # auth, quota, unavailable or invalid
```

Run them:

```sh
go test ./engine/internal/connectors/manifest/
```

## Trust

Every connector carries a badge wherever it appears.

| Badge | Who |
| --- | --- |
| Built by Outflare | Connectors the Outflare team looks after (publisher `outflare`) |
| Verified | Publishers listed in [publishers.yaml](publishers.yaml): they proved their domain with a DNS TXT record or `https://<domain>/.well-known/outflare.json`, gave a security contact and passed review |
| Community | Everyone else, after review. Your connector works fully; the badge says the publisher isn't verified yet |

Only maintainers change `publishers.yaml`. A manifest can't claim a listed publisher's id or name.

To be verified, open an issue with your domain and security contact, and we'll send you the record to publish.

## Sending a pull request

1. Fork the repository and add `connectors/<id>/` with the four files above.
2. Run the tests.
3. Open a pull request. CI checks the manifest, the logo and your tests, scans for secrets and real people's data, and a maintainer reviews it.
4. Once merged, it ships with the next deploy.

Building something only your own company uses? You don't need a pull request: private connectors, added to one workspace only, are coming next.
