# nolu Makefile

BINARY_DIR  := bin
VERSION     := $(shell cat VERSION)
NATS_URL    ?= nats://localhost:4222
NATS_STREAM ?= NOLU_EVENTS


.PHONY: all build demo demo1 demo2 demo3 demo4 demo-nats test test-verbose vet check verify help \
        distclean release \
        run-demo1 run-demo2 run-demo3 run-demo4 \
        run-docker-demo2 run-docker-demo3 run-docker-demo4 \
        vendor tidy test-e2e test-e2e-health test-e2e-xolureg test-e2e-hotswap \
        demo1-up demo2-up demo3-up demo4-up \
        demo1-down demo2-down demo3-down demo4-down \
        docker-build docker-up docker-down docker-demo \
        docker-demo1 docker-demo2 docker-demo3 docker-demo4 \
        docker-verify docker-e2e docker-nats-info docker-logs clean version

# ── Default ───────────────────────────────────────────────────────────────────

all: build

# ── Build ─────────────────────────────────────────────────────────────────────

build:
	@mkdir -p $(BINARY_DIR)
	go build -o $(BINARY_DIR)/nolu        ./cmd/nolu
	go build -o $(BINARY_DIR)/nolu-proxy  ./cmd/nolu-proxy
	go build -o $(BINARY_DIR)/nolu-demo   ./cmd/demo
	go build -o $(BINARY_DIR)/nolu-demo1 ./cmd/demo1
	go build -o $(BINARY_DIR)/nolu-demo2 ./cmd/demo2
	go build -o $(BINARY_DIR)/nolu-demo3 ./cmd/demo3
	go build -o $(BINARY_DIR)/nolu-demo4 ./cmd/demo4
	@echo "Built: nolu nolu-proxy nolu-demo nolu-demo1 nolu-demo2 nolu-demo3 nolu-demo4"

# ── Local targets (no Docker required) ───────────────────────────────────────

# demo: alias for demo1 (backwards compatibility)
demo: demo1

# Demo 1: three separate xolu instances, one org each, memory registry.
# Demonstrates the fundamental nolu identity model without persistence.
demo1: build
	@echo "Running demo1 (memory registry, three xolu instances)..."
	$(BINARY_DIR)/nolu-demo1 -bus=memory

# Demo 2: one multi-tenant xolu instance, three org tenants, memory registry.
# Requires xolu-hub at localhost:9090 (make demo2-up).
demo2: build
	@echo "Running demo2 (multi-tenant xolu)..."
	$(BINARY_DIR)/nolu-demo2 -xolu=http://localhost:9090

# Demo 3: XoluRegistry (durable), 3-node NATS, three org xolus + registry.
# Requires make demo3-up first.
demo3: build
	@echo "Running demo3 (XoluRegistry + 3-node NATS cluster)..."
	$(BINARY_DIR)/nolu-demo3 \
		-vendo=http://localhost:9090 \
		-retail=http://localhost:9091 \
		-service=http://localhost:9092 \
		-registry=http://localhost:9093 \
		-nats=nats://localhost:4222

# Demo 4: large federation — 5 xolu instances, multi-tenant, 5-node NATS.
# Requires make demo4-up first.
demo4: build
	@echo "Running demo4 (large federation topology)..."
	$(BINARY_DIR)/nolu-demo4 \
		-eu=http://localhost:9090 \
		-us=http://localhost:9091 \
		-apac=http://localhost:9092 \
		-service=http://localhost:9093 \
		-registry=http://localhost:9094 \
		-nats=nats://localhost:4222

# Run the clearinghouse scenario against a local NATS instance.
# Requires NATS to be running at NATS_URL (default: nats://localhost:4222).
demo-nats: build
	@echo "Running demo (NATS bus at $(NATS_URL))..."
	$(BINARY_DIR)/nolu-demo -bus=nats -nats=$(NATS_URL)

# Run the test suite. TestDemoScenario asserts the exact Phase 8 snapshot.
test:
	go test ./... -count=1

# Run the test suite with verbose output — shows each test name and result.
test-verbose:
	go test ./... -v -count=1

# Run the Go vet static analyser.
vet:
	go vet ./...

# Full local verification: vet + test + demo.
# This is what CI should run. Exits non-zero on any failure.
check: vet test demo
	@echo ""
	@echo "✓  vet passed"
	@echo "✓  all package tests passed (registry, server, proxy, hotswap, transfer)"
	@echo "✓  demo ran successfully"

# Verify Phase 8 state deterministically by running only TestDemoScenario.
# Faster than 'make check' — useful after targeted changes.
verify:
	go test ./pkg/registry/ -run TestDemoScenario -v -count=1

