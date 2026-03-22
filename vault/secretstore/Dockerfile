# Build stage
FROM golang:1.25.5-alpine AS build

WORKDIR /app

COPY go.mod ./
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  go mod download

RUN adduser -D -u 1001 altuser

COPY . .

RUN CGO_ENABLED=0 go build \
  -ldflags="-s -w" \
  -o secretstore

# Production stage
FROM alpine:3.23.3

WORKDIR /

COPY --from=build /app/secretstore secretstore
COPY --from=build /etc/passwd /etc/passwd

RUN chmod +x /secretstore

USER altuser

EXPOSE 8001

# ENGINE_TOKEN must be set at runtime — the service will refuse to start without it.
# PORT defaults to 8001 if not set.
CMD ["/secretstore"]
