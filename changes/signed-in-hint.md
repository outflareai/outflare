---
kind: launch
area: analytics
---
The website script can tell your marketing site that a visitor is signed in to your app. Call `outflare.signedIn(true)` after sign-in and every page on your domain can ask `outflare.isSignedIn()`, show or hide elements with `data-outflare-signed-in`, and use a "Signed in" condition on flags and A/B tests. The cookie only says yes: it never says who the visitor is.
