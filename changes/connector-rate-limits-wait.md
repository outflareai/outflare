---
kind: improvement
area: connectors
---
A rate limit answer (HTTP 429) from web data, Parallel, EmailBison, Blitz or an email verifier is now waited out, honouring its Retry-After, instead of being read as an empty account; only out of credits (HTTP 402) still stops work for a person. Long provider calls (Treg, web data, Parallel) are no longer cut off after 30 seconds, and a finder call that timed out is never remembered as "no email".
