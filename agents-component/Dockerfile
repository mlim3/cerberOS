# Stage 1: build both binaries.
FROM golang:1.24-alpine AS builder

WORKDIR /src

# Download dependencies before copying source so this layer is cached across
# code changes that don't touch go.mod / go.sum.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/aegis-agents  ./cmd/aegis-agents
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/agent-process ./cmd/agent-process
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/simulator     ./cmd/simulator

# Stage 2: minimal runtime image.
# Both binaries land in /app so AEGIS_AGENT_PROCESS_PATH=/app/agent-process
# resolves without any extra configuration.
FROM alpine:3.21

# ca-certificates is required for HTTPS calls made by the agent-process binary
# (web_fetch tool, Anthropic API). Without it TLS verification fails.
RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /out/aegis-agents  /app/aegis-agents
COPY --from=builder /out/agent-process /app/agent-process
COPY --from=builder /out/simulator     /app/simulator

# Prometheus metrics endpoint — exposed so docker-compose and external scrapers
# can reach it. The actual port is controlled by AEGIS_METRICS_PORT (default 9090).
EXPOSE 9090

ENTRYPOINT ["/app/aegis-agents"]