# ── Docker targets ────────────────────────────────────────────────────────────

# Build the nolu Docker image.
# Vendors dependencies first so the Docker build has no network dependency.
docker-build: vendor
	docker compose build

# Vendor all dependencies into ./vendor for use in Docker builds.
# GOTOOLCHAIN=local prevents go mod vendor from switching to a newer toolchain
# and silently rewriting the go directive in go.mod.
vendor:
	GOTOOLCHAIN=local go mod vendor
	@echo "Vendored dependencies into ./vendor"

# Tidy go.mod and go.sum, then pin the go directive back to 1.22.
# Run this after adding or updating dependencies, then commit vendor/.
tidy:
	GOTOOLCHAIN=local go mod tidy
	@python3 -c "\
import re, sys; \
src = open('go.mod').read(); \
src = re.sub(r'^go .*$$', 'go 1.22', src, flags=re.M); \
src = '\n'.join(l for l in src.splitlines() if not l.startswith('toolchain ')); \
open('go.mod','w').write(src+'\n'); \
print('go.mod: go directive pinned to 1.22')"
	@$(MAKE) vendor

# ── Per-demo stack management ─────────────────────────────────────────────────

demo1-up: docker-build
	docker compose --profile demo1 up -d
	@echo "Demo1 stack up. Run 'make docker-demo1'."

demo2-up: docker-build
	docker compose --profile demo2 up -d
	@echo "Demo2 stack up. Run 'make docker-demo2'."

demo3-up: docker-build
	docker compose --profile demo3 up -d
	@echo "Demo3 stack up. Waiting 10s for cluster..."
	@sleep 10

demo4-up: docker-build
	docker compose --profile demo4 up -d
	@echo "Demo4 stack up. Waiting 15s for cluster..."
	@sleep 15

demo1-down: ; docker compose --profile demo1 down -v
demo2-down: ; docker compose --profile demo2 down -v
demo3-down: ; docker compose --profile demo3 down -v
demo4-down: ; docker compose --profile demo4 down -v

docker-demo1:
	@echo "Running demo1 in Docker..."
	docker compose --profile demo1 run --rm demo1

docker-demo2:
	@echo "Running demo2 in Docker..."
	docker compose --profile demo2 run --rm demo2

docker-demo3:
	@echo "Running demo3 in Docker (XoluRegistry + 3-node NATS)..."
	docker compose --profile demo3 run --rm demo3

docker-demo4:
	@echo "Running demo4 in Docker (large federation)..."
	docker compose --profile demo4 run --rm demo4

# ── Compound one-shot targets ─────────────────────────────────────────────────
# Bring up the stack, run the demo, tear down. Exit code reflects the demo.

run-demo1: demo1

run-demo2: demo2-up
	$(MAKE) demo2; STATUS=$$?; $(MAKE) demo2-down; exit $$STATUS

run-demo3: demo3-up
	$(MAKE) demo3; STATUS=$$?; $(MAKE) demo3-down; exit $$STATUS

run-demo4: demo4-up
	$(MAKE) demo4; STATUS=$$?; $(MAKE) demo4-down; exit $$STATUS

# Docker variants (demo runs inside the nolu-net network).
run-docker-demo2: demo2-up
	$(MAKE) docker-demo2; STATUS=$$?; $(MAKE) demo2-down; exit $$STATUS

run-docker-demo3: demo3-up
	$(MAKE) docker-demo3; STATUS=$$?; $(MAKE) demo3-down; exit $$STATUS

run-docker-demo4: demo4-up
	$(MAKE) docker-demo4; STATUS=$$?; $(MAKE) demo4-down; exit $$STATUS

# Legacy aliases for backwards compatibility.
docker-up: demo1-up
docker-demo: docker-demo1
docker-down: demo1-down

docker-verify: demo1-up
	@sleep 5
	docker compose --profile demo1 run --rm demo1
	@echo "✓  Demo1 exited cleanly"

docker-nats-info:
	docker compose --profile demo3 exec nats-d3-1 nats stream info NOLU_DEMO3 --server nats://localhost:4222 2>/dev/null || \
	docker compose --profile demo4 exec nats-d4-1 nats stream info NOLU_DEMO4 --server nats://localhost:4222

docker-logs:
	docker compose logs -f

# ── Housekeeping ──────────────────────────────────────────────────────────────

clean:
	rm -rf $(BINARY_DIR)

