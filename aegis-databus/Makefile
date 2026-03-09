.PHONY: build test test-unit test-integration bench bench-validate run demo demo-full up down streams

BINARY := aegis-databus
STUBS := aegis-stubs
DEMO := aegis-demo

build:
	go build -o bin/$(BINARY) ./cmd/databus
	go build -o bin/$(STUBS) ./cmd/stubs
	go build -o bin/$(DEMO) ./cmd/demo

# Unit tests (no Docker, fast)
test-unit:
	go test -v ./pkg/... ./internal/...

# Integration tests (TC001-TC006, requires Docker + testcontainers)
test-integration:
	go test -v ./tests/...

# All tests (integration requires Docker)
test:
	go test -v ./...

# NFR-DB-001/002: throughput and latency benchmarks (requires Docker)
bench:
	go test -bench=. -benchmem ./tests/...

# Validate NFR targets (50K msg/s, 5ms P99)
bench-validate:
	./scripts/benchmark-validate.sh

run: build
	./bin/$(BINARY)

demo: build
	./bin/$(STUBS)

demo-full: build
	@./scripts/demo.sh

up:
	docker compose up -d

down:
	docker compose down

# FR-DB-007: Cluster failover (requires: make up, then AEGIS_TEST_CLUSTER=1)
test-cluster-failover:
	AEGIS_TEST_CLUSTER=1 go test -v -run TestFRDB007_ClusterFailover ./tests/...

# NFR-DB-003/010: Availability and scaling (availability requires Docker)
test-availability:
	go test -v -run TestNFRDB003 ./tests/...

# NFR-DB-010: 1-node vs 3-node throughput comparison (make up for cluster)
test-scaling:
	./scripts/test-scaling.sh

streams:
	@echo "Streams created by DataBus on first run. Use: nats stream ls"

# Zero Trust: generate NKey, update configs, create .env.nkeys
setup-zero-trust:
	go run ./cmd/setup-zero-trust

# Run with Zero Trust (NKey auth). Requires: make setup-zero-trust, then source .env.nkeys
up-secure:
	docker compose -f docker-compose.yml -f docker-compose.secure.yml up -d

# NFR-DB-006: Generate TLS 1.3 certs for demo
generate-tls-certs:
	./scripts/generate-tls-certs.sh

# Run with TLS 1.3. Requires: make generate-tls-certs
# Clients: AEGIS_NATS_URL=tls://127.0.0.1:4222 AEGIS_NATS_TLS_CA=config/certs/ca.pem
up-tls:
	docker compose -f docker-compose.yml -f docker-compose.tls.yml up -d
