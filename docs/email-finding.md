# Email finding and the verification waterfall

Outflare finds a likely work email for a person and checks whether an address
is real, using your own first-party data first and a third-party verifier only
when it has to. It spends nothing until the last tier, and that last tier runs
on your own connected account.

## How finding works

For a person's name and a company domain, the finder builds the candidate
addresses a company might use — `first.last@`, `flast@`, `firstlast@`,
`first@` and so on — and ranks them against what it has learned about that
domain.

It learns each domain's **pattern profile** from your workspace's own data: the
leads you already hold at that domain and how their addresses turned out when
verified or sent to. If the people you reach at `acme.example` are
`jane.roe@`, `bob.smith@` and `john.doe@`, the finder learns the domain uses
`first.last` and proposes `alice.nguyen@acme.example` for a new Alice Nguyen,
with a confidence score. A domain it has seen bounce on a shape learns to avoid
that shape. A domain it knows nothing about falls back to how common each shape
is in general, at low confidence.

The profile is **per workspace**: it is built from your data and used only for
you. Nothing is shared between workspaces.

## The verification waterfall

Checking an address runs in tiers, cheapest first, stopping as soon as it has
an answer:

1. **First-party history** — if you have already seen this exact address
   deliver or get a reply, it is proven. Free, and the finder stops here.
2. **The learned pattern** — a confident match to the domain's known shape.
   Free.
3. **Built-in checks** — syntax, mail servers (MX), disposable domains and role
   addresses. Free. These can prove an address *undeliverable*, never that a
   mailbox exists.
4. **Your own verifier** — only when the tiers above can neither prove nor rule
   out the address and confidence stays low does it roll up to the verification
   service you have connected (for example Bouncer). This is **bring-your-own
   key**: it runs on your account and is billed to you, never to Outflare. With
   no verifier connected, the address is returned as *risky* for you to decide.

Every check at every tier is recorded, so the finder's accuracy improves over
time from your own outcomes.

## Result codes

Finding and verifying return a code per address so a pipeline can decide what
to do next:

| Code | Meaning | Next step |
| --- | --- | --- |
| `verified` | Proven deliverable (first-party history). | use it |
| `found` | A confident guess from the pattern. | use it |
| `risky` | Catch-all or low confidence. | your policy |
| `not_found` | No address could be built. | skip |
| `undeliverable` | Known bad. | skip |
| `pending` | A slow check is running. | re-check shortly |
| `retry` | A transient failure. | retry later |
| `stop` | Can't continue (no verifier connected, out of credits). | fix and resume |

## Calling it

Three endpoints, all scoped to one workspace:

- `GET /v1/workspaces/{ws}/email-finder/pattern?domain=acme.example` — the
  domain's learned shape and a confidence. Free. (`leads.read`)
- `POST /v1/workspaces/{ws}/email-finder/find` — a batch of people (name +
  company domain) to propose addresses for, up to 50. Free. (`leads.read`)
- `POST /v1/workspaces/{ws}/email-finder/verify` — a batch of addresses to run
  through the waterfall, up to 50. Spends only at the last tier, on your own
  connected verifier. (`leads.manage`)

Each result carries the caller's own `ref` echoed back, so match by that rather
than by position. Through MCP the same three are `email_pattern`, `find_emails`
and `verify_addresses`.

## What it never does

- It never shares one workspace's addresses, outcomes or patterns with another.
- It never spends on a third-party verifier when a free tier already has the
  answer.
- It never uses an Outflare-held key for your verification: the third-party
  tier is always your own connected account.
