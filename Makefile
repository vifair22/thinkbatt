# thinkbatt - battery charge-policy and fuel-gauge calibration for ThinkPads.
#
# All build output lands under build/: binaries in build/bin/, coverage in
# build/coverage/. Pure ASCII, per project convention.
#
# make            build both binaries into build/bin/
# make test       go tests under -race
# make coverage   tests with a statement-coverage profile and the 80% gate
# make analyze    go vet, gofmt check, golangci-lint when installed
# make check      ascii-check over the tracked tree
# make install    install both binaries into $(PREFIX)/bin
# make clean      remove build/

SHELL       := /bin/bash
.SHELLFLAGS := -o pipefail -c

VARIANT      ?= release
PREFIX       ?= /usr/local
GO           ?= go
COVERAGE_MIN ?= 80

BUILD_DIR := build
BIN_DIR   := $(BUILD_DIR)/bin
COV_DIR   := $(BUILD_DIR)/coverage

VERSION      := $(shell tr -d '\n' < release_version)
BUILD_STAMP  := $(shell date -u +%Y%m%d.%H%M)
FULL_VERSION := $(VERSION)_$(BUILD_STAMP).$(VARIANT)

GO_LDFLAGS := -s -w -X main.version=$(FULL_VERSION) -X main.buildTime=$(BUILD_STAMP)
ifeq ($(VARIANT),debug)
GO_FLAGS   :=
GO_LDFLAGS := -X main.version=$(FULL_VERSION) -X main.buildTime=$(BUILD_STAMP)
else
GO_FLAGS   := -trimpath
endif

DAEMON_BIN := $(BIN_DIR)/thinkbattd
CLI_BIN    := $(BIN_DIR)/thinkbatt

.DEFAULT_GOAL := all
# The build targets are phony: go build is itself incremental and the correct
# arbiter of staleness, so always invoke it rather than tracking sources here.
.PHONY: all test coverage analyze check install uninstall clean version \
        $(DAEMON_BIN) $(CLI_BIN)

all: $(DAEMON_BIN) $(CLI_BIN)

version:
	@echo "$(FULL_VERSION)"

$(BIN_DIR) $(COV_DIR):
	mkdir -p $@

$(DAEMON_BIN): | $(BIN_DIR)
	$(GO) build $(GO_FLAGS) -ldflags '$(GO_LDFLAGS)' -o $@ ./cmd/thinkbattd

$(CLI_BIN): | $(BIN_DIR)
	$(GO) build $(GO_FLAGS) -ldflags '$(GO_LDFLAGS)' -o $@ ./cmd/thinkbatt

test:
	$(GO) test -race ./...

# The gate reads the module total from `go tool cover` rather than any one
# package, so a well-covered package cannot hide a bare one.
coverage: | $(COV_DIR)
	$(GO) test -race -covermode=atomic -coverprofile=$(COV_DIR)/go.out ./...
	@$(GO) tool cover -func=$(COV_DIR)/go.out | tail -1
	@$(GO) tool cover -func=$(COV_DIR)/go.out | awk -v min=$(COVERAGE_MIN) \
	    '/^total:/ { sub("%","",$$3); if ($$3+0 < min) { printf "coverage %s%% is below %s%%\n", $$3, min; exit 1 } }'

analyze:
	$(GO) vet ./...
	@out=$$(gofmt -l $$(git ls-files -co --exclude-standard '*.go')); \
	    if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; \
	    else echo "golangci-lint not installed; CI runs it"; fi

check:
	ascii-check -r .

install: all
	install -Dm755 $(DAEMON_BIN) $(DESTDIR)$(PREFIX)/bin/thinkbattd
	install -Dm755 $(CLI_BIN) $(DESTDIR)$(PREFIX)/bin/thinkbatt

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/thinkbattd $(DESTDIR)$(PREFIX)/bin/thinkbatt

clean:
	rm -rf $(BUILD_DIR)
