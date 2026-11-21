---
kind: fix
area: connectors
---
The Treg connector now reads an answer without an address as "no address found". Before, some of those answers were taken for a broken service, retried, and could switch the connection off for a while.
