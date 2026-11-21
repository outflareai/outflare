---
kind: launch
area: analytics
---
Your pages can now tell Outflare who a signed-in visitor is, safely. Turn on browser identify for a site in Website tracking, have your server sign each user id with the secret you get, and call `outflare.identify(userId, signature)` in the page. The visitor's earlier visits join their record. A page can't name a person without your server's signature.
