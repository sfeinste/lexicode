# Lexicode — one binary with an embedded SPA (decision D-1).
#
# The only target CI runs is `check`. The only target a release runs is `release`.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

BINARY      := lexicode
PKG         := ./cmd/lexicode
DIST        := dist
GO          ?= go
NPM         ?= npm

VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

PLATFORMS   := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64

.PHONY: help
help: ## List the targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[1m%-12s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- checks -----

.PHONY: check
check: check-go check-web ## Everything CI runs: build, vet, lint, test, frontend typecheck and lint

.PHONY: check-go
check-go:
	$(GO) build ./...
	$(GO) vet ./...
	golangci-lint run
	$(GO) test ./...

.PHONY: check-web
check-web: web/node_modules check-api-types
	cd web && npx tsc -b --noEmit
	cd web && npx eslint .
	cd web && npx vitest run

# The committed types.gen.ts must match what openapi.yaml generates — regenerate to a scratch
# file and diff, so a stale commit fails check without dirtying the tree.
.PHONY: check-api-types
check-api-types: web/node_modules
	cd web && npx openapi-typescript ../internal/api/v1/openapi.yaml -o /tmp/lexicode-types-check.gen.ts >/dev/null
	@diff -u web/src/lib/api/types.gen.ts /tmp/lexicode-types-check.gen.ts \
		|| (echo "web/src/lib/api/types.gen.ts is stale — run 'make api-types'"; exit 1)
	@rm -f /tmp/lexicode-types-check.gen.ts

.PHONY: test
test: ## Run the Go tests
	$(GO) test ./...

.PHONY: fmt
fmt: ## Format the Go sources
	gofmt -w ./cmd ./internal ./web

# --------------------------------------------------------------- frontend ----

# npm ci is re-run whenever the manifest or the lockfile is newer than the tree it produced.
web/node_modules: web/package.json web/package-lock.json
	cd web && $(NPM) ci
	@touch web/node_modules

.PHONY: web
web: web/node_modules ## Build the frontend into web/dist (embedded by the Go build)
	cd web && $(NPM) run build

.PHONY: api-types
api-types: web/node_modules ## Regenerate web/src/lib/api/types.gen.ts from internal/api/v1/openapi.yaml
	cd web && npx openapi-typescript ../internal/api/v1/openapi.yaml -o src/lib/api/types.gen.ts

# ------------------------------------------------------------------ build ----

.PHONY: build
build: web ## Build ./lexicode with the frontend embedded and the version injected
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) $(PKG)

.PHONY: dev
dev: web/node_modules ## Run the Go server and the Vite dev server together
	@echo "api  http://127.0.0.1:7717"
	@echo "web  http://127.0.0.1:5173  (proxies /api to the Go server)"
	@trap 'kill 0' EXIT INT TERM; \
	$(GO) run $(PKG) serve --open-browser=false & \
	(cd web && $(NPM) run dev) & \
	wait

# SHA256 is spelled differently on the two platforms this is built from.
SHA256 := $(shell command -v sha256sum >/dev/null 2>&1 && echo "sha256sum" || echo "shasum -a 256")

.PHONY: release
release: web ## Cross-compile darwin/linux x amd64/arm64 into dist/, with SHA256 checksums
	@rm -rf $(DIST)
	@mkdir -p $(DIST)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=$(DIST)/$(BINARY)_$(VERSION)_$${os}_$${arch}; \
		echo "  $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $$out $(PKG); \
	done
	@cp scripts/install.sh $(DIST)/install.sh
	@cd $(DIST) && $(SHA256) $(BINARY)_* > SHA256SUMS
	@echo "built $(VERSION) for: $(PLATFORMS)"
	@echo "checksums: $(DIST)/SHA256SUMS"

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BINARY) $(DIST) web/dist/index.html web/dist/assets
