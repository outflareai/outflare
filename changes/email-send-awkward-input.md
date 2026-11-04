---
kind: fix
area: lifecycle
---
Sending an email through the API no longer fails with a server error when a link is very long or the content holds a NUL character. Links over 2,000 characters are sent as written without click tracking, and a NUL character in the subject, body, tags, metadata or data is refused with a clear message.
