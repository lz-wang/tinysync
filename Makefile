# =============================================================================
# TinySync Makefile
# =============================================================================

.DEFAULT_GOAL := help

.PHONY: build version help

BINARY_NAME := tinysync
MAIN_PATH := .

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
GO_LDFLAGS := -X $(VERSION_VAR)=$(TINYSYNC_VERSION)
# Release / cross build 追加 -s -w 去符号；开发构建保持可调试。
GO_RELEASE_LDFLAGS := -s -w $(GO_LDFLAGS)

## Build tinysync for the current platform.
build:
	@echo "[tinysync] build $(TINYSYNC_VERSION) -> ./$(BINARY_NAME)"
	@CGO_ENABLED=0 go build -trimpath -ldflags "$(GO_LDFLAGS)" -o "./$(BINARY_NAME)" $(MAIN_PATH)

## Print the resolved version string.
version:
	@echo "$(TINYSYNC_VERSION)"

## Show available targets.
help:
	@echo "TinySync Makefile"
	@echo ""
	@echo "Version: $(TINYSYNC_VERSION)"
	@echo ""
	@echo "Targets:"
	@echo "  make build          Build tinysync for the current platform"
	@echo "  make version        Print the resolved version"
	@echo "  make help           Show this help"
