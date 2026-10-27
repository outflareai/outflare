# Paid media

Outflare reads what your ad accounts spend and what the platforms say it
brought in, so a person, a script or an agent can ask "what did ads cost this
month, per brand, and what did they return?" without opening six dashboards.
This first part only reads: nothing is changed at the platform.

Every connection is the workspace's own: it signs in to its own ad accounts and,
where the platform needs one, adds its own developer token. Outflare never
provides an ad account, an API token or a developer token, and an ad platform
stays off until the workspace connects it.

## Platforms

| Platform | Connect with | Also needs |
| --- | --- | --- |
| Google Ads (`google_ads`) | Sign in with Google (read-only Google Ads access). Manager accounts list the accounts under them. | The workspace's own developer token, from its Google Ads manager account (API Center). |
| Meta Ads (`meta_ads`) | A system user access token from your own Business Manager with `ads_read`, or sign in with Facebook | Nothing |
| Microsoft Advertising (`microsoft_ads`) | Sign in with Microsoft | The workspace's own developer token, from the Microsoft Advertising Developer Portal. |
| Reddit Ads (`reddit_ads`) | Sign in with Reddit (read-only ads access) | Nothing |
| X Ads (`x_ads`) | Your own X developer app's API key and secret, plus an access token and secret for the account that runs the ads | Ads API access approved for that app by X |
| LinkedIn Ads (`linkedin_ads`) | Sign in with LinkedIn (`r_ads`, `r_ads_reporting`) | Advertising API access approved for the app by LinkedIn |

Signing in uses the operator's OAuth app (`CONNECTOR_<ID>_CLIENT_ID` and
`_CLIENT_SECRET`, see [connectors.md](connectors.md)); the tokens it produces
belong to the workspace's own ad account. When a connection still needs a
developer token, `GET /v1/workspaces/{ws}/ads/connections` lists it under
`setup_needed`, and a person adds it in Integrations
(`PUT /v1/workspaces/{ws}/ads/connections/{id}/setup`). Outflare checks the
token against the platform before storing it, sealed like every other
credential.

## What is stored

A sync runs every six hours. The first one reads 90 days; later runs read the
last 7 days, and the last 30 once a day, because platforms keep revising recent
numbers. Each run stores:

- **Ad accounts**, with currency and time zone. Each account can belong to a
  brand (`PATCH /v1/workspaces/{ws}/ads/accounts/{id}`) or be shared, and can be
  switched off.
- **Campaigns**, with status, objective and daily budget.
- **Daily campaign metrics**: spend, impressions, clicks, and the platform's own
  conversions and conversion value where it reports them. Days are the account's
  own days, in its time zone.
- **Spend in the costs ledger**: one entry per campaign per day, channel
  `paid_ads`, in the account's currency and brand, so paid ads sit beside every
  other cost and outcome. An entry is updated when the platform revises the day.

Conversions and ROAS here are **platform-reported**: each platform's own count,
with its own attribution window. They are not Outflare's attribution, and they
are not added up across platforms as if they were.

## Reading it

In the app, open **Paid ads** in the sidebar: spend, clicks, CTR, cost per click and platform conversions for 7, 28 or 90 days, by platform and campaign, with each account's brand and sync switch. A connection that still needs a developer token asks for it at the top.

| Call | What it answers |
| --- | --- |
| `GET /v1/workspaces/{ws}/ads/accounts` | Every account, its brand and sync status |
| `GET /v1/workspaces/{ws}/ads/campaigns` | Campaigns ranked by spend over `from`–`to`, with CPC, CTR, cost per conversion and platform ROAS |
| `GET /v1/workspaces/{ws}/ads/summary` | Totals grouped by `connector`, `brand`, `account` or `day`, per currency |
| `POST /v1/workspaces/{ws}/ads/sync` | Sync now instead of within six hours |

All of them take `brand`. Money is in micros of the account's currency
(1,000,000 = one unit); totals are given per currency, never converted. The MCP
server has a tool for each (`list_ad_accounts`, `list_ad_campaigns`,
`get_ads_summary`, `sync_ads`, `list_ad_connections`, `update_ad_account`).

## Coming next

Sending conversions from Outflare's own customer events back to the platforms,
syncing audiences from segments, and letting an agent change budgets behind
mission approval are planned.
