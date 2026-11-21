---
kind: improvement
area: general
---
Every error is now an event you can keep on your own server. Once someone is signed in, the web app sends its own events (an error shown, a crash, a page opened) to the server, so they reach your events workspace with or without PostHog. Errors and warnings the server only wrote to its log become events too, with where in the code they were logged.
