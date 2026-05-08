.PHONY: build build-iolu run clean test install deps fmt lint benchmark test-race test-unit test-integration

# Binary name
BINARY_NAME=olu
ADMIN_BINARY=iolu
MIGRATE_BINARY=olu-migrate
MAIN_PATH=./cmd/olu
ADMIN_PATH=./cmd/iolu
MIGRATE_PATH=./cmd/olu-migrate

# Build the application
build: deps
	@echo "Building ${BINARY_NAME}..."
	@go build -o ${BINARY_NAME} ${MAIN_PATH}
	@echo "Build complete: ${BINARY_NAME}"

# Build migration tool
build-migrate:
	@echo "Building ${MIGRATE_BINARY}..."
	@go build -o ${MIGRATE_BINARY} ${MIGRATE_PATH}
	@echo "Build complete: ${MIGRATE_BINARY}"

# Build admin CLI
build-iolu:
	@echo "Building ${ADMIN_BINARY}..."
	@go build -o ${ADMIN_BINARY} ${ADMIN_PATH}
	@echo "Build complete: ${ADMIN_BINARY}"

# Build all binaries
build-all-tools: build build-iolu build-migrate
	@echo "All tools built successfully"

# Run the application
run: build
	@echo "Running ${BINARY_NAME}..."
	@./${BINARY_NAME}

# Clean build artifacts

# =============================================================================
# Release
# =============================================================================

# Full release: runs tests once, generates TESTING.md, updates badges, cuts zip
# Usage: make release VERSION=0.9.4
#        make release VERSION=0.9.5-rc1 RELEASE_FLAGS=--short
release:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make release VERSION=<version>" >&2; exit 1; fi
	@./release.sh $(VERSION) $(RELEASE_FLAGS)

# Dry run: everything except the zip
release-dry:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make release-dry VERSION=<version>" >&2; exit 1; fi
	@./release.sh $(VERSION) --no-zip $(RELEASE_FLAGS)

