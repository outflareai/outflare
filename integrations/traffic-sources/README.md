# Traffic-source templates

One file per network a brand buys clicks from, read by the engine
(`engine/internal/trafficsources`). A brand's traffic source starts as a copy
of a template, so getting a template right saves every brand that uses the
network from setting it up by hand.

```yaml
id: acme_native              # the file's name, lowercase a-z, 0-9 and _
name: Acme Native
category: native             # search_social, native, push_pop, display, mobile or adult
connector: acme_ads          # optional: the Outflare connector that syncs its spend
currency: USD                # optional, USD when left out
click_id: {param: clickid, token: "{click_id}"}   # leave the token out when the network adds the parameter itself
cost: {param: cost, token: "{cpc}"}               # optional
parameters:                  # up to 20, each into its own custom field (1-20)
  - {name: Zone ID, param: zone_id, token: "{zone_id}", field: 1}
postback:                    # optional: where the network takes a conversion
  url: "https://acme.example/postback?clickid={source_click_id}&payout={payout}&key=REPLACE"
  method: GET
note: "Anything a person setting it up needs to know."
sources:                     # where every token comes from: the network's own docs first
  - https://acme.example/docs/tracking-macros
```

- **Tokens on the left are the network's.** `token` is written exactly as the
  network fills it in (`{zone_id}`, `__CAMPAIGN_ID__`, `[CLICK_ID]`,
  `##ZONE_ID##`). They go into the campaign URL a brand pastes into the network.
- **Tokens in the postback are Outflare's.** `{source_click_id}` is the
  network's click id Outflare kept from the click; `{click_id}` is Outflare's
  own. The others are `{payout}`, `{currency}`, `{transaction_id}`,
  `{conversion_type}`, `{status}`, `{campaign}`, `{campaign_code}`,
  `{country}`, `{random}` and `{field1}` to `{field20}`, with the functions
  `|raw`, `|hash` and a percentage such as `{payout|80%}`.
- **`REPLACE`** marks a value only the brand has (an account id, a key). A
  brand can't save a postback until every one is filled in.
- **Only what you can source.** List where each token comes from. When the
  network's docs and trackers' guides disagree, leave the token out and say so
  in `note`, rather than guess: a wrong token silently breaks a brand's
  conversion tracking.

`go test ./engine/internal/trafficsources/` checks every file. The networks
Outflare syncs (Google, Meta and the rest) use the same macros as the site
script's tracking templates in `engine/internal/adtrack`, and the test checks
that the two agree.
