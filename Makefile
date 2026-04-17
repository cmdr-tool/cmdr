BIN_DIR     := $(HOME)/.local/bin
APP_DIR     := /Applications
PLIST_TPL   := com.cmdr.plist.tpl
CONFIG_FILE := $(HOME)/.cmdr/cmdr.env
LAUNCH_DIR  := $(HOME)/Library/LaunchAgents
GUI_DOMAIN  := gui/$(shell id -u)

# Values read from $(CONFIG_FILE) (populated by scripts/setup.sh on first install)
LABEL             = $(shell [ -f $(CONFIG_FILE) ] && grep ^CMDR_LABEL= $(CONFIG_FILE) | cut -d= -f2-)
CMDR_CODE_DIR     = $(shell [ -f $(CONFIG_FILE) ] && grep ^CMDR_CODE_DIR= $(CONFIG_FILE) | cut -d= -f2-)
CMDR_OLLAMA_URL   = $(shell [ -f $(CONFIG_FILE) ] && grep ^CMDR_OLLAMA_URL= $(CONFIG_FILE) | cut -d= -f2-)
CMDR_OLLAMA_MODEL = $(shell [ -f $(CONFIG_FILE) ] && grep ^CMDR_OLLAMA_MODEL= $(CONFIG_FILE) | cut -d= -f2-)
CMDR_SUMMARIZER   = $(shell [ -f $(CONFIG_FILE) ] && grep ^CMDR_SUMMARIZER= $(CONFIG_FILE) | cut -d= -f2-)
CMDR_MULTIPLEXER  = $(shell [ -f $(CONFIG_FILE) ] && grep ^CMDR_MULTIPLEXER= $(CONFIG_FILE) | cut -d= -f2-)
CMDR_TERMINAL_APP = $(shell [ -f $(CONFIG_FILE) ] && grep ^CMDR_TERMINAL_APP= $(CONFIG_FILE) | cut -d= -f2-)
CMDR_EDITOR       = $(shell [ -f $(CONFIG_FILE) ] && grep ^CMDR_EDITOR= $(CONFIG_FILE) | cut -d= -f2-)
PLIST_NAME        = $(LABEL).plist

.PHONY: all build web go app summarize install setup configure uninstall restart clean dev test

# Default: build everything
all: build

# Build frontend + backend + app + summarizer
build: web go app summarize

# Build SvelteKit SPA → web/build/
web:
	@echo "cmdr: building frontend..."
	@cd web && bun run build

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Build Go binary (embeds web/build/, stamps version)
go:
	@echo "cmdr: building backend ($(VERSION))..."
	@go build -ldflags="-X main.version=$(VERSION)" -o cmdr ./cmd/cmdr

# Build cmdr-summarize (Apple Intelligence title generation)
summarize:
	@echo "cmdr: building summarizer..."
	@mkdir -p build
	@swiftc -O -o build/cmdr-summarize tools/cmdr-summarize/main.swift -framework FoundationModels 2>/dev/null \
		|| echo "cmdr: skipped cmdr-summarize (requires macOS 15.1+ SDK)"

# Build macOS app bundle (frameless webview wrapper)
app:
	@echo "cmdr: building app..."
	@mkdir -p build
	@swiftc -O -o build/cmdr-app app/main.swift -framework Cocoa -framework WebKit -framework UserNotifications
	@mkdir -p build/cmdr.app/Contents/{MacOS,Resources}
	@cp build/cmdr-app build/cmdr.app/Contents/MacOS/cmdr-app
	@cp app/Info.plist build/cmdr.app/Contents/
	@cp app/assets/AppIcon.icns build/cmdr.app/Contents/Resources/
	@cp app/assets/menubarTemplate.png app/assets/menubarTemplate@2x.png build/cmdr.app/Contents/Resources/
	@rm -f build/cmdr-app

# Full deploy: build → install binary + app → restart service
install: setup build
	@mkdir -p $(BIN_DIR) $(LAUNCH_DIR)
	@cp cmdr $(BIN_DIR)/cmdr
	@codesign --force --sign "cmdr" --options runtime $(BIN_DIR)/cmdr
	@xattr -d com.apple.provenance $(BIN_DIR)/cmdr 2>/dev/null || true
	@if [ -f build/cmdr-summarize ]; then \
		cp build/cmdr-summarize $(BIN_DIR)/cmdr-summarize; \
		codesign --force --sign "cmdr" --options runtime $(BIN_DIR)/cmdr-summarize; \
		xattr -d com.apple.provenance $(BIN_DIR)/cmdr-summarize 2>/dev/null || true; \
	fi
	@echo "cmdr: installed binary to $(BIN_DIR)/cmdr"
	@launchctl bootout "$(GUI_DOMAIN)/$(LABEL)" 2>/dev/null || true
	@sleep 1
	@sed -e 's|__LABEL__|$(LABEL)|g' \
	     -e 's|__CMDR_BIN__|$(BIN_DIR)/cmdr|g' \
	     -e 's|__CMDR_CODE_DIR__|$(CMDR_CODE_DIR)|g' \
	     -e 's|__CMDR_OLLAMA_URL__|$(CMDR_OLLAMA_URL)|g' \
	     -e 's|__CMDR_OLLAMA_MODEL__|$(CMDR_OLLAMA_MODEL)|g' \
	     -e 's|__CMDR_SUMMARIZER__|$(CMDR_SUMMARIZER)|g' \
	     -e 's|__CMDR_MULTIPLEXER__|$(CMDR_MULTIPLEXER)|g' \
	     -e 's|__CMDR_TERMINAL_APP__|$(CMDR_TERMINAL_APP)|g' \
	     -e 's|__CMDR_EDITOR__|$(CMDR_EDITOR)|g' \
	     $(PLIST_TPL) > $(LAUNCH_DIR)/$(PLIST_NAME)
	@launchctl bootstrap "$(GUI_DOMAIN)" "$(LAUNCH_DIR)/$(PLIST_NAME)"
	@rm -f cmdr
	@rsync -a --delete build/cmdr.app/ "$(APP_DIR)/cmdr.app/"
	@echo "cmdr: installed app to $(APP_DIR)/cmdr.app"
	@$(BIN_DIR)/cmdr init
	@echo "cmdr: service installed and started ✓"

# Run first-run setup if config is missing
setup:
	@[ -f $(CONFIG_FILE) ] || bash scripts/setup.sh

# Re-run setup (edit config) and regenerate plist
configure:
	@bash scripts/setup.sh

# Stop and remove service
uninstall:
	@launchctl bootout "$(GUI_DOMAIN)/$(LABEL)" 2>/dev/null || true
	@rm -f $(BIN_DIR)/cmdr $(BIN_DIR)/cmdr-summarize $(LAUNCH_DIR)/$(PLIST_NAME)
	@echo "cmdr: uninstalled ✓"

# Restart service without rebuilding
restart:
	@launchctl bootout "$(GUI_DOMAIN)/$(LABEL)" 2>/dev/null || true
	@launchctl bootstrap "$(GUI_DOMAIN)" "$(LAUNCH_DIR)/$(PLIST_NAME)"
	@echo "cmdr: restarted ✓"

# Dev: just Vite HMR, proxies API to production daemon
dev:
	@cd web && bun run vite dev

# Run Go tests
test:
	@go test ./...

# Clean build artifacts
clean:
	@rm -f cmdr
	@rm -rf web/build build/
	@echo "cmdr: cleaned ✓"
