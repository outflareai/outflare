---
kind: fix
area: developer-setup
---
Developer setup no longer treats older payments from a history import as possible retries under new IDs. The retry check now compares only payments received within 24 hours of when they happened.
