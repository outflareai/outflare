---
kind: fix
area: paid-ads
---
Paid ads tables set to "All" now list every campaign, ad group and ad, including paused ones that never spent. Before, a campaign with no spend in the dates shown was left out whatever the status filter said. The API takes `idle=true` on the campaign, ad and performance lists for the same.
