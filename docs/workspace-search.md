# Search

Search finds customer records and their history by the words in them, or by what they mean ([below](#meaning-based-search)): people and companies by name, attribute text, email, phone or id, and the emails sent to and received from them, their meetings, deals and lifecycle email. It runs on Postgres full-text search, with an index for each kind (migration `0200`), so there is nothing to sync and no other service.

```sh
curl "$OUTFLARE_URL/v1/workspaces/$WS/search?q=pricing+team" -H "Authorization: Bearer $OUTFLARE_TOKEN"
```

```json
{"data": [{
  "kind": "message", "id": "…", "record_id": "…", "record_name": "Ada Lovelace",
  "title": "Re: your note", "detail": "received",
  "snippet": "Thanks. Could you send the pricing for the team plan before Friday?",
  "at": "2026-10-27T09:12:00Z", "rank": 0.42
}]}
```

Agents use the MCP tool `search_workspace`.

- **Matching.** Every word must match, each as the start of a word, so `pric` finds "pricing". Words aren't stemmed, so names, email addresses and words in any language match as written. What you type is never search syntax.
- **What is searched.** `record`: names, the text in attributes, and current emails, phones, ids and domains. `message`: outreach emails sent (subject) and mail received (subject and body). `meeting`: title and notes. `deal`: name and stage. `email`: lifecycle email subjects. `kinds=record,message` searches only some.
- **Results.** Ranked best first, records ahead of the rest, up to `limit` (default 20, at most 100). Each says whose it is (`record_id`, `record_name`), and a reply or meeting has a snippet of the text that matched. The kinds are the same as on a record's timeline.
- **Who sees what.** Each kind needs the read scope the API asks for it: `customers.read` for records, `sequences.read` for emails sent, `inbox.read` for mail received, `outcomes.read` for meetings and deals, `emails.read` for lifecycle email. A kind you can't read is left out. A caller kept to some brands finds their brands' rows and the shared ones, as on the record timeline.

To count or add things up rather than find them, use [Query](query.md).

## Meaning-based search

`mode=meaning` finds what means the same in other words: "unhappy with price" finds a reply saying "too expensive for us", and "asking about an API" finds "can we integrate it with our stack?". It covers records, mail received, meetings and deals (not lifecycle email or outreach sent); the same vectors are kept for [knowledge](knowledge.md) passages, which AI answers search with their own rules, with the same scopes and brands as above. Agents pass `mode: "meaning"` to `search_workspace`.

```sh
curl "$OUTFLARE_URL/v1/workspaces/$WS/search?q=unhappy+with+price&mode=meaning" -H "Authorization: Bearer $OUTFLARE_TOKEN"
```

**How it works.** Each record, reply, meeting and deal is turned into a vector: a list of 1,024 numbers that captures what the text means, so texts that mean similar things get similar numbers. Search turns what you asked into a vector the same way and returns the nearest ones. `rank` is then how similar they are, from -1 to 1. The vectors live in Postgres with [pgvector](https://github.com/pgvector/pgvector) (migration `0201`, table `search_embeddings`), next to the data, and hold no text: only the vector and a hash of the text it came from.

**What it costs, and whose key.** Making a vector is an AI call (an "embedding"), on the workspace's own AI connection, billed by that provider to the workspace's account. Outflare never supplies a key. It uses the AI model chosen under Settings → AI model when that provider makes embeddings, else the first connection that does: OpenAI (`text-embedding-3-small`, US$0.02 per million tokens), Gemini (`gemini-embedding-001`), Mistral (`mistral-embed`) or OpenRouter (`openai/text-embedding-3-small`, which reports its own cost). Claude doesn't offer embeddings, so a workspace on Claude alone needs one of the others for this. Each item is embedded once and again only when its text changes, and each search embeds only what was asked (the same question within 10 minutes is free), so for most workspaces this is cents a month. It shows under its own tool, **Meaning-based search** (`semantic_search`), in [AI usage](ai-usage.md), where it can have a monthly limit of its own; the workspace's AI limit applies too. Spend is booked rounded up to a cent per run of up to 512 items, so the books never show less than the provider charges.

**On by default.** It runs in every workspace with an AI connection that makes embeddings, within minutes of connecting one: a background job (`search.embed`) embeds what is new or changed every five minutes. An owner or admin can turn it off under Settings → Meaning-based search (`PUT .../semantic-search` with `{"enabled": false}`, or the MCP tool `set_semantic_search`). Off stops the calls and deletes the workspace's vectors; word search keeps working. `GET .../semantic-search` says whether it is on, which model makes the vectors, how many items are ready and waiting, and what is missing when it can't run.

**Deleted data.** A record that is deleted, merged into another or archived, and a reply, meeting or deal that is deleted, loses its vector on the next run. Deleting a workspace deletes its vectors.

### Self-hosting: pgvector

Meaning-based search needs the pgvector extension in the Postgres database. Without it everything else works as before, `GET .../semantic-search` says `semantic_search_unavailable`, and `mode=meaning` answers 503.

- **Managed Postgres:** most hosts (Neon, Supabase, Amazon RDS, Google Cloud SQL, Azure and others) include pgvector. Outflare adds it itself (`CREATE EXTENSION vector`) when its database user may; if not, have the database's owner run `CREATE EXTENSION vector;` once. A host whose Postgres has no pgvector usually offers a version or template that has it.
- **Your own Postgres:** install the `pgvector` package for your Postgres version (for example `postgresql-16-pgvector` on Debian and Ubuntu), or use the `pgvector/pgvector:pg16` image instead of `postgres:16`. The `deploy/docker-compose.yml` here still uses `postgres:16-alpine`, so existing installs don't change under you; change its image to switch.
- **Moving an existing database from `postgres:16-alpine` to `pgvector/pgvector:pg16`** changes the C library under it, which sorts text differently: run `REINDEX DATABASE outflare;` once after the switch, before starting Outflare.

Outflare checks again every five minutes, so there is no need to restart after adding pgvector. Version 0.8 or later searches a workspace's vectors best; older versions work.

## Events

Each search is an `api.called` event (`searchWorkspace`), plus `search.ran` with `mode` (words or meaning), the number of results by kind, how many words were searched and how long it took. The words themselves are never sent. Meaning-based search adds `search.embedded` for each run that embedded or deleted vectors (`items`, `unchanged`, `deleted`, `provider`, `duration_ms`), `search.embed_failed` with the error code when a run fails (`connector_auth` for a refused key, `ai_budget_reached`, `connector_unavailable`), and `semantic_search.updated` when it is turned on or off.
