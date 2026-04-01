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
  -o vault

# Production stage
FROM alpine:3.23.3

WORKDIR /

COPY --from=build /app/vault vault
COPY --from=build /etc/passwd /etc/passwd

RUN chmod +x /vault

USER altuser

EXPOSE 8000

CMD ["/vault"]
