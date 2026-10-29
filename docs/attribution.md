# Attribution

Attribution works out which channel brought each website visit, and which
visits get the credit for each sign-up and payment. Every report, channel
and ad reads the same credits, so the numbers add up the same way
everywhere. It lives under **Analytics → Channels**, and in the API, MCP and
SDKs. Nothing here calls a third-party service: it runs inside Outflare on
the visits the website tracking snippet records ([website-tracking.md](website-tracking.md))
and the customer events the workspace sends or syncs ([customers.md](customers.md)).

## Each visit's channel

A background run gives every visit (`site_sessions`) a channel, source and
campaign within a minute or two, and stores what decided it in `tag_method`.
The first rule that matches wins:

| Order | What the visit arrived with | Channel | `tag_method` |
| --- | --- | --- | --- |
| 1 | An `ofc` code from a [campaign link](campaign-links.md) | the campaign's channel, source and slug | `ofc` |
| 2 | A partner's `ref` code | `affiliates`, source `partner` | `ref` |
| 3 | An ad click id: `gclid`, `gbraid`, `wbraid`, `dclid` (Google), `msclkid` (Microsoft), `li_fat_id` (LinkedIn), `rdt_cid` (Reddit), `twclid` (X), `ttclid` (TikTok), `ScCid` (Snapchat), `epik` (Pinterest), `tblci` (Taboola), `ob_click_id` (Outbrain), `qclid` (Quora); or Impact's `irclickid`, which is a partner click | `paid_ads`, or `affiliates` for `irclickid` | `click_id` |
| 4 | UTM tags: `utm_medium` decides the channel (`cpc`, `paid_social`, `display` are paid ads; `email` is lifecycle email, or outbound email from a sequencer; `social` is organic social or Reddit; `affiliate`, `referral`, `organic`, `ai`), otherwise the source does | from the tags | `utm` |
| 5 | A referrer that isn't one of the workspace's own sites: AI assistants (ChatGPT, Perplexity, Gemini, Claude, Copilot), Reddit, search engines, social networks, else referral | from the referrer | `referrer` |
| 6 | Nothing | `direct` | `none` |

