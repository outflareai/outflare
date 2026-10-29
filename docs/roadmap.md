# v1 roadmap: sequencer core

Each step is one pull request.

1. Foundation: repo, licenses, Docker Compose, Postgres schema, job queue, API skeleton, OpenAPI, CI
2. Workspace and access: sign-in, workspaces, members, roles, API tokens, audit log
3. Mailboxes: CSV bulk import, SMTP and IMAP sign-in checks, SPF/DKIM/DMARC health, pools and daily limits, encrypted credentials
4. Leads and suppression: CSV import, dedupe, lists, suppression list
5. Sequences and sending: sequence builder, scheduler, policy check, SMTP sending, rotation, unsubscribe and bounce handling
6. Inbox: IMAP sync, unified inbox, threads, reply, stop on reply, classification
7. Cheap Inboxes, Google and Microsoft: Cheap Inboxes connector (import, app passwords, sync by webhook and polling), admin-level authorisation, OAuth
8. Web app polish and TypeScript SDK
9. MCP server: agents run a workspace through the API with scoped tokens
10. Sending domains: per-domain health with fixes, daily DNS and blocklist checks, sending stats, pause and daily cap in the policy check
11. Warm-up: switched on and off in a connected sequencer (EmailBison, Instantly, Smartlead), with its warm-up score per mailbox. Outflare sends no warm-up mail itself
12. Mailbox allocation: teams and senders, one sequence per mailbox, automatic or suggested assignment with reasons and undo, priorities, locks, drains that keep follow-ups on their mailbox, and agent limits on API tokens
13. Outside platforms: EmailBison, Smartlead and Instantly campaigns in the same allocation, on the shared connectors framework and Apollo watched read-only (done)
14. Projects: one motion per project with a versioned brief (shipped, then removed in favour of brands and sequences; see ADR 12)
15. Outcomes and cost: meetings from Google Calendar, Outlook, Calendly, Cal.com and HubSpot, deals from HubSpot, a spend ledger tagged by sequence, brand and actor, cost per meeting by sequence and brand, warehouse export
16. Agent missions: goals agents run with the company and brand context, dry runs, plan approval, held writes, send and spend limits, connect requests and a log of every call
17. Lead lists from data providers: searches at the customer's own Apollo or People Data Labs account, an email waterfall with a cache and credit caps, ICP fit scores, workspace-wide dedupe, verification and scheduled refreshes; signal connectors next
18. Goal to mission: type a goal and your AI model (Claude, OpenAI, Gemini or Mistral) drafts the mission with your own key; Outflare's built-in agent runs it in turns through the same gate
19. Usage stats and update checks: an anonymous daily report from self-hosted servers (opt out on first run, in Settings or with `OUTFLARE_TELEMETRY=off`), update and security notices in Settings → This server, and the collector and installs dashboard in Cloud
20. Analytics connectors: read Google Analytics, Search Console, Bing Webmaster Tools, PostHog, Plausible, Fathom, Matomo, Umami, Mixpanel, Amplitude, Cloudflare, Simple Analytics, Clarity and Vercel through five shared reports, on an Analytics page, the API and MCP
21. Analytics history: a sync stores every tool's daily numbers so websites line up by domain across tools, with an overview, a day-by-day comparison and history queries over the API and MCP
22. SEO connectors: DataForSEO and Ahrefs for domain rating, backlinks, keyword rankings, competitors and site audit, with monthly budgets, costs in Outcomes and weekly snapshots joined to each website
23. Analytics for agents: a what-changed answer per website (headlines, period on period per tool, movers, unusual days, SEO moves, gaps), a catalog of what each website can be asked, MCP prompts and compact answers

Later: a built-in CRM on the customer records, with a one-off import from Attio (records available now; lists, notes and tasks next); more CRMs (Salesforce, Pipedrive) to import from, provider marketplace, Cloud.
