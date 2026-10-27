# 1. License: AGPL-3.0 core, Apache-2.0 SDKs

Status: Decided (22 October 2026)

## Decision

The engine, web app and API spec are AGPL-3.0-only. The SDKs, provider SDK and examples are Apache-2.0. Contributors sign a CLA.

## Why

Anyone can self-host, modify and build on Outflare. Integrating through the API or SDKs carries no copyleft obligation, because those are Apache-2.0. A company that runs a modified core as a hosted service must publish its changes, which protects the managed Cloud offering from closed clones. The CLA keeps the option to relicense later.

## Alternatives considered

- Apache-2.0 everywhere: widest adoption, but a competitor could host it as a closed service.
- Source-available (FSL or BSL): stronger protection, but not OSI open source, which undercuts the open-source positioning.
