---
kind: improvement
area: api
---
Google and Microsoft mailboxes connected before mailbox sign-ins were connections now sign in through Gmail and Outlook connections: they are moved automatically, keep sending, and show with your other connections. The older `/oauth-connections` and `/oauth/{provider}/authorize` routes, and `oauth_providers` in `/v1/meta`, are gone.

Upgrade: nothing to do. The move runs once when the server starts; a mailbox that cannot be moved is logged and needs connecting again.
