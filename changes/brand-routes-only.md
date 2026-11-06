---
kind: improvement
area: api
---
Brands are fully separate: the old workspace routes for a brand's data answer 410 `route_moved` with the `/v1/brands/{brand}/` route to call instead. Install health names any token or app still calling an old route, and the SDKs, MCP server and web app use the brand routes.