clean:
	@echo "Cleaning..."
	@go clean -testcache
	@rm -f ${BINARY_NAME}
	@rm -f ${ADMIN_BINARY}
	@rm -f ${MIGRATE_BINARY}
	@rm -rf data/*
	@rm -f *.db
	@rm -f coverage.out coverage.html test-report.json
	@echo "Clean complete"

# =============================================================================
# Testing
# =============================================================================

# Run all tests (excludes stress tests)
test:
	@echo "Running tests..."
	@go test -short ./...

# Run all tests with verbose output
test-v:
	@echo "Running tests (verbose)..."
	@go test -short -v ./...

# Run tests with race detector
test-race:
	@echo "Running tests with race detector..."
	@go test -short -race ./...

# Run tests with coverage
coverage:
	@./run_tests.sh

# Run tests with coverage including Redis
coverage-redis:
	@./run_tests.sh --redis

# Coverage with HTML report
coverage-html:
	@./run_tests.sh --html

# Coverage gate — fails if aggregate drops below threshold
# Usage: make coverage-check THRESHOLD=75
THRESHOLD ?= 75
coverage-check:
	@./run_tests.sh --threshold $(THRESHOLD)

# Quick test (no verbose, cached results ok)
test-quick:
	@go test -short ./...

# Generate test report in JSON
test-report:
	@echo "Generating test report..."
	@go test -short -v -json ./... > test-report.json
	@echo "Test report: test-report.json"

# =============================================================================
# Package-specific tests
# =============================================================================

# Run storage tests
test-storage:
	@echo "Running storage tests..."
	@go test -v ./pkg/storage/...

# Run SQLite tests only
test-sqlite:
	@echo "Running SQLite tests..."
	@go test -v ./pkg/storage/ -run TestSQLite

# Run graph tests
test-graph:
	@echo "Running graph tests..."
	@go test -v ./pkg/graph/...

# Run OQL tests
test-oql:
	@echo "Running OQL tests..."
	@go test -v ./pkg/oql/...

# Run Sulpher tests
test-sulpher:
	@echo "Running Sulpher tests..."
	@go test -v ./pkg/sulpher/...

# Run validation tests
test-validation:
	@echo "Running validation tests..."
	@go test -v ./pkg/validation/...

# Run server tests
test-server:
	@echo "Running server tests..."
	@go test -v ./pkg/server/...

# Run cache tests with Redis
# Starts Redis container, runs tests, stops Redis
test-redis:
	@echo "Starting Redis container..."
	@docker run -d --name olu-redis-test -p 6379:6379 redis:7-alpine > /dev/null 2>&1 || true
	@sleep 1
	@echo "Running Redis cache tests..."
	@go test -v ./pkg/cache/... -run Redis; \
	EXIT_CODE=$$?; \
	echo "Stopping Redis container..."; \
	docker stop olu-redis-test > /dev/null 2>&1 || true; \
	docker rm olu-redis-test > /dev/null 2>&1 || true; \
	exit $$EXIT_CODE

# Run Redis stress tests (concurrent access, large payloads, pattern delete)
test-redis-stress:
	@echo "Starting Redis container..."
	@docker run -d --name olu-redis-test -p 6379:6379 redis:7-alpine > /dev/null 2>&1 || true
	@sleep 1
	@echo "Running Redis stress tests..."
	@go test -v ./pkg/cache/... -run RedisStress -timeout 120s; \
	EXIT_CODE=$$?; \
	echo "Stopping Redis container..."; \
	docker stop olu-redis-test > /dev/null 2>&1 || true; \
	docker rm olu-redis-test > /dev/null 2>&1 || true; \
	exit $$EXIT_CODE

# =============================================================================
# Stress Tests
# =============================================================================

# Run all stress tests
stress:
	@echo "Running stress tests (10,000 records)..."
	@go test -v -run TestStress ./pkg/storage/...

# Run stress tests with race detector
stress-race:
	@echo "Running stress tests with race detector..."
	@go test -v -race -run TestStress ./pkg/storage/...

# Run individual stress tests
stress-bulk:
	@echo "Running bulk creation stress test..."
	@go test -v -run TestStress_BulkCreation ./pkg/storage/...

stress-workers:
	@echo "Running concurrent workers stress test..."
	@go test -v -run TestStress_ConcurrentWorkers ./pkg/storage/...

stress-dashboard:
	@echo "Running dashboard queries stress test..."
	@go test -v -run TestStress_DashboardQueries ./pkg/storage/...

stress-mixed:
	@echo "Running mixed workload stress test..."
	@go test -v -run TestStress_MixedWorkload ./pkg/storage/...

# =============================================================================
# Benchmarks
# =============================================================================

# Run all benchmarks
bench:
	@echo "Running all benchmarks..."
	@go test -bench=. -benchmem -run=^$$ ./...

# Run benchmarks with longer duration
bench-long:
	@echo "Running benchmarks (5s each)..."
	@go test -bench=. -benchmem -benchtime=5s -run=^$$ ./...

# Run OQL benchmarks
bench-oql:
	@echo "Running OQL benchmarks..."
	@go test -bench=. -benchmem -run=^$$ ./pkg/oql/...

# Run storage benchmarks
bench-storage:
	@echo "Running storage benchmarks..."
	@go test -bench=. -benchmem -run=^$$ ./pkg/storage/...

# Run stress benchmarks (10k records)
bench-stress:
	@echo "Running stress benchmarks (10k records)..."
	@go test -bench=BenchmarkStress -benchmem -run=^$$ ./pkg/storage/...

# Run server benchmarks
bench-server:
	@echo "Running server benchmarks..."
	@go test -bench=. -benchmem -run=^$$ ./pkg/server/...

# Run Sulpher benchmarks
bench-sulpher:
	@echo "Running Sulpher benchmarks..."
	@go test -bench=. -benchmem -run=^$$ ./pkg/sulpher/...

# Run specific benchmark by name
bench-%:
	@echo "Running benchmark $*..."
	@go test -bench=$* -benchmem -run=^$$ ./...

# =============================================================================
# Combined targets
# =============================================================================

# Full test suite (tests + stress + race detection)
test-full: test stress-race
	@echo "✓ Full test suite passed!"

# All checks before commit
pre-commit: clean build test test-race
	@echo "✓ All pre-commit checks passed!"

# CI pipeline simulation
ci: clean deps build test-race coverage bench
	@echo "✓ CI pipeline passed!"

# =============================================================================
# Development
# =============================================================================

# Install dependencies
# Install dependencies
# GONOSUMDB=* skips checksum verification for consistent behaviour
# across Go versions (1.22, 1.24, etc.)
deps:
	@echo "Installing dependencies..."
	@GONOSUMDB=* go mod download
	@echo "Dependencies installed"

# Format code
fmt:
	@echo "Formatting code..."
	@go fmt ./...

# Run linter
lint:
	@echo "Running linter..."
	@golangci-lint run ./...

# Build for multiple platforms
build-all:
	@echo "Building for multiple platforms..."
	@GOOS=linux GOARCH=amd64 go build -o ${BINARY_NAME}-linux-amd64 ${MAIN_PATH}
	@GOOS=darwin GOARCH=amd64 go build -o ${BINARY_NAME}-darwin-amd64 ${MAIN_PATH}
	@GOOS=darwin GOARCH=arm64 go build -o ${BINARY_NAME}-darwin-arm64 ${MAIN_PATH}
	@GOOS=windows GOARCH=amd64 go build -o ${BINARY_NAME}-windows-amd64.exe ${MAIN_PATH}
	@echo "Multi-platform build complete"

# Install the binary
install: build
	@echo "Installing ${BINARY_NAME}..."
	@go install ${MAIN_PATH}

# Development mode with auto-reload (requires air)
dev:
	@which air > /dev/null || (echo "Installing air..." && go install github.com/cosmtrek/air@latest)
	@air

# Docker build
docker-build:
	@echo "Building Docker image..."
	@docker build -t olu:latest .

# Docker run
docker-run:
	@echo "Running Docker container..."
	@docker run -p 9090:9090 -v $(PWD)/data:/app/data olu:latest

# Docker Compose - basic (memory cache, no auth)
docker-up:
	@echo "Starting Olu (basic)..."
	@docker compose up -d olu

# Docker Compose - with Redis cache
docker-up-redis:
	@echo "Starting Olu with Redis..."
	@docker compose --profile redis up -d

# Docker Compose - full features (Redis, SQLite+FTS, auth, rate limiting)
docker-up-full:
	@echo "Starting Olu with all features..."
	@docker compose --profile full up -d

# Docker Compose - run integration tests
docker-test:
	@echo "Running Docker integration tests..."
	@docker compose --profile test up --build --abort-on-container-exit test-runner
	@docker compose --profile test down -v

# Docker Compose - stop all
docker-down:
	@echo "Stopping all containers..."
	@docker compose --profile redis --profile full --profile test down

# Docker Compose - stop and clean volumes
docker-clean:
	@echo "Stopping containers and removing volumes..."
	@docker compose --profile redis --profile full --profile test down -v

# =============================================================================
# Help
# =============================================================================

help:
	@echo "Available targets:"
	@echo ""
	@echo "Build & Run:"
	@echo "  build           - Build the application"
	@echo "  build-iolu      - Build admin CLI (iolu)"
	@echo "  build-migrate   - Build migration tool"
	@echo "  build-all-tools - Build all binaries"
	@echo "  build-all       - Build for multiple platforms"
	@echo "  run             - Build and run the application"
	@echo "  clean           - Remove build artifacts and data"
	@echo "  install         - Install binary"
	@echo ""
	@echo "Testing:"
	@echo "  test            - Run all tests (excludes stress)"
	@echo "  test-v          - Run all tests (verbose)"
	@echo "  test-race       - Run tests with race detector"
	@echo "  test-quick      - Quick test run (cached results)"
	@echo "  test-full       - Full test suite (tests + stress + race)"
	@echo "  coverage        - Run tests with coverage report"
	@echo "  coverage-redis  - Coverage including Redis backend"
	@echo "  coverage-html   - Coverage with HTML report"
	@echo "  coverage-check  - Fail if coverage below threshold (THRESHOLD=75)"
	@echo "  test-report     - Generate JSON test report"
	@echo ""
	@echo "Package Tests:"
	@echo "  test-storage    - Run storage tests"
	@echo "  test-sqlite     - Run SQLite tests only"
	@echo "  test-graph      - Run graph tests"
	@echo "  test-oql        - Run OQL tests"
	@echo "  test-sulpher    - Run Sulpher tests"
	@echo "  test-validation - Run validation tests"
	@echo "  test-server     - Run server tests"
	@echo "  test-redis      - Run Redis cache tests (starts/stops Redis container)"
	@echo "  test-redis-stress - Run Redis stress tests (concurrent, large payloads)"
	@echo ""
	@echo "Stress Tests (10,000 records):"
	@echo "  stress          - Run all stress tests"
	@echo "  stress-race     - Run stress tests with race detector"
	@echo "  stress-bulk     - Bulk record creation"
	@echo "  stress-workers - Concurrent worker simulation"
	@echo "  stress-dashboard - Dashboard query patterns"
	@echo "  stress-mixed    - Mixed workload simulation"
	@echo ""
	@echo "Benchmarks:"
	@echo "  bench           - Run all benchmarks"
	@echo "  bench-long      - Run benchmarks (5s duration)"
	@echo "  bench-oql       - OQL benchmarks"
	@echo "  bench-storage   - Storage benchmarks"
	@echo "  bench-stress    - Stress benchmarks (10k records)"
	@echo "  bench-server    - Server benchmarks"
	@echo "  bench-sulpher   - Sulpher benchmarks"
	@echo "  bench-NAME      - Run specific benchmark"
	@echo ""
	@echo "Development:"
	@echo "  deps            - Install dependencies"
	@echo "  fmt             - Format code"
	@echo "  lint            - Run linter"
	@echo "  dev             - Run with auto-reload"
	@echo "  pre-commit      - Run all checks before committing"
	@echo "  ci              - Simulate CI pipeline"
	@echo ""
	@echo "Docker:"
	@echo "  docker-build    - Build Docker image"
	@echo "  docker-run      - Run Docker container"
	@echo "  docker-up       - Start basic Olu (memory cache, no auth)"
	@echo "  docker-up-redis - Start Olu with Redis cache"
	@echo "  docker-up-full  - Start Olu with all features"
	@echo "  docker-test     - Run integration tests in Docker"
	@echo "  docker-down     - Stop all containers"
	@echo "  docker-clean    - Stop containers and remove volumes"
