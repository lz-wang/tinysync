# =============================================================================
# TinySync Makefile
# =============================================================================

.DEFAULT_GOAL := help

.PHONY: \
	build build-all build-os dist \
	test coverage check format setup clean version help ci \
	web-install web-ci-install web-lint web-typecheck web-build web-format \
	_build-platform _install-go-tools _check-go-format _check-go-mod

BINARY_NAME := tinysync
MAIN_PATH := .
DIST_DIR := dist
WEB_DIR := web
COVERAGE_DIR := coverage
BACKEND_COVERAGE := $(COVERAGE_DIR)/backend.out
BACKEND_COVERAGE_HTML := $(COVERAGE_DIR)/backend.html

GO := go
NPM := npm
TAR := tar
ZIP := zip
GOIMPORTS_REVISER := goimports-reviser
GOIMPORTS_REVISER_VERSION := v3.12.6

PLATFORMS := \
	linux/amd64 \
	linux/arm64 \
	darwin/amd64 \
	darwin/arm64 \
	windows/amd64 \
	windows/arm64

# -----------------------------------------------------------------------------
# 版本解析：唯一事实来源是 Git。
#   HEAD 正好位于 vX.Y.Z tag   -> X.Y.Z
#   普通开发 commit            -> dev-<commit日期YYYYMMDD>-<commit7>
#   日期取 commit date（非当前机器时间），保证构建结果可重复。
# -----------------------------------------------------------------------------
COMMIT_ID := $(shell git rev-parse --short=7 HEAD 2>/dev/null || echo unknown)
COMMIT_DATE := $(shell git show -s --format=%cd --date=format:%Y%m%d HEAD 2>/dev/null || echo unknown)
EXACT_TAG := $(shell git describe --tags --exact-match --match 'v[0-9]*.[0-9]*.[0-9]*' HEAD 2>/dev/null || true)
TAG_VERSION := $(patsubst v%,%,$(EXACT_TAG))

VERSION ?=
ifeq ($(strip $(VERSION)),)
ifneq ($(strip $(EXACT_TAG)),)
TINYSYNC_VERSION := $(TAG_VERSION)
else ifeq ($(COMMIT_ID),unknown)
TINYSYNC_VERSION := dev-unknown-unknown
else
TINYSYNC_VERSION := dev-$(COMMIT_DATE)-$(COMMIT_ID)
endif
else
TINYSYNC_VERSION := $(VERSION)
endif

VERSION_VAR := tinysync/internal/buildinfo.Version
GO_BUILD_FLAGS := -trimpath -buildvcs=false
GO_LDFLAGS := -X $(VERSION_VAR)=$(TINYSYNC_VERSION)
# Release / cross build 追加 -s -w 去符号；开发构建保持可调试。
GO_RELEASE_LDFLAGS := -s -w $(GO_LDFLAGS)
HOST_GOEXE := $(shell $(GO) env GOEXE 2>/dev/null)
HOST_BINARY := $(BINARY_NAME)$(HOST_GOEXE)

## Build tinysync for the current platform; depends on web-build.
build: web-build
	@echo "[tinysync] build $(TINYSYNC_VERSION) -> ./$(HOST_BINARY)"
	@CGO_ENABLED=0 $(GO) build \
		-tags webui \
		$(GO_BUILD_FLAGS) \
		-ldflags "$(GO_LDFLAGS)" \
		-o "./$(HOST_BINARY)" \
		$(MAIN_PATH)

## Cross-compile six platform binaries into dist/.
build-all: web-build
	@set -eu; \
	for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		$(MAKE) --no-print-directory _build-platform OS="$$os" ARCH="$$arch"; \
	done

## Build one platform, for example: make build-os OS=linux ARCH=amd64.
build-os: web-build
	@$(MAKE) --no-print-directory _build-platform OS="$(OS)" ARCH="$(ARCH)"

_build-platform:
	@if [ -z "$(OS)" ] || [ -z "$(ARCH)" ]; then \
		echo "Usage: make build-os OS=linux ARCH=amd64"; \
		exit 2; \
	fi
	@target="$(OS)/$(ARCH)"; \
	case " $(PLATFORMS) " in \
		*" $$target "*) ;; \
		*) \
			echo "Error: unsupported platform $$target"; \
			exit 2; \
			;; \
	esac
	@mkdir -p "$(DIST_DIR)"
	@ext=""; \
	if [ "$(OS)" = "windows" ]; then ext=".exe"; fi; \
	output="$(DIST_DIR)/$(BINARY_NAME)_$(OS)_$(ARCH)$$ext"; \
	echo "[tinysync] build $(OS)/$(ARCH) -> $$output"; \
	CGO_ENABLED=0 GOOS="$(OS)" GOARCH="$(ARCH)" \
		$(GO) build \
			-tags webui \
			$(GO_BUILD_FLAGS) \
			-ldflags "$(GO_RELEASE_LDFLAGS)" \
			-o "$$output" \
			$(MAIN_PATH)

