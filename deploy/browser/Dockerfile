# The engine with Chromium, for the browser role (docs/browser-workers.md):
#   docker build -f deploy/browser/Dockerfile -t outflare-browser .
#   docker run -e OUTFLARE_ROLE=browser -e DATABASE_URL=... -e OUTFLARE_SECRET_KEY=... outflare-browser
# It takes the engine binary from the engine image built by the root
# Dockerfile (the same code, nothing else), on Debian with Chromium.
ARG ENGINE_IMAGE=outflare:latest
FROM ${ENGINE_IMAGE} AS engine

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends chromium ca-certificates fonts-liberation tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 outflare
COPY --from=engine /outflare /outflare
USER outflare
ENV OUTFLARE_ROLE=browser OUTFLARE_BROWSER=/usr/bin/chromium
# Go turns on Multipath TCP for listeners by default (Go 1.24+). Nothing
# here needs it, so turn it off: it only widens what the kernel's TCP code
# has to handle (for example CVE-2026-98432).
ENV GODEBUG=multipathtcp=0
ENTRYPOINT ["/outflare"]
CMD ["serve"]
