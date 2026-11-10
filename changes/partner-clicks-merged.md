---
kind: improvement
area: partners
---
Partner short links now keep the same details as redirect links: where the visitor is, their device, and a keyed hash of their address. Clicks from link scanners and previews no longer count toward a partner's clicks. The click id a link adds (`ref_click`) is now a 16-character key; ids from older links still work as `click_id`.

Upgrade: runs migration 0509.
