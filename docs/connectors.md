# Connectors

Outflare is the brain of an outbound operation. Connectors are how it reaches
out to everything else: email verification, mailbox suppliers, other
sequencers, lead data, CRMs, calendars, domain registrars, DNS hosts,
warehouses and notification tools. Every connector works the same way for a
person in the dashboard, for a script through the REST API, and for an agent
through MCP.

## Concepts

| Term | What it is |
| --- | --- |
| Connector | A service Outflare can talk to, defined in code: id, name, category, how it signs in, what it can do. |
| Integration | A workspace's connected account at a connector. A workspace can connect several accounts of the same connector, for example two Instantly accounts. |
| Capability | Something a connector can do, such as `verify` or `import.campaigns`. Engine features look for connectors by capability, not by name. |
| Catalog | Every connector the engine ships, available or planned. `GET /v1/connectors` returns it without a key, so the website and agents can read it. |

Nothing has to be connected for Outflare to run: mailboxes can be added directly (IMAP/SMTP, CSV or Google/Microsoft sign-in) and verification falls back to the free built-in checks. Connectors add to it.

Credentials are sealed with the instance key (`OUTFLARE_SECRET_KEY`, AES-256-GCM, bound to the integration id) and never returned by the API.

## Categories

| Category | Examples |
| --- | --- |
| `email_verification` | Built-in checks, ZeroBounce, NeverBounce, MillionVerifier, Bouncer, Reoon, Emailable, DeBounce, Hunter |
| `mailbox_provider` | Cheap Inboxes, Mailreef, Infraforge. Without one, upload mailboxes directly (IMAP/SMTP or CSV) |
| `sequencer` | Instantly, Smartlead, EmailBison, PlusVibe, Apollo |
| `data` | Apollo, People Data Labs, MoltSets, Prospeo, LeadMagic, Findymail, Hunter (email finder). Planned: Clay, ZoomInfo, Lusha and more. See [lists.md](lists.md) |
| `signals` | Planned: Crunchbase, TheirStack, PredictLeads, UserGems, RB2B, Common Room, Trigify |
| `analytics` | Google Analytics 4, Google Search Console, Bing Webmaster Tools, PostHog, Plausible, Fathom, Matomo, Umami, Mixpanel, Amplitude, Cloudflare Web Analytics, Simple Analytics, Microsoft Clarity, Vercel Web Analytics. See [analytics.md](analytics.md) |
| `seo` | DataForSEO, Ahrefs. Paid per call, with a monthly budget. See [seo.md](seo.md). Geonode reads pages through its own network: an unknown email domain's homepage for [classification.md](classification.md), and your own site's pages when its firewall blocks OutflareBot or they need JavaScript; see [crawler.md](crawler.md). It also runs web searches (`web.search`) on your Geonode account, for finding companies to research, finds brands' social links on their websites, and reads public X follower counts ([social.md](social.md)) |
| `code` | GitHub, Vercel: production deploys of your websites, read only. See [deploys.md](deploys.md) |
| `social` | Reddit, with the workspace's own app: search, and replies a person approves from the brand's own account. X, with the workspace's own developer app, and LinkedIn, signed in to your own account: followers and your own posts' numbers, read every day. See [social.md](social.md) |
| `crm` | HubSpot, Salesforce, Pipedrive, Attio |
| `payments` | Stripe. Who pays you, read into customer records. See [Payments](#payments) |
| `calendar` | Google Calendar, Outlook, Calendly, Cal.com |
| `dns`, `registrar` | Cloudflare, Namecheap, GoDaddy, Route 53 |
| `warehouse`, `notification` | Exports and alerts |
| `model` | Claude, OpenAI, Gemini, Mistral, xAI, or a router (OpenRouter, Vercel AI Gateway, Requesty: one key for many vendors' models), with your own API key. OpenAI and xAI are tagged for subscription sign-in (ChatGPT plans, SuperGrok), which is coming; use a key for now. They plan missions from a goal, run Outflare's built-in agent and ask the questions [AI visibility](ai-visibility.md) samples. Pick one under Workspace settings → AI model |
| `custom` | A generic HTTP API or MCP server registered by the workspace |

## Connecting

People open **Connectors** in the web app, pick a card and click **Connect**. Many services offer two ways in, and the person chooses:

- **Sign in** (OAuth, recommended): approve access on the provider's own page. Nothing to copy, and it can be revoked there.
- **API key**: paste a key (or for Google, a service account key). Works on any server, including ones without an OAuth app for that provider.

When this server has no OAuth app for a provider, the dialog says so and offers the key. Keys are tested before they are saved. A signed-in connection can switch to a key later ("Use an API key instead"); pasting a key on it replaces the sign-in.

Connections that take an address (a self-hosted Matomo, Umami, Plausible, PostHog, EmailBison or Cal.com) and mailboxes' SMTP and IMAP servers only reach public internet addresses, checked on the address actually dialled, so a connection can never be pointed at the server's own network. A self-hosted server whose tools run on the same private network sets `OUTFLARE_CONNECTORS_ALLOW_PRIVATE=true`.

Every catalog entry says how it connects: `auth_methods` lists the ways, recommended first, and `auth_options` says which of them work on this server and, if not, what to set. Each connection has `auth_method` (`oauth2` or `api_key`).

| Connector | Sign in (OAuth) | API key |
| --- | --- | --- |
| Google Analytics, Search Console | Google, through the server's Google client | Service account key (JSON) |
| Google Calendar | Google | |
| Outlook Calendar | Microsoft, through the server's Microsoft client | |
| HubSpot | HubSpot app | Private app token |
| Attio | Attio app | Access token |
| PostHog | PostHog Cloud, no app to register | Personal API key (also self-hosted) |
| Calendly | Calendly app | Personal access token |
| Apollo | Apollo app | Master API key |
| Ahrefs | Ahrefs Connect app | API key |
| Cloudflare Web Analytics | Cloudflare OAuth client | API token and account id |
| Cal.com | Cal.com OAuth client (Cal.com's cloud) | API key (also self-hosted) |
| Stripe | Stripe App | Restricted key |
| OpenRouter | OpenRouter, no app to register: signing in makes a key on your own account | API key |
| X, X Ads, Reddit | Signs in through the workspace's own developer app, because X and Reddit grant and bill API access per app | The app's keys |
| Everything else | Not offered by the vendor | API key or token |

Over the API (scope `integrations.manage`):

```sh
# What can be connected
curl -s $OUTFLARE_URL/v1/connectors

# Connect with an API key: the key is tested first, and refused if wrong
curl -s -X POST $OUTFLARE_URL/v1/workspaces/$WS/integrations \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"connector": "zerobounce", "credentials": {"api_key": "..."}}'

# Or sign in instead, for connectors whose auth_methods include oauth2:
# open this in a browser (or add ?format=json to get the link)
open "$OUTFLARE_URL/v1/workspaces/$WS/integrations/oauth/hubspot/authorize"
```

| Method | Path | Does |
| --- | --- | --- |
| GET | `/v1/connectors` | Public catalog |
| GET | `/v1/workspaces/{ws}/connectors` | Catalog with how many accounts the workspace connected |
| GET, POST | `/v1/workspaces/{ws}/integrations` | List or connect |
| GET, PATCH, DELETE | `/v1/workspaces/{ws}/integrations/{id}` | Read, rename, replace the key, change config, disconnect |
| POST | `/v1/workspaces/{ws}/integrations/{id}/test` | Sign in again with the stored key |
| GET | `/v1/workspaces/{ws}/integrations/oauth/{connector}/authorize` | Start an OAuth sign-in |

Signing in needs an app registered at the provider. Set `CONNECTOR_<ID>_CLIENT_ID` and `CONNECTOR_<ID>_CLIENT_SECRET` (for example `CONNECTOR_HUBSPOT_CLIENT_ID`) and use `<PUBLIC_URL>/v1/integrations/oauth/callback` as the redirect URL. Google connectors fall back to `GOOGLE_OAUTH_CLIENT_ID`, Outlook to `MICROSOFT_OAUTH_CLIENT_ID`, PostHog needs no app at all (only a public https `PUBLIC_URL`), and neither does OpenRouter. If the app was registered with different scopes, `CONNECTOR_<ID>_SCOPES` (space separated) replaces the defaults. Without an app, connectors that also take a key still connect with the key.

| Provider | Register the app at | Variables |
| --- | --- | --- |
| HubSpot | developers.hubspot.com, Apps, Create app (scopes: `crm.objects.contacts.read`, `crm.objects.contacts.write`, `crm.objects.deals.read`, `crm.schemas.deals.read`, `oauth`) | `CONNECTOR_HUBSPOT_CLIENT_ID`, `_CLIENT_SECRET` |
| LinkedIn | linkedin.com/developers, create an app with the Community Management API product (scopes: `r_basicprofile`, `w_member_social`, `r_organization_social`, `w_organization_social`, `rw_organization_admin`) | `CONNECTOR_LINKEDIN_CLIENT_ID`, `_CLIENT_SECRET` |
| Attio | build.attio.com, create an app with read access to records, object configuration, lists, list entries, list configuration, notes, tasks and workspace members (scopes are set on the app, not the consent screen) | `CONNECTOR_ATTIO_CLIENT_ID`, `_CLIENT_SECRET` |
| Calendly | developer.calendly.com, Create a new app (scopes `users:read`, `scheduled_events:read`) | `CONNECTOR_CALENDLY_CLIENT_ID`, `_CLIENT_SECRET` |
| Apollo | Apollo, Settings, Integrations, API, OAuth registration (people search, person enrichment, email accounts) | `CONNECTOR_APOLLO_CLIENT_ID`, `_CLIENT_SECRET` |
| Ahrefs | Ahrefs, Account settings, Ahrefs Connect (production access is approved by Ahrefs) | `CONNECTOR_AHREFS_CLIENT_ID`, `_CLIENT_SECRET` |
| Cloudflare (DNS) | Cloudflare dashboard, Manage account, OAuth clients (Zone read, DNS write, refresh tokens on). Outflare asks for `offline_access zone.read dns.write`; set `CONNECTOR_CLOUDFLARE_SCOPES` if the client shows other names | `CONNECTOR_CLOUDFLARE_CLIENT_ID`, `_CLIENT_SECRET` |
| Cloudflare Web Analytics | Cloudflare dashboard, Manage account, OAuth clients (Account read, Account Analytics read, refresh tokens on) | `CONNECTOR_CLOUDFLARE_WEB_ANALYTICS_CLIENT_ID`, `_CLIENT_SECRET` |
| Cal.com | app.cal.com, Settings, Developer, OAuth clients, new client (scopes `PROFILE_READ`, `BOOKING_READ`). Cal.com reviews new clients before they work | `CONNECTOR_CAL_COM_CLIENT_ID`, `_CLIENT_SECRET` |
| Stripe | A Stripe App (`stripe apps create`) with the read permissions `customer_read`, `charge_read`, `event_read`, `product_read`, `plan_read` and `subscription_read`, `stripe_api_access_type: oauth`, public distribution and the callback in `allowed_redirect_uris`. Stripe reviews public apps. The client id is the app's `ca_...` id, the secret is the secret key of the Stripe account that owns the app, and `CONNECTOR_STRIPE_AUTH_URL` is the app's OAuth install link (Settings tab) | `CONNECTOR_STRIPE_CLIENT_ID`, `_CLIENT_SECRET`, `_AUTH_URL` |
| Google | The server's Google client (see [analytics.md](analytics.md)) | `GOOGLE_OAUTH_CLIENT_ID`, `_SECRET` |
| Microsoft | The server's Microsoft Entra app, with `Calendars.Read`, `User.Read`, `offline_access` | `MICROSOFT_OAUTH_CLIENT_ID`, `_SECRET` |

PostHog and HubSpot used to be two connectors each, one per way in (`posthog_cloud`, `hubspot_private_app`). They are one connector each now; the old ids still work in API calls, and existing connections moved over.

## DNS records without a connection (Domain Connect)

Where Outflare needs DNS records (a site's proof of ownership, a tracking domain, a sending domain's DKIM, SPF and DMARC, a cold email domain's missing SPF or DMARC), the page offers the same four ways: one click, a prompt for the person's own AI agent, a message for a developer, or the records to copy. The one-click button first checks whether the domain's DNS host supports [Domain Connect](https://www.domainconnect.org/). If it does and has taken Outflare's templates, the button sends the person to their DNS host's own screen, which lists the exact records and asks them to approve. They approve once and come back; Outflare gets no token and keeps no access. Everywhere else the button falls back to connecting Cloudflare (a sign-in or an API token), as before.

- **Templates.** One per kind of change, in `engine/internal/domainconnect/templates/` (provider id `outflare.ai`): Outflare's own (`siteverify`, `trackingdomain`), sending domains on the workspace's own provider (`ses`, `resend`, `postmark`), cold email domains on its own mail host (`googleworkspace`, `microsoft365`), and single records other services ask for (`dmarc`, `spf`, `dkim`, `dkimcname`, `servicecname`). The one-click button uses the first two groups and `dmarc` and `spf` (a cold email domain missing one of them) today; the others are there so Mailboxes and connectors can offer the same approval without a new round of onboarding at each DNS host. They are generated from `templates.go` (`go test ./engine/internal/domainconnect/ -run TestTemplateFiles -update`) and submitted to the public [Domain Connect templates](https://github.com/Domain-Connect/Templates) repository; each DNS host onboards them on its own schedule.
- **Signed links.** Every link is signed. The server makes its signing key on first use and keeps it sealed in the database (`domain_connect_keys`, resealed with the rest on a key rotation). `GET /v1/domain-connect` shows its public key as the TXT records to publish at `_dck1.outflare.ai`; the approval is offered only once the published key matches. A self-hosted server's key isn't published there, so it always uses the fallback.
- **API.** `GET .../seo-sites/{site}/dns/approval`, `.../tracking-domains/{id}/dns/approval` and `.../email-provider-domains/{id}/dns/approval` give the link, or `available: false` with a `reason` (`not_configured`, `no_provider`, `not_onboarded`, `no_template`, `nothing_to_add`, `lookup_failed`). After the DNS host sends the person back, the web app reports the result to the matching `.../dns/approved`, which checks the records the usual way.

## Brands

In a workspace with [brands](../README.md#brands), a tool is connected once and shared, and a brand with an account of its own can still have it:

- **A workspace connection** (`brand_id` null) is used by every brand, or only by the brands in `brand_ids`. One Google sign-in covers Analytics and Search Console for every brand, and each brand reads its own properties on it ([analytics.md](analytics.md)).
- **A brand's own connection** (`brand_id` set) is used by that brand alone, in place of the workspace's connections to the same tool. Repocket can have its own Plausible while every other brand keeps the company's.

`GET /v1/workspaces/{ws}/integrations?brand=repocket` lists what a brand uses: its own connections and the shared ones, minus any shared connection to a tool it has its own of. `brand=none` lists the connections every brand shares. Set who uses a connection with `POST /integrations` or `PATCH /integrations/{id}` (`brand_id`, `brand_ids`), or `?brand=` on the OAuth `authorize` link; moving a connection to a brand stops sharing it. In the app, **Connectors** shows who uses each connection, and lists what the brand picked in the sidebar uses. Someone kept to some brands sees only the connections their brands use. The SQL function `integration_serves(integration, brand)` gives the same answer to queries.

An analytics site read through a brand's own connection counts for that brand. Sites read through a workspace connection go to the brand whose domains include theirs, among the brands it is shared with.

## Letting an agent connect things

The end goal is that you tell an agent what you want and it sets everything up. An agent can find any connector and connect it, but it should never ask you to paste a key into a chat. So it asks for a **connect request** instead:

1. The agent calls `request_connection` (MCP) or `POST /v1/workspaces/{ws}/connect-requests` with the connector and one sentence on why.
2. It gets back a one-time link (`/connect#ofc_...`) and gives it to you.
3. You open the link and sign in to Outflare. Only an owner or admin of the workspace that asked can use it; anyone else is told the link isn't for them and sees nothing about the workspace. The page says which workspace and service it is for, who asked (the email of the person behind the agent) and why. You paste the key, or click through the provider's sign-in for OAuth connectors. Only connect when you expected the request: Outflare never asks for keys in an email or a chat.
4. The key is tested, sealed and saved. The agent sees the request turn `connected` (`get_connection_request`) and carries on.

Links work once, expire after 24 hours by default (10 minutes to 7 days), can be cancelled, and can only connect the one connector they name. The audit log records the connection under the token that asked for it. A verification connector connected this way becomes the workspace's verification service unless the request says otherwise.

| Method | Path | Does |
| --- | --- | --- |
| GET, POST | `/v1/workspaces/{ws}/connect-requests` | List, or ask for a connection and get the link |
| GET, DELETE | `/v1/workspaces/{ws}/connect-requests/{id}` | Check status, or cancel |
| POST | `/v1/connect/lookup`, `/v1/connect/complete`, `/v1/connect/oauth` | Used by the link's page: a signed-in owner or admin of the workspace, with the link's token |

## Switching from another sequencer

Connectors with the `import.campaigns` capability (Instantly, Smartlead) implement `connectors.Importer`. `GET /v1/workspaces/{ws}/integrations/{id}/import-preview` reads the account live; `POST /v1/workspaces/{ws}/integrations/{id}/imports` runs an import in the background, and `GET /v1/workspaces/{ws}/imports/{id}` shows what it did. Campaigns become draft sequences, leads keep their step and mailbox, bounced and unsubscribed leads are suppressed, and sending accounts are matched by address (the ones still to connect are listed). Re-running updates the same sequences, tracked in `import_items`.

## Payments

Connectors with the `payments.read` capability (Stripe) implement `connectors.PaymentReader`. They only read: Outflare never charges, refunds or changes anything in the billing account, and never holds money.

- **Connect.** Create a restricted key in Stripe (Developers, API keys, Create restricted key) with Read access to Customers, Charges, Events, Products, Prices and Subscriptions, and nothing else, then connect it on the Connectors page. A test-mode key reads test data.
- **Sync.** Every connected payment account is read every five minutes. The first sync takes Stripe's change cursor, then lists every customer, subscription, payment and refund; later syncs read only Stripe's events since the last one. A run stops after about 90 seconds and carries on next time. If Stripe has forgotten the cursor (it keeps events for 30 days), the next run lists everything again, without doubling anything. `GET /v1/workspaces/{ws}/integrations/{id}/payment-sync` (scope `integrations.read`, MCP `get_payment_sync`) shows the phase, totals and last error; `POST` on the same path (scope `integrations.manage`, MCP `run_payment_sync`) syncs now. A key Stripe refuses marks the connection as needing attention.
- **Customers become people.** Each Stripe customer is a [person record](customers.md) with a `stripe.customer` identity. A customer whose metadata has `user_id` joins the person who already has that user id. The customer's email is added unverified: a billing address never claims or merges another record, and one another person already has lists the two in Duplicates. A `user_id` another person already holds is added unverified too, the two are listed in Duplicates, and the sync's `id_conflicts` total counts it, so a wrong id in Stripe's metadata shows up.
- **Events on the timeline.** `payment.succeeded`, `payment.refunded`, `subscription.started`, `subscription.changed` and `subscription.canceled`, with `amount_minor` and `currency` (or the subscription's `mrr`), `provider` and the Stripe ids. Each is stored once however often it is read.
- **Billing fields.** `billing_plan` (the product of the person's largest live subscription), `mrr` (what active and past-due subscriptions bring in a month, before discounts and tax), `lifetime_revenue` (payments less refunds), `customer_since` (the first payment) and `billing_status` (`active`, `past_due`, `trialing` or `canceled`) are computed fields on person and company records. Outflare works them out again whenever a person's payments or subscriptions change; nobody writes them through the API. When two people are merged, their subscriptions move with them and the fields are worked out again from both. A field of your own made earlier with one of these keys stays yours, and so does an option set of your own called `billing_status`. Payments leave both alone.
- **Coming.** Sheepy (crypto payments) next, then other payment providers on the same reader.

## Moving from another CRM

Connectors with the `import.crm` capability (Attio) implement `connectors.CRMImporter`. It only reads; nothing changes in the CRM, and there is no ongoing sync.

- **Preview.** `GET /v1/workspaces/{ws}/integrations/{id}/crm-import-preview` (scope `integrations.read`, MCP `preview_crm_import`) reads the CRM live and changes nothing: every object (People, Companies, Deals and custom ones) with its fields, select options and pipeline stages, every list with its own fields, and how many records, entries, notes, tasks and members it holds. Each count stops at 5,000 and says so with `capped`; `filled` is how many of the counted records use each field.
- **Import.** In the app, **Import** on the connection under Connectors (owners and admins) shows the preview, lets you pick objects and follows the import. Through the API, `POST /v1/workspaces/{ws}/integrations/{id}/crm-imports` (scopes `catalog.manage` and `customers.manage`, MCP `start_crm_import`) copies the CRM into [customer records](customers.md) in the background, optionally only some `objects`. Follow it with `GET .../crm-imports/{import}` (MCP `get_crm_import`). One import runs per connection at a time.

What the import does:

- **Objects become record types.** People are `person` records and Companies `account` records. Deals and custom objects become record types of their own (`deal`), or the type of that name the workspace already has.
- **Fields go into the catalog.** Each CRM field becomes a field of its record type: an existing one when it already holds the same kind of value (a company's industry goes into the built-in industry field, matched by option label), otherwise a new one. Select and status choices become option lists (deal stages become `deal_stage`). Name, email, phone and domain fields become the record's name and identities; a person's company becomes `companies`, job title `job_title`, LinkedIn `linkedin_url`, and a location also fills `city` and `country`. Fields the CRM works out itself (interactions, connection strength) and a company's team (the other side of each person's companies) are left out, and the summary says so.
- **Members.** Owner and other member fields point at the Outflare member with the same email; CRM members who aren't in the workspace are left out.
- **Matching, not duplicating.** A person whose email, or a company whose domain, a record here already holds verified is filled in instead of added. Emails and phones from a CRM come in unverified, since nobody proved who owns them.
- **Precedence.** Values come in as CRM data (`source: crm_sync`), so they never overwrite what someone typed in Outflare or what the workspace's own product sent, and they win over enrichment.
- **Safe to run again.** Every record keeps its CRM id as an identity (`attio.record`), so running the import again, such as on switch-over day, updates the same records and adds only new ones. A record another record points at that the CRM did not return gets an empty placeholder, filled in when its object is imported.
- **Large CRMs.** It reads 500 records at a time and works in steps of about 90 seconds, saving where it got to after each page, so a retry picks up where it stopped.
- **Not yet.** Lists and their entries, notes, tasks and comments stay in the CRM until Outflare's CRM has lists, notes and tasks; the preview counts them.

## Live updates (webhooks)

Some tools can tell Outflare when something happens, instead of Outflare asking them again and again. A connection that can do this shows **Live updates** on its page in Connectors.

- **Turning it on** gives the connection its own address, `https://<your server>/v1/hooks/ofh_...`. For tools Outflare can set up itself (Geonode), it adds the address in your account on the tool for you, using the connection's own key, and removes it again when you turn live updates off or disconnect. For other tools you copy the address into the tool, and paste its signing secret back if it has one.
- **Every delivery is checked.** With a signing secret, a delivery whose signature doesn't match is refused. Without one, the address itself is the secret: keep it private, and use **New address** if it leaks (the old one stops working straight away).
- **Repeats are ignored.** A tool that sends the same event twice gets it handled once.
- **Events are handled by workers**, with retries, so the address answers at once. The last 90 days are listed on the connection and under Logs → Incoming webhooks; the bodies are kept for 30 days, so a failed event can be **run again**. Events nothing in Outflare uses yet are kept as "logged".
- **The server has to be reachable.** Live updates need `PUBLIC_URL` to be the server's public https address; a server on `localhost` or a private network can't receive webhooks.

Geonode sends `extract_completed`, `batch_completed` and `crawl_completed` when a scrape job finishes. Outflare keeps the job id, status and counts, never page content.

API: `GET`, `POST` (turn on) and `DELETE` (turn off) `/v1/workspaces/{ws}/integrations/{id}/hook`, `POST .../hook/rotate`, `PUT .../hook/signing-secret`, `GET /v1/workspaces/{ws}/hook-events` and `POST .../hook-events/{event}/replay`. A connector supports live updates when it lists the `hooks.receive` capability; [Writing a connector](#writing-a-connector) explains `connectors.Hooked`.

## Writing a connector

1. Implement `connectors.Connector` in a package under `engine/internal/connectors/` (one package per service, or one per category for small ones like `verifiers`):

   ```go
   type Connector interface {
       Definition() Definition
       // Signs in and returns the account. Must not change anything or spend credits.
       Test(ctx context.Context, c *Client, creds Creds) (Account, error)
   }
   ```

2. Implement the capability interfaces it supports, for example `connectors.Verifier`, and list the capabilities in its `Definition`.
3. Make every HTTP call through the `*connectors.Client` you are given, with the base URL from `c.Base("<id>", "<default>")`, so tests can point it at a fake.
4. Return `connectors.Errf(kind, ...)` errors: `auth` (wrong or revoked key, marks the integration broken), `quota` (out of credits, rate limited), `unavailable` (down, retry later) or `invalid`.
5. Add it to `catalog.Available()` in `engine/internal/connectors/catalog`, and remove its placeholder from `planned()` if it had one.
6. Use it from engine code with `Store.Open(ctx, q, workspaceID, integrationID)`, which returns the connector, its credentials (refreshed if it is OAuth) and the client. Never unseal credentials yourself.
7. To react when someone connects or disconnects, append to `Store.OnConnect` / `Store.OnDisconnect`; the hooks run in the same transaction.
8. To receive webhooks (live updates), implement `connectors.Hooked` (`HookSetup`, `VerifyHook`, `ParseHook`; `connectors.VerifyHMAC` checks the usual signature formats) and list `connectors.CapHooks`. If the tool's API can add and remove webhooks, implement `connectors.HookRegistrar` too, and Outflare sets the address up for the workspace. To act on an event, register a handler with `inbound.Service.On(connector, handler)`; events with no handler are logged.

## Email verification

Every lead's address is checked before its first email. Undeliverable addresses are never emailed. Each workspace picks a verification connector (the free built-in checks by default) and decides whether risky, catch-all and unknown addresses are blocked too.

| Method | Path | Does |
| --- | --- | --- |
| GET, PUT | `/v1/workspaces/{ws}/verification-settings` | Which connector, verify on import, hold the first email until verified, what to block |
| POST | `/v1/workspaces/{ws}/verifications` | Queue addresses, a lead list, or every unverified lead |
| GET | `/v1/workspaces/{ws}/verifications` | Results, filtered by status or address |
| GET | `/v1/workspaces/{ws}/verifications/summary` | Counts by status and how many are waiting |

All providers' answers are mapped to four statuses: `deliverable`, `undeliverable`, `risky` and `unknown`, with `catch_all`, `disposable`, `role` and `free` flags and the provider's own words in `provider_status`. If the connected service refuses the key or runs out of credits, verification falls back to the built-in checks so sending is not held up, and the connection shows the error.

Engine code verifies with `verify.Enqueue` (queued, in a transaction) or `verify.Service.Verify` (now, in a job).

## Mailbox suppliers

A mailbox supplier (Cheap Inboxes today) sells Google Workspace and Microsoft 365 mailboxes. Its connector implements `connectors.MailboxVendor` and declares the capability `vendor.mailboxes`; everything else is shared, in `engine/internal/vendorsync`:

```go
type MailboxVendor interface {
    Connector
    ListMailboxes(ctx, c, creds, VendorPage) ([]VendorMailbox, nextCursor string, error)
    GetMailbox(ctx, c, creds, externalID) (VendorMailbox, error)
    MailboxCredentials(ctx, c, creds, externalID) (VendorCredentials, error) // ErrInvalid until it is active
}
// Optional:
type AppPasswordVendor interface { RequestAppPassword; AppPasswordStatus; AppPasswordRate() int } // Google app passwords
type WebhookVendor interface { RegisterWebhook; DeleteWebhook; ParseWebhook }                // push instead of poll
```

Statuses are mapped to `provisioning`, `active`, `cancelling`, `cancelled` and `failed`. `vendorsync` then:

- keeps `vendor_mailboxes`, every mailbox the supplier holds, refreshed every 15 minutes and on verified webhooks (deduplicated by event id);
- imports chosen mailboxes, or all of them with `auto_import`, through `mailbox.Service.Import`, the same path and checks as a CSV import, with `source` set to the connector id;
- for Google mailboxes asks the supplier for app passwords first, claiming each one before asking so two workers never ask twice, within the supplier's hourly rate and three at a time;
- imports mailboxes still being set up as soon as they turn active, and pauses imported ones the supplier cancels;
- registers the webhook on connect when `PUBLIC_URL` is https, and deletes it on disconnect.

| Method | Path | Does |
| --- | --- | --- |
| GET | `/v1/workspaces/{ws}/integrations/{id}/mailboxes` | The supplier's mailboxes with `import_state`, filters and counts |
| POST | `/v1/workspaces/{ws}/integrations/{id}/mailboxes/import` | Import `{"all": true}`, or by `emails` or `external_ids` |
| POST | `/v1/workspaces/{ws}/integrations/{id}/mailboxes/sync` | Re-read the supplier now |
| GET, PATCH | `/v1/workspaces/{ws}/integrations/{id}/mailbox-sync` | `auto_import`, `app_passwords`, `daily_limit` |
| POST | `/v1/integrations/{id}/webhook` | The supplier's events, HMAC-checked |

MCP tools: `list_supplier_mailboxes`, `import_supplier_mailboxes`, `sync_supplier_mailboxes`, `update_supplier_sync`.

The Cheap Inboxes package also implements `infra.Supplier` (`cheapinboxes.Supplier`, registered at start-up), so an infrastructure plan can buy on the customer's own Cheap Inboxes account:

- `Quote` checks each domain with Cheap Inboxes' domain search, then prices the free ones with their mailboxes (`POST /v1/orders/quote`). It changes nothing.
- `Order` checks out once, with the plan id as the `Idempotency-Key`, so a retry after a crash replays the first order instead of buying twice. It never retries checkout itself, not even on a rate limit. Domains the workspace already owns are sent as `isExisting`, so only their mailboxes are bought.
- `OrderStatus` reports each domain and mailbox as pending, done or failed, with a reason a person can read. A mailbox is done when it is active at Cheap Inboxes; the import above then brings it in, waiting on a Google app password where needed.
- `Cancel` schedules a mailbox, found by address, to end with its billing period.

Only an approved plan calls `Order`, because it charges the card on file at Cheap Inboxes.

### Receiving pushes: the EmailBison-compatible API

Suppliers push mailboxes to sequencers through the sequencer's API. Cheap Inboxes, for one, takes an EmailBison API key and an optional base URL for a self-hosted EmailBison. Outflare answers that API at `/emailbison/{ws}/api/...` (`engine/internal/api/emailbison_compat.go`), authenticated by an Outflare API token:

| Call | What Outflare does |
| --- | --- |
| `GET users` | The workspace, as the EmailBison user and team |
| `GET sender-emails`, `GET sender-emails/{id or email}` | Mailboxes as senders, paginated like EmailBison (`page`, `per_page`, `search`) |
| `POST sender-emails/imap-smtp`, `POST sender-emails/bulk` | Imports mailboxes (source `emailbison_api`), then verifies them like any import |
| `PATCH sender-emails/{id}` | Daily limit and name; a new password re-imports |
| `DELETE sender-emails/{id}` | Pauses the mailbox, keeping its history |
| `PATCH warmup/sender-emails/enable` and `disable` | Turns warm-up on or off |

Sender ids are numbers, as in EmailBison; each mailbox keeps its number in `emailbison_compat_ids`, and responses also carry `outflare_id`. Anything else answers 404 in EmailBison's error shape. Every call goes into `emailbison_compat_calls` (method, path, field names, status, never values), readable at `GET /v1/workspaces/{ws}/emailbison-compat/calls`, so the first real push shows which endpoints are missing.

A supplier connector that implements `connectors.PushVendor` (Cheap Inboxes does, in `cheapinboxes/push.go`) can also be set up from Outflare, so nobody copies a URL and token by hand. `PATCH .../mailbox-sync {"push": true}` mints a token with `mailboxes.read` and `mailboxes.manage` (the caller needs those and `integrations.manage`), seals it in `vendor_syncs`, and queues `vendor.push_connect`. That job calls `ConnectPush` once, never retried, since a second try could add a second connection. `POST .../mailboxes/push` queues `vendor.push`, which calls `PushMailboxes` (link, then sync, 399 at a time). Turning push off, or disconnecting the supplier, revokes the token and calls `DisconnectPush`. The state is `push` in `GET .../mailbox-sync`: `available`, `state` (off, connecting, on, failed), `base_url`, `last_result` and `error`.

## Calendars and CRMs (outcomes)

Calendar and CRM connectors feed the Outcomes page and `GET /v1/workspaces/{ws}/outcomes/report`. A connection with the `meetings.sync` capability is read every 15 minutes, or straight away with `POST /v1/workspaces/{ws}/outcomes/syncs/{integration_id}/run`. Only events with a lead among the attendees are stored; the rest of a calendar is never copied.

| Connector | Connect with | Reads | Writes |
| --- | --- | --- | --- |
| `google_calendar` | OAuth, `calendar.events.readonly` | Meetings (incremental, with Google's sync token) | Nothing |
| `outlook_calendar` | OAuth, `Calendars.Read` | Meetings (Microsoft Graph delta) | Nothing |
| `calendly` | OAuth, or a personal access token | Scheduled events and invitees | Nothing |
| `cal_com` | OAuth, or an API key (with an optional `base_url` for self-hosted Cal.com) | Accepted bookings | Nothing |
| `hubspot` | OAuth, or a private app token | Meetings with their outcome, deals with stage and amount | Booked meetings, when `push_meetings` is on |

Settings on each connection, sent as `config` with `PATCH /v1/workspaces/{ws}/integrations/{id}`:

- `own_domains`: your own email domains, so colleagues on an invite are never mistaken for prospects.
- `calendar_id` (Google, Outlook): which calendar to read. The primary calendar by default.
- `push_meetings` (HubSpot): write meetings booked from outreach into HubSpot, upserting the contact by email. Off by default.
- `paused`: stop syncing without disconnecting.

Google Calendar and Outlook use the server's Google and Microsoft clients unless `CONNECTOR_GOOGLE_CALENDAR_CLIENT_ID` or `CONNECTOR_OUTLOOK_CALENDAR_CLIENT_ID` (with `_CLIENT_SECRET`) override them. HubSpot, Calendly and Cal.com sign-in need their own app (table above); their keys need none.

A new source implements `outcomes.MeetingSource` (and `outcomes.DealSource` or `outcomes.CRMWriter` for a CRM) on its connector and declares the matching capability; the syncer, cursors, attribution and de-duplication come for free. Engine code records spend with `outcomes.Record`, in the same transaction as the work that cost money, with a `ref` so a retry never counts it twice.
