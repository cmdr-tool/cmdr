import Cocoa
import WebKit

// MARK: - Gradient view behind traffic lights

class TitlebarGradientView: NSView {
    override func draw(_ dirtyRect: NSRect) {
        guard let context = NSGraphicsContext.current?.cgContext else { return }
        let colors = [
            NSColor(red: 0.07, green: 0.06, blue: 0.05, alpha: 0.95).cgColor,  // bourbon-950
            NSColor(red: 0.07, green: 0.06, blue: 0.05, alpha: 0.0).cgColor
        ] as CFArray
        if let gradient = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(), colors: colors, locations: [0, 1]) {
            context.drawLinearGradient(gradient, start: CGPoint(x: 0, y: bounds.height), end: CGPoint(x: 0, y: 0), options: [])
        }
    }
}

// MARK: - Hover-tracking content view

class TrackingContentView: NSView {
    var onMouseEnter: (() -> Void)?
    var onMouseExit: (() -> Void)?
    private var trackingArea: NSTrackingArea?

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if let existing = trackingArea { removeTrackingArea(existing) }
        trackingArea = NSTrackingArea(
            rect: bounds,
            options: [.mouseEnteredAndExited, .activeAlways],
            owner: self,
            userInfo: nil
        )
        addTrackingArea(trackingArea!)
    }

    override func mouseEntered(with event: NSEvent) { onMouseEnter?() }
    override func mouseExited(with event: NSEvent) { onMouseExit?() }
}

// MARK: - App Delegate

class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate {
    var window: NSWindow!
    var webView: WKWebView!
    var statusItem: NSStatusItem!
    var gradientView: TitlebarGradientView!
    private var trafficLightButtons: [NSButton] = []

    func applicationDidFinishLaunching(_ notification: Notification) {
        setupMenuBar()
        setupMainMenu()
        setupWindow()
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        if !flag { window.makeKeyAndOrderFront(nil) }
        return true
    }

    func applicationSupportsSecureRestorableState(_ app: NSApplication) -> Bool { true }

    // Hide instead of close — the app stays alive for the menubar icon
    func windowShouldClose(_ sender: NSWindow) -> Bool {
        sender.orderOut(nil)
        return false
    }

    // MARK: - Main Menu

