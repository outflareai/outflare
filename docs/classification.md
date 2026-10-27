# Email classification

`POST /v1/workspaces/{ws}/classifications` says what kind of email
infrastructure an address or domain is. It is for sign-up flows ("should we
let this address in?") and for agents and imports ("is this lead on a real
company domain?"). It never says whether a mailbox exists or who owns it:
verifications do that.

```sh
curl -s "$OUTFLARE_URL/v1/workspaces/$WS/classifications" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" \
  -d '{"email": "jo@proton.me"}'
```

Send `email` or `domain`, never both. A domain alone keeps the person's
address out of the request. `GET /v1/workspaces/{ws}/email-domains/{domain}`
is the same lookup as a GET. Scope `leads.read`. MCP: `classify_email`.

## What comes back

- `global`: Outflare's assessment, the same for every workspace.
  - `category`: `free_personal` (Gmail, Outlook.com, ISP mail),
    `private_mailbox` (Proton, Tuta, Fastmail), `relay_alias` (Hide My Email,
    Firefox Relay, SimpleLogin, addy.io, DuckDuckGo), `disposable`,
    `education`, `government` or `unknown`. `business`, `personal_custom`,
    `nonprofit_institutional` and `managed_other` are reserved for when
    there is evidence to tell them apart; a custom domain with Google
    Workspace mail is `unknown`, not `business`.
  - `subcategory`: for relays, `permanent_alias` or `anonymous_alias`; for
    throwaway domains, `rotating_domain_provider` when the service issues
    many domains, else `unknown_disposable_subtype`.
  - `confidence` is `null` until it is calibrated against a reviewed set of
    domains. `confidence_status` says what the answer rests on: `verified`
    (a provider Outflare reviewed), `corroborated` (two lists agree),
    `inferred` (one list, or a suffix such as `.edu`), `conflicted` or
    `insufficient_evidence`.
  - `attributes` are `true`, `false` or `"unknown"`. No evidence is
    `"unknown"`, never `false`. `is_role_address` (info@, support@) is only
    there for an email. `has_mx`, `has_mail_receiving_path` and
    `mx_provider_id` come from DNS.
  - `risk`: a rule-based score from 0 to 100 (higher is more concern) with
    its signals, or `not_assessed`. It is about the domain, never a
    probability that a person is fraudulent.
  - `evidence`: each fact behind the answer and its source.
- `effective`: what this workspace makes of it, and the recommended
  `action` (`allow`, `verify`, `review`, `block`). Until workspaces have
  their own overrides and rules, Outflare's defaults apply: block throwaway
  domains and domains that take no mail, `verify` unknown domains, allow the
  rest. The action is advice; your flow decides.
- `research`: what this workspace's own research made of the domain, when
  it has researched it (see below). Absent otherwise, and absent for every
  other workspace.
- `analysis.provisional` is true while a domain is unknown.
  `analysis.status` is `complete`, `not_queued` (unknown, research off),
  `queued` (research running) or `researched`.

## Where the data comes from

The data ships inside the binary (`engine/internal/classify/data`), so a
self-hosted server needs no outside service:

| Source | What | Licence |
| --- | --- | --- |
| Outflare | Reviewed free, private and alias providers (`providers.yaml`) | AGPL |
| [disposable-email-domains](https://github.com/disposable-email-domains/disposable-email-domains) | Throwaway domains | CC0 |
| [fakefilter](https://github.com/7c/fakefilter) | Throwaway domains, grouped by the service that issues them | BSD 3-Clause |

Reviewed providers outrank the lists. Plus DNS for mail servers, and the
public suffix list for `.edu`, `.ac.uk`, `.gov.au` and the like.

Nothing a workspace does changes this data: not its lookups, not its
verification results, not (later) its overrides.

## Researching the long tail

Most domains the lists and DNS cannot place say what they are on their own
homepage: "free disposable email, no sign-up", "secure mailbox from €1 a
month", or nothing about email at all. A workspace can have those pages
read.

It runs entirely on the workspace's own accounts, because Outflare never
supplies an API key for a connector or for AI:

1. Connect **Geonode** (connector `geonode`), which reads the page.
2. Connect an **AI model** with your own key (Claude, OpenAI, Gemini or
   Mistral) under Settings → AI model. Claude Haiku is the cheap, accurate
   choice: a researched domain costs a fraction of a cent.
3. Turn on `domain_research` in the same settings.

Then an unknown domain comes back as `queued`, a background job fetches
`https://<domain>/` through Geonode and asks the model what the words mean,
and the next call answers `researched`:

```json
{
  "research": {
    "category": "disposable",
    "subcategory": "short_lived_mailbox",
    "confidence_status": "inferred",
    "signals": ["site_offers_temporary_addresses"],
    "note": "The site hands out inboxes that expire after ten minutes.",
    "model": "claude-haiku-5-5",
    "researched_at": "2026-10-26T14:30:00Z"
  }
}
```

What it will not do:

- **Change the global data.** A verdict belongs to the workspace that paid
  for it. Where research disagrees with Outflare's reviewed data, the global
  answer stands and the action becomes `review` for a person to settle.
- **Spend without a limit.** Research stops at the workspace's domain
  research limit (US$200 a calendar month by default, `0` stops it) or at
  the workspace's overall AI limit, whichever comes first. A domain is
  researched again only after 90 days, and at most once a day.
- **Follow what a page says.** The homepage arrives inside the question as
  quoted evidence, and the model is told to treat every word of it as
  evidence about the site, never as an instruction.
- **Ask without being asked.** Research is off until a workspace turns it
  on, and classification works without it, on the lists and DNS.

Lead verification uses the same classifier: the free built-in checks block
the same throwaway domains, and Proton counts as a private mailbox, not a
free one.
