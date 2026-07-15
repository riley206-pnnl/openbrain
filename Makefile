GO      ?= go
GOTOOLCHAIN := local
export GOTOOLCHAIN
BINDIR  := bin
CMDS    := openbrain openbrain-mcp openbrain-web openbrain-telegram openbrain-slack openbrain-watchd
BINS    := $(addprefix $(BINDIR)/,$(CMDS))
BUILD_TAGS ?=

# Version stamping: the version flows from the nearest git tag into the
# binary at build time via linker flags. No source file is rewritten at
# release time. An un-injected build (no tag reachable) falls back to the
# "dev" sentinel baked into internal/version/version.go.
VERSION_PKG := github.com/windingriverholdings/openbrain/internal/version
VERSION     := $(shell git describe --tags --always 2>/dev/null || echo dev)

# go_ldflags: the linker version stamp for a given version string. Single
# source of truth for every build target (build, install, dist), so a future
# second -X flag (commit SHA, build date) is added here once and inherited
# everywhere instead of drifting between targets.
go_ldflags = -X $(VERSION_PKG).Version=$(1)
LDFLAGS    := $(call go_ldflags,$(VERSION))

.PHONY: all build build-ocr dist test test-cover test-verbose lint vet clean install fixtures setup-db viz open-web rotate-web-token

## Default: show help
all: help

build: $(BINS)

$(BINDIR)/%: cmd/%/*.go internal/**/*.go go.mod go.sum
	@mkdir -p $(BINDIR)
	$(GO) build $(if $(BUILD_TAGS),-tags $(BUILD_TAGS)) -ldflags "$(LDFLAGS)" -o $@ ./cmd/$*

## Build all binaries with OCR support (requires tesseract-ocr + libtesseract-dev)
build-ocr:
	$(MAKE) build BUILD_TAGS=ocr

# ---------------------------------------------------------------------------
# Release assets (OB-043).
#
# `make dist DIST_VERSION=vX.Y.Z` builds the six binaries as versioned,
# platform-suffixed release assets into dist/ and writes dist/SHA256SUMS over
# them. It is invoked by @semantic-release/exec's prepareCmd at release time
# with the computed version (v${nextRelease.version}), so the assets carry the
# ACTUAL release version, not the git-describe value `make build` uses.
#
# DIST_VERSION is REQUIRED and has no default: a dist build with no explicit
# version is a mistake (it would ship an unversioned or "dev" asset). This
# target only BUILDS: it runs no git add/commit/push and rewrites no source.
#
# linux/amd64 only for now (the bigmon deploy target and the ubuntu-latest
# runner native arch). A cross-platform matrix is a trivial follow-up.
DISTDIR       := dist
DIST_PLATFORM := linux-amd64
DIST_VERSION  ?=

## Build the six binaries as versioned linux/amd64 release assets + SHA256SUMS
dist:
	@if [ -z "$(DIST_VERSION)" ]; then \
		echo "ERROR: DIST_VERSION is required, e.g. make dist DIST_VERSION=v0.3.1" >&2; \
		exit 1; \
	fi
	rm -rf $(DISTDIR)
	@mkdir -p $(DISTDIR)
	@for cmd in $(CMDS); do \
		out="$(DISTDIR)/$$cmd-$(DIST_VERSION)-$(DIST_PLATFORM)"; \
		echo "building $$out (version $(DIST_VERSION))"; \
		GOOS=linux GOARCH=amd64 $(GO) build -ldflags "$(call go_ldflags,$(DIST_VERSION))" -o "$$out" ./cmd/$$cmd || exit 1; \
	done
	cd $(DISTDIR) && sha256sum openbrain*-$(DIST_VERSION)-$(DIST_PLATFORM) > SHA256SUMS
	@echo "wrote $(DISTDIR)/SHA256SUMS"

## Run all unit tests
test:
	$(GO) test ./internal/... -count=1

## Run tests with verbose output
test-verbose:
	$(GO) test ./internal/... -v -count=1

## Run tests with coverage report
test-cover:
	$(GO) test ./internal/... -v -count=1 -coverprofile=coverage.out -covermode=atomic
	$(GO) tool cover -func=coverage.out
	@echo ""
	@echo "HTML report: go tool cover -html=coverage.out"

## Run go vet on all packages
vet:
	$(GO) vet ./...

## Run linters (vet + staticcheck if installed)
lint: vet
	@which staticcheck >/dev/null 2>&1 && staticcheck ./... || echo "staticcheck not installed (go install honnef.co/go/tools/cmd/staticcheck@latest)"

## Generate test fixtures from Python implementation
fixtures:
	pixi run python scripts/generate_fixtures.py

## Run database migrations
setup-db:
	bash scripts/setup-db.sh

## Generate brain map data (runs UMAP + clustering + LLM labels, writes brain.json)
viz:
	python3 scripts/build-brain-viz.py

