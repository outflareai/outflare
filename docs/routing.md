# Routing

Routing decides which teammate gets a person or company that has no owner. A brand keeps a short list of rules, tried from the top. The first rule whose conditions hold gives the record to the member of its pool who owns the fewest of the brand's records, so work is shared evenly. Someone who already has an owner always keeps them.

Routing belongs to one brand. It works the same from the REST API (`/v1/brands/{brand}/routing`), the MCP server (`get_routing`, `set_routing`, `route_record`) and the app (CRM → Customer data → Routing). Reading needs `customers.read`, changing the rules `catalog.manage`, and routing one record by hand `customers.manage`.

## Rules

A rule has:

- a **name**, such as Enterprise or Europe;
- who it is about: **companies** (`account`) or **people** (`person`);
- up to five **conditions** on the record's fields, the same tests [rules](rules.md) and [scores](scores.md) use: `company_size` `gt` `200`, `country` `eq` `DE`, `scores.fit_band` `eq` `high`. With several, `match` says whether `all` must hold or `any`. A rule with no conditions and no territory takes every record of its type: keep one last as the catch-all, so nobody is left without an owner;
- a **territory**, when it should keep to one: `countries`, a list of two-letter country codes (`["DE", "FR"]`). The rule then takes only records whose Country field is one of them. A record with no country is in no territory, so a rule further down gets it;
- a **pool**: who gets what the rule takes. Either a list of one to 50 workspace members (`members`), or a team (`team_id`, from the workspace's teams). With one member, everything goes to them. With several, each record goes to whoever owns the fewest of the brand's records at that moment; on a tie, the member listed first. A team's pool is whoever is in the team when the record is routed, so a new teammate starts getting records by being added to the team, with nothing to change in the rules;
- a **status**: a `paused` rule is skipped.

A brand keeps up to 50 rules. The list is saved whole, in order:

```http
PUT /v1/brands/{brand}/routing
{"rules": [
  {"name": "Enterprise", "record_type": "account", "members": ["<user id>"],
   "conditions": [{"attribute": "company_size", "op": "gt", "value": 200}]},
  {"name": "DACH", "record_type": "account", "team_id": "<team id>", "countries": ["DE", "AT", "CH"]},
  {"name": "Everyone else", "record_type": "account", "members": ["<user id>", "<user id>"]}
]}
```

Send a rule with its `id` to keep it (and its count of records routed); a rule left out is removed.

## When records are routed

- **By themselves.** About once a minute Outflare looks at the people and companies with no owner that arrived or changed since it last looked, and routes each one a rule matches. A record that matches no rule stays without an owner until it changes or the rules do.
- **After the rules change.** Saving the rules has every record with no owner looked at again.
- **In a journey.** The "Assign an owner" step has `routing: true` ("Use the routing rules"), for routing at a chosen moment, such as when a lead becomes qualified. With `replace_owner` it also moves someone who has an owner.
- **By hand or by an agent.** `POST /v1/brands/{brand}/records/{record}/routing` routes one record now (`{"replace": true}` to change an owner already set), and `GET` on the same path says which rule matches and who would get it without changing anything.

Every owner routing sets is in the record's history with the source `routing`, and rules, journeys and webhooks that watch the owner field hear about it like any other change.

## What it doesn't do

Routing never takes a record from its owner by itself, and it doesn't rebalance: when a teammate joins a pool they get new records until they have as many as the others. A member who leaves the workspace is skipped; when a rule's whole pool has left, or its team is empty or has been deleted, its records stay without an owner and the page says so.
