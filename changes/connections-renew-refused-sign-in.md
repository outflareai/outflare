---
kind: fix
area: connectors
---
A signed-in connection such as Google Search Console no longer breaks when the provider refuses a token early: Outflare renews the sign-in and checks again first. When it really has expired, the connection says so in plain words, with a Sign in again button that now works on an expired connection and also mends the other connections to the same Google account.