# Remove all artifacts that should not appear in a release zip.
# Run this before 'make release' or manually before packaging.
distclean: clean
	find . -name "*.bak" -delete
	find . -name "*.tmp" -delete
	find . -name "*.out" -delete
	find . -name "*.test" -delete
	@echo "dist clean complete"

# Cut a release zip. Cleans all artifacts first.
# Usage: make release VERSION=0.4.3
# The VERSION variable must match the top entry in CHANGELOG.md.
release: distclean vendor
	$(eval V := $(or $(VERSION),$(shell cat VERSION)))
	@echo "Releasing nolu-$(V)..."
	@grep -q "^## $(V)" CHANGELOG.md || (echo "ERROR: $(V) not in CHANGELOG.md" && exit 1)
	@grep -q "^$(V)" VERSION || (echo "ERROR: VERSION file says $$(cat VERSION), not $(V)" && exit 1)
	@grep -q '"$(V)"' pkg/version/version.go || (echo "ERROR: version.go not updated to $(V)" && exit 1)
	cd .. && zip -r nolu-$(V).zip nolu/ 		--exclude "nolu/.git/*" 		--exclude "nolu/bin/*" 		--exclude "nolu/*.bak" 		--exclude "nolu/*.tmp" 		--exclude "nolu/*.out" 		--exclude "nolu/*.test" 		--exclude "nolu/*.zip" \
		--exclude "nolu/xolu/.git/*" \
		--exclude "nolu/xolu/bin/*"
	@echo "Created: ../nolu-$(V).zip"

version:
	@echo $(VERSION)

# ── Help ──────────────────────────────────────────────────────────────────────

