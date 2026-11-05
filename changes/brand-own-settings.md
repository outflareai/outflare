---
kind: improvement
area: analytics
---
Each brand has its own attribution, ad consent and visitor settings, under `/v1/brands/{brand}/.../settings`, and two brands of one workspace can use the same flag key, exclusion group, conversion key or attribution model key. The workspace settings routes are deprecated and act on the first brand.
