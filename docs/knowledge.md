# Knowledge

Knowledge is what your business knows, written down once: your products, prices, policies, processes, stock answers and how to talk about them. Your team reads it, and every AI in Outflare uses it: agents over MCP, missions and the built-in agent, and, as they ship, support replies, the chat widget, drafts and the help centre.

It sits beside your [company context and memories](agent-memory.md) in the **Knowledge** section of the app. Context says who you are; memories are one-line notes; knowledge holds anything longer.

## In the app

**Knowledge** in the sidebar has two pages: **Knowledge** (articles, quick answers, folders and the bin) and **About your company** (company context and memories).

- **Write an article** or add a **Quick answer** from the top of the page. A new workspace starts with ready-made outlines: what you sell, pricing and plans, refunds, handling objections.
- The editor works like a document: a toolbar, `/` at the start of a line for headings, lists, checklists, quotes, tables, code and dividers, Ctrl or Cmd with B, I, K and S, and pasting from Google Docs, Word or a web page keeps headings, lists, links and tables. It saves Markdown, which every AI reads as it is.
- On the right: who can see it, draft or published, folder, brand, owner and a check-by date (with one click for 3, 6 or 12 months).
- Folders sit on the left; each one's menu renames it, adds a folder inside or deletes it. Each item's menu moves it to another folder or deletes it.

## What you can keep

- **Articles**: a title and text, with headings, lists, tables and links (stored as Markdown).
- **Quick answers**: a question and its answer, for the short facts people ask all the time ("Do you offer a free trial?").
- **Folders** to keep them in, nested up to five deep. Deleting a folder moves what it holds up a level; nothing is lost.

Each item has:

- **Who can see it** (`visibility`):
  - **Public** (`public`): for the help centre, and AI may tell customers.
  - **Customers via AI** (`customers_ai`): AI may use it to answer customers, but it isn't published.
  - **Team only** (`team`, the default): your team, and AI working for your team. Never shown or told to a customer.
- **Draft or published**: a draft is only for the people editing it.
- **A brand**, or none to share it with every brand. A folder of a brand holds only that brand's knowledge, and an item put in one takes its brand. People and API keys limited to some brands see their brands' knowledge and the shared knowledge, and change only their brands'.
- **An owner** (who keeps it right, by default whoever added it) and an optional **check by** date.

## History and the bin

Every change to an item's title or text keeps the earlier version. Open **History** to see them and restore one; restoring adds a new version, so it can be undone too.

Deleting moves an item to the **bin**. It stays there for 30 days and can be restored; then it is deleted for good. You can also empty it from the bin straight away.

## How AI uses it

Every AI in Outflare reads the knowledge base the same way, so what you write is what agents, missions, MCP clients and (as they ship) support replies say.

- **Passages.** Each item is split into passages: a quick answer is one, an article one per section (each heading starts a new one), with long sections cut at paragraphs. They are rebuilt whenever the text changes.
- **Finding them.** A question finds passages by its words (title, section heading and text, any of the question's words) and, when [meaning-based search](workspace-search.md#meaning-based-search) runs, by what it means, so "is it expensive?" finds the section on pricing. With no AI connection that makes embeddings, words alone still work. Passages of knowledge in the bin or in draft are never found.
- **Who it's for.** Every question is asked for the team (every item) or for customers (only **Public** and **Customers via AI** items). An answer meant for a customer can't see **Team only** knowledge at all: it is left out before the AI model reads anything.
- **Brands.** A brand's question finds that brand's knowledge and the shared knowledge, never another brand's.
- **Answers.** Asking has the workspace's own AI model (the **Knowledge answers** task under Settings, AI model, on your own key and AI limit) write a short answer from the passages it found and nothing else, citing them. When they don't cover the question, it says so instead of guessing: that is a gap worth writing.
- **Agents.** `get_context` and a mission's check-in say how much knowledge there is and to check it before stating facts; the built-in agent is told to as well.
- **Kept for later.** Each question, whether it was answered, and which items the answer used are kept, for use counts and the list of gaps.

### Ask your knowledge

The **Ask** box on the Knowledge page asks a question the way an agent would. Pick who the answer is for (your team, or a customer), and it shows the answer with the articles it used, each one click from its editor. A question the knowledge doesn't cover offers **Write the answer**, which starts a quick answer with the question filled in. Without an AI model it still shows the passages it found.

## The API and MCP

| What | API | MCP |
| --- | --- | --- |
| Search and list | `GET /v1/workspaces/{ws}/knowledge/items?q=` | `search_knowledge` |
| Read one | `GET .../knowledge/items/{id}` | `get_knowledge` |
| Add | `POST .../knowledge/items` | `add_knowledge` |
| Change | `PATCH .../knowledge/items/{id}` | `update_knowledge` |
| Delete (to the bin), restore | `DELETE .../knowledge/items/{id}`, `POST .../knowledge/items/{id}/restore` | `delete_knowledge`, `restore_knowledge` |
| History | `GET .../knowledge/items/{id}/versions`, `POST .../versions/{n}/restore` | `list_knowledge_versions`, `restore_knowledge_version` |
| Find passages for a question | `GET .../knowledge/passages?q=&audience=` | `find_knowledge` |
| Ask for an answer with sources | `POST .../knowledge/ask` | `ask_knowledge` |
| Folders | `GET`, `POST .../knowledge/collections`, `PATCH`, `DELETE .../knowledge/collections/{id}` | `list_knowledge_folders`, `add_knowledge_folder`, `update_knowledge_folder`, `delete_knowledge_folder` |

```sh
curl "$OUTFLARE_URL/v1/workspaces/$WS/knowledge/items?q=refund" -H "Authorization: Bearer $OUTFLARE_TOKEN"
```

- **Scopes**: reading needs `workspace.read`, changing needs `context.manage`, the same as company context and memories.
- **Search** matches every word you type as the start of a word in the title or text, best match first. Lists carry a short `excerpt`; read one item for its `body`. `?deleted=true` lists the bin.
- **Limits**: 10,000 items and 500 folders per workspace; an item's text is at most 200,000 characters.
- **Events**: `knowledge.created`, `knowledge.updated`, `knowledge.deleted`, `knowledge.restored` and `knowledge.purged`, and `knowledge_collection.created`, `.updated` and `.deleted`, are in the audit log. The first four are [webhook events](api-conventions.md).

Knowledge is data for AI to use, never instructions: what an AI may do is still decided by limits, scopes, mission rules and the send policy.