help:
	@printf '\n'
	@printf '\033[1m\033[36mnolu v%s\033[0m  —  federated entity registry for xolu\n' "$(VERSION)"
	@printf '\n'
	@printf '\033[1mUSAGE\033[0m\n'
	@printf '  make \033[36m<target>\033[0m  [NATS_URL=nats://host:4222]  [NATS_STREAM=NOLU_EVENTS]\n'
	@printf '\n'
	@printf '\033[1mBUILD\033[0m\n'
	@printf '  \033[36m%-22s\033[0m %s\n' "build"        "Compile nolu and nolu-demo binaries into ./bin"
	@printf '  \033[36m%-22s\033[0m %s\n' "vendor"       "Vendor dependencies into ./vendor (required before docker-build)"
	@printf '  \033[36m%-22s\033[0m %s\n' "tidy"         "go mod tidy + pin go directive to 1.22 + re-vendor" 
	@printf '  \033[36m%-22s\033[0m %s\n' "clean"        "Remove ./bin"
	@printf '  \033[36m%-22s\033[0m %s\n' "distclean"    "Remove ./bin + all *.bak *.tmp *.out *.test artifacts"
	@printf '  \033[36m%-22s\033[0m %s\n' "release"      "Cut a clean release zip — verifies CHANGELOG + version.go"
	@printf '  \033[36m%-22s\033[0m %s\n' "version"      "Print current version"
	@printf '\n'
	@printf '\033[1mVERIFICATION  (no external services required)\033[0m\n'
	@printf '  \033[36m%-22s\033[0m %s\n' "check"        "vet + test + demo  ← CI target, exits non-zero on any failure"
	@printf '  \033[36m%-22s\033[0m %s\n' "verify"       "Run TestDemoScenario only  (fast, asserts Phase 8 state)"
	@printf '  \033[36m%-22s\033[0m %s\n' "test"         "Full test suite  (25 tests across registry + transfer)"
	@printf '  \033[36m%-22s\033[0m %s\n' "test-verbose" "Full test suite with per-test names and timing"
	@printf '  \033[36m%-22s\033[0m %s\n' "vet"          "Go static analysis"
	@printf '\n'
	@printf '\033[1mDEMO  (local)\033[0m\n'
	@printf '  \033[36m%-22s\033[0m %s\n' "demo"         "Run clearinghouse scenario (memory bus, no NATS needed)"
	@printf '  \033[36m%-22s\033[0m %s\n' "demo-nats"    "Run clearinghouse scenario against local NATS (see NATS_URL)"
	@printf '\n'
	@printf '\033[1mDOCKER\033[0m\n'
	@printf '  \033[36m%-22s\033[0m %s\n' "docker-build"     "Build nolu Docker image"
	@printf '  \033[36m%-22s\033[0m %s\n' "docker-up"        "Start NATS + 3 xolu instances + nolu-registry (detached)"
	@printf '  \033[36m%-22s\033[0m %s\n' "docker-demo"      "Run demo against live Docker Compose stack"
	@printf '  \033[36m%-22s\033[0m %s\n' "docker-verify"    "docker-up + docker-demo + exit-code check"
	@printf '  \033[36m%-22s\033[0m %s\n' "docker-nats-info" "Inspect JetStream stream (expect 10 msgs after demo)"
	@printf '  \033[36m%-22s\033[0m %s\n' "docker-logs"      "Tail logs from all services"
	@printf '  \033[36m%-22s\033[0m %s\n' "docker-down"      "Stop all containers and remove volumes"
	@printf '\n'
	@printf '\033[1mRUN DEMOS  (one-shot: up + demo + down)\033[0m\n'
	@printf '  \033[36m%-22s\033[0m %s\n' "run-demo1"         "Demo 1 (no stack needed)"
	@printf '  \033[36m%-22s\033[0m %s\n' "run-demo2"         "Demo 2: stack up, run locally, stack down"
	@printf '  \033[36m%-22s\033[0m %s\n' "run-demo3"         "Demo 3: stack up, run locally, stack down"
	@printf '  \033[36m%-22s\033[0m %s\n' "run-demo4"         "Demo 4: stack up, run locally, stack down"
	@printf '  \033[36m%-22s\033[0m %s\n' "run-docker-demo2"  "Demo 2: stack up, run in Docker, stack down"
	@printf '  \033[36m%-22s\033[0m %s\n' "run-docker-demo3"  "Demo 3: stack up, run in Docker, stack down"
	@printf '  \033[36m%-22s\033[0m %s\n' "run-docker-demo4"  "Demo 4: stack up, run in Docker, stack down"
	@printf '\n'
	@printf '\033[1mE2E TESTS  (requires docker-up)\033[0m\n'
	@printf '  \033[36m%-22s\033[0m %s\n' "test-e2e-health"  "Connectivity smoke test — confirms stack is reachable"
	@printf '  \033[36m%-22s\033[0m %s\n' "test-e2e"         "Full e2e suite against live xolu + NATS (skips if not up)"
	@printf '  \033[36m%-22s\033[0m %s\n' "test-e2e-xolureg" "XoluRegistry durability + concurrency + negotiation tests"
	@printf '  \033[36m%-22s\033[0m %s\n' "docker-e2e"       "docker-up + wait + test-e2e"
	@printf '\n'
	@printf '\033[1mCOMMON WORKFLOWS\033[0m\n'
	@printf '  Quick local check after a change:\n'
	@printf '    \033[36mmake verify\033[0m\n'
	@printf '\n'
	@printf '  Full pre-commit gate:\n'
	@printf '    \033[36mmake check\033[0m\n'
	@printf '\n'
	@printf '  First Docker run:\n'
	@printf '    \033[36mmake docker-build docker-verify\033[0m\n'
	@printf '\n'
	@printf '  Re-run demo against running stack:\n'
	@printf '    \033[36mmake docker-demo\033[0m\n'
	@printf '\n'
	@printf '  Inspect NATS after demo:\n'
	@printf '    \033[36mmake docker-nats-info\033[0m\n'
	@printf '\n'
	@printf '  First real e2e run:\n'
	@printf '    \033[36mmake docker-e2e\033[0m\n'
	@printf '\n'
	@printf '  Tear everything down cleanly:\n'
	@printf '    \033[36mmake docker-down clean\033[0m\n'
	@printf '\n'

# ── E2E targets ───────────────────────────────────────────────────────────────

# Run e2e tests against local ports (docker-up exposes them on localhost).
# Skips automatically if services are not reachable.
test-e2e:
	go test ./test/e2e/ -v -count=1 -timeout 60s \
		-run TestE2E

# Run XoluHotswapManager e2e tests (durability across restart).
test-e2e-hotswap:
	go test ./test/e2e/ -v -count=1 -timeout 60s \
		-run TestE2E_XoluHotswapManager

# Run XoluRegistry-specific e2e tests (durability, concurrency, negotiation).
# Requires docker-up. Uses xolu-vendocorp as the durable backing store.
test-e2e-xolureg:
	go test ./test/e2e/ -v -count=1 -timeout 90s \
		-run TestE2E_XoluRegistry

# Run only the connectivity smoke test — fast way to confirm the stack is up.
test-e2e-health:
	go test ./test/e2e/ -v -count=1 -timeout 10s \
		-run TestE2E_XoluInstancesHealthy

# Full Docker e2e: bring up stack, wait, run e2e tests, report.
docker-e2e: docker-up
	@echo "Waiting for services to be healthy..."
	@sleep 8
	@$(MAKE) test-e2e || (echo "✗  e2e tests failed" && exit 1)
	@echo "✓  e2e tests passed"
