# Builds the engine and the web app into one small image.

# The web app and the Go build run on the builder's own platform; Go
# cross-compiles, so multi-arch release images don't need emulation.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY web/package.json web/
COPY sdk/ts/package.json sdk/ts/
COPY mcp/package.json mcp/
RUN pnpm install --frozen-lockfile
COPY sdk/ts sdk/ts
COPY web web
COPY mcp mcp
# The app's "What's new" shows the newest releases from it.
COPY CHANGELOG.md ./
RUN pnpm --filter @outflare/sdk build && pnpm --filter @outflare/web build
# The MCP server as one file, which the engine runs and serves at /mcp.
RUN pnpm --filter @outflare/mcp build && pnpm --filter @outflare/mcp bundle

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS engine
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api api
COPY engine engine
COPY playbooks playbooks
COPY integrations integrations
ARG VERSION=dev
# Where the daily anonymous usage report goes (docs/telemetry.md); empty sends nothing.
ARG TELEMETRY_URL=
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -X github.com/outflareai/outflare/engine/internal/api.Version=${VERSION} -X github.com/outflareai/outflare/engine/internal/telemetry.DefaultURL=${TELEMETRY_URL}" \
    -o /out/outflare ./engine/cmd/outflare

# Node runs the hosted MCP server next to the engine (OUTFLARE_MCP_SERVER).
FROM gcr.io/distroless/nodejs22-debian12:nonroot
COPY --from=engine /out/outflare /outflare
COPY --from=web /src/web/dist /web
COPY --from=web /src/mcp/dist/outflare-mcp.mjs /mcp/outflare-mcp.mjs
ENV HTTP_ADDR=:8080 WEB_DIR=/web OUTFLARE_MCP_SERVER=/mcp/outflare-mcp.mjs OUTFLARE_NODE=/nodejs/bin/node
EXPOSE 8080
ENTRYPOINT ["/outflare"]
CMD ["serve"]
