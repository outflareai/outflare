# Affiliate-network templates

One file per affiliate network, or network platform, whose offers a brand
sends tracker clicks to. The engine reads them
(`engine/internal/affiliatenetworks`). A brand's postback source names its
template, and then:

- the postback URL the brand pastes into the network comes written in the
  network's own macros;
- the network's click id parameter is added to the URL of each of the brand's
  offers from that network.

```yaml
id: acme_network             # the file's name, lowercase a-z, 0-9 and _
name: Acme Network
kind: network                # platform (software many networks run on) or network
click_id_param: sub1         # where the click id goes in the network's offer links
macros:                      # the network's macro for each value Outflare's postback reads
  cid: "{sub1}"              # required: the click id it passes back
  payout: "{payout}"
  txid: "{transaction_id}"
  currency: "{currency}"     # optional, as are status and type
note: "Anything a person setting it up needs to know."
sources:                     # where every macro comes from: the network's own docs first
  - https://acme.example/docs/postback-macros
```

- **Macros are written exactly as the network fills them in**, such as
  `{sub1}`, `#s2#` or `[TID]`. The keys are Outflare's postback parameters:
  `cid`, `payout`, `txid`, `currency`, `status` and `type`.
- **Only what you can source.** List where each macro comes from. When the
  network's docs and trackers' guides disagree, leave the macro out and say so
  in `note`, rather than guess: a wrong macro silently breaks a brand's
  conversion tracking.
- A network that runs on a platform (Everflow, TUNE, CAKE, Affise) can use the
  platform's template; add its own file only when it differs.

`go test ./engine/internal/affiliatenetworks/` checks every file.
