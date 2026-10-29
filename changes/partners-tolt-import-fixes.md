---
kind: fix
area: partners
---
Partners: the Tolt import keeps Tolt's own customer id (`cus_...`) on each referred customer, so renewals your backend reports with it still credit the partner (run the import again to add it to customers already imported). An import whose pages don't advance now stops with `pagination_stuck` instead of reading the first page forever.
