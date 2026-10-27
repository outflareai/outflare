# 2. Go for the engine, TypeScript for what people build on

Status: Decided (22 October 2026)

## Decision

The engine (API, sending, inbox sync, scheduling, policy check) is Go. The SDK, CLI, MCP server and provider SDK are TypeScript. The web app is React with Vite.

## Why

The engine's work is I/O-bound: thousands of long-lived IMAP connections, SMTP and provider API calls, and Postgres. Go handles that with cheap goroutines, ships as one static binary that is easy to self-host, and is quick for contributors to learn. Throughput is capped by per-mailbox sending limits (tens of emails per inbox per day), so Rust's raw speed advantage would not be visible to users. TypeScript is where integration developers and agent builders already work.

A CPU-bound component (for example high-volume email parsing) can be added later in Rust without changing this.
