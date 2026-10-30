---
kind: improvement
area: workspace
---
Every workspace now starts with a brand, named after the workspace, and workspaces without one got one. Brands are becoming the owner of all their data, so two brands in one workspace never mix: their own API calls live under `/v1/brands/{brand}/`, starting with reading the brand itself.
