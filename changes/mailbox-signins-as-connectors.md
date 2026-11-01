---
kind: improvement
area: api
---
Gmail and Outlook mailbox sign-ins are now connections like any other: signing in through the Gmail or Outlook connector adds the mailbox, and an admin connection (a Google service account or a Microsoft Entra app) adds mailboxes by address. The older mailbox sign-in routes are deprecated.

Upgrade: register `$PUBLIC_URL/v1/integrations/oauth/callback` as a redirect URI on your Google and Microsoft OAuth apps, and give the Microsoft app the delegated permissions `IMAP.AccessAsUser.All` and `SMTP.Send`.
