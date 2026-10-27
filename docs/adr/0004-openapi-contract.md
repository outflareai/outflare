# 4. OpenAPI as the single contract

Status: Recommended (22 October 2026)

## Decision

`api/openapi.yaml` describes every endpoint. The TypeScript SDK, the MCP tools and the API docs are generated from it. The engine serves it at `/v1/openapi.yaml`, and `/.well-known/outflare.json` points agents to it.

## Why

The web app, CLI, SDKs, MCP server and third-party agents all use the same API. One machine-readable contract keeps them in step and lets anyone generate a client in any language.
