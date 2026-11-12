---
kind: improvement
area: seo
---
Paid link orders can now be queried as `coverage_link_orders` and `coverage_donor_profiles` (with `coverage.read`), and the product guide is in the docs. Catalog import is CSV only; a Google Sheet is not imported. SE Ranking does not feed referring-domain candidates.

Upgrade: this version adds migration 0521, which creates the two Query views.
