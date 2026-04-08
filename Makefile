BIN_DIR     := $(HOME)/.local/bin
APP_DIR     := /Applications
PLIST_NAME  := com.mikehu.cmdr.plist
LABEL       := com.mikehu.cmdr
LAUNCH_DIR  := $(HOME)/Library/LaunchAgents
GUI_DOMAIN  := gui/$(shell id -u)

.PHONY: all build web go app install uninstall restart clean dev

# Default: build everything
all: build

# Build frontend + backend + app
build: web go app

# Build SvelteKit SPA → web/build/
web:
	@echo "cmdr: building frontend..."
	@cd web && bun run build

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Build Go binary (embeds web/build/, stamps version)
go:
	@echo "cmdr: building backend ($(VERSION))..."
	@go build -ldflags="-X main.version=$(VERSION)" -o cmdr ./cmd/cmdr

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
install: build
	@mkdir -p $(BIN_DIR) $(LAUNCH_DIR)
	@codesign -s "cmdr" -f cmdr
	@cp cmdr $(BIN_DIR)/cmdr
	@echo "cmdr: installed binary to $(BIN_DIR)/cmdr"
	@launchctl bootout "$(GUI_DOMAIN)/com.mikehu.cmdrd" 2>/dev/null || true
	@rm -f $(BIN_DIR)/cmdrd $(LAUNCH_DIR)/com.mikehu.cmdrd.plist
	@launchctl bootout "$(GUI_DOMAIN)/$(LABEL)" 2>/dev/null || true
	@sleep 1
	@sed 's|__CMDR_BIN__|$(BIN_DIR)/cmdr|g' $(PLIST_NAME) > $(LAUNCH_DIR)/$(PLIST_NAME)
	@launchctl bootstrap "$(GUI_DOMAIN)" "$(LAUNCH_DIR)/$(PLIST_NAME)"
	@rm -f cmdr
	@rsync -a --delete build/cmdr.app/ "$(APP_DIR)/cmdr.app/"
	@echo "cmdr: installed app to $(APP_DIR)/cmdr.app"
	@echo "cmdr: service installed and started ✓"

# Stop and remove service
uninstall:
	@launchctl bootout "$(GUI_DOMAIN)/$(LABEL)" 2>/dev/null || true
	@rm -f $(BIN_DIR)/cmdr $(LAUNCH_DIR)/$(PLIST_NAME)
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
