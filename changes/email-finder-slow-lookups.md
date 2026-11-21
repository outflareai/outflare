---
kind: fix
area: connectors
---
A slow email lookup no longer stalls the email step. When a connected finder such as Treg gives no answer in time for a person, that counts as no address from it and the step moves on, where before it asked again up to five times and could switch the connection off for a while.
