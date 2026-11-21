# Destinations

A **destination** forwards a brand's [product events](customers.md#product-events), as they arrive, to a tool the brand already uses. Events come into Outflare once (the website script, the API, an SDK) and go on from there, so each tool sees the same events without its own tracking code.

In the app: **CRM → Customer data → Events → Destinations**. Each brand has its own, at most 20.

| Kind | What it does | What it needs |
| --- | --- | --- |
| `webhook` | Posts each event as JSON to your URL, signed like [webhooks](https://outflare.ai/docs/api/webhooks/). | An https URL. The signing secret is shown once when you add it. |
| `slack` | Posts a line in a channel: the event, who did it and its first few properties. | The workspace's own Slack connection and a channel. |
| `ga4` | Sends the event to Google Analytics 4 through the Measurement Protocol. | The web data stream's measurement id (`G-…`) and its Measurement Protocol API secret, which Outflare seals and never shows again. |
| `hubspot` | Creates or updates the event's person as a HubSpot contact, by email, and sets the contact properties you choose (for example `lifecyclestage` to `customer` on `order_completed`). | The workspace's own HubSpot connection. |
| `bigquery` | Writes each event as a row of a table in your BigQuery dataset. | Your Google Cloud project, a dataset that exists, a table name, and the JSON key file of a service account of yours that may write to the dataset (the roles BigQuery Data Editor on the dataset is enough). |
| `snowflake` | Writes each event as a row of a table in your Snowflake database. | Your account identifier, a user set up for key-pair sign-in and its private key (PEM, no passphrase), the database, schema, table and warehouse, and optionally a role. |

Conversions for ad platforms (Meta's Conversions API, Google Ads and the others) are [ad conversion rules](paid-media.md), which also check ad consent; they are linked from the same page.

**Your own accounts.** Every destination runs on the workspace's own connection or key. Outflare has none of its own, so a destination without its connection or secret can't be saved, and one whose connection is removed fails with that reason until you pick another.

## Which events

`events` names the events a destination takes, by catalog name (`order_completed`). Empty sends every tracked event; page views, identifies and the rest go only when named (`page.viewed`). Only events that arrive after the destination is made are sent, and one switched off and on again starts from the moment it is switched on.

## What is sent

- **webhook:** `{"id", "type": "event", "name", "raw_name", "kind", "brand_id", "occurred_at", "received_at", "user_id", "anonymous_id", "record_id", "properties", "traits"}` with the `webhook-id`, `webhook-timestamp` and `webhook-signature` headers. Answer 2xx within 10 seconds. The person's details are not in the body: read them from the API with `record_id`.
- **ga4:** the event's name (dots become underscores, 40 characters at most) with up to 25 of its text, number and true/false properties as parameters, `client_id` from the event's anonymous id (or its user id) and `user_id` when it has one. Google takes events up to 72 hours old; older ones are skipped.
- **bigquery** and **snowflake:** one row per event with the columns `id`, `name`, `raw_name`, `kind`, `brand_id`, `occurred_at`, `received_at`, `user_id`, `anonymous_id`, `record_id`, `properties` and `traits` (the last two as JSON: `JSON` in BigQuery, `VARIANT` in Snowflake). Outflare makes the table the first time it sends (in BigQuery, split by day on `occurred_at`); a table of your own with these columns works too. Events go up to 200 at a time, about a minute after they arrive, and an event is written once: BigQuery drops a repeat by its id, and Snowflake only adds ids the table doesn't hold. The key is sealed and never shown again; the person's details are not in the row, only `record_id`.
- **hubspot:** the person's email, first and last name, then the properties you set. An event whose person has no email address is skipped.

## The log

Every event a destination takes is a row you can read: **sent**, **skipped** (with why), **failed** (the tool refused it) or still **queued**. A tool that can't be reached (no answer, a 429 or a 5xx) is tried again each minute, five times, before the event is marked failed; while it is down the destination shows since when, and the events behind it wait in order.

## Send a test event

**Send a test event** on a destination sends one made-up event named `outflare_test` to it, whether it is on or off, and shows within a few seconds whether the tool took it, or why not. The test is not stored as one of the brand's events and is not in the log. For HubSpot it only checks the connection, so no contact is made or changed. For a warehouse it also makes the table.

## The API

```http
GET    /v1/brands/{brand}/destinations
POST   /v1/brands/{brand}/destinations                  {"kind", "name", "events", "config", "integration_id", "secret"}
GET    /v1/brands/{brand}/destinations/{id}
PATCH  /v1/brands/{brand}/destinations/{id}
DELETE /v1/brands/{brand}/destinations/{id}
POST   /v1/brands/{brand}/destinations/{id}/test
GET    /v1/brands/{brand}/destinations/{id}/sends?status=failed
```

Reading needs `customers.read`, changing `customers.manage`. `config` is `{"url"}` for a webhook, `{"channel", "channel_name"}` for Slack, `{"measurement_id"}` for GA4, `{"properties": {…}}` for HubSpot, `{"project", "dataset", "table"}` for BigQuery and `{"account", "user", "database", "schema", "table", "warehouse", "role"}` for Snowflake. A destination's `test` says how its last test went. MCP: `list_destinations`, `create_destination`, `update_destination`, `delete_destination`, `test_destination`, `list_destination_sends`.
