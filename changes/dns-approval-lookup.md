---
kind: fix
area: connectors
---
The one-click DNS approval ("Approve in Cloudflare") now finds a domain's DNS provider on hosted installs whose resolver didn't answer cleanly for a subdomain, where it answered "lookup failed" and offered only the API connection.
