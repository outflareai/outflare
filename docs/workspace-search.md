# Search

Search finds customer records and their history by the words in them: people and companies by name, attribute text, email, phone or id, and the emails sent to and received from them, their meetings, deals and lifecycle email. It runs on Postgres full-text search, with an index for each kind (migration `0200`), so there is nothing to sync and no other service.

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

## Events

Each search is an `api.called` event (`searchWorkspace`), plus `search.ran` with the number of results by kind, how many words were searched and how long it took. The words themselves are never sent.
