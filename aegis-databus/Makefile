.PHONY: build test run demo demo-full up down streams

BINARY := aegis-databus
STUBS := aegis-stubs
DEMO := aegis-demo

build:
	go build -o bin/$(BINARY) ./cmd/databus
	go build -o bin/$(STUBS) ./cmd/stubs
	go build -o bin/$(DEMO) ./cmd/demo

test:
	go test -v ./...

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

streams:
	@echo "Streams created by DataBus on first run. Use: nats stream ls"
