---
kind: fix
area: crm
---
**Connect Gmail** in Sales (My email) and in CRM email no longer fails with Google's "redirect_uri_mismatch" error. Both now return to the same Google address as "Continue with Google".

Upgrade: the Google OAuth client needs only the `/v1/auth/google/callback` redirect URI for these; `/v1/sales/google/callback` and `/v1/crm-mail/google/callback` are no longer used for new connections.
