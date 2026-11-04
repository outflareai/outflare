---
kind: improvement
area: agents
---
Browser tasks now stay signed in to a brand's accounts between runs, like a person's browser, so they don't sign in and meet MFA every time. A session is saved only after the task confirms the right account, holds only that site's cookies, lasts at most 30 days, and is cleared when the site shows the wrong account. Remove the account's session secret to sign the agent out.
