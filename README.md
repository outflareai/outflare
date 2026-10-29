<h1><a href="https://outflare.ai"><picture><source media="(prefers-color-scheme: dark)" srcset="https://outflare.ai/brand/svg/outflare-lockup-white.svg"><img src="https://outflare.ai/brand/svg/outflare-lockup-black.svg" alt="Outflare" width="200" height="40"></picture></a></h1>

**[Website](https://outflare.ai)** · **[Docs](https://outflare.ai/docs/)** · [Quickstart](https://outflare.ai/docs/quickstart/) · [API reference](https://outflare.ai/docs/api/reference/) · [Agents and MCP](https://outflare.ai/docs/agents/mcp/) · [hello@outflare.ai](mailto:hello@outflare.ai)

Open-source GTM infrastructure for humans and agents, built so you can prompt an agent that connects your tools and runs your outbound, through one API that people, scripts and agents share. Read more at **[outflare.ai](https://outflare.ai)**.

The first module is a self-hostable cold-email sequencer: connect hundreds of reseller inboxes at once, run multi-step sequences across mailbox pools, and send every email through one suppression and policy check. Agents, missions and a provider marketplace build on the same engine next.

> Status: early development. Foundation, workspace access, mailbox connection, leads with suppression, sequences with sending, the inbox, sending domains, sending servers, mailbox warm-up, infrastructure plans with mailbox stages, automatic mailbox allocation (including EmailBison, Instantly and Smartlead campaigns), projects, Google and Microsoft OAuth, connectors, Cheap Inboxes, and an MCP server for agents are in place. See [docs/roadmap.md](docs/roadmap.md) for what ships in v1.

## Self-host

The full guide, with configuration, HTTPS and upgrades, is at [outflare.ai/docs/self-hosting](https://outflare.ai/docs/self-hosting/docker-compose/).

```sh
git clone https://github.com/outflareai/outflare
cd outflare
cp deploy/.env.example deploy/.env   # change POSTGRES_PASSWORD
echo "OUTFLARE_SECRET_KEY=$(openssl rand -base64 32)" >> deploy/.env
docker compose -f deploy/docker-compose.yml up -d
```

Or, once a version is released, run it without cloning or building: download `outflare-<version>.tar.gz` from [Releases](https://github.com/outflareai/outflare/releases), unpack it and follow its README. Its `docker-compose.yml` pulls `ghcr.io/outflareai/outflare:<version>`. No version has been released yet. [docs/releasing.md](docs/releasing.md) explains how releases are made.

Open http://localhost:8080. Check it with `curl localhost:8080/readyz`.

`OUTFLARE_SECRET_KEY` encrypts mailbox passwords and OAuth keys at rest. Back it up with your database: without it, stored credentials cannot be read.

To rotate it, set `OUTFLARE_SECRET_KEY_PREVIOUS` to the current key and `OUTFLARE_SECRET_KEY` to a new one (`openssl rand -base64 32`), then restart. Stored secrets are re-encrypted with the new key at start-up (the log says how many). Keep the old key listed as long as unsubscribe links in mail you already sent should keep working; once you remove it, those links stop verifying. Several old keys can be listed, comma-separated.

### Sign in with Google

People sign in with email and password. To add **Continue with Google**, create an OAuth client (type "Web application") in Google Cloud Console, set `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET`, and add `$PUBLIC_URL/v1/auth/google/callback` to its authorised redirect URIs. Sign-in asks only for `openid email profile`, which needs no Google app verification. The same client can also connect Gmail mailboxes (below), with its own redirect URI.

A Google account signs in to the Outflare account with the same Google-verified email, and stays linked to it. Google never creates an account by itself: people still need an invite, which they can accept with Google instead of choosing a password.

### Passkeys

People can sign in with a passkey (Face ID, Touch ID, Windows Hello, a phone or a security key) after adding one under **Your account**. A passkey belongs to one web address, so passkeys turn on by themselves once `PUBLIC_URL` is an `https://` address, and passkeys made for one address won't work if it changes. `PASSKEYS=off` turns them off; `PASSKEYS=on` turns them on for `http://localhost`, to try them out.

**Support access.** Under **Your account**, each person can let staff of the server's operator view their account to help them (`GET`/`PATCH /v1/me/support-access`). It is on by default. A view is read-only: requests that change anything get `403 support_view_read_only`, and the inbox and email content stay hidden (`support_view_hidden`). Each view ends after an hour and is listed for the person. The engine has no staff of its own: an extension starts views (`Accounts.StartSupportView`), so on a plain self-hosted server nobody can.

### Account emails

Outflare emails people a link to confirm their address and, when they forget it, to reset their password. These go through a transactional email service, never your sending mailboxes: set `EMAIL_FROM` and one of `POSTMARK_SERVER_TOKEN` ([Postmark](https://postmarkapp.com)), `RESEND_API_KEY` ([Resend](https://resend.com)) or `CLOUDFLARE_EMAIL_API_TOKEN` with `CLOUDFLARE_ACCOUNT_ID` ([Cloudflare Email Service](https://developers.cloudflare.com/email-service/), which needs Workers Paid) in `deploy/.env`. Without one, nothing is sent and each link is written to the server log instead, which is fine on your own machine. `GET /v1/meta` shows which is in use as `account_email`.

### Third-party keys

Every workspace brings its own keys for every third-party API, AI models and connectors alike: each one connects its own Claude, OpenAI, Geonode, Apollo or DataForSEO account on the Connectors page. A server never lends its own. At start-up the server removes vendor keys such as `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` or `GOOGLE_APPLICATION_CREDENTIALS` from its environment and logs that it ignored them, so setting one in `deploy/.env` does nothing. The only third-party settings an operator gives the server run Outflare itself: account email, OAuth app ids (people still sign in to their own accounts) and your own PostHog.

### Usage stats and updates

Once a day the server sends an anonymous report (version, platform, usage counts as ranges, connector ids; never names, emails, domains or content) and checks for new versions and security fixes, shown under **Settings → This server**. Turn it off on the first-run form, in that page, or with `OUTFLARE_TELEMETRY=off` (or `DO_NOT_TRACK=1`) in `deploy/.env`. [docs/telemetry.md](docs/telemetry.md) lists every field, and `GET /v1/instance/telemetry/preview` shows the exact report.

Want to see how people and their agents use your server? Set `OUTFLARE_EVENTS_WORKSPACE` to one of your own workspaces to keep every action, API and MCP call, and error there as product events, or `OUTFLARE_POSTHOG_KEY` to send them to your own PostHog project, with ids and error codes but never customer data. It is off by default; see [docs/product-analytics.md](docs/product-analytics.md). For large volumes, add ClickHouse and an S3-compatible bucket such as Cloudflare R2 ([docs/analytics-storage.md](docs/analytics-storage.md)), and run the app, website event collection and workers as separate services with `OUTFLARE_ROLE` ([docs/scaling.md](docs/scaling.md)).

To watch the server itself (request latency per route, the job queue, the database pool, Postgres, CPU and memory), set `OUTFLARE_METRICS_ADDR=:9091` for Prometheus metrics on a private port, and start Docker Compose with `--profile monitoring` for a ready-made Grafana dashboard and alert rules. See [docs/monitoring.md](docs/monitoring.md).

## Get started

A new workspace opens on **Get started** for its owners and admins: a full page, without the rest of the app, that sets up one brand and ends with Outflare doing something. Add your brand and its website (more brands come later), add your AI key (the page says why: Outflare has no AI of its own and never pays for yours), pick what to do first (find and email new customers, grow search traffic, see what's working, or nurture existing customers), connect the website in one step (the script on your pages, then every DNS record the brand needs, added all at once), connect only the tools that goal needs, star and follow Outflare if you like, then give it a first goal and get a plan to approve. Inviting your team waits for Home. Every step, and the whole thing, can be skipped. A step ticks itself off however it was done (on its own page, over the API or by an agent), and Home keeps one checklist of what's left, Get started's first and then the brand's own, until you hide it; ⌘K → Get started opens it again any time. Agents see the same steps in `get_context` and `get_onboarding` (`GET /v1/workspaces/{ws}/onboarding`), and pick the goal or skip steps with `update_onboarding`.

Every DNS record a brand needs (its site's proof, its sending domains' records, its tracking domain) is on one list in **Company → Brand setup → Add your DNS records** (`GET /v1/workspaces/{ws}/brands/{brand}/dns`, `get_brand_dns`). Add them all with one button through your own Cloudflare (`POST .../dns`, `add_brand_dns`), approve them on your DNS provider's own screen one group after another without coming back, or send the whole list to a developer or an AI coding tool.

## Connect mailboxes

Upload the CSV your inbox reseller gives you (Mailboxes → Import CSV) or post it to the API:

```sh
curl -X POST localhost:8080/v1/workspaces/$WS/mailboxes/import?pool=Warm \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: text/csv" --data-binary @mailboxes.csv
```

The only required columns are `email` and `password` (an app password). Common export headers are recognised, such as `Email Address`, `App Password`, `SMTP Host` and `Daily Limit`. Google Workspace and Microsoft 365 mailboxes are detected from the domain's MX records and need no host columns; for other providers add `smtp_host`, `smtp_port`, `imap_host` and `imap_port`. Each mailbox is then signed in to over SMTP and IMAP, and each domain's SPF, DKIM and DMARC are checked.

### Google Workspace and Microsoft 365 without passwords

Mailboxes can also sign in with OAuth (XOAUTH2) instead of an app password. Under Mailboxes → Google and Microsoft sign-in:

- **Admin connection, for bulk.** Add a Google service account key with [domain-wide delegation](https://support.google.com/a/answer/162106) for the scope `https://mail.google.com/`, or a Microsoft Entra app with the Exchange Online application permissions `IMAP.AccessAsApp` and `SMTP.SendAsApp` (and the app [registered in Exchange with access to the mailboxes](https://learn.microsoft.com/exchange/client-developer/legacy-protocols/how-to-authenticate-an-imap-pop-smtp-application-by-using-oauth#use-client-credentials-grant-flow-to-authenticate-smtp-imap-and-pop-connections)). Then paste the list of addresses, one per line, or post it:

  ```sh
  curl -X POST localhost:8080/v1/workspaces/$WS/oauth-connections/$CONN/mailboxes \
    -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
    -d '{"mailboxes":[{"email":"ana@acme-com.example","display_name":"Ana Smith"}],"pool":"Warm"}'
  ```

- **One mailbox at a time.** "Connect a Google mailbox" opens Google's or Microsoft's consent screen. This needs an OAuth app of your own: set `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET`, or `MICROSOFT_OAUTH_CLIENT_ID` and `MICROSOFT_OAUTH_CLIENT_SECRET`, and register `$PUBLIC_URL/v1/oauth/callback` as its redirect URI.

Keys, secrets and refresh tokens are encrypted with `OUTFLARE_SECRET_KEY` like passwords, and never returned by the API.

### From a mailbox supplier (Cheap Inboxes)

Connect your [Cheap Inboxes](https://cheapinboxes.com) account under Connectors with its API key (`ci_live_…`, from Cheap Inboxes → Integrations → API). Outflare lists every mailbox in the account, and you import the ones you want, or turn on automatic import so new mailboxes arrive on their own. Each import reads the password from Cheap Inboxes and goes through the same SMTP, IMAP and DNS checks as a CSV.

```sh
# Connect (the key is tested first), then import everything that is active
curl -X POST localhost:8080/v1/workspaces/$WS/integrations -H "Authorization: Bearer $OUTFLARE_TOKEN" \
  -H "Content-Type: application/json" -d '{"connector":"cheapinboxes","credentials":{"api_key":"ci_live_..."}}'
curl -X POST localhost:8080/v1/workspaces/$WS/integrations/$INTEGRATION/mailboxes/import -H "Authorization: Bearer $OUTFLARE_TOKEN" \
  -H "Content-Type: application/json" -d '{"all":true}'
```

Google refuses plain passwords over SMTP and IMAP, so for each Google mailbox Outflare first asks Cheap Inboxes for an app password. Cheap Inboxes makes 20 an hour, so a few hundred Google mailboxes take most of a day; follow `import_state` in `GET …/integrations/$INTEGRATION/mailboxes`. The account is re-read every 15 minutes, and straight away on Cheap Inboxes webhooks when `PUBLIC_URL` is an https address. Mailboxes cancelled at Cheap Inboxes are paused. Other suppliers plug in the same way; see [docs/connectors.md](docs/connectors.md#mailbox-suppliers).

#### Let the supplier push mailboxes in

Suppliers like Cheap Inboxes push mailboxes straight into the sequencers they support. Outflare answers the part of the EmailBison API they use, and can set the connection up for you: on the supplier's page, choose **Turn on push**, then **Push**. Outflare adds itself in the supplier as a self-hosted EmailBison, with an API token that can only read and add mailboxes.

```sh
# Turn push on (needs integrations.manage too), wait for push.state "on", then push
curl -X PATCH localhost:8080/v1/workspaces/$WS/integrations/$INTEGRATION/mailbox-sync -H "Authorization: Bearer $OUTFLARE_TOKEN" \
  -H "Content-Type: application/json" -d '{"push":true}'
curl -X POST localhost:8080/v1/workspaces/$WS/integrations/$INTEGRATION/mailboxes/push -H "Authorization: Bearer $OUTFLARE_TOKEN" \
  -H "Content-Type: application/json" -d '{"all":true}'
```

Or add it in the supplier yourself:

- **Base URL:** `https://<your Outflare host>/emailbison/<workspace id>`
- **API key:** an Outflare API token with `mailboxes.read` and `mailboxes.manage`

Pushed mailboxes go through the same SMTP, IMAP and DNS checks as any import. A supplier removing a sender pauses the mailbox here, it is never deleted. Turning push off revokes the token and removes the connection at the supplier. Cheap Inboxes also asks for a sequencer login; Outflare has no sequencer screen to sign in to, so it sends your email and a random password it never keeps. Every call is logged with field names only (never values) at `GET /v1/workspaces/$WS/emailbison-compat/calls`, so if a supplier asks for something Outflare does not answer yet, you can see exactly what. This was built from EmailBison's and Cheap Inboxes' public APIs and has not yet been tested against a live Cheap Inboxes push. It needs `PUBLIC_URL` set to an https address the supplier can reach.

## Sending domains

The Domains tab lists every domain your mailboxes send from (each one is added when its first mailbox is connected), plus any you add by hand to check before buying inboxes on it. For each domain it shows:

- **Health and what to fix.** MX, SPF, DKIM and DMARC are looked up, and the domain is checked against the Spamhaus DBL and SURBL blocklists, when it is added and again every day. Problems come back as issues, most severe first, each with the fix, and the exact DNS record to publish where the fix is one (for example `TXT _dmarc v=DMARC1; p=none`). A bounce rate over 2% in the last 7 days, or mailboxes that cannot sign in, are flagged too.
- **How it is sending.** Mailboxes on the domain, sends in the last 24 hours against its daily capacity, replies and bounces over 7 days, and sends, replies and bounces per day for 14 days.
- **Settings.** Pause the domain to stop every sequence and test send from its mailboxes at once (replies you write in the inbox still go), cap the domain's total sends per 24 hours across all its mailboxes, keep notes, and set the daily limit and minimum gap on all its mailboxes in one go.

Pausing and the domain cap are part of the policy check, so a waiting lead shows `domain_paused` or `domain_daily_limit`. Over the API:

```sh
curl localhost:8080/v1/workspaces/$WS/sending-domains -H "Authorization: Bearer $OUTFLARE_TOKEN"
curl -X PATCH localhost:8080/v1/workspaces/$WS/sending-domains/acme-com.example \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"status":"paused","daily_limit":150}'
```

Blocklists refuse lookups from public DNS resolvers such as 8.8.8.8 and 1.1.1.1, and the result then reads "couldn't check" rather than clear. To get real answers, run Outflare on a host that uses its own recursive resolver (for example Unbound).

## Sending servers (SMTP vendors and your own mail server)

Cold-email SMTP vendors such as Mailreef and Infraforge, and self-hosted mail servers (Postal, Postfix, KumoMTA), give you mailboxes that all sign in to one server and send from the same few dedicated IPs. Connect those mailboxes like any other, by CSV or API with their SMTP and IMAP details. Every mailbox outside Google and Microsoft is grouped by its SMTP host into a **sending server**, so the IPs they share can be watched and capped together:

- **IP blocklists.** The server's IPs come from its host's DNS records (or set them by hand when mail leaves from other addresses) and are checked against Spamhaus ZEN, SpamCop and Barracuda when the server is added and every day after. A listed IP pauses the server on its own, and it resumes on its own once every IP is clear. Turn that off per server with `pause_on_listing`. A pause by a person is never lifted automatically.
- **A shared daily limit.** Cap the sends from all of the server's mailboxes together per 24 hours, on top of each mailbox's and domain's own limits, to keep new IPs within what they can carry while they warm up.
- **How it is sending.** Mailboxes and domains on the server, sends against its daily capacity, replies and bounces over 7 days, and issues with the fix (listed IPs, bounce rate, mailboxes that can't sign in).

In the web app, **Deliverability → Servers** lists every server worst-first; open one to see what to fix, each IP on each blocklist with a link to request removal, its mailboxes, and its settings (name, server daily cap, sending IPs, pause when blocklisted, notes).

The server pause and cap are part of the policy check, so a waiting lead shows `server_paused` or `server_daily_limit`. Over the API (agents use `list_sending_servers` and `update_sending_server` over MCP):

```sh
curl localhost:8080/v1/workspaces/$WS/sending-servers -H "Authorization: Bearer $OUTFLARE_TOKEN"
curl -X PATCH localhost:8080/v1/workspaces/$WS/sending-servers/$SERVER \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"Mailreef box 1","daily_limit":1500}'
curl "localhost:8080/v1/workspaces/$WS/mailboxes?server_id=$SERVER" -H "Authorization: Bearer $OUTFLARE_TOKEN"
```

Mainstream sending services (SendGrid, Mailgun, Amazon SES, Postmark and the like) forbid cold email in their terms, so Outflare does not connect to them as a sending path. The same resolver note as for domains applies: Spamhaus refuses lookups from public DNS resolvers.

## Warm up mailboxes

New inboxes that send cold email straight away land in spam, so warm each one up for about two weeks first and leave it on afterwards. Outflare doesn't send warm-up mail itself: warm-up runs in a connected sequencer with its own warm-up network, EmailBison, Instantly or Smartlead, and Outflare switches it on or off there and shows the sequencer's warm-up score. Add your mailboxes to the sequencer, connect it on the Connectors page, then turn warm-up on from the Warm-up page, a mailbox's menu, or the sequencer's card on the Mailboxes page (`set_warmup` over MCP).

Turning warm-up on keeps the mailbox's sequencer, or picks a connected one it is in; a mailbox in two picks one with **Warm up in…** (`"provider": "<integration id>"` in the API). A mailbox in no connected sequencer can't be warmed up, and the API says so. A mailbox shows **Turning on in EmailBison** until the sequencer's next sync shows its warm-up on (`warmup.waiting_on` is `platform`), then the sequencer's score, which also decides when a planned mailbox is ready. A mailbox whose warm-up is already on in the sequencer when Outflare first reads it is taken as warming up there; one warming up in two sequencers is flagged **Also warming in Instantly**, and each mailbox lists which sequencers have their own warm-up on (`platforms[].warmup_enabled`). Sequencers tag their warm-up mail with a short code: paste it on the sequencer's card on the Mailboxes page (`config.warmup_tags` on the integration) and the Outflare inbox leaves that mail out, without reading or moving it, so the sequencer still does its own engagement.

```sh
# Every mailbox in a pool; leave out pool_id for all, or pass mailbox_ids
curl -X POST localhost:8080/v1/workspaces/$WS/mailboxes/warmup \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"enabled":true,"pool_id":"'$POOL'"}'
```

## Plan domains and mailboxes

"10 domains and 30 mailboxes for this project by the 14th" is one **infrastructure plan** (Infrastructure → New plan). Give it a count, a brand, who the mailboxes send as and a date; it suggests domain names that read as your brand (getacme-com.example, acmehq-com.example; cold email never goes from your main domain), names the mailboxes after your people (jane@, jane.doe@), works out when they can be ready and warns you before you approve if the date is too soon, the warm-up too short or one domain carries too many mailboxes. Preview a plan without saving it, then save a draft and approve it once.

After approval the plan builds itself. Domains and mailboxes are bought in your own accounts: no reseller sits in between and you pay your registrar and mailbox provider directly.

- **You buy them (no connection needed).** The plan lists what to buy. Mark the domains bought, or just buy them: the plan moves on when their MX records point at Google or Microsoft, waits for SPF and DKIM, then links each mailbox as soon as you connect or upload it on the Mailboxes page. Domains you already have here only get mailboxes added.
- **A supplier buys them.** With a supplier connector (Cheap Inboxes first), approving a plan whose quote you have seen places the order once, never twice, and the mailboxes are imported as they are created.

Each mailbox then has a **stage**. A new mailbox is **warming**: it sends no cold email while it warms up in your connected sequencer. It becomes **ready** when the warm-up days are done and the sequencer's warm-up score is at least 85. A ready mailbox whose score drops below the plan's floor (75 by default) **rests** for 7 days, still warming up, and comes back when the score recovers; **retired** is final and pauses the mailbox. Only ready mailboxes send cold email, and the allocator and sending engine both check. Mailboxes added any other way start ready and never move on their own; move any mailbox by hand. Every count of **warming** mailboxes (Home, Mailboxes, Domains, Servers, `GET /stats`) means this stage, and **reply rate** everywhere is people who replied out of people contacted in the last 7 days (`replied_leads_7d` / `contacted_leads_7d`), shown once at least 10 people were contacted.

```sh
# Preview: nothing is saved
curl -X POST localhost:8080/v1/workspaces/$WS/infrastructure/preview \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"domain_count":10,"mailboxes_per_domain":3,"brand":"Acme","provider":"google",
       "personas":[{"first_name":"Jane","last_name":"Doe"}],"ready_by":"2026-11-14"}'
# Save, then approve (needs the infrastructure.approve scope)
curl -X POST localhost:8080/v1/workspaces/$WS/infra-plans -H "Authorization: Bearer $OUTFLARE_TOKEN" \
  -H "Content-Type: application/json" -d '{"name":"Q4 founders", ...same fields...}'
curl -X POST localhost:8080/v1/workspaces/$WS/infra-plans/$PLAN/approve -H "Authorization: Bearer $OUTFLARE_TOKEN"
# Every mailbox by stage, and moving some by hand
curl localhost:8080/v1/workspaces/$WS/infrastructure -H "Authorization: Bearer $OUTFLARE_TOKEN"
curl -X POST localhost:8080/v1/workspaces/$WS/infrastructure/move -H "Authorization: Bearer $OUTFLARE_TOKEN" \
  -H "Content-Type: application/json" -d '{"mailbox_ids":["'$MAILBOX'"],"stage":"resting","until":"2026-11-20T00:00:00Z","reason":"Spam complaints"}'
```

Tokens need `infrastructure.read` to look, `infrastructure.manage` to draft plans and move mailboxes, and `infrastructure.approve` to approve a plan, which is what spends money. Operators get the first two; only owners and admins approve.
## Mailbox health

Every mailbox is graded ok, warning or critical from every signal the engine has: sign-in and inbox sync errors (translated, so "535 5.7.8" becomes "app password rejected" and a disabled account says it's suspended), send failures, bounce rate, the domain's SPF, DKIM, DMARC and blocklists, and new mailboxes sending without warm-up. Each problem comes with what to do. A mailbox whose bounce rate spikes (5% of at least 20 sends in 24 hours, adjustable) is paused automatically, with the reason on the mailbox and in the audit log. See it on Mailboxes, at `GET /v1/workspaces/$WS/mailbox-health`, or ask an agent (`get_mailbox_health`).

## Import leads

Upload any CSV with an email column (Leads → Import, or the API). Exports from Apollo, Clay, CRMs and spreadsheets work as they are: name, company, title, phone, LinkedIn and website columns are recognised, and every other column is kept as a custom field for templates. Leads are upserted by email, so re-importing enriches rather than duplicates. 100,000 rows import in a few seconds.

```sh
curl -X POST "localhost:8080/v1/workspaces/$WS/leads/import?list=Q4%20founders" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: text/csv" --data-binary @leads.csv
```

## Build lead lists

Search the data providers you already pay for (Apollo, People Data Labs, MoltSets; emails from Prospeo, LeadMagic, Findymail, Hunter) straight into a lead list (Lead lists → New list). Preview ten people for free, then build: emails are found through a waterfall of your providers in the order you choose, cached so nobody is paid for twice, and verified before they are added. Each list has a credit cap and a lead cap per run, a minimum fit score against the project's ideal customer profile, and leaves out anyone suppressed, already in a sequence or emailed recently. Refresh by hand or every few days to add only people who are new. Every run says what it added, what it left out and why, and what it cost; the credits land in the spend ledger. Outflare keeps no contact database of its own. See [docs/lists.md](docs/lists.md).

```sh
curl -X POST "localhost:8080/v1/workspaces/$WS/lead-lists" -H "Authorization: Bearer $OUTFLARE_TOKEN" \
  -H "Content-Type: application/json" -d '{"name": "AU growth leaders", "search_integration_id": "'$APOLLO'",
  "filters": {"titles": ["Head of Growth"], "locations": ["Australia"]}, "credit_cap": 200, "build": true}'
```

## Switch from Instantly or Smartlead

Connect your Instantly or Smartlead account (Connectors, or ask your agent) and import it. Campaigns come over as draft sequences with their steps converted (variables renamed, HTML turned into plain text, anything that could not be carried over listed as a warning), so nothing sends until you start them. Leads keep the step they reached and the mailbox that wrote to them, so follow-ups stay in the same thread. Bounced and unsubscribed leads go straight onto the suppression list. Sending accounts are matched to your mailboxes by address; neither tool shares passwords, so the ones not here yet are listed with a CSV to fill in. Running an import again updates instead of duplicating, and nothing in the other tool changes.

```sh
curl "localhost:8080/v1/workspaces/$WS/integrations/$ID/import-preview" -H "Authorization: Bearer $OUTFLARE_TOKEN"
curl -X POST "localhost:8080/v1/workspaces/$WS/integrations/$ID/imports" -H "Authorization: Bearer $OUTFLARE_TOKEN" \
  -H "Content-Type: application/json" -d '{"leads": true}'
```

## Connectors and email verification

Outflare runs on its own: nothing needs connecting to send. Connectors plug it into everything else: email verification (ZeroBounce, NeverBounce, MillionVerifier, Bouncer, Reoon, Emailable, DeBounce, Hunter), sequencers to switch from, mailbox suppliers, CRMs, data, website analytics, DNS and more. Where a service offers both, you choose: sign in with it (OAuth, recommended) or paste an API key. Or let an agent ask: it gets a one-time link where you sign in or paste the key, so the key never passes through the chat. Every lead's address is checked before its first email (free built-in checks if no service is connected) and undeliverable addresses are never emailed. See [docs/connectors.md](docs/connectors.md).

Ask what kind of email domain any address or domain is (free mailbox, private mailbox like Proton, alias service like Hide My Email, throwaway inbox, school, government, or unknown), with attributes, risk, the evidence and a recommended sign-up action: `POST /v1/workspaces/$WS/classifications` or the MCP tool `classify_email`. The data ships with the engine, from Outflare's reviewed providers and two open lists. See [docs/classification.md](docs/classification.md).

## Suppression and the policy check

The suppression list holds addresses and whole domains that must never be emailed (unsubscribes, bounces, complaints, competitors, do-not-contact). Every send passes one policy check against it, and agents can ask the same check before enrolling anyone:

```sh
curl -X POST localhost:8080/v1/workspaces/$WS/recipients/check \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"emails": ["ana@acme-io.example", "bo@competitor-com.example"]}'
```

Operators can add suppressions; only owners and admins can remove one. Deleting a lead with `?suppress=true` honours an erasure request without the person being emailed again if they are re-imported.

Each entry says why it is there, who added it, from which tool, and whether a person, an automation or the AI (reading a reply as an opt-out) added it. The list is kept in step with the blocklists of connected EmailBison, Instantly and Smartlead accounts. Every person also has a contact basis (opted in, customer, third party or unknown) that decides what may go out to them: third-party leads get cold outreach but never newsletters or product updates without their consent. See [docs/opt-outs.md](docs/opt-outs.md).

## Projects

A project is one go-to-market motion: an offer, sold to one kind of customer, in one market (Projects → New project). It holds the **brief** people and agents work from, and owns the domains, mailboxes, lead lists and sequences that run the motion. Anything outside a project is shared by the workspace.

- **The brief.** Offer, audience and market in words, the ideal customer as filters (titles, seniority, industries, company size, locations, keywords), and a written brief in Markdown: messaging, proof, objections, words to avoid. Every change bumps its version. An edit sent with the version it was based on fails with `409 brief_changed` if someone changed the brief since, so a person and an agent never overwrite each other.
- **What it owns.** Move domains (with their mailboxes), mailboxes, lead lists and sequences into a project, or create a sequence inside one with `project_id`. A project's mailboxes send only for its own sequences; shared mailboxes can still fill a project's shortfall. List endpoints take `?project_id=<id>` or `?project_id=none`.
- **One switch and one limit.** Pausing a project holds every send from its sequences, and its daily send limit caps them together, on top of mailbox and domain limits. Waiting leads show `project_paused` or `project_daily_limit`.
- **Project tokens.** An API token created with a `project_id` sees and changes only that project. Today it can read and edit its project and brief, read the project's sending domains and mailboxes, build and run the project's lead lists, and create, edit, start and delete the project's sequences. It enrols only leads on its project's lists, and its new sequences always belong to its project. Every other endpoint refuses it with a 403, so a project token never reaches the rest of the workspace. More endpoints open to project tokens as they learn to stay inside one project.

```sh
# What an agent reads before writing copy or building a list
curl localhost:8080/v1/workspaces/$WS/projects/$PROJECT/brief -H "Authorization: Bearer $OUTFLARE_TOKEN"

# Move a domain and its mailboxes into the project
curl -X POST localhost:8080/v1/workspaces/$WS/projects/$PROJECT/add \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" -d '{"domains": ["acme-mail.com"]}'
```

## Brands

A company that markets several businesses from one workspace, each with its own website (say geonode.com, repocket.com and a games studio), adds each as a **brand** (Company → Brands). A brand also keeps its links: App Store and Google Play listings, TV app stores, socials and docs, with the kind guessed from the address. With Geonode connected, Outflare reads the brand's website and suggests the profiles and listings it links to, for you to add, correct or dismiss ([docs/social.md](docs/social.md#finding-a-brands-links)). Connectors, people, API tokens, leads and the suppression list stay the workspace's, so nothing is reconnected or re-invited; each brand has its own context, memories, projects and sending setup.

- **Context in layers.** Company context says what is true for every brand, plus *How our brands relate*. Each brand adds what it is, who it's for, goals, voice, rules and notes: its rules add to the company's, the rest wins where set. `GET /context?brand=repocket` gives agents both layers and that brand's memories in one answer.
- **What belongs to a brand.** Move projects (with everything in them), domains (with their mailboxes), mailboxes, lead lists, sequences and missions into a brand with `POST /brands/{brand}/add`. Records in a project always take the project's brand. A brand's mailboxes send only for that brand's sequences; shared ones send for any.
- **One brand at a time.** Like a project in PostHog, the brand picker at the top of the sidebar, under the workspace, narrows every page to one brand, and the URL holds it after the workspace (`/acme/repocket/seo`, `/acme/all/seo` for every brand; older `?brand=repocket` links still open). It opens on the brand picked last, and **All brands** compares them. Switching brand reloads the page, and an item from the other brand closes back to its list. List endpoints and analytics take `?brand=<handle or id>`, `?brand=none` for shared records, or `?brand=all`. The MCP server takes `OUTFLARE_BRAND` as its default. With a brand picked you see only that brand: Home, infrastructure, warm-up, mailbox health, leads, allocation, meetings and deals count nothing from other brands or from records in no brand.
- **Records in no brand.** Under **All brands**, Home asks you to sort records that belong to no brand, and **Company → Brands** lists them (projects, domains, mailboxes, lead lists and sequences) to tick and move in one go. Agents use the `add_to_brand` and `remove_from_brand` tools.
- **One person, one brand at a time.** Leads and unsubscribes are the workspace's, so an unsubscribe from one brand stops them all. Turn on the cross-brand contact rule (Company → Brands, or `PATCH /brand-rules` with `cross_brand_days`) and a brand's sequence skips anyone another brand emailed in that many days. `POST /recipients/check` with `brand` asks first.
- **Reports by brand.** `GET /reports` and `GET /outcomes/report` take `group_by=brand` and `?brand=`, so you can compare cost per meeting across brands. Audit events, the events feed and webhook payloads carry `brand_id`, and a webhook can be kept to one brand.
- **Connect once, share with brands.** A connection belongs to the workspace and is shared with every brand or the ones you pick (`brand_ids`), or it is one brand's own (`brand_id`) and replaces the shared one for that brand. `GET /integrations?brand=repocket` lists what a brand uses ([docs/connectors.md](docs/connectors.md#brands)).
- **Analytics by brand.** A GA4 property, Search Console site or PostHog project reports for the brand whose domains include its domain. Pick another brand, or make it shared, with `PUT /analytics/{id}/site-brand`.
- **Nobody switches accounts.** Owners and admins see every brand. Keep a member or viewer (or anyone on a role without workspace-wide permissions, see [docs/roles.md](docs/roles.md)) to some brands (Settings → Members, or `PATCH /members/{user}` with `brand_ids`, and on the invite), and an API token to one brand (`"brand": "repocket"` when you create it). They see their brands' work plus anything shared, change only their brands' records, and get `403` from what needs every brand, such as workspace settings, webhooks and missions (marked `brand_limited` in the API reference).

```sh
curl -X POST localhost:8080/v1/workspaces/$WS/brands -H "Authorization: Bearer $OUTFLARE_TOKEN" \
  -H "Content-Type: application/json" -d '{"name": "Repocket", "website": "repocket.com", "platforms": ["web", "ios", "android"]}'
curl "localhost:8080/v1/workspaces/$WS/context?brand=repocket" -H "Authorization: Bearer $OUTFLARE_TOKEN"
```

## Company context and memory

Outflare remembers the business, so nobody explains it twice: not to a new chat, a different agent, or a teammate. Settings → **Company** holds it, and every agent reads it first.

- **Company context.** Company name, website, what you do, goals, voice, rules and notes. It is true for every project; offers, audiences and markets stay on project briefs. Every change bumps its version, and an edit sent with an old `version` fails with `409 context_changed`. `missing` lists the onboarding fields still empty; agents ask for them once, in whichever client you use first, and stop when they are filled or you press "Don't ask again".
- **Memories.** Short facts, preferences and decisions agents and people save as they work ("Never mention pricing in a first email"). Never transcripts: the chat stays in your client. Each records who saved it and the mission it came from. Pinned memories go into every conversation; agents search the rest. You can correct or forget any of them on the Company page.
- **Status.** `GET /v1/workspaces/{ws}/status` lists everything stopping the workspace, worst first (mailboxes that can't sign in or aren't ready, critical domains, paused servers, broken connections, mission changes waiting for you, sequences short of mailboxes, plans to approve, open errors on the fixes list, unread replies), each with the tool and page that fix it and whether an agent or a person has to act. Home shows the same list as **Needs you**, worst first, with the next set-up step in it.

Memories are notes for agents, never permissions: limits, scopes and the send policy decide what runs. Reading needs `workspace.read`; editing needs `context.manage` (in "Can make changes" on API tokens). The built-in mission agent reads the context and pinned memories every turn.

```sh
# What an agent reads at the start of a conversation
curl localhost:8080/v1/workspaces/$WS/context -H "Authorization: Bearer $OUTFLARE_TOKEN"

# Remember a decision
curl -X POST localhost:8080/v1/workspaces/$WS/memories -H "Authorization: Bearer $OUTFLARE_TOKEN" \
  -H "Content-Type: application/json" -d '{"text": "Only target Series A and later", "kind": "decision"}'
```

## Run a sequence

A sequence is a list of steps (Sequences → New sequence). The first step has a subject; a follow-up with an empty subject replies in the same thread, from the same mailbox. Templates use `{{first_name}}`, `{{company|your team}}` (with a fallback), any custom field from your lead CSV, and `{{sender_first_name}}`. Add a lead list, set the sending window and time zone, and activate it.

```sh
curl -X POST localhost:8080/v1/workspaces/$WS/sequences/$SEQ/enrollments \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" -d '{"list_id": "'$LIST'"}'
```

Every email passes the policy check just before it is sent, in this order: the recipient is not suppressed; it is inside the sequence's sending window; the sequence's cap on new leads per day has room; and a mailbox allocated to the sequence is active, on a domain that is not paused and under its daily cap, under its own daily limit and past its minimum gap since its last send. New leads rotate across the sequence's mailboxes, least used first; follow-ups always go from the mailbox that sent the first email. If a check says wait, the lead is rescheduled with the reason shown (for example `mailbox_daily_limit`); if a lead is missing a template variable, it is not emailed at all. A step is never sent twice, even if the engine restarts mid-send.

Every email carries a one-click unsubscribe link and `List-Unsubscribe` headers (RFC 8058), as Gmail and Yahoo require of bulk senders. Unsubscribing adds the address to the suppression list and stops all its sequences. A hard bounce (the server refuses the address) suppresses it too. Set `PUBLIC_URL` to the address people can reach your instance on, since unsubscribe links point there. The header can be turned off per sequence (`unsubscribe_header: false`); the `{{unsubscribe_url}}` link in the body works either way.

### Tracking domain and click tracking (optional)

Both are off until you turn them on. Add a tracking domain such as `links.yourdomain-com.example` on the Domains page (or `POST /v1/workspaces/$WS/tracking-domains`) and point it at your instance with a CNAME record; once it checks out, unsubscribe and click links use it instead of `PUBLIC_URL`. A tracking domain serves only those links (`/c/`, `/o/`, `/u/`): the rest of Outflare, sign-in included, answers 404 there. Give it its own HTTPS certificate on your proxy, as for `PUBLIC_URL`. If your customers should point their CNAME at another host than `PUBLIC_URL`'s (a proxy that serves their domains), set `OUTFLARE_TRACKING_TARGET` to it. An edge that can only forward requests to the server's own address (an edge proxy in front of a host that routes by name) can pass on the domain and visitor address in `X-Outflare-Edge-Host` and `X-Outflare-Edge-IP`, with `X-Outflare-Edge-Secret` set to `OUTFLARE_EDGE_SECRET`; the server serves only tracking-domain links to it and ignores those headers without the secret. Extensions can get the certificates themselves (`Host.SetTrackingCerts`): then a domain's links use it only once its certificate is active, and `https_status` on the tracking domain says where it stands. Turn on `track_clicks` on a sequence to swap its links for redirects and count clicks. Rewritten links can count against you with spam filters, so leave it off unless you need it and use it with a tracking domain. Clicks from link scanners (known scanner user agents, or a click within a minute of sending) are kept out of the counts. Clicks show on the sequence and in reports. Opens are not tracked: tracking pixels hurt deliverability and Apple Mail fakes them.

## Allocate mailboxes

Each mailbox sends for one sequence at a time, and Outflare decides which, so sequences don't fight over the same inboxes. Give a sequence a **daily new-lead limit** or a **daily send target** and it gets enough mailboxes when you activate it; the sequence's **Mailboxes** tab, or `GET /v1/workspaces/$WS/sequences/$SEQ/allocation`, shows which ones and why. The allocator plans again every minute, and straight away after a change to a mailbox or sequence.

- **Who gets what.** A sequence takes free, ready mailboxes from its preferred senders, then its team's senders, then shared senders. Taking another team's mailbox needs that team's approval. Mailboxes are spread across domains. A mailbox is ready when it is active, its domain is not paused or blocklisted and has SPF, it is in the ready stage, and any warm-up score a sequencer reports for it is at least 75.
- **Priority.** A high-priority sequence that is short of mailboxes may take them from lower-priority ones, down to their minimum. Equal priorities never take from each other.
- **Follow-ups never switch mailbox.** A mailbox that moves while leads are mid-sequence **drains** first: it takes no new leads, finishes those follow-ups in the same thread, then moves. Pausing a sequence keeps the mailboxes that still have follow-ups due and frees the rest. If a mailbox is deleted, its follow-ups wait for you to cancel them or hand them to another mailbox as a fresh email.
- **Automatic or suggest.** In automatic mode (the default) changes happen straight away and each comes with a reason; undo any of them. In suggest mode they wait for you to apply. Set the mode for the workspace, a team, or one sequence.
- **Locks.** Keep a mailbox on one sequence, on one team's sequences, or out of automatic allocation altogether (manual only), optionally until a date. Assigning or releasing by hand always works.
- **Teams and senders.** A sender is who a mailbox sends as: a person, a persona, or a shared identity. Senders belong to teams, or to none (shared). Outflare suggests a sender when a mailbox's display name matches one exactly, but each mailbox's sender is confirmed, never guessed. A workspace without teams shares every mailbox.
- **Agents.** Agents follow the same rules as people, with limits on their API token: a daily send budget, the highest priority they may set, whether their sequences may take mailboxes from others without your approval (off by default), and the team their sequences belong to.

```sh
# Assign two mailboxes by hand; a busy one drains into the sequence
curl -X POST localhost:8080/v1/workspaces/$WS/mailboxes/assign \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"sequence_id": "'$SEQ'", "mailbox_ids": ["'$MB1'", "'$MB2'"]}'

# What needs a person: approvals, shortages, follow-ups without a mailbox
curl localhost:8080/v1/workspaces/$WS/allocation/attention -H "Authorization: Bearer $OUTFLARE_TOKEN"
```

### Campaigns in other sequencers

Connect an **EmailBison**, **Instantly** or **Smartlead** account under Connectors and its mailboxes and campaigns join the same allocation. Outflare reads each account every 5 minutes (or now: `POST /v1/workspaces/$WS/platforms/$ID/sync`).

- Each live campaign becomes a sequence linked to it (`links` on the sequence). The platform keeps its steps, leads, schedule and status; Outflare only decides which mailboxes it uses, and adds or removes them in the platform. Give it a **daily send target** to start allocating.
- Mailboxes that exist only in the platform are created with `has_login: false`: they can serve that platform's campaigns, but Outflare never sends from them or syncs their inbox. Their warm-up is switched in the platform like any other mailbox's. With teams, confirm their sender like any other mailbox.
- A newly connected account's first plan is only suggested. Look at it on the Allocation page and choose **Allow changes** (`POST .../platforms/$ID/review`) before anything moves.
- Platforms don't say which leads each mailbox still owes follow-ups, so a mailbox taken off a platform campaign stays for the campaign's sequence length (14 days unless the platform says) before it is detached.
- Mailboxes added or removed in the platform itself are accepted as manual changes. An account that can't be read for 30 minutes makes its mailboxes count as busy, so nothing is planned on stale data.
- Apollo picks the sending mailbox for each contact itself, so it is only watched: connecting Apollo (also used for lead data) flags any of your mailboxes that are connected there too, and allocation keeps using them. Apollo's sequences don't become sequences here.

```sh
curl localhost:8080/v1/workspaces/$WS/platforms -H "Authorization: Bearer $OUTFLARE_TOKEN"
```

## Inbox and replies

Every active mailbox is checked over IMAP every two minutes, and replies land in one inbox across all of them (Inbox tab, or `GET /v1/workspaces/$WS/inbox`). Each message is matched to the lead and sequence it answers, by its threading headers or else by the sender's address, and acted on:

- **A reply** stops the lead's sequences (unless a sequence has stop on reply turned off).
- **A reply asking to be removed** ("unsubscribe", "remove me from your list", "stop emailing me") also adds the address to the suppression list, and so does a complaint (a spam report or a legal threat). Only the new text is read, not the quoted email.
- **An out-of-office or other auto-reply** is shown but does not stop the sequence.
- **A bounce report** suppresses the address it names, if this workspace emailed it.

Each reply also gets a label for what it means, in four groups:

| Group | Labels |
| --- | --- |
| Hot | interested, meeting booked, question |
| Later | referral, not now, objection |
| Closed | not interested, wrong person, unsubscribed, complaint |
| Automatic | out of office, auto-reply |

Built-in rules label the clear cases on arrival. When the workspace has connected its own AI model (Settings → AI model; there is no other key), the rest are read by that model in the background, and it labels them too: on its own when it is at least 80% sure, otherwise with the label flagged for a person to check. It also notes details the reply states (a return date, a referred contact, a proposed meeting time, the objection) in `details`, and why in `category_reason`. Its calls count towards the workspace's monthly AI limit. A reply asking to be removed is suppressed whatever the AI's confidence, and flagged when unsure; changing its label to anything else lifts that suppression in one step, and the sequences it stopped stay stopped as replied. Without an AI model, the rest stay "not labelled yet", so a person or an agent can read and label them (`PATCH /v1/workspaces/$WS/inbox/{id}` with `category`, or the MCP tool `triage_inbox_message`). Every reply says who labelled it (`category_source`: rules, ai, human, api_token or agent), how sure they were (`category_confidence`, 0 to 1) and whether a person should check it (`needs_review`); a person or agent setting the label settles the review. The same call assigns a reply to a member, snoozes it until a time, marks it done or sets tags. The inbox opens on what still needs handling (`?status=open`); unsubscribes and automatic replies need nothing from you, so they arrive already done.

Filter the inbox by `category` (one label, several separated by commas, or `none`), `group` (hot, later, closed, automatic), `source` (`mailbox` for Outflare's own mailboxes, or a connected sequencer), `tag`, `assigned_to` (`me`, `none` or a user id), `needs_review`, `min_confidence`, `project_id`, `brand`, `unread` and `q` (sender, subject and text). Save a set of filters as a view (Views menu, or `POST /v1/workspaces/$WS/inbox-views`): views are personal unless shared, and ones an API token saves are shared. Tags have colours and are managed under Tags (`/v1/workspaces/$WS/inbox-tags`); renaming or deleting a tag changes every reply that carries it.

Every new or changed label is an `inbox.classified` event, for webhooks and `GET /v1/workspaces/$WS/events`, with the label, its group, who set it, how sure they were and the ids. A webhook can listen for some labels only: give it `categories` (labels or groups, such as `["hot"]`) and events about replies with other labels are not sent to it.

Reply rules act on a label once it is sure (`GET`/`PUT /v1/workspaces/$WS/reply-rules`, for the workspace or a project or sequence): snooze a "not now", reschedule around an out-of-office, add the person a referral names. Any label a person writes can also move a deal in Outflare's own CRM (`deal`: the record type, its stage field, the stage, and the closed stages). The reply's deal is the newest open one that points at the person or their company, or a new one named after the company. It only moves forward and never over a stage a person set, and every move is in the record's history.

Reply from the inbox and the email goes from the same mailbox, in the same thread, through the recipient check. The first sync of a mailbox reads only the last day of mail, so connecting an existing inbox does not import its history.

Replies your **EmailBison** campaigns get come into the same inbox (`source: emailbison`), read with each 5-minute sync of the account (or `POST /v1/workspaces/$WS/platforms/$ID/sync`, which says how many `replies` it brought in). They get the same rules: labelled on arrival, opt-outs and complaints suppressed, the lead's sequences stopped when Outflare holds the lead. Mail between your own mailboxes and mail with the account's warm-up tag is left out. Answering one sends it through EmailBison, from the sender email it arrived at and in the same thread, after the same recipient check. The first read of an account goes back three days.

## Outcomes and cost per meeting

The Outcomes page (or `GET /v1/workspaces/$WS/outcomes/report`) answers what outreach produced and what it cost: meetings, deals and spend for a period, with cost per reply, per meeting, per held meeting and per won deal, for the whole workspace or by project or sequence. Outflare records costs; it never bills. You pay your providers directly.

- **Meetings** come from Google Calendar, Outlook, Calendly, Cal.com and HubSpot (connect them under Connectors), or are logged by a person or an agent. A meeting counts when an attendee is a lead you emailed, or failing that shares a company domain with one (never a free-mail domain). It counts for the last sequence that emailed that lead before the booking, and that sequence's project. Fix any meeting by hand; a person's choice is never overwritten by a sync. The same meeting seen in two places is counted once.
- **Deals** are read from HubSpot (or logged by hand). HubSpot stays the source of truth; Outflare keeps a copy for reporting and can write booked meetings back to HubSpot when you turn that on. It is not a CRM.
- **Spend** is a ledger of charges, each tagged with the project, sequence, mailbox or domain it was for and the person, token or agent that caused it. Record money, or units (2,400 verification credits) priced later with a per-unit rate. Recurring costs (mailbox seats, a data subscription, an SDR) are spread over each day they were active. Costs tied to a sequence or project count for it; mailbox and domain costs follow the sequences that sent from them; the rest is shared by emails sent. Reports say when a number is estimated or understated, and other currencies are listed, never converted.
- **Export** meetings, deals, spend or the report as CSV or NDJSON for a warehouse with `GET /v1/workspaces/$WS/outcomes/export?dataset=meetings&format=ndjson`.

```sh
# Cost per meeting by sequence, this month
curl "localhost:8080/v1/workspaces/$WS/outcomes/report?group_by=sequence&from=2026-10-01" -H "Authorization: Bearer $OUTFLARE_TOKEN"

# Record a charge; the ref makes it safe to retry
curl -X POST localhost:8080/v1/workspaces/$WS/spend \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"category": "verification", "provider": "zerobounce", "units": 2400, "unit_name": "credit", "project_id": "'$PROJECT'", "ref": "zb-batch-118"}'
```

The `outcomes.read` scope reads all of this; `outcomes.manage` logs meetings, deals and spend.

## Reports

The Reports tab, `GET /v1/workspaces/$WS/reports` and the MCP tool `get_report` answer the same question: what was sent, and what happened to it. Pick a period (any range up to 400 days, last 30 by default) and group by day, week, sequence, step, mailbox or sending domain. Every row has sends, new leads contacted, follow-ups, failures, bounces, replies, auto-replies, unsubscribes and link clicks (when click tracking is on), with reply, bounce, unsubscribe and click rates worked out. Filter to one sequence, mailbox or domain with `sequence_id`, `mailbox_id` or `domain`.

```sh
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/reports?from=2026-10-01&to=2026-10-31&group_by=mailbox"
```

## Analytics

The Analytics tab reads the analytics tools already on your website: Google Analytics 4, Google Search Console, Bing Webmaster Tools, PostHog, Plausible, Fathom, Matomo, Umami, Mixpanel, Amplitude, Cloudflare Web Analytics, Simple Analytics, Microsoft Clarity and Vercel Web Analytics. Each one answers the same reports (traffic by day, top pages, sources, search queries, events and conversions) in the same shape, through the page, `GET /v1/workspaces/$WS/analytics/...` and the MCP tools `get_analytics_summary` and `get_analytics_report`. Every six hours a sync stores each tool's daily numbers in Outflare, for every site a sign-in can see (each GA4 property, PostHog project or Search Console property, switchable one by one), so you, a script or an agent can look back months, line one website up across tools by domain, compare tools day by day and add up search clicks across Google and Bing without calling the providers again (`/analytics/overview`, `/analytics/history`, `/analytics/compare`, and the MCP tools `get_analytics_overview`, `query_analytics_history` and `compare_analytics_tools`). For agents, `/analytics/changes` (MCP `get_analytics_changes`) answers "what changed?" for each website in one call: headline sentences with their tool and dates, this period against the one before, the pages, sources and queries that moved most, unusual days, SEO moves and what is missing, with the connector that fills each gap; `/analytics/catalog` says what each website can be asked. Outflare only reads, and nothing else in Outflare needs analytics connected. Google Analytics and Search Console share one sign-in through your Google OAuth client (or take a service account key), and PostHog Cloud signs in with no app to register; see [docs/analytics.md](docs/analytics.md) for the API, the scopes and Google's verification.

Campaign links (Analytics → Campaign links) tag every link you share with one taxonomy: nine channels, each with a fixed `utm_medium`, cleaned sources (`fb` and `Facebook` are both `facebook`), a campaign slug as `utm_campaign`, and a short campaign id, `ofc`, that still names the campaign when other tags are stripped. Tags never change once a campaign exists, so reports stay clean. Use the page, `/v1/workspaces/$WS/tracking-campaigns` or the MCP tools `create_tracking_campaign` and `create_tracking_link`; see [docs/campaign-links.md](docs/campaign-links.md).

```sh
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/summary?from=2026-09-27&to=2026-10-24"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/history?report=search_queries&group=key&from=2026-07-24"
```

## Paid media

Connect Google Ads, Meta Ads, Microsoft Advertising, Reddit Ads, X Ads, LinkedIn Ads, TikTok Ads and Snapchat Ads with your own ad accounts (Google and Microsoft also need your own developer token). Every six hours Outflare stores each account's campaigns and daily spend, impressions, clicks and platform-reported conversions, puts the spend in the costs ledger under the account's brand, and answers it through `GET /v1/workspaces/$WS/ads/...` and the MCP tools `list_ad_campaigns` and `get_ads_summary`. It only reads; nothing changes in your ad accounts. See [docs/paid-media.md](docs/paid-media.md).

## SEO

Connect DataForSEO or Ahrefs and Outflare reads how your domain looks from the outside: domain rating, backlinks, referring domains, the keywords you rank for and where, competitors in search and (Ahrefs) site audit issues, in one shape for both tools, on the SEO page, through `GET /v1/workspaces/$WS/seo/...` and the MCP tools `list_seo`, `get_seo_history` and `get_seo_report`. Both are paid per call, so each connection has a monthly budget ($10 on DataForSEO, 10,000 API units on Ahrefs by default, changeable), answers are kept for a day, and every call's cost is recorded in Outcomes. A weekly snapshot of each domain's overview and top keywords is stored, so each website shows its SEO figures next to its analytics and keyword moves week to week. See [docs/seo.md](docs/seo.md).

The SEO module's **Keywords** page shows what Google shows each page of your site for and how often people click, for 16 months, with how Google indexed each page and the sitemaps it read, all stored from your own Search Console sign-in (`GET /v1/workspaces/$WS/seo-sites/$SITE/search`, `.../indexing`, `.../sitemaps`, MCP `get_site_search`, `get_site_indexing`). Give each page a target keyword (or accept the one Search Console suggests) and every crawl checks the page carries it, putting what is missing on the fixes list (`.../targets`, MCP `list_page_targets`, `set_page_target`). Pages that compete for the same search are found too, with the page to keep (`.../cannibalisation`), and so are internal links worth adding to target pages (`.../internal-links`). Track where the site ranks on Google for the keywords you choose, through your own DataForSEO account (`.../rankings`, MCP `track_keywords`, `list_rankings`), and which landing pages bring conversions, from your own PostHog (`.../conversions`, MCP `get_site_conversions`). Drops in clicks, rankings, indexing, conversions or site health are found after every sync and land on the fixes list (`.../regressions`). See [docs/search-intelligence.md](docs/search-intelligence.md).

```sh
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo/history?report=keywords&host=example.com"
```

Outflare also audits your own sites with its crawler, OutflareBot ([outflare.ai/bot](https://outflare.ai/bot/)), which needs no connector. Add a site (`POST /v1/workspaces/$WS/seo-sites`), prove you own it with a DNS record, a meta tag or a file (`POST .../verify`), and crawl it (`POST .../crawls`): it obeys robots.txt and Crawl-delay, slows down when the site struggles and records every page it found with its status, redirects, noindex and robots.txt blocks (`GET .../crawls/{crawl}/pages`). Proving ownership raises the limits from 100 to 10,000 pages and lets you audit pages your own robots.txt hides. Each crawl ends with an audit: 63 rules from broken links and redirect loops to hreflang, HTTPS, link previews and what AI crawlers may read (with llms.txt), grouped by URL template (`/blog/*`) so one fix clears many pages, and a health score (`GET .../crawls/{crawl}/issues`). A proved site's pages that its firewall blocks, or that only JavaScript fills in, can be read through your Geonode key. Each fix can go to your coding agent as a prompt and be checked by a fresh crawl. See [docs/crawler.md](docs/crawler.md).

Connect GitHub or Vercel and Outflare also knows when each website ships: pick the repository or project behind a site and every production deploy is recorded with its commit (`GET /v1/workspaces/$WS/deploys`, the `deploy.detected` webhook), so fixes are re-checked and experiments start on the real deploy. It only reads. See [docs/deploys.md](docs/deploys.md).

Every workspace also gets a digest: a daily or weekly summary of fixes, deploys, site health, outreach, lifecycle email and missions, with what needs a person first. It is kept in the app (Settings → Digests), sent as the `digest.ready` webhook, and emailed only through your own email provider when you pick one. See [docs/digests.md](docs/digests.md).

AI visibility asks AI models the questions your buyers ask ("best tool for ...") on a schedule, on your own AI connections, and counts how often the answers name your brand against your competitors (mention rate, share of voice, position), which pages they cite, by model and week. List what is true about each brand and every answer naming it is checked against those facts, so you see what AI gets wrong, in its own words. Prompts whose answers never name you, and facts answers contradict, go on the fixes list. Set up prompt cohorts per brand (`POST /v1/workspaces/$WS/ai-cohorts`), pick the models (OpenRouter reaches most of them with one key), and read `GET .../ai-visibility`. It has its own monthly spending limit on top of the workspace's AI limit. See [docs/ai-visibility.md](docs/ai-visibility.md).

To learn whether a change helped, run an SEO experiment: Outflare splits a site's pages into matched treatment and control groups from your Search Console data, starts measuring on the next deploy, and reports the lift with a p-value. See [docs/experiments.md](docs/experiments.md).

Agents in a mission can work through the fixes list on their own as far as you allow: copilot (the default) holds every fix for a person, rules lets the kinds of fix you pick through, and autonomous lets all of them through, except fixes to redirects, canonicals, indexing, robots.txt or URLs, which always wait for a person. See [docs/fix-autonomy.md](docs/fix-autonomy.md).

## Partners: referrals, affiliates and influencers

Run customer referral, affiliate and influencer programs from one place (Partners). Each program starts from a preset and keeps its own credit rule. Partners are customer records with a ref code, a short link, a campaign link and promo codes. Your backend reports sign-ups, sales and refunds server to server, or through a postback URL, the way it would call Tolt. Rewards stack: a share of the sale, a fixed amount, tiers, a fixed fee, or an action your own system carries out (such as granting credit), for the partner and the friend they brought. Each reward waits out a hold, refunds claw it back, and payouts are marked paid with a reference once you've paid from your own account. Outflare never holds money. Each program can open a hosted partner portal where partners apply, sign in with a link sent through your own email provider, and see their links, masked referred customers, rewards and payouts. It is served on your own domain, on a subdomain of the server's partners domain (`OUTFLARE_PARTNERS_DOMAIN`), or at `/p/<program>/`. Moving from Tolt? Import the program with your own Tolt API key, as often as you like until the day you switch: partners keep their link codes ([docs/partners-tolt-migration.md](docs/partners-tolt-migration.md)). Affiliates can have each conversion sent to their own tracker (a postback URL), and due payouts download as CSV for your payout tool. Missions can aim at the revenue partners refer. See [docs/partners.md](docs/partners.md).

## Social: Reddit, X and LinkedIn

Watch Reddit for threads where your brand can genuinely help ("which proxy", "passive income app"), have your own AI model draft a reply that answers the question and says who you are, and approve each one before it posts from the brand's own account through your own Reddit app. Each reply's link is tagged, so the visitors and sign-ups it brings show beside it. No bought upvotes, other accounts or undisclosed posts, and only a person can approve. See [docs/social.md](docs/social.md).

Reviews brings each brand's Trustpilot, G2, Capterra and Google reviews into one page, read every day through your own Geonode: stars, sentiment, the link and your answer, each site's score day by day, Outflare's review score (stars weighted by how many and how recent) and whether reviews are growing, steady or slipping. **Draft answer** writes a public answer with your own AI model from the brand's reply guide, for a person to post. See [docs/reviews.md](docs/reviews.md).

Connect your own X developer app or sign in to LinkedIn, and the Social page shows followers day by day and your own posts' impressions and engagement, for X accounts, the LinkedIn company pages you administer and your own LinkedIn profile, per channel and per brand.

## Lifecycle email

Receipts, password resets, sign-in codes and onboarding email go through your own email provider account: Amazon SES, Postmark or Resend. You pay the provider directly and keep your sending reputation; cold outreach never goes this way. The Lifecycle module walks you through it: each provider has a step-by-step guide with a prompt to hand your own AI agent, which does the clicking and stops when it is time for you to create the key, so the secret never passes through a chat. Connecting sets up delivery events (deliveries, bounces, complaints) on its own; adding a sending domain registers it with the provider and lists the DNS records, which Outflare checks every minute at first. Templates take variables (`{{first_name|there}}`, `{{#if coupon}}`), keep every published version and preview as you type. Every send passes the suppression list, hard bounces and complaints are suppressed for you, and lifecycle email carries a one-click unsubscribe that transactional email (which people need) does not. The API is `POST /v1/workspaces/$WS/emails` with the `emails.send` scope, the MCP tools are `send_email` and friends, and each change is a webhook event (`email.delivered`, `email.bounced` and so on). See [docs/lifecycle-email.md](docs/lifecycle-email.md).

```sh
curl -X POST "$URL/v1/workspaces/$WS/emails" -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: $(uuidgen)" \
  -d '{"to": "ana@customer.example", "template": "password-reset", "data": {"reset_url": "https://app.example.com/r/abc"}}'
```

## API

Your first call, against a fresh instance: create a token under **API tokens** (choose Read only, Can make changes, or area by area, and when it expires; you can rename it or change what it may do later) (or `POST /v1/workspaces/{ws}/api-tokens`), then

```sh
export OUTFLARE_TOKEN=oft_...
curl localhost:8080/v1/whoami -H "Authorization: Bearer $OUTFLARE_TOKEN"     # its workspace, slug and scopes
curl localhost:8080/v1/workspaces/acme/stats -H "Authorization: Bearer $OUTFLARE_TOKEN"
```

Any scope in an area includes reading it (`leads.manage` can list leads), and a `403` names the scope that is missing.

Everything the web app does goes through the REST API, described in [`api/openapi.yaml`](api/openapi.yaml) and served by every instance at `/v1/openapi.yaml`. Authenticate with an API token (`Authorization: Bearer oft_...`) created under API tokens, scoped to what the caller needs. The TypeScript SDK, [`@outflare/sdk`](sdk/ts), is generated from the same file:

```ts
import { createClient, ok } from "@outflare/sdk";

const gtm = createClient({ baseUrl: "https://outflare.example.com", token: process.env.OUTFLARE_TOKEN });
const stats = ok(await gtm.GET("/v1/workspaces/{ws}/stats", { params: { path: { ws } } }));
```

The Python SDK, [`sdk/python`](sdk/python), is generated from it too, with one method per operation, and retries, idempotency keys and paging built in:

```python
from outflare import Outflare

client = Outflare()  # reads OUTFLARE_TOKEN, and OUTFLARE_URL for a self-hosted server
for lead in client.paginate(client.list_leads, q="acme"):
    print(lead["email"])
```

`GET /v1/workspaces/{ws}/stats` is the one-call overview: mailbox and domain health, sends, replies and bounces. The web app's Home page shows the same numbers.

Each operation in the document says who may call it in `x-outflare-access` (for example `{ scope: leads.manage, project_tokens: true }`), every error has the same body (`{"error": {"code", "message", "request_id"}}`, plus `param` naming the field a `400` is about; `GET /v1/error-codes` lists every code), and a workspace write made inside a mission may answer `202` with `MissionHeld`. Workspace writes take an `Idempotency-Key` header (any unique string, such as a UUID): if a call times out, retry it with the same key and you get the first answer back, marked `Idempotent-Replayed: true`, instead of creating a second lead or recording spend twice. Keys last 24 hours and belong to the caller. Each API token may make 600 requests a minute (`API_RATE_LIMIT`); every answer carries `RateLimit` and `RateLimit-Policy` headers saying what is left, and going over answers `429` with `Retry-After`. Work that runs in the background answers `202` with the job it queued, and `GET /v1/workspaces/{ws}/jobs/{id}` says how it is going. Every paged list answers `next_cursor`: pass it back as `cursor` for the next page, until it is `null` (the CLI's `--all` does this for you). Add `fields=id,email` to any GET to get back only the fields you read. The engine's tests check the document against the running server on every change: each response a test receives must match it, and each route must require exactly the access it states.

Every workspace has a slug, its name in URLs: the app opens it at `/{slug}`, for example `https://app.outflare.ai/acme/connectors`, and `/v1/workspaces/acme/...` works anywhere the API takes a workspace ID. Owners and admins change the name and slug under **Settings → General** (or `PATCH /v1/workspaces/{ws}`, scope `workspace.manage`). A slug can change, so scripts and integrations should store the workspace ID, which never does; an old slug keeps working until another workspace takes it. Sequences, projects, lead lists, missions, servers, infrastructure plans, API tokens and webhooks get readable URLs too (`/acme/sequences/q3-founders`), made from their name; renaming one gives it a new URL and the old one still opens it. A connection's URL is its connector (`/acme/connectors/posthog`, then `posthog-2` for a second PostHog), and the same URL opens the dialog to connect PostHog when nothing is connected yet (`GET /v1/workspaces/{ws}/links/{kind}/{ref}` answers what a URL names). Old app links with IDs, such as `/w/{workspace id}/...` or `/acme/sequences/{id}`, still open and are rewritten.

In the web app, the sidebar has the everyday pages (Home, Inbox, Missions, Projects), then one entry per module (CRM, Outreach, Deliverability, Lifecycle, SEO, Social, Analytics) whose pages show beneath it, one open at a time, then the Company hub (About your company, Brands). The footer has only Connectors and Settings; Settings holds the workspace, AI apps, notifications (Digests) and the developer pages (API tokens, Webhooks, Logs, Audit log, All tools). Pin any page (hover it, or use All tools) to keep it under Pinned at the top. The **API** button (top right) shows the call behind the page you are on as cURL, TypeScript and the matching MCP tool, with your instance's URL and IDs filled in. Press <kbd>⌘K</kbd> (<kbd>Ctrl+K</kbd>) for the command palette, or <kbd>G</kbd> then a letter to jump to a page: home (<kbd>O</kbd>), all <kbd>T</kbd>ools, sending <kbd>R</kbd>eports, website analytics (<kbd>W</kbd>), s<kbd>E</kbd>o, outcomes and cost (<kbd>C</kbd>), <kbd>I</kbd>nbox, missions (<kbd>A</kbd>), <kbd>P</kbd>rojects, <kbd>S</kbd>equences, <kbd>L</kbd>eads, do not contact (<kbd>X</kbd>), <kbd>M</kbd>ailboxes, <kbd>D</kbd>omains, warm-<kbd>U</kbd>p, infrastructure (<kbd>F</kbd>), co<kbd>N</kbd>nectors. The app is dark by default; switch to light in the sidebar.

### Test workspaces (the sandbox)

A test workspace works like any other, but nothing in it reaches the outside world, so you can try sequences or build an integration without emailing anyone. Create one under **Workspace settings → Create a test workspace**, or with `POST /v1/workspaces` and `{"name": "Sandbox", "test": true, "sample_data": true}` (three mailboxes and ten leads to start from). Inside one:

- Every mailbox is simulated (`provider: simulated`): it connects at once, its domain checks pass, and sends are recorded with a real `Message-ID` but delivered nowhere. Real mailboxes imported into it are turned into simulated ones.
- Verification is made up from the address and costs nothing: an address containing `undeliverable`, `risky` or `unknown` gets that result, any other is deliverable.
- Nothing can be bought: approving an infrastructure plan answers `409 test_workspace`.
- `POST /v1/workspaces/{ws}/test/send-now` sends every due step at once, skipping step delays, the sending window and the new-lead cap (mailbox daily limits still apply).
- `POST /v1/workspaces/{ws}/test/inbound` with a `lead_id` or `message_id` and a `kind` (`reply`, `auto_reply`, `opt_out` or `bounce`) answers a sent email as the lead's server would. It is handled like real mail: it lands in the inbox, a reply stops the sequence, an opt-out or bounce suppresses the address, and webhooks fire.

Every answer from a test workspace carries `Outflare-Test-Workspace: true`, and `test` is on the workspace and in `GET /v1/whoami`, so a client can tell where it is. The `/test/` endpoints answer `409 live_workspace` anywhere else. The MCP server has them as `send_now` and `simulate_inbound`.

### Command line

The `outflare` binary is also a client for any Outflare server. Every API operation is a command, read from the API document built into the binary, so the CLI always has the endpoints its version of the server has:

```sh
go install github.com/outflareai/outflare/engine/cmd/outflare@latest   # or use the binary in the Docker image
outflare login --url https://outflare.example.com     # asks for an API token; it knows its own workspace
outflare upsert-lead --email ana@acme-com.example --first-name Ana --company Acme
outflare list-leads --q acme
outflare list-audit-events --all                       # follows the pages
outflare create-webhook --url https://example.com/hook --events lead.replied,lead.bounced
outflare help create-webhook                           # flags, the scope it needs, an example
outflare upsert-lead --email ana@acme-com.example --curl       # print the request as curl instead
outflare listen --forward-to http://localhost:3000/hooks # new events, signed, to a receiver on your machine
```

Flags are the operation's parameters and body fields; `--data` takes a JSON body, `@file` or `-` for stdin. Writes carry an `Idempotency-Key`, so the CLI retries them safely when the network or server hiccups. `OUTFLARE_URL`, `OUTFLARE_TOKEN` and `OUTFLARE_WORKSPACE` override the saved login, as they do for the MCP server. Errors go to stderr with the request id, and the exit code is non-zero, so it scripts well. `GET /v1/whoami` is what `login` uses to find a token's workspace and scopes; any client can call it.

### Webhooks

Webhooks tell your own systems what happened as it happens: a lead replied, an email bounced, a mailbox was paused for its bounce rate, a mission is waiting for approval, a person's record changed or they withdrew consent, a broadcast went out, a crawl finished, a social reply was posted. Add an endpoint with the events it wants (`GET /v1/webhook-events` lists them; `lead.*` or `*` match several), and Outflare POSTs each one as JSON:

```sh
curl -X POST https://outflare.example.com/v1/workspaces/$WS/webhooks -H "Authorization: Bearer $TOKEN" \
  -d '{"url": "https://example.com/hooks/outflare", "events": ["lead.replied", "lead.bounced", "mailbox.auto_paused"]}'
# The answer includes the signing secret (whsec_...). Send a test event and see the result:
curl -X POST https://outflare.example.com/v1/workspaces/$WS/webhooks/$HOOK/test -H "Authorization: Bearer $TOKEN"
```

Deliveries are signed the [Standard Webhooks](https://www.standardwebhooks.com) way (`webhook-id`, `webhook-timestamp` and `webhook-signature` headers), so any of its libraries verifies them, or use the SDK:

```ts
import { verifyWebhook } from "@outflare/sdk";

const event = await verifyWebhook(await req.text(), req.headers, process.env.OUTFLARE_WEBHOOK_SECRET!);
if (event.type === "lead.replied") { /* event.data.inbound_id is the reply */ }
```

Every event comes from the audit log and is written in the same transaction as the change, so you hear about every change that happened and none that didn't. A delivery that does not get a 2xx answer is retried with backoff for about three and a half hours; `GET .../webhooks/{id}/deliveries` shows each try, and `POST .../deliveries/{id}/redeliver` sends one again. An endpoint that answers `410 Gone`, or fails everything for five days, is turned off until you turn it on again. Rotating the secret keeps the old one signing for 24 hours, so receivers can switch without missing anything. Webhooks only go to public `https` URLs; set `WEBHOOKS_ALLOW_PRIVATE=true` on a self-hosted server whose receivers are on its own network. Managing webhooks needs the `webhooks.manage` scope (owners and admins).

Can't receive webhooks (an agent, a script behind a firewall)? Poll the same events instead: `GET /v1/workspaces/{ws}/events?types=lead.*` returns them oldest first with a `next_cursor`; call again with `cursor=` set to it to get only what is new. Agents use the `list_events` MCP tool, and the CLI `outflare list-events --types lead.replied`. Building a receiver on your laptop? `outflare listen --forward-to http://localhost:3000/webhooks` sends each new event there, signed like a real delivery with a secret it prints, so your verification code is tested too.

### Logs

Every API call made in a workspace is in its request log: from the web app, the API, MCP agents, the CLI and missions, with the status, how long it took, the error code, the operation, the MCP tool and agent app, and who made it. Open **Logs** in the sidebar of the web app to search and filter them (level, status, surface, operation, error code, brand, project, time range, or click the chart to zoom in), follow new calls live, open one to see why it failed (the error code, the field it named or the scope it was missing, what the code means and what to do) and the audit log entries it wrote, step through calls with ↑ and ↓, copy its details for a bug report, share a link to a filtered view, or download it as CSV or JSON. Paste the `request_id` from an error to find that call; press `/` to search.

```sh
curl "https://outflare.example.com/v1/workspaces/$WS/logs?level=warning,error&surface=mcp&since=1h" -H "Authorization: Bearer $TOKEN"
outflare logs --follow --level error   # the latest calls, then new ones as they happen
```

Logs keep only what describes a call, never request or response bodies, query values, headers, keys or IP addresses, and are kept for 30 days (`OUTFLARE_LOG_RETENTION_DAYS` on a self-hosted server). Reading them needs the `audit.read` scope (owners and admins); a brand token sees only calls about its brand. Agents use the `list_logs` and `get_log` MCP tools.

## Agents (MCP)

[`mcp/`](mcp) is an MCP server, so Claude, ChatGPT, Cursor and other agents can run a workspace: read a project's brief, import leads, report cost per meeting, record spend, check them against the suppression list, build and start sequences, and answer replies. It calls the same API with a scoped token, so an agent gets only the scopes it was given, every send passes the same policy check, and every change is in the audit log.

**Connect your AI** (in the sidebar) is the easy way: the server is hosted at `https://<your server>/mcp`, and an app connects with that one URL. It signs in with OAuth: you approve it on a consent screen, pick the workspace, and switch off anything it shouldn't change (everything starts on except spending). The app never gets to manage people, tokens, webhooks or the suppression list, or approve missions, and it spends money only if you switch that on. Connected apps are listed on the same page. Click one to change what it may do with the same switches, at any time and without connecting again; anyone can change or disconnect their own app, and owners and admins anyone's. Connecting the same app again replaces its old connection. When an app is refused something, the error names the switch and links to it.

Apps connected this way make changes only inside a **mission** a person approved: outside one they read, and a write gets `403 mission_required` telling them to start a mission (MCP `create_mission`), which a person approves before anything runs. Turn **Only inside missions** off for an app on its page to let it change things directly. Apps Outflare doesn't recognise by where they send you back (Claude, ChatGPT, Cursor, VS Code, or an app on your own computer) may only read until the server's operator checks them: their page shows the app's id, and `outflare verify-app <client_id>` (or `unverify-app`) on the server lets people give it more.

```sh
claude mcp add --transport http outflare https://outflare.example.com/mcp
```

The release image runs the hosted server itself. Built from source, set `OUTFLARE_MCP_SERVER` to the bundled file (`pnpm --filter @outflare/mcp build && pnpm --filter @outflare/mcp bundle` writes `mcp/dist/outflare-mcp.mjs`) and, if `node` isn't on the path, `OUTFLARE_NODE`. Without them, `/mcp` answers `404` and the page shows how to run the server on your own computer with an API token instead: Set `OUTFLARE_PRIVACY_URL` and `OUTFLARE_TERMS_URL` to your own policies and AI apps can link to them from the sign-in metadata.

```sh
pnpm --filter @outflare/sdk build && pnpm --filter @outflare/mcp build
claude mcp add outflare -e OUTFLARE_URL=https://outflare.example.com -e OUTFLARE_TOKEN=oft_... -- node "$PWD/mcp/dist/index.js"
```

Set `OUTFLARE_MCP_READ_ONLY=1` there for an agent that only reports.

An agent starts every conversation with `get_context`: what Outflare knows about the company, its memories and projects, and what is blocked, in one call. It asks only for what is missing, saves what you tell it with `update_context` and `remember`, and every failed call says the next step to take. See [docs/agent-memory.md](docs/agent-memory.md) for the design, and [mcp/README.md](mcp/README.md) for the tools and for Claude Desktop and Cursor setup. Longer things the business knows (policies, product details, stock answers) go in the [knowledge base](docs/knowledge.md), which agents search with `search_knowledge`.

### Missions

A mission is a goal an agent works toward inside a project, such as "book 10 meetings with fintech CFOs in Australia by 30 November". The agent acts with its ordinary API calls plus one header, `Outflare-Mission: <id>` (and optionally `Outflare-Reason: <why>`). Every call is logged against the mission, and the mission decides what each write does:

| Mission | A write |
| --- | --- |
| Draft, or waiting for approval | Dry run: recorded, never run, answered `202` |
| Running, mode `dry_run` | Dry run |
| Running, mode `approve_writes` | Held, answered `202`; a person approves it (it then runs as the agent) or rejects it |
| Running, mode `autonomous` | Runs, within `max_sends`, `max_spend_cents` and the token's own limits |
| Paused | Refused; reads still work and its sequences stop sending |
| Completed, stopped or failed | Every call refused |

The usual flow: the agent creates the mission (it starts as a draft run by its token), reads the project brief with `GET /v1/workspaces/{ws}/missions/{id}`, rehearses, asks a person to connect any tool it needs (`kind: "connect_request"`; the request closes by itself once the tool is connected, and the Missions page can send a one-time link to whoever holds the key), and submits its plan. A person approves it on the Missions page, or an approver token does over the API, and picks the mode. An agent can never approve its own mission or its own writes. The agent posts progress and questions, reads people's instructions when it checks in, and finishes with a report. Sequences it creates carry the mission, so sends, replies, meetings and spend roll up to it.

Give a mission agent `missions.read` and `missions.run` on top of the outreach scopes (Missions set to **Change** on a token). `missions.approve` is for people and approver bots.

#### From a goal, with Outflare's own agent

You don't need an agent of your own. Connect an AI model on the Connectors page with your own API key: **Claude**, **OpenAI**, **Gemini**, **Mistral**, **xAI**, **DeepSeek** (hosted in China), or a router, **OpenRouter**, **Vercel AI Gateway** or **Requesty** (one key for many vendors' models; usage is billed to your account with them). OpenAI and xAI also let some apps use a ChatGPT or SuperGrok plan instead of a key; the Connectors page tags them, and that sign-in is coming to Outflare. Choose which one and which of its models under **Settings → General → AI model** (`PUT /v1/workspaces/{ws}/ai-settings`), and, if you like, a different model per task (planning, agent turns, routine check-ins, reply labelling, domain research) so routine work runs on a cheaper model; under Claude Opus 5.5, check-ins default to Claude Sonnet 5.5, and reply labelling and domain research to Claude Haiku 5.5. Every workspace uses its own key: Outflare never supplies one. Then type a goal in the box at the top of the Missions page, or post it:

```sh
curl -X POST "$OUTFLARE_URL/v1/workspaces/$WS/missions/plan" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{"goal": "Book 10 meetings with fintech CFOs in Australia by 30 November"}'
```

The model drafts the mission from a summary of the workspace: mailbox counts and capacity, lead and suppression counts, sequence names, the project brief and which connectors are connected. Lead lists, email addresses and credentials are never sent. The draft holds the name, a goal to track, the steps, instructions, send and spend limits, an end date, the connectors it needs (as connect requests) and its questions. It is saved waiting for approval, so nothing runs until you approve it.

When you approve, choose who runs it. **Outflare's agent** (`"runner": "builtin"` on `POST .../approve`) carries out the plan with your AI model, in turns. It gets a token of its own (operator scopes, never more than yours, never `missions.approve`) and goes through the same gate as any agent, so the mode still decides whether its writes run, wait for you or are dry runs. A turn runs straight away, whenever you write to the mission or decide on a held change, and when the agent asks to look again (15 minutes to a day). Each plan's and turn's cost is recorded in the mission's spend, priced from the model's list price (Claude) or the price you enter with your choice (OpenAI, Gemini, Mistral); OpenRouter reports each call's cost. On top of each mission's limit, the workspace has a monthly AI limit (US$50 unless you change it): planning stops and the agent pauses when it is reached. The mission pauses with a reason if the model is disconnected, its key is refused, a limit is reached, the model declines, or three turns fail in a row.

Keys are handled with care, because an AI key can run up a bill. Each is encrypted with the integration it belongs to, sent only to its provider's own API (the engine fixes the address; only the operator can point it at a gateway with `ANTHROPIC_BASE_URL`, `OUTFLARE_OPENAI_BASE_URL`, `OUTFLARE_GEMINI_BASE_URL` or `OUTFLARE_MISTRAL_BASE_URL`), never returned by the API, logged, put in an event or shown to the model, and scrubbed from any error text. Outflare's agent treats what it reads from outside (email replies, lead fields, web pages, connector data) as data, never as instructions, and its email replies, test sends and changes to mailboxes, sending servers, senders, sign-ins, webhooks, suppressions, integrations, tokens and company context always wait for a person, even in autonomous mode. Use a key made just for Outflare and set a spend limit with the provider too. [ADR 9](docs/adr/0009-ai-models.md) has the details. **Your own agent** (`"runner": "external"`) works as described above.

## Develop

You need Go 1.25+, Node 22+, pnpm 10 and Postgres 16, with [pgvector](docs/workspace-search.md#self-hosting-pgvector) for meaning-based search (optional; the rest works without it).

```sh
make migrate          # create the schema in DATABASE_URL
make run              # engine: API + workers on :8080
make web              # web app on :5173, proxying the API
make test             # Go tests (needs TEST_DATABASE_URL)
```

## Layout

| Path | What | License |
| --- | --- | --- |
| `engine/` | Go engine: API, job queue, sending, inbox sync, policy check; `engine/outflare` is the extension API | AGPL-3.0 |
| `api/openapi.yaml` | The API contract; SDKs and docs are generated from it | AGPL-3.0 |
| `web/` | React web app | AGPL-3.0 |
| `sdk/ts/` | TypeScript SDK | Apache-2.0 |
| `sdk/python/` | Python SDK | Apache-2.0 |
| `mcp/` | MCP server for agents | Apache-2.0 |
| `deploy/` | Docker Compose for self-hosting | AGPL-3.0 |
| `docs/adr/` | Architecture decision records | |
| `docs/telemetry.md` | What the daily usage report sends, and how to turn it off | |
| `docs/monitoring.md` | Prometheus metrics, the Grafana dashboard and alert rules for running a server | |
| `docs/releasing.md` | How a version tag becomes an image and a GitHub release | |

## License

The core (engine, web app, API spec) is [AGPL-3.0](LICENSE). The SDKs, the MCP server and examples are Apache-2.0, so you can build on and integrate with Outflare without license obligations. See [docs/adr/0001-license.md](docs/adr/0001-license.md).

Self-hosted and the hosted Cloud run the same code: Cloud is this repository plus a private layer for billing, sign-up and operations, added through the extension API rather than a fork ([docs/adr/0005-open-core-cloud.md](docs/adr/0005-open-core-cloud.md)). Contributions need the [CLA](CLA.md).

## Links

- Website and docs: [outflare.ai](https://outflare.ai) (source: [outflareai/website](https://github.com/outflareai/website))
- Contact: [hello@outflare.ai](mailto:hello@outflare.ai)
- Security reports: see [SECURITY.md](SECURITY.md)
