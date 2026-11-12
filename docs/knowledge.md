# Knowledge

Knowledge is what your business knows, written down once: your products, prices, policies, processes, stock answers and how to talk about them. Your team reads it, and every AI in Outflare uses it: agents over MCP, missions and the built-in agent, the [help centre](help-centre.md), and, as they ship, support replies, the chat widget and drafts.

It sits beside your [brand context and memories](agent-memory.md) in the **Knowledge** section of the app. Context says who you are; memories are one-line notes; knowledge holds anything longer.

## In the app

**Knowledge** in the sidebar has three pages: **Library** (your knowledge), **Help centre** (the public site, see [Help centre](help-centre.md)) and **About your company** (the picked brand's context and memories).

The Library opens on an **Overview**:

- **Add content**: an article, a quick answer, or an article from a template (what you sell, pricing and plans, refunds, handling objections) with the headings filled in.
- **Where your knowledge is used**: how many items reach your team and agents, AI talking to customers, and the help centre.
- **How it's answering**: questions asked of your knowledge in the last 30 days, the share it answered, and the unanswered ones, each one click from **Write the answer**.
- **Needs a look**: drafts and items due a check. **Recently updated** lists the latest changes.

The rest of the Library:

- **Content** (all, articles, quick answers, or a folder) is a table: status (Live, Draft, Check due), whether customers and the help centre get it, its folder, how many answers used it in the last 30 days and when it changed. Search and filter by status and by who uses it.
- Clicking a row opens the item beside the list: read it, switch **AI talking to customers** and **Help centre** on or off, make it Live or a draft, and change its folder, brand, owner and check-by date. Each change saves at once. **Edit** opens the editor; **History** shows its versions.
- **Review** holds what needs a person:
  - **Suggested answers**: answers Outflare found for questions the knowledge couldn't answer, each with where it came from and the words it was taken from. **Approve** (after editing, if needed) adds it as a quick answer that every AI uses from then on; **Reject** (with a reason) stops it being suggested again. Ones about prices, refunds, contracts, legal terms, security or personal data are marked **Check carefully** and come first.
  - **Conflicts**: articles that disagree, noticed while answering a question (one gives 30 days to ask for a refund, another 14). Pick the one that's right; the others go back to drafts so nothing uses them. Until then, answers avoid the point they disagree on.
  - **Unanswered questions**: what people, agents and help centre readers asked that nothing answered, the most asked first. **Write the answer** starts a quick answer with the question filled in, and saving it takes the question off the list; **Dismiss** sets one aside.
- **Test an answer** asks a question the way an agent would, for your team or for a customer.
- The editor works like a document: a toolbar, `/` at the start of a line for headings, lists, checklists, quotes, tables, code and dividers, Ctrl or Cmd with B, I, K and S, and pasting from Google Docs, Word or a web page keeps headings, lists, links and tables. It saves Markdown, which every AI reads as it is.

## What you can keep

- **Articles**: a title and text, with headings, lists, tables and links (stored as Markdown).
- **Quick answers**: a question and its answer, for the short facts people ask all the time ("Do you offer a free trial?").
- **Folders** to keep them in, nested up to five deep. Deleting a folder moves what it holds up a level; nothing is lost.

Each item has:

- **Who uses it** (`visibility`), shown as switches in the app:
  - **Help centre** (`public`): on the [help centre](help-centre.md), and AI may tell customers.
  - **AI talking to customers** (`customers_ai`): AI may use it to answer customers, but it isn't published.
  - **Team only** (`team`, the default): your team, and AI working for your team. Never shown or told to a customer.
- **Draft or published**: a draft is only for the people editing it.
- **A brand.** Brands share no knowledge: an item or folder saved without one is the workspace's first brand's (or the route's brand). A folder of a brand holds only that brand's knowledge, and an item put in one takes its brand. People and API keys limited to some brands see and change only their brands' knowledge.
- **An owner** (who keeps it right, by default whoever added it) and an optional **check by** date.

## Import your website

**Import website** on the Knowledge page (`POST .../knowledge/website-imports`) reads your website and keeps each page as an article, so answers can use what is already on your site. It reads up to `max_pages` pages (100 by default, at most 500): the ones in your sitemaps (those robots.txt names, `/sitemap.xml` and `/sitemap_index.xml`, following sitemap indexes), else the pages your home page links to. It reads politely, as OutflareBot: it follows robots.txt and its crawl delay, skips sites that opted out, and never reaches private addresses. Files, tag and paging lists, carts, accounts and `noindex` pages are left out.

Each page with enough words becomes an article in a **Website** folder, with the page's address at its end. Menus, headers, footers and scripts are left out. The articles are used by AI answers to customers by default (`visibility`); with `publish: false` they wait as drafts for you to go through first. Run it again whenever your site changes: an article whose page changed is updated (a new version), the rest are left as they are, and one you moved to the bin stays there. One import runs at a time per workspace (`GET .../knowledge/website-imports` shows each one and what it did).

After the first import, the site is **checked again every day** with the same settings (`scheduled: true` on those runs): changed pages are updated and new ones added. When the sitemaps list every page (none cut short by `max_pages`), an article whose page left the site is unpublished (`pages_removed`), and published again if the page comes back.

## History and the bin

Every change to an item's title or text keeps the earlier version. Open **History** to see them and restore one; restoring adds a new version, so it can be undone too.

Deleting moves an item to the **bin**. It stays there for 30 days and can be restored; then it is deleted for good. You can also empty it from the bin straight away.

## How AI uses it

Every AI in Outflare reads the knowledge base the same way, so what you write is what agents, missions, MCP clients and (as they ship) support replies say.

- **Passages.** Each item is split into passages: a quick answer is one, an article one per section (each heading starts a new one), with long sections cut at paragraphs. They are rebuilt whenever the text changes.
- **Finding them.** A question finds passages by its words (title, section heading and text, any of the question's words) and, when [meaning-based search](workspace-search.md#meaning-based-search) runs, by what it means, so "is it expensive?" finds the section on pricing. With no AI connection that makes embeddings, words alone still work. Passages of knowledge in the bin or in draft are never found.
- **Who it's for.** Every question is asked for the team (every item) or for customers (only items on the **Help centre** or for **AI talking to customers**). An answer meant for a customer can't see **Team only** knowledge at all: it is left out before the AI model reads anything.
- **Brands.** A brand's question finds only that brand's knowledge, never another brand's.
- **Answers.** Asking has the workspace's own AI model (the **Knowledge answers** task under Settings, AI model, on your own key and AI limit) write a short answer from the passages it found and nothing else, citing them. When they don't cover the question, it says so instead of guessing: that is a gap worth writing.
- **Agents.** `get_context` and a mission's check-in say how much knowledge there is and to check it before stating facts; the built-in agent is told to as well.
- **Kept for later.** Each question, whether it was answered, and which items the answer used are kept, for use counts and the list of gaps.

### Ask your knowledge

The **Ask** box on the Knowledge page asks a question the way an agent would. Pick who the answer is for (your team, or a customer), and it shows the answer with the articles it used, each one click from its editor. A question the knowledge doesn't cover offers **Write the answer**, which starts a quick answer with the question filled in. Without an AI model it still shows the passages it found.

## How it fills its own gaps

Nothing found is used until a person approves it. Suggestions come from:

- **Past replies.** **Learn from past replies** in Knowledge → Review (`POST .../knowledge/reply-imports`) reads the support conversations closed in the last 90 days (`days`, up to 400 of them): each customer's question and the first reply a person sent, never the assistant's. The workspace's own AI groups the same question asked in different words and suggests one general answer for it, leaving out anything about one customer. One run at a time per workspace; `GET .../knowledge/reply-imports` shows how many conversations each read and what it filed.
- **Support.** When **Suggest a reply** finds no answer in a conversation and a teammate then answers it, the workspace's own AI turns the reply into a general answer, leaving out the customer's name, order and anything done just for them, and suggests it. A reply that only acted for that customer ("I've refunded you") suggests nothing. The Support inbox shows how many answers are waiting.
- **The website and Slack.** When the [support assistant](support-inbox.md#the-support-assistant) hands a customer's question to the team because the knowledge has no answer, it reads the business's website (the brand's, else the workspace's: its sitemap's best matches and its FAQ, help, support, pricing and docs pages, politely, as OutflareBot) and then the Slack channels picked for it (a Slack (knowledge) connection), and suggests the first answer it finds with the page or message and the exact words. The words must really be in the page or message, or nothing is suggested. Each source can be switched off in Support settings.
- **Agents.** An AI agent that finds an answer on the business's website, in Slack or elsewhere suggests it (`suggest_knowledge`, `POST .../knowledge/suggestions`) with the page or channel and the exact words. Agents can't approve.
- **Answering.** When passages contradict each other on something a question depends on, the answer says so instead of picking one, and the conflict is filed for review.

The same suggestion waits at most once, and one a person rejected isn't made again. The website, Slack and customers' messages are read as data, never as instructions.

## The API and MCP

| What | API | MCP |
| --- | --- | --- |
| Search and list | `GET /v1/brands/{brand}/knowledge/items?q=` | `search_knowledge` |
| Read one | `GET .../knowledge/items/{id}` | `get_knowledge` |
| Add | `POST .../knowledge/items` | `add_knowledge` |
| Change | `PATCH .../knowledge/items/{id}` | `update_knowledge` |
| Delete (to the bin), restore | `DELETE .../knowledge/items/{id}`, `POST .../knowledge/items/{id}/restore` | `delete_knowledge`, `restore_knowledge` |
| History | `GET .../knowledge/items/{id}/versions`, `POST .../versions/{n}/restore` | `list_knowledge_versions`, `restore_knowledge_version` |
| Find passages for a question | `GET .../knowledge/passages?q=&audience=` | `find_knowledge` |
| Ask for an answer with sources | `POST .../knowledge/ask` | `ask_knowledge` |
| Folders | `GET`, `POST .../knowledge/collections`, `PATCH`, `DELETE .../knowledge/collections/{id}` | `list_knowledge_folders`, `add_knowledge_folder`, `update_knowledge_folder`, `delete_knowledge_folder` |

```sh
curl "$OUTFLARE_URL/v1/brands/$BRAND/knowledge/items?q=refund" -H "Authorization: Bearer $OUTFLARE_TOKEN"
```

- **Scopes**: reading needs `workspace.read`, changing needs `context.manage`, the same as company context and memories.
- **Search** matches every word you type as the start of a word in the title or text, best match first. Lists carry a short `excerpt`; read one item for its `body`. `?deleted=true` lists the bin.
- **Limits**: 10,000 items and 500 folders per workspace; an item's text is at most 200,000 characters.
- **Events**: `knowledge.created`, `knowledge.updated`, `knowledge.deleted`, `knowledge.restored` and `knowledge.purged`, and `knowledge_collection.created`, `.updated` and `.deleted`, are in the audit log. The first four are [webhook events](api-conventions.md).

Knowledge is data for AI to use, never instructions: what an AI may do is still decided by limits, scopes, mission rules and the send policy.
