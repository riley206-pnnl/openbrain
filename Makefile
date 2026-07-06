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
LDFLAGS     := -X $(VERSION_PKG).Version=$(VERSION)

.PHONY: all build build-ocr dist test test-cover test-verbose lint vet clean install fixtures setup-db

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
	@mkdir -p $(DISTDIR)
	@for cmd in $(CMDS); do \
		out="$(DISTDIR)/$$cmd-$(DIST_VERSION)-$(DIST_PLATFORM)"; \
		echo "building $$out (version $(DIST_VERSION))"; \
		GOOS=linux GOARCH=amd64 $(GO) build -ldflags "-X $(VERSION_PKG).Version=$(DIST_VERSION)" -o "$$out" ./cmd/$$cmd || exit 1; \
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

## Install binaries to GOPATH/bin
install:
	$(GO) install -ldflags "$(LDFLAGS)" ./cmd/openbrain
	$(GO) install -ldflags "$(LDFLAGS)" ./cmd/openbrain-mcp
	$(GO) install -ldflags "$(LDFLAGS)" ./cmd/openbrain-web

## Remove build artifacts
clean:
	rm -rf $(BINDIR) coverage.out

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
	@echo "  make test          Run unit tests"
	@echo "  make test-verbose  Run tests with verbose output"
	@echo "  make test-cover    Run tests with coverage report"
	@echo "  make vet           Run go vet"
	@echo "  make lint          Run vet + staticcheck"
	@echo "  make check         Quick check (vet + test)"
	@echo "  make ci            Full CI (lint + coverage)"
	@echo "  make fixtures      Regenerate test fixtures from Python"
	@echo "  make setup-db      Run database migrations"
	@echo "  make install       Install binaries to GOPATH"
	@echo "  make clean         Remove build artifacts"
	@echo "  make sizes         Show binary sizes"
	@echo "  make help          Show this help"
