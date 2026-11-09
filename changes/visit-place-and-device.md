---
kind: launch
area: analytics
---
Website visits and redirect link clicks now keep the visitor's region, city and network (internet provider or mobile carrier), their device's maker and model, their system and browser versions, and their language. Break visits down by any of them in Insights, or read them in Query.

Upgrade: runs migration 0504. Each server that answers visitors downloads DB-IP's free City Lite and ASN Lite files (about 65 MB, monthly, no key) from download.db-ip.com into OUTFLARE_GEOIP_DIR; set OUTFLARE_GEOIP_DOWNLOAD=off on a server without internet access and copy the files in yourself.
