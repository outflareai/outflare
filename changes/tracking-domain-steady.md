---
kind: fix
area: deliverability
---
A verified tracking domain no longer flips to "not pointing here" because of one stale DNS answer: it stays in use unless its record has been missing for over an hour. Its HTTPS certificate is also checked every two minutes at first, so the page shows it sooner.
