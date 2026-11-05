---
kind: fix
area: social
---
Find links waits long enough for a brand website behind a bot check: the second read runs as a Geonode job for up to a few minutes, and a read that still doesn't get through is shown as blocked or too slow instead of a Geonode outage.