# ---------------------------------------------------------------------------
# Web UI token helpers (OB-058).
#
# openbrain-web gates /graph and / behind a `?token=` query param compared
# exactly against OPENBRAIN_WEB_WS_TOKEN in .env. These two targets read and
# rotate that token without hand-editing .env or hand-crafting the URL.
#
# ENV_FILE and HOST are overridable, e.g.:
#   make open-web HOST=localhost:10203
#   make rotate-web-token ENV_FILE=/path/to/.env
ENV_FILE      ?= .env
HOST          ?= openbrain.wr-s.net
WEB_TOKEN_VAR := OPENBRAIN_WEB_WS_TOKEN

## Print (and try to open) the tokenized web UI URL: make open-web [HOST=host[:port]]
open-web:
	@test -f "$(ENV_FILE)" || { echo "ERROR: $(ENV_FILE) not found" >&2; exit 1; }
	@token=$$(sed -n 's/^$(WEB_TOKEN_VAR)=//p' "$(ENV_FILE)" | tail -n1); \
	if [ -z "$$token" ]; then \
		echo "ERROR: $(WEB_TOKEN_VAR) not set (or empty) in $(ENV_FILE)" >&2; \
		exit 1; \
	fi; \
	encoded=$$(python3 -c "import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=''))" "$$token"); \
	url="https://$(HOST)/graph?token=$$encoded"; \
	echo "$$url"; \
	if command -v xdg-open >/dev/null 2>&1; then xdg-open "$$url" >/dev/null 2>&1 & fi; \
	true

## Rotate OPENBRAIN_WEB_WS_TOKEN in .env and restart openbrain-web (invalidates old URLs)
rotate-web-token:
	@test -f "$(ENV_FILE)" || { echo "ERROR: $(ENV_FILE) not found" >&2; exit 1; }
	@command -v openssl >/dev/null 2>&1 || { echo "ERROR: openssl not found" >&2; exit 1; }
	@new_token=$$(openssl rand -base64 36 | tr -d '\n'); \
	if grep -q "^$(WEB_TOKEN_VAR)=" "$(ENV_FILE)"; then \
		sed -i "s|^$(WEB_TOKEN_VAR)=.*|$(WEB_TOKEN_VAR)=$$new_token|" "$(ENV_FILE)"; \
	else \
		printf '%s=%s\n' "$(WEB_TOKEN_VAR)" "$$new_token" >> "$(ENV_FILE)"; \
	fi; \
	chmod 600 "$(ENV_FILE)"; \
	systemctl --user restart openbrain-web; \
	encoded=$$(python3 -c "import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=''))" "$$new_token"); \
	url="https://$(HOST)/graph?token=$$encoded"; \
	echo "Rotated $(WEB_TOKEN_VAR); openbrain-web restarted."; \
	echo "New URL: $$url"; \
	echo "WARNING: previously issued token URLs (browser history, shared links) are now invalid."

## Install binaries to GOPATH/bin
install:
	$(GO) install -ldflags "$(LDFLAGS)" ./cmd/openbrain
	$(GO) install -ldflags "$(LDFLAGS)" ./cmd/openbrain-mcp
	$(GO) install -ldflags "$(LDFLAGS)" ./cmd/openbrain-web

## Remove build artifacts
clean:
	rm -rf $(BINDIR) $(DISTDIR) coverage.out

## Show binary sizes
sizes: build
	@ls -lh $(BINDIR)/ | grep -v total

## Quick check: vet + test
check: vet test

## Full CI pipeline: lint + test with coverage
ci: lint test-cover

## Help
help:
	@echo "OpenBrain Go — available targets:"
	@echo ""
	@echo "  make build         Build all 6 binaries"
	@echo "  make build-ocr     Build with OCR support (needs tesseract)"
	@echo "  make dist          Build versioned release assets (needs DIST_VERSION)"
	@echo "  make test          Run unit tests"
	@echo "  make test-verbose  Run tests with verbose output"
	@echo "  make test-cover    Run tests with coverage report"
	@echo "  make vet           Run go vet"
	@echo "  make lint          Run vet + staticcheck"
	@echo "  make check         Quick check (vet + test)"
	@echo "  make ci            Full CI (lint + coverage)"
	@echo "  make fixtures      Regenerate test fixtures from Python"
	@echo "  make setup-db      Run database migrations"
	@echo "  make viz           Generate brain map data (brain.json)"
	@echo "  make open-web      Print/open the tokenized web UI URL"
	@echo "  make rotate-web-token  Rotate the web UI token and restart openbrain-web"
	@echo "  make install       Install binaries to GOPATH"
	@echo "  make clean         Remove build artifacts"
	@echo "  make sizes         Show binary sizes"
	@echo "  make help          Show this help"
