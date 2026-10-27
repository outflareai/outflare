# Agent memory and self-discovery

Outflare is used through many clients at once: Claude Desktop, ChatGPT, Cursor and other MCP clients, the CLI, the dashboard, and agents running missions. None of them share memory, and a chat forgets everything when it ends. So Outflare keeps what is worth remembering, and every client reads it with one call.

## What Outflare stores, and what it doesn't

Outflare remembers the business, not the conversation.

| Kept in Outflare (shared, lasting) | Kept by the client (this chat only) |
| --- | --- |
| Company context: company, website, what they do, goals, voice, rules, notes | The conversation and its reasoning |
| Memories: one-line preferences, decisions, facts and feedback | Drafts not accepted yet |
| Project briefs: offer, audience, market, ICP filters | Which calls it made and why |
| What happened: audit log, events feed, missions and their calls | Plans for this session |

Chat transcripts are never stored: the client keeps them, they are large and sensitive, and little in them is worth reusing. What is worth reusing the agent distills into a memory, tells the person in one line, and the person can correct or forget it on the Company page.

Memories are data agents read, never instructions that change what runs. Limits, scopes, mission gates and the send policy decide that.

## The pieces

- **Company context** (`GET`/`PATCH /v1/workspaces/{ws}/context`, table `workspace_context`): one per workspace, versioned like a project brief. `missing` lists the onboarding fields still empty (company, about, goals, voice); `ask_for_missing` turns false when they are filled or a person dismisses the question (`onboarding_dismissed: true`). The GET also returns pinned and recent memories, the projects, and a Markdown version for a prompt.
- **Brands** (`/v1/workspaces/{ws}/brands`, tables `brands` and `brand_context`): for a workspace that markets several businesses. Company context gains `brands` (how they relate); each brand has its own versioned context (about, audience, goals, voice, rules, notes). `GET /context?brand=` layers the brand's context on the company's, with company-wide and that brand's memories, and the projects that are the brand's or shared. Without `brand` it lists every brand in a line each. A project token reads its project's brand without asking.
- **Memories** (`/v1/workspaces/{ws}/memories`, table `memories`): up to 500 characters each, `kind` preference, decision, fact or feedback, optional `project_id` or `brand_id` (a project memory takes its project's brand), `pinned`, who saved it and the mission it came from. Saving the same text twice keeps one. At most 1000 per workspace. Project tokens see workspace-wide memories and their own project's.
- **Status** (`GET /v1/workspaces/{ws}/status`): blockers worst first, each with a stable `code`, `severity`, `count`, up to five `items`, a `detail` and a `fix` naming the MCP tool, the dashboard page and who must act (`agent` or `person`). Plus `can_send`, a set-up checklist and a one-line `summary`. Home's **Needs you** list shows the same blockers. It is the one to-do list for every module: mailboxes, domains, servers, connections, missions, sending, the fixes list, the inbox, analytics, lifecycle email (`email_domain_failed`, `email_broadcast_failed`), social drafts waiting for approval (`social_drafts_waiting`) and customer data (`duplicates_waiting`, `events_quarantined`, `crm_import_failed`). A new module that finds work for people adds a blocker here (or fixes on the fixes list), never a list of its own.
- **Scopes**: reading needs `workspace.read`; editing context and memories needs `context.manage`, which "Can make changes" on API tokens and the operator role include.
- **Events**: changes record `context.updated`, `brand.context_updated`, `memory.created`, `memory.updated` and `memory.deleted` in the audit log, in the same transaction.

## How each client uses it

- **MCP clients**: the server instructions tell the agent to call `get_context` first (with `brand` when the work is for one brand; `OUTFLARE_BRAND` sets a default). It returns the context, memories, projects and status in one answer. If `ask_for_missing` is true the agent asks for the missing fields in one message (or offers to read the website) and saves them with `update_context`. When the person states something lasting, the agent calls `remember`. The `set_up_outflare` prompt runs onboarding; `whats_blocked` walks the status.
- **Built-in mission agent**: the mission check-in (`GET /missions/{id}`) includes `company` with the context and pinned memories, and its system prompt tells it to write in the company's voice, within its rules, and to save memories.
- **Dashboard**: Company → About your company edits the context and lists memories (pin, edit, change kind, forget). Home's set-up card starts with it.
- **CLI and scripts**: `outflare get-context`, `outflare get-status`, `outflare create-memory --text "..."` and the rest come from the OpenAPI document like every other command.

## Errors an agent can act on

The MCP server turns every API error into a first line (`409 conflict: ... (request req_...)`), a `Next step:` line, and `structuredContent.error` with `status`, `code`, `param`, `message`, `request_id`, `retry` and `next_step`. Next steps are keyed by error code in `mcp/src/server.ts` (`nextStep`). When you add an error code to `engine/internal/api/errorcodes.go`, add its next step there if an agent should do something specific.
