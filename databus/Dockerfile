# syntax=docker/dockerfile:1
# Multi-stage builds for Aegis DataBus binaries. Non-root, static Go binaries, minimal Alpine runtime.
# Build: docker build --target aegis-databus -t aegis-databus:latest .
# See docs/CONTAINER.md for compose integration.

ARG GO_VERSION=1.24.0

# --- shared builder base ---
FROM golang:${GO_VERSION}-bookworm AS base
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0 \
    GOOS=linux

# Per-binary compile stages (trimpath + stripped symbols for smaller images)
FROM base AS build-databus
RUN go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/databus

FROM base AS build-demo
RUN go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/demo

FROM base AS build-stubs
RUN go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/stubs

FROM base AS build-memory-stub
RUN go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/memory-stub

FROM base AS build-orchestrator-stub
RUN go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/orchestrator-stub

# --- runtime base (non-root, CA bundle for TLS to NATS / OTLP) ---
FROM alpine:3.20 AS runtime-base
LABEL org.opencontainers.image.title="aegis-databus" \
      org.opencontainers.image.description="Aegis OS DataBus and demo binaries"
RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -g 65532 app \
    && adduser -D -u 65532 -G app app
USER app:app
WORKDIR /app

# DataBus: metrics & health on 9091
FROM runtime-base AS aegis-databus
COPY --from=build-databus /out/app /app/app
EXPOSE 9091
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q -O- http://127.0.0.1:9091/healthz >/dev/null || exit 1
ENTRYPOINT ["/app/app"]

# Demo / stubs: NATS clients only (no HTTP listener); omit image HEALTHCHECK — use compose depends_on + optional TCP checks
FROM runtime-base AS aegis-demo
COPY --from=build-demo /out/app /app/app
ENTRYPOINT ["/app/app"]

FROM runtime-base AS aegis-stubs
COPY --from=build-stubs /out/app /app/app
ENTRYPOINT ["/app/app"]

FROM runtime-base AS aegis-memory-stub
COPY --from=build-memory-stub /out/app /app/app
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -q -O- http://127.0.0.1:8090/v1/memory/ping >/dev/null || exit 1
ENTRYPOINT ["/app/app"]

FROM runtime-base AS aegis-orchestrator-stub
COPY --from=build-orchestrator-stub /out/app /app/app
EXPOSE 8091
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -q -O- http://127.0.0.1:8091/healthz >/dev/null || exit 1
ENTRYPOINT ["/app/app"]
