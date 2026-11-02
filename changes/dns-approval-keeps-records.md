---
kind: improvement
area: deliverability
---
Approving DNS records at Cloudflare now works for domains on Cloudflare DNS. When a domain already has a DKIM key at the same selector, a DMARC policy or its own MX record, Outflare offers the other ways to add the records instead, so the approval never puts a second record beside one you already have.
