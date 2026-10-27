# Classifier data

The global intelligence the classifier ships with. Workspace input
(overrides, feedback, paid verifier results) never goes into these files.

| File | Source | Licence | Refresh |
| --- | --- | --- | --- |
| `providers.yaml` | Curated by Outflare from each provider's own published domains | AGPL, with the engine | By hand, reviewed in a PR |
| `disposable_email_blocklist.conf` | [disposable-email-domains](https://github.com/disposable-email-domains/disposable-email-domains) | CC0 1.0 (`disposable_email_blocklist.LICENSE.txt`) | Download `disposable_email_blocklist.conf` over this file |
| `fakefilter.txt` | [7c/fakefilter](https://github.com/7c/fakefilter) `txt/data.txt` | BSD 3-Clause (`fakefilter.LICENSE.md`; keep it next to the data) | Download `txt/data.txt` over this file |

`providers.yaml` wins over the lists: a domain listed there is never called
disposable because a list says so, and the response shows the disagreement.
Before adding a list, check its licence allows commercial use and
redistribution, and add a row here.

When you refresh a list, bump `DataVersion` in `../data.go`. The tests refuse
a list that lost more than 20% of its domains since the last version.
