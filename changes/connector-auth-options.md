---
kind: improvement
area: api
---
Connectors and data providers now say how to connect in one place: `auth_options` lists each way (sign in or API key), whether it works on this server and which is recommended. The repeated `auth` and `auth_methods` fields are gone from the API.
