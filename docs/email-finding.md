# Email finding and the verification waterfall

Outflare finds a likely work email for a person and checks whether an address
is real, using your own first-party data first and a third-party email finder
or verifier only when it has to. It spends nothing until the paid tiers, and
those run on your own connected accounts.

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
3. **Your own email finder** — when the tiers above leave only a guess below
   75 confidence, or no address at all, the people-finding step of a pipeline
   asks the email finder you have connected (for example Blitz, Treg, Prospeo,
   LeadMagic or Findymail) for the person's address. This is
   **bring-your-own key**: it runs on your account and is billed to you, never
   to Outflare. With several connections to the same finder, calls take turns
   across them, and a connection that is rate limited, out of credits or at
   its usage limit is skipped. With several different finders, the next one is
   asked only when the one before had no address, in the order
   [below](#which-finder-is-asked-first). An answer is remembered for
   90 days (the same cache the list builder uses), so a retry or a second run
   never pays for the same person twice. Each call has its own 160 seconds,
   and one person takes at most six minutes at this tier (two calls at their
   slowest). The limit is that long because Treg asks up to four providers one
   after another for one person ([below](#treg)). A finder that gives no answer
   in that time for a person counts as having no address for them: it is not
   asked again in that run, because the lookup may still finish and be billed.
   With no finder connected this tier does nothing.
4. **Built-in checks** — syntax, mail servers (MX), disposable domains and role
   addresses, on the finder's address or on the guess. Free. These can prove
   an address *undeliverable*, never that a mailbox exists.
5. **Your own verifier** — only when the tiers above can neither prove nor rule
   out the address does it roll up to the verification service you have
   connected (for example Bouncer): for a guess whose confidence stays low,
   and for every address a finder returned without saying it checked the
   mailbox. This is **bring-your-own key** too. With no verifier connected, a
   finder's address is returned as *found* at confidence 80 (a provider looked
   the person up, which is more than a guessed shape, but nobody confirmed the
   mailbox), and a guess is returned as *risky* for you to decide.

When a finder says it checked the mailbox itself, the address is *verified*
(confidence 90) and the verifier is not asked. An address a finder returns that
you already know is undeliverable counts as no answer. When every connection to
your finders refuses the key, is out of credits or is at its usage limit, the
pipeline pauses with the reason (`connector_needed`, `connector_quota` or
`usage_limit_reached`) instead of quietly settling for a guess; a finder that
can't be reached is tried again later.

Every check at every tier is recorded, so the finder's accuracy improves over
time from your own outcomes.

## Which finder is asked first

Finders are asked in the order you connected them, with one exception: a
person whose **LinkedIn profile** is known. A profile names the very person,
where a name and a domain only describe them, so a lookup by profile is the
best answer there is, and usually the cheapest and fastest. For such a person:

1. Finders that find an email from a profile and nothing else go first
   (Blitz).
2. Then the other finders that can read a profile (Treg, whose first route is
   Blitz).
3. Then every other finder, asked by name and domain as always.

Within each step the order is the order you connected them. A person without
a profile is asked about in connection order, and a finder that only reads a
profile is not asked about them at all (no call, nothing spent).

The profile comes from whoever names the person: a people search that returns
it, or a pipeline that sets it on the person (`Contact.LinkedInURL` for the
shared people steps, `linkedin_url` on a find item). Only a person's own
profile counts (`linkedin.com/in/...`); a company page or any other URL is
ignored. The profile is given only to finders that keep to the company asked
about (Blitz and Treg): a profile also lists the jobs a person has left, and
an address from one of those must never be used. MoltSets, Apollo, Prospeo and
People Data Labs accept a profile in the list builder, but in this tier they
are asked by name and domain.

**What is remembered.** A finder's answer is remembered for 90 days under what
it was asked: the name and domain, or the profile with the domain. So:

- A person asked about before by name, with no address found, is asked about
  **again** once their profile is known, by the finders that read a profile.
  The earlier miss said nothing about the profile.
- An address a finder already found from the name alone still stands. It is
  not paid for a second time because a profile turned up.
- The same profile at another company is another question, and is asked.
- Finders that cannot read a profile keep their answer for the name either
  way. Nothing remembered before this rule existed is lost: answers without a
  profile are stored exactly as they were.

## How fast finders are asked

- **16 people at once** for each batch of up to 50. A slow finder takes half a
  minute or more for one person, so a lower number left most of a batch
  waiting for the next run.
- **120 lookups a minute for each connection**, unless the finder names its
  own pace: **Blitz 600 a minute** (ten a second, what its smallest paid plan
  allows on each endpoint). A finder that answers "too many requests" has its
  pace halved for five minutes, down to an eighth, and people who could not be
  asked yet come back as `pending`. The default is deliberately not higher: an
  eighth of it (15 a minute) has to be slow enough for a gateway such as Treg,
  which passes each lookup on to other providers and allows fewer of them
  than a provider asked directly.
- To cap what a connection may spend, set its usage limit on the Connectors
  page; these paces only protect the provider's own rate limit.

## Blitz

A [Blitz](https://blitz-api.ai) connection is your own Blitz API key (Connectors
→ Blitz). A plan is a number of records a month: one record for each person or
email found, nothing for a miss. Each record counts as one credit against the
connection's usage limits and a list's credit cap.

| What | Blitz call | Notes |
| --- | --- | --- |
| Checking the key | `GET /v2/account/key-info` | Free. Shows the records left and the plan |
| People at a company | `POST /v2/enrichment/domain-to-linkedin`, then `POST /v2/search/waterfall-icp-keyword` | The company's domain is mapped to its LinkedIn page (one record), then the page is searched by job title (one record a person). A company with no page has nobody |
| A person's work email | `POST /v2/enrichment/email` | By LinkedIn profile only. One record when found |
| A company's LinkedIn page | `POST /v2/enrichment/domain-to-linkedin` | From its domain, by itself (`data.company_page`). One record when found |
| People on a page already known | `POST /v2/search/waterfall-icp-keyword` | The search alone, when the page was looked up earlier: it is not paid for twice |
| Reading a profile | `POST /v2/enrichment/person` | The jobs a LinkedIn profile lists as current (`data.read_profile`, [connectors.md](connectors.md#reading-a-profile)). One record when found |
| A company's size | `POST /v2/enrichment/company` | The employee range and the employees on LinkedIn of a company page already known (`data.company_profile`, [connectors.md](connectors.md#a-companys-linkedin-page)). One record when found |

- **People search** needs at least one company domain, and looks at one
  company a page. Job titles, when given, are the search. Otherwise each
  seniority asked for is a step, most senior first. With neither, Blitz is
  asked for the decision makers: founders, owners and chief executives, then
  other chiefs, presidents and partners, then heads, vice presidents and
  directors. Each person comes back with their name, current title at the
  company and LinkedIn profile, so the email step can ask by profile.
- **Email** is found from the profile. Blitz has no lookup by name, so a person
  without a profile has no address from Blitz and no call is made. Blitz lists
  every address it holds for the person: only one at the company asked about,
  or a subdomain of it, counts. Someone whose only address is at another
  company has moved on, and has no address here (the record is still counted).
- An address from Blitz is never treated as verified by itself. Your verifier
  checks it like any finder's; with none connected it is *found* at confidence
  80.
- A refused key stops the step (`connector_needed`); a plan out of records, a
  locked account or a rate limit that a second try a moment later does not
  clear is `connector_quota`. One call has 30 seconds.

## Treg

A [Treg](https://treg.to) connection is one token and one prepaid balance in
front of many providers. Outflare names the provider for every call (a pinned
route, `POST https://treg.to/call/<route>`), and never uses Treg's own router
(`treg.people.email.find`): the router asked its providers inside one call,
which often ran out of time, and it stopped at the first answer, including an
address at a company the person had left.

**Finding an email** asks these routes in order and stops at the first address:

| Order | Route | Asked with | Price ceiling a call |
| --- | --- | --- | --- |
| 1 | `blitzapi.people.email.find` | the person's LinkedIn profile URL; skipped without one | $0.01 |
| 2 | `filtrat-finder.people.email.find` | first name, last name, domain | $0.01 |
| 3 | `trykitt.people.email.find` | full name, domain | $0.01 |
| 4 | `anymailfinder.people.email.find` | full name, domain | $0.03 |

- Blitz reads a LinkedIn profile, so only an address at the company asked
  about (or a subdomain of it) counts; one at another company is no address.
- An address Kitt calls `valid`, or Anymailfinder returns as its checked
  `valid_email`, is *verified* and skips your verifier. Every other address is
  the provider's best match and is checked like any finder's.
- Each call Treg bills counts as one credit against the connection's usage
  limits and a list's credit cap, so one person costs up to four credits and
  at most $0.06.
- A route that fails or gives no answer in time has no address and the next
  is asked. Each route waits up to 75 seconds, longer than the 60 seconds Treg
  itself holds a slow provider, so Treg's "still running" answer is seen (that
  route may still bill, so it counts as a credit and is not asked again). The
  whole chain has 150 seconds: with less than 20 left, the next route is not
  started. Only when no route answered at all is the lookup a failure:
  unreachable (tried again later) or out of time (no address). A refused token
  or an empty balance ends the chain at once.
- **A busy Treg is never a miss.** When Treg has no room for a call it charges
  nothing and says so: HTTP 429 `share_busy` (a provider's shared capacity is
  in use), HTTP 429 `money_admission_timeout` (Treg could not admit the call in
  its own time), a plain rate limit, or a body with `treg_saturated`. The same
  call is asked again in place, up to three times, after 2, 4 and 8 seconds (a
  shorter pause Treg names is waited instead; one longer than 8 seconds is not
  waited for), as long as the call began less than a minute ago. If it is
  still busy, the same call goes to the workspace's other Treg connection when
  it has one (two connections take turns for every call), and only then does
  the chain go on to the next route; when no other route has the address
  either, the lookup is "busy" and the person is asked about again later,
  since the busy route may have had it. A lookup Treg is still working on, or
  one that got no answer in time, is never handed to the other connection: the
  first may still finish and bill it. Any HTTP 429 from Treg is read this way:
  it is never the account's quota (an empty balance is 402), so it never stops
  a run or shows as a problem with the connection. `share_busy`,
  `money_admission_timeout` and `treg_saturated` leave the connection's pace
  alone: everyone's calls fill Treg, the pace is shared by every route, and
  slowing it would hold back the routes asked next. Any other 429 (a rate
  limit on this account) slows the pace for five minutes, like any finder's.

**Reading a profile** (`data.read_profile`) asks one more pinned route,
`fetchinio.linkedin.user.profile`, for the jobs a LinkedIn profile lists as
current ([connectors.md](connectors.md#reading-a-profile)). It is not part of
finding an email.

**Finding a profile by name** (`data.find_profile`) asks the pinned route
`crawl4ai.people.enrich` for the LinkedIn profile of a name at a company's
domain ([connectors.md](connectors.md#finding-a-profile-by-name)). A name it
has no record of is a free 404. It is not part of finding an email either.

**Verifying an address** works when the Treg connection is chosen as the
verification service (Connectors, or `PUT /v1/workspaces/{ws}/verification-settings`
with its `integration_id`). Connecting Treg does not make it the verifier by
itself: turn on "Use it to verify email addresses" when you connect it, or
pick it afterwards:

1. Filtrat checks the address and resolves accept-all domains itself
   (`filtrat.people.email.verify.resolve_catchall`). An address it passes is
   *deliverable*.
2. Anything else Filtrat says (invalid, accept-all, unknown, or no answer)
   gets BounceBan's deep check as a second opinion
   (`bounceban.people.email.verify`, then `.status` while it is still
   checking), and BounceBan's verdict is the result: *deliverable*,
   *undeliverable*, *risky* or *unknown*. Filtrat turns down some good
   addresses, and BounceBan wins those back. An address that is not confirmed
   on an accept-all domain is flagged catch-all.

Each call waits up to 75 seconds, past Treg's own 60 second hold, and one
address has four minutes: Filtrat gets 100 seconds of it, across its retries
and the workspace's Treg connections, and BounceBan the rest. A BounceBan
check that fails, is busy or is still running then is never a verdict: the
address is checked again later. A busy Filtrat (HTTP 429, asked again in place
first, then on the other connection) counts as no answer from it, so the other
verifier or BounceBan is asked. Only a refused token or an empty balance falls
back to the built-in checks, as with any verifier: a busy provider never does,
because the built-in checks cannot confirm a mailbox and would store a good
address as risky.

**Two Treg connections.** A workspace with more than one connected Treg
account uses them all to verify, whichever is chosen as the verification
service: the accounts take turns for each call, and one that is refused, out
of balance or still busy after its own retries hands the same call to the
next at once. BounceBan's check is asked about on the account that took it.
Each address is booked, and stored, on the connection whose answer decided
(`email.verifier_handed_over` when that was not the chosen one). A connection
at its usage limit is left out. Only when every account refuses is the check
the refusal it is.

**When Filtrat is unavailable** (its call fails, or Treg is still waiting for
it) and the workspace has another verification service connected, that one is
asked before BounceBan: the oldest other connected verifier that serves the
brand, on its own key. Another Treg connection is not that verifier (it is the
same service, asked already): with Treg and Bouncer connected, Bouncer is
asked when Filtrat has no room on any Treg account. A real answer from Filtrat, whatever it says, never
asks it.

| The other verifier says | Result |
| --- | --- |
| undeliverable | *undeliverable* |
| deliverable, and that the domain does not accept every address | *deliverable* |
| risky, and that the domain does not accept every address | *risky* |
| deliverable or risky on an accept-all domain, or without saying the domain is not one | BounceBan's deep check decides, as above |
| unknown, or it fails | as without it: BounceBan decides alone |

Most verifiers only flag a domain that accepts everything and say nothing
otherwise, so only one that says outright that the domain is not accept-all
counts as confirming it: Bouncer (`domain.acceptAll` is `no`; `unknown` says
neither), Reoon, Emailable and Hunter. Each address the other verifier
answers is one credit on its own connection, and the result is still stored as
the Treg connection's. A refused key or an empty balance there shows on that
connection, and it is not asked again in the batch.

## Asking one step at a time

The pipeline's email step asks whole providers and stops at the first address
any of them returns. A feature that wants to judge and verify an address
itself before paying for the next lookup asks one *step* instead: one lookup
at one source. It is for extensions (`EmailFind().FinderSteps` and
`AskFinderStep`); there is no API endpoint for it.

- **The steps** are those of the workspace's own connected finders that can be
  asked this way: Blitz has one (`blitz`, by LinkedIn profile), Treg has four
  (`blitz`, `filtrat`, `kitt`, `anymailfinder`: its routes, one a call, with
  the same requests and price ceilings as above). They are listed in the order
  to ask them, the connector that reads only a profile first, so with Blitz
  and Treg connected: `blitz`, `filtrat`, `kitt`, `anymailfinder`. A step two
  connectors offer (`blitz`) names both, Blitz itself first.
- **One step returns every address** the provider offered for the person, best
  first, not filtered by company and not checked. For `blitz` that is every
  address on the profile, including ones at jobs the person has left; the
  caller decides which, if any, is theirs.
- **A miss is final for the step.** The step's first connector is asked. Its
  connections take turns, and one that cannot take the call (a refused key, no
  credits left, a usage limit, busy, or waiting a moment for its own turn)
  hands the same call to the connector's next connection at once. The next
  connector that offers the same step is asked only when the first could not
  answer: refused, unreachable, busy, or its own pace asking for a wait of
  more than a few seconds. An answer with no address does not ask it.
- **A busy provider is not an answer.** When the provider behind the step has
  no room for the call (Treg's HTTP 429), the result is `pending` with `busy`
  set and `retry_after`: nothing was charged, nothing is remembered, and
  nothing is known about the person from that step. The caller may ask its
  other steps meanwhile and asks this one again later; it must never read it
  as "no address".
- **Nothing is paid twice.** An answer is remembered for 90 days per connector,
  apart from what the whole provider said. A lookup the provider was still
  working on (Treg's "still running"), or that got no answer in time, may still
  be billed, so it is remembered as a miss and never asked again.
- **A step that doesn't apply calls nobody**: `blitz` without a LinkedIn
  profile, or the others without a first name, last name and company domain. A
  test workspace never calls a provider.
- Each call keeps the connection's usage limits and pace, books its credits in
  the spend ledger ("Email finder lookup") and records `email.finder_asked` or
  `email.finder_failed` with the `step`.

The result has no code for an answer (a hit or a miss), or `pending` (wait
`retry_after`; with `busy` when a provider had no room rather than our own
pace), `retry` (every connector failed for now) or `stop` with
`connector_quota`, `connector_needed` or `usage_limit_reached` (every
connector refused, or none offers the step).

## Result codes

Finding and verifying return a code per address so a pipeline can decide what
to do next:

| Code | Meaning | Next step |
| --- | --- | --- |
| `verified` | Proven deliverable (first-party history, your verifier, or a finder that checked the mailbox). | use it |
| `found` | A confident guess from the pattern, or your connected finder's answer. | use it |
| `risky` | Catch-all or low confidence. | your policy |
| `not_found` | No address could be built. | skip |
| `undeliverable` | Known bad. | skip |
| `pending` | A slow check is running, or your finder is rate limited. | re-check shortly |
| `retry` | A transient failure. | retry later |
| `stop` | Can't continue (a finder or verifier refused the key, out of credits). | fix and resume |

## Calling it

Three endpoints, all scoped to one workspace:

- `GET /v1/workspaces/{ws}/email-finder/pattern?domain=acme.example` — the
  domain's learned shape and a confidence. Free. (`leads.read`)
- `POST /v1/workspaces/{ws}/email-finder/find` — a batch of people (name +
  company domain) to propose addresses for, up to 50. Free. (`leads.read`)
  It uses the free tiers only and never asks your connected finder; the
  pipeline step (`find_email`, [pipeline.md](pipeline.md)) runs every tier.
  An item may carry the person's `linkedin_url`; the free tiers ignore it,
  and the pipeline step uses it as [above](#which-finder-is-asked-first).
- `POST /v1/workspaces/{ws}/email-finder/verify` — a batch of addresses to run
  through the waterfall, up to 50. Spends only at the last tier, on your own
  connected verifier. (`leads.manage`)

Each result carries the caller's own `ref` echoed back, so match by that rather
than by position. Through MCP the same three are `email_pattern`, `find_emails`
and `verify_addresses`.

## What it never does

- It never shares one workspace's addresses, outcomes or patterns with another.
- It never spends on a third-party finder or verifier when a free tier already
  has the answer.
- It never uses an Outflare-held key, or another workspace's connection, to
  find or verify an address: the third-party tiers are always your own
  connected accounts.
