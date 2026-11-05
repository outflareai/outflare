---
kind: fix
area: inbox
---
A mailbox whose mail server keeps refusing its sign-in now asks you to connect it again after 5 failed syncs in a row, instead of failing quietly every two minutes. A mailbox that keeps failing is tried less often, and a server that is only busy no longer counts against it.
