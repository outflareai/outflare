---
kind: improvement
area: api
---
A connected sequencer account's name is now `integration_name` everywhere, beside `integration_id`: on campaign links (was `platform`), platform accounts (was `name`), a mailbox's assignment and its `platforms` list (was `platform`) and a sequence's assigned mailboxes (was `platform`). `connector` stays the connector's id.