    private func setupMainMenu() {
        let mainMenu = NSMenu()

        // App menu
        let appMenu = NSMenu()
        appMenu.addItem(withTitle: "About cmdr", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Hide cmdr", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        let hideOthers = appMenu.addItem(withTitle: "Hide Others", action: #selector(NSApplication.hideOtherApplications(_:)), keyEquivalent: "h")
        hideOthers.keyEquivalentModifierMask = [.command, .option]
        appMenu.addItem(withTitle: "Show All", action: #selector(NSApplication.unhideAllApplications(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Quit cmdr", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        let appItem = NSMenuItem()
        appItem.submenu = appMenu
        mainMenu.addItem(appItem)

        // File menu
        let fileMenu = NSMenu(title: "File")
        fileMenu.addItem(withTitle: "Close Window", action: #selector(hideWindow), keyEquivalent: "w")
        let fileItem = NSMenuItem()
        fileItem.submenu = fileMenu
        mainMenu.addItem(fileItem)

        // Edit menu (enables Cmd+C/V/X/A in the webview)
        let editMenu = NSMenu(title: "Edit")
        editMenu.addItem(withTitle: "Undo", action: Selector(("undo:")), keyEquivalent: "z")
        editMenu.addItem(withTitle: "Redo", action: Selector(("redo:")), keyEquivalent: "Z")
        editMenu.addItem(.separator())
        editMenu.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        editMenu.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        editMenu.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        editMenu.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        let editItem = NSMenuItem()
        editItem.submenu = editMenu
        mainMenu.addItem(editItem)

        // View menu
        let viewMenu = NSMenu(title: "View")
        viewMenu.addItem(withTitle: "Reload", action: #selector(reloadPage), keyEquivalent: "r")
        let viewItem = NSMenuItem()
        viewItem.submenu = viewMenu
        mainMenu.addItem(viewItem)

        NSApplication.shared.mainMenu = mainMenu
    }

    @objc private func hideWindow() {
        window.orderOut(nil)
    }

    @objc private func reloadPage() {
        webView.reload()
    }

    // MARK: - Menu Bar

    private func setupMenuBar() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        if let button = statusItem.button {
            if let img = NSImage(contentsOfFile: Bundle.main.resourcePath! + "/menubarTemplate.png") {
                img.isTemplate = true
                button.image = img
            }
            button.action = #selector(handleMenuBarClick(_:))
            button.target = self
            button.sendAction(on: [.leftMouseUp, .rightMouseUp])
        }
    }

    @objc private func handleMenuBarClick(_ sender: NSStatusBarButton) {
        guard let event = NSApp.currentEvent else { return }
        if event.type == .rightMouseUp {
            let menu = NSMenu()
            menu.addItem(withTitle: "Quit cmdr", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "")
            statusItem.menu = menu
            sender.performClick(nil)
            DispatchQueue.main.async { self.statusItem.menu = nil }
        } else {
            showWindow()
        }
    }

    @objc private func showWindow() {
        window.makeKeyAndOrderFront(nil)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }

    // MARK: - Traffic Lights

    private func setupTrafficLights() {
        trafficLightButtons = [.closeButton, .miniaturizeButton, .zoomButton].compactMap {
            window.standardWindowButton($0)
        }
        // Start hidden
        setTrafficLightsVisible(false, animated: false)
    }

    private func setTrafficLightsVisible(_ visible: Bool, animated: Bool) {
        let targetAlpha: CGFloat = visible ? 1 : 0
        let gradientAlpha: CGFloat = visible ? 1 : 0

        if animated {
            NSAnimationContext.runAnimationGroup { context in
                context.duration = visible ? 0.2 : 0.4
                context.timingFunction = CAMediaTimingFunction(name: .easeInEaseOut)
                for button in trafficLightButtons {
                    button.animator().alphaValue = targetAlpha
                }
                gradientView.animator().alphaValue = gradientAlpha
            }
        } else {
            for button in trafficLightButtons { button.alphaValue = targetAlpha }
            gradientView.alphaValue = gradientAlpha
        }
    }

    // MARK: - Window

    private func setupWindow() {
        let screen = NSScreen.main!
        let width: CGFloat = 1280
        let height: CGFloat = 860
        let x = (screen.frame.width - width) / 2
        let y = (screen.frame.height - height) / 2

        window = NSWindow(
            contentRect: NSRect(x: x, y: y, width: width, height: height),
            styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
            backing: .buffered,
            defer: false
        )
        window.titlebarAppearsTransparent = true
        window.titleVisibility = .hidden
        window.title = "cmdr"
        window.delegate = self
        window.isMovableByWindowBackground = true
        window.minSize = NSSize(width: 800, height: 500)
        window.backgroundColor = NSColor(red: 0.07, green: 0.06, blue: 0.05, alpha: 1) // bourbon-950

        // Content view with mouse tracking
        let container = TrackingContentView()
        container.autoresizingMask = [.width, .height]
        container.onMouseEnter = { [weak self] in self?.setTrafficLightsVisible(true, animated: true) }
        container.onMouseExit = { [weak self] in self?.setTrafficLightsVisible(false, animated: true) }
        window.contentView = container

        // Web view fills the container
        let config = WKWebViewConfiguration()
        // Disable right-click context menu
        let script = WKUserScript(source: "document.addEventListener('contextmenu', e => e.preventDefault());", injectionTime: .atDocumentStart, forMainFrameOnly: false)
        config.userContentController.addUserScript(script)
        webView = WKWebView(frame: container.bounds, configuration: config)
        webView.customUserAgent = "cmdr-app"
        webView.autoresizingMask = [.width, .height]
        container.addSubview(webView)

        // Gradient overlay behind traffic lights
        gradientView = TitlebarGradientView()
        gradientView.frame = NSRect(x: 0, y: container.bounds.height - 42, width: 100, height: 42)
        gradientView.autoresizingMask = [.minYMargin]
        gradientView.alphaValue = 0
        container.addSubview(gradientView)

        let url = URL(string: "http://127.0.0.1:7369")!
        webView.load(URLRequest(url: url))

        setupTrafficLights()

        window.makeKeyAndOrderFront(nil)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }
}

// MARK: - Main

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.run()
