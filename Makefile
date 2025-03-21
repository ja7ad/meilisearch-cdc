GO ?= go
BIN := bin/meilisearch-cdc

.PHONY: gosec golangci check build run test test-race integration e2e ui-test ui-real e2e-env e2e-env-down ui-full lint docker demo bench load pgo

build:
	$(GO) build -trimpath -o $(BIN) ./cmd/meilisearch-cdc


run: build
	./$(BIN)

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

# Needs Docker: connectors and the engine against real services.
integration:
	$(GO) test -race -tags integration -count=1 -timeout 30m ./...

# Needs Docker: builds the binary and syncs real databases end to end.
e2e:
	$(GO) test -tags e2e -count=1 -timeout 40m ./test/e2e/

# Dashboard tests (Playwright). UI_BASE_URL=http://localhost:8080 targets a real server.
ui-test:
	cd test/ui && npm ci && npx playwright install chromium && npx playwright test

# Dashboard tests against the real service, Meilisearch and a SQLite source.
ui-real:
	cd test/ui && npm ci && npx playwright install chromium && npx playwright test -c playwright.real.config.mjs && npx playwright test -c playwright.real-multi.config.mjs

# Full GUI end-to-end environment: every source, two Meilisearch instances
# and Mailpit in Docker (see docs/e2e.md).
e2e-env:
	docker compose -f deploy/e2e/docker-compose.yml up -d --wait

e2e-env-down:
	docker compose -f deploy/e2e/docker-compose.yml down -v

# Needs `make e2e-env`, then the lab and the service running (docs/e2e.md).
ui-full:
	cd test/ui && npx playwright test -c playwright.full.config.mjs

bench:
	$(GO) test -run '^$$' -bench . -benchmem ./internal/transform/ ./internal/sink/

PROFILE_DIR ?= profiles
# End-to-end load tests (Docker): prints docs/s and writes CPU/heap profiles.
load:
	mkdir -p $(PROFILE_DIR)
	$(GO) test -tags bench -count=1 -timeout 60m -v -run 'TestLoad$$' -cpuprofile $(PROFILE_DIR)/cpu-memory.pprof -memprofile $(PROFILE_DIR)/mem-memory.pprof ./test/bench/
	$(GO) test -tags bench -count=1 -timeout 60m -v -run 'TestLoadPostgres' -cpuprofile $(PROFILE_DIR)/cpu-postgres.pprof ./test/bench/

# Refresh the PGO profile (default: the load test profiles; PROFILES= for production ones).
PROFILES ?= $(PROFILE_DIR)/cpu-memory.pprof $(PROFILE_DIR)/cpu-postgres.pprof
pgo:
	$(GO) tool pprof -proto $(PROFILES) > cmd/meilisearch-cdc/default.pgo

lint:
	gofmt -l . | (! grep .)
	$(GO) vet ./...
	$(GO) vet -tags integration ./...
	$(GO) vet -tags e2e ./test/e2e/
	$(GO) vet -tags bench ./test/bench/

gosec:
	gosec -quiet ./...

golangci:
	golangci-lint run ./...

# Everything CI checks without Docker.
check: lint golangci gosec test-race

docker:
	docker build -t meilisearch-cdc .

demo:
	docker compose -f deploy/docker-compose.yml up --build