## Create six distribution archives and checksums.txt in dist/.
dist: clean build-all
	@command -v "$(TAR)" >/dev/null 2>&1 || { echo "Error: tar is required"; exit 2; }
	@command -v "$(ZIP)" >/dev/null 2>&1 || { echo "Error: zip is required"; exit 2; }
	@if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then \
		echo "Error: sha256sum or shasum is required"; \
		exit 2; \
	fi
	@set -eu; \
	stage="$(DIST_DIR)/.stage"; \
	mkdir -p "$$stage"; \
	for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		binary="$(DIST_DIR)/$(BINARY_NAME)_$${os}_$${arch}$$ext"; \
		name="$(BINARY_NAME)_$(TINYSYNC_VERSION)_$${os}_$${arch}"; \
		package_dir="$$stage/$$name"; \
		mkdir -p "$$package_dir"; \
		cp "$$binary" "$$package_dir/$(BINARY_NAME)$$ext"; \
		cp README.md LICENSE "$$package_dir/"; \
		if [ "$$os" = "windows" ]; then \
			(cd "$$package_dir" && $(ZIP) -qr "$(CURDIR)/$(DIST_DIR)/$$name.zip" .); \
		else \
			$(TAR) -C "$$package_dir" -czf "$(DIST_DIR)/$$name.tar.gz" .; \
		fi; \
		rm -f "$$binary"; \
	done; \
	rm -rf "$$stage"
	@cd "$(DIST_DIR)" && { \
		for file in *.tar.gz *.zip; do \
			[ -f "$$file" ] || continue; \
			if command -v sha256sum >/dev/null 2>&1; then \
				sha256sum "$$file"; \
			else \
				shasum -a 256 "$$file"; \
			fi; \
		done; \
	} > "checksums.txt"
	@echo "[tinysync] distributions -> $(DIST_DIR)/"

## Run Go tests.
test:
	@echo "[tinysync] test Go"
	@$(GO) test -timeout 30s ./...

## Generate backend coverage files for Codecov and local inspection.
coverage:
	@mkdir -p "$(COVERAGE_DIR)"
	@CGO_ENABLED=0 $(GO) test \
		-timeout 30s \
		-covermode=atomic \
		-coverprofile="$(BACKEND_COVERAGE)" \
		./...
	@$(GO) tool cover \
		-html="$(BACKEND_COVERAGE)" \
		-o "$(BACKEND_COVERAGE_HTML)"
	@go tool cover -func="$(BACKEND_COVERAGE)" | tail -1

## Run read-only static checks and tests.
check: _check-go-format _check-go-mod
	@$(GO) vet ./...
	@$(MAKE) --no-print-directory web-lint
	@$(MAKE) --no-print-directory web-typecheck
	@$(MAKE) --no-print-directory test

_check-go-format:
	@$(GOIMPORTS_REVISER) \
		-rm-unused \
		-format \
		-imports-order std,general,company,project,blanked,dotted \
		-list-diff \
		-set-exit-status \
		./...

_check-go-mod:
	@$(GO) mod tidy -diff
	@$(GO) mod verify

## Format Go and Web sources.
format:
	@$(GOIMPORTS_REVISER) \
		-rm-unused \
		-format \
		-imports-order std,general,company,project,blanked,dotted \
		./...
	@$(MAKE) --no-print-directory web-format

## Install development tools and project dependencies.
setup: _install-go-tools web-install
	@$(GO) mod download

_install-go-tools:
	@$(GO) install github.com/incu6us/goimports-reviser/v3@$(GOIMPORTS_REVISER_VERSION)

## Install deterministic dependencies and run all checks in CI.
ci: _install-go-tools web-ci-install
	@$(GO) mod download
	@$(MAKE) --no-print-directory check

## Install Web dependencies locally.
web-install:
	@cd "$(WEB_DIR)" && $(NPM) install

## Install exact Web dependencies in CI.
web-ci-install:
	@cd "$(WEB_DIR)" && $(NPM) ci

## Check Web sources with Biome.
web-lint:
	@cd "$(WEB_DIR)" && $(NPM) run lint

## Type-check Web sources.
web-typecheck:
	@cd "$(WEB_DIR)" && $(NPM) run type-check

## Build React WebUI for Go embedding.
web-build:
	@cd "$(WEB_DIR)" && $(NPM) run build

## Format Web sources with Biome.
web-format:
	@cd "$(WEB_DIR)" && $(NPM) run format

## Build and start the server locally.
serve: build
	./$(HOST_BINARY) serve

## Remove binaries, coverage files and dist/.
clean:
	@rm -f \
		"./$(BINARY_NAME)" \
		"./$(BINARY_NAME).exe"
	@rm -rf "$(DIST_DIR)" "$(COVERAGE_DIR)" "$(WEB_DIR)/dist"

## Print the resolved version string.
version:
	@echo "$(TINYSYNC_VERSION)"

## Show available targets.
help:
	@echo "TinySync Makefile"
	@echo ""
	@echo "Version: $(TINYSYNC_VERSION)"
	@echo ""
	@echo "Development:"
	@echo "  make setup          Install tools and dependencies"
	@echo "  make build          Build tinysync for the current platform"
	@echo "  make serve          Build and start the server"
	@echo "  make version        Print the resolved version"
	@echo ""
	@echo "Quality:"
	@echo "  make format         Format Go and Web sources"
	@echo "  make check          Static checks and tests"
	@echo "  make test           Run Go tests"
	@echo "  make coverage       Generate coverage (coverage/backend.*)"
	@echo "  make ci             Deterministic install + all checks"
	@echo ""
	@echo "Web:"
	@echo "  make web-install    Install Web dependencies"
	@echo "  make web-ci-install Install exact Web dependencies (npm ci)"
	@echo "  make web-lint       Check Web sources with Biome"
	@echo "  make web-build      Build WebUI for embedding"
	@echo ""
	@echo "Distribution:"
	@echo "  make build-all      Cross-compile six platform binaries to dist/"
	@echo "  make build-os       Build one platform with OS= and ARCH="
	@echo "  make dist           Create six archives and checksums.txt"
	@echo ""
	@echo "Maintenance:"
	@echo "  make clean          Remove generated files"
	@echo "  make help           Show this help"