`fbclid` is not an ad click id: Facebook adds it to every link, paid or not,
so Meta ads need UTM tags, a campaign link or a [tracking template](website-tracking.md#ad-tracking-templates).
The list lives in `engine/internal/adtrack`. Webmail referrers are ignored.

## Conversions and credit

A conversion is a customer event the workspace counts: `signed_up` and
`payment.succeeded` unless changed. A business that sells on calls adds
`meeting.booked` or `meeting.held`, which every meeting tied to a person
records ([customers.md](customers.md#meetings-as-events)). A workspace's own [conversion definitions](conversions.md)
count too: an event with filters on its properties and a value, such as a
call over a minute. A few minutes after one happens (so the
visit and the identify call that tie it to a person have landed), the run
finds the visits before it, by the person's record or, before they were
identified, by the browser's anonymous id, up to the lookback (30 days unless changed) and
at most 50 visits, and stores the credit under all five models at once:

| Model | `model` | Who gets the credit |
| --- | --- | --- |
| Last non-direct touch (default) | `last_non_direct` | The last visit that wasn't direct |
| First touch | `first_touch` | The first visit |
| Linear | `linear` | Every visit equally |
| 40/20/40 | `position` | 40% the first, 40% the last, 20% shared by the rest |
| Time decay | `time_decay` | Shared, halving for every week before the conversion |

Direct visits get credit only when there is nothing else. A conversion with
no visit before it goes to direct, with `matched_by: none`.

Payments carry their value (`amount_minor` and `currency`, or Segment-style
`revenue` or `value` in major units). A `payment.refunded` with the same
`payment_id` gets the same credits as its payment with a negative value, so
revenue per channel is net of refunds.

Because every model is stored, switching the model in a report is instant
and changes nothing. Changing the lookback or which events count credits
every conversion again in the background.

## Your own models

The five models are a starting point. A workspace can make its own from any
of them (**Analytics → Channels → Attribution models**, where the model
switch lists them too;
`POST /v1/workspaces/{ws}/attribution/models` or MCP
`create_attribution_model`, scope `analytics.manage`), changing:

| Option | What it does |
| --- | --- |
| `lookback_days` | How far back a visit can be (the workspace's look-back unless set) |
| `channel_lookback_days` | A window per channel, such as `{"paid_ads": 7}`: a paid click counts for 7 days, everything else for the look-back |
| `exclude_channels` | Channels that never get credit, such as `direct` or `lifecycle_email` |
| `first_weight`, `last_weight` | For `position`: the first and last visit's shares (0.4 each by default); the middle shares the rest |
| `half_life_days` | For `time_decay`: a visit's weight halves for every this many days before the conversion (7 by default) |

```json
{"key": "paid-7-day", "name": "Paid ads 7 days", "spec": {"base": "last_non_direct", "channel_lookback_days": {"paid_ads": 7}}}
```

A conversion with no visit left after the options goes to direct.

**Versions.** A model's key names its latest version. Changing its options
(`PATCH .../attribution/models/{key}`) adds the next version and credits
every conversion again in the background, so the new version covers the
past too; a new name alone keeps the version. Reports take the key as
`model` and say which version they read (`model_version`). Archiving a
model (`DELETE`) stops its credits; the workspace's default model can't be
archived.

**Who uses which model.**

- Reports and the CMO view use the workspace's default (a built-in model or
  one of its own), with the model switch to compare others.
- Missions use the default unless the mission asks for its own model, by
  passing `model` to the reports.
- A module that must never move, such as an affiliate program's commission
  rule, pins the exact version it uses (`attribution.Pin` in
  `engine/internal/attribution`). A pinned version keeps being credited after
  the model changes or is archived, and reports can still read it as
  `key@version`, so a report change never changes what partners are owed.
  `GET .../attribution/models/{key}` lists each version's pins.
- Conversions sent back to ad networks ([paid-media.md](paid-media.md)) carry
  no attribution: every conversion goes to every network that has a rule
  for it, each network credits it by its own rules, and our reports show its
  numbers next to ours.

## Reports

- `GET /v1/workspaces/{ws}/attribution/channels`: for every channel, visits,
  credited conversions (fractional under the shared models), revenue per
  currency, and the spend recorded against it in the cost ledger. Spend with
  no channel is `unallocated_cost`.
- `GET /v1/workspaces/{ws}/attribution/campaigns?channel=`: the same per
  source and campaign.
- `GET /v1/workspaces/{ws}/attribution/conversions`: credited conversions,
  newest first, with the visits that got the credit.
- `GET` and `PATCH /v1/workspaces/{ws}/attribution/settings`: the default
  model (built in or the workspace's own), `lookback_days` and
  `conversion_events`.

Each takes `from`, `to`, `model` and `brand`. Reading needs
`analytics.read`; changing settings `analytics.manage`. Paid ads read the
same credits for ad-level results; the conversions each ad platform reports
stay separate ([paid-media.md](paid-media.md)).

Home shows the channels report's totals for the last 28 days (visits,
conversions, revenue and cost) below Needs you, once the site has visits,
for anyone with `analytics.read`.

A mission can aim at revenue, in whole US dollars. `revenue_attributed`
counts payments credited, under the default model and net of refunds, to the
campaign links of the mission's sequences. `revenue_deals` counts won deals
tagged with the mission. `revenue_referred` counts sales partners referred,
less refunds, in the partner programs of the mission's brand (every program
when it has no brand) since the mission started; imported history never
counts ([partners.md](partners.md)). Money in other currencies is left out,
never converted. Mission progress shows them as `revenue_attributed_cents`,
`revenue_deals_cents` and `revenue_referred_cents`.

The [digest](digests.md) has a Channels section: visits, conversions,
revenue and cost per currency for the period against the one before, under
the default model, and the three channels with the most conversions.

## Storage

Migrations 0230 and 0231 add `channel`, `source`, `campaign`,
`tracking_campaign_id` and `tag_method` to `site_sessions`, and the tables
`attribution_settings`, `attribution_conversions` (one row per credited
event, with its value, how it was matched and how many visits came before
it) and `attribution_credits` (one row per model and credited visit, with
its weight). Migration 0240 adds `attribution_models` (one row per version
of a workspace's model, its options in `spec`) and `attribution_model_pins`;
a workspace model's credits are in `attribution_credits` under the model
`m:` and its version's id. It also made 30 days the default look-back:
workspaces that had never changed theirs moved from 90 to 30 and were
credited again. The code is `engine/internal/attribution`.

## Events

`attribution.credited` (with `conversions`, `refunds` and `backfill`) after
a run credits anything, `attribution.run_failed` when a run fails, and the
audit actions `attribution.settings_updated`, `attribution.model_created`,
`attribution.model_updated`, `attribution.model_renamed` and
`attribution.model_archived`.

## Not built yet

- Credit goes to people, not to companies or deals, and every conversion
  counts the same whether the person was new or already known.
