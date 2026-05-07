# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
  -ldflags="-s -w" \
  -o memory-api ./cmd/server

# Production stage
FROM alpine:3.21

RUN apk --no-cache add ca-certificates
WORKDIR /app

COPY --from=builder /app/memory-api .

EXPOSE 8080

ENTRYPOINT ["/app/memory-api"]
