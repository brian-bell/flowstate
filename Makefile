BIN_DIR  = bin
BINARY   = $(BIN_DIR)/flowstate
VERSION_PACKAGE = github.com/brian-bell/flowstate/internal/version
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X $(VERSION_PACKAGE).version=dev -X $(VERSION_PACKAGE).commit=$(COMMIT) -X $(VERSION_PACKAGE).date=$(DATE)
PNPM ?= pnpm
WEB_DIR = web
WEB_BUILD_OUTPUT = $(WEB_DIR)/dist/client
WEB_EMBED_DIR = server/webassets/dist
SPA_SHELL = _shell.html

.PHONY: build test run clean tidy web-install web-typecheck web-build web-dev web-test

build: web-build
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/flowstate

web-install:
	$(PNPM) --dir $(WEB_DIR) install --frozen-lockfile

web-typecheck: web-install
	$(PNPM) --dir $(WEB_DIR) typecheck

web-build: web-typecheck
	$(PNPM) --dir $(WEB_DIR) build
	test -f "$(WEB_BUILD_OUTPUT)/$(SPA_SHELL)"
	find "$(WEB_EMBED_DIR)" -mindepth 1 -delete
	mkdir -p "$(WEB_EMBED_DIR)"
	cp -R "$(WEB_BUILD_OUTPUT)/." "$(WEB_EMBED_DIR)/"
	node "$(WEB_DIR)/scripts/normalize-spa-shell.mjs" "$(WEB_EMBED_DIR)/$(SPA_SHELL)"

web-dev: web-install
	$(PNPM) --dir $(WEB_DIR) dev

web-test:
	node --test "$(WEB_DIR)/scripts/normalize-spa-shell.test.mjs"

test: web-test
	go test ./...

run: build
	XDG_CONFIG_HOME="$(CURDIR)/.config" ./$(BINARY)

clean:
	rm -rf $(BIN_DIR) $(WEB_DIR)/dist

tidy:
	go mod tidy
