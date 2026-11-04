# Bulk edits

A bulk edit changes many people or companies of one brand at once: give them an owner, add or remove tags, set or clear a field, put them in or take them out of a static list, or archive them. Only the workspace's **owners**, signed in, run one. Anyone who may change records can preview it first.

Every change a bulk edit makes is written to each record's own history, with who ran it and which bulk edit it was. Nothing is recorded only as a total. An undo adds history too: the record shows the change and the undo, and the original change is never erased.

## Which records

Pick exactly one:

- **A smart or static list** (`smart_list_id`): its members now. A list of companies changes companies.
- **A saved query** (`saved_query_id`): its current version runs, and its `record_id` column (or `id`) picks the records, up to 10,000 rows. Write it in [Query Studio](query.md), for example `SELECT id FROM people WHERE country = 'DE'`.
- **Records by id** (`record_ids`): up to 10,000, picked by hand.

In the app, start one from a list's menu, from a saved query's menu in Query (Bulk edit its records), or on the People or Companies page: tick the records you want, or tick none to change every record shown.

The records are the brand's live people or companies of one kind: merged and deleted records are left out, and one bulk edit changes people or companies, not both. A bulk edit changes up to 50,000 records. The records are fixed when the bulk edit is made, so it changes exactly what the preview counted.

## What it changes

```json
{
  "smart_list_id": "…",
  "changes": {
    "set": { "owner": "<user id>", "segment": "enterprise", "region": null },
    "add_tags": ["churn-risk"],
    "remove_tags": ["trial"],
    "add_to_list": "<static list id>",
    "remove_from_list": "<static list id>",
    "archive": true
  }
}
```

- `set` writes fields by key, checked against the [catalog](customers.md#the-catalog) like a hand edit; `null` clears a field. `owner` is a field, so this is how you reassign records.
- `add_tags` and `remove_tags` keep the record's other tags.
- `add_to_list` and `remove_from_list` are static lists of the brand, and work on people only.
- `archive` archives each record. Deleting is not a bulk edit: use Recently deleted for that.

A bulk edit writes as a person (source `manual`), so it wins over imports and syncs by the usual [source precedence](customers.md#which-source-wins). Each record it changes is a `record.updated` (or `record.archived`) like a hand edit, so [rules](rules.md), journeys and webhooks act on it.

## Preview, run, undo

1. `POST /v1/brands/{brand}/bulk-edits/preview` checks the changes and shows how many records, and the first 20 with each change from and to. `can_run` says whether you may run it.
2. `POST /v1/brands/{brand}/bulk-edits` (owners) makes it and answers `202`. It runs in the background, 200 records at a time; `GET .../bulk-edits/{id}` shows its `status` (queued, running, done or failed) and `counts`: records `changed`, `unchanged` (already so) and `skipped` (deleted or merged since), and the history rows it wrote (`fields`, `lists`).
3. `POST .../bulk-edits/{id}/undo` (owners, within 7 days) puts the records back: each field goes back unless someone changed it since, archived records are restored, and list moves are reversed. What changed since stays and is counted as `kept`. The undo is a bulk edit of its own (source `undo`).

A bulk edit that fails part way after its retries keeps what it did (`counts.done` records) and can be undone.

## Where to see it

- **On each record:** `GET .../records/{record}/history` lists each field change with `bulk_edit_id` (archiving shows as the field `archived`), and the timeline shows list moves with `bulk_edit_id` in their detail.
- **All bulk edits:** `GET /v1/brands/{brand}/bulk-edits`, newest first, and the `bulk_edits` table in [Query](query.md).
- **The audit log:** `bulk_edit.started`, `bulk_edit.finished` and `bulk_edit.undone`.

MCP has `list_bulk_edits`, `get_bulk_edit` and `preview_bulk_edit`. An agent can prepare and preview a bulk edit, and an owner runs it.
