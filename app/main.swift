import Cocoa
import WebKit
import UserNotifications

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

class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate, WKScriptMessageHandler, WKUIDelegate, UNUserNotificationCenterDelegate {
    var window: NSWindow!
    var webView: WKWebView!
    var statusItem: NSStatusItem!
    var gradientView: TitlebarGradientView!
    private var trafficLightButtons: [NSButton] = []
    private var menuBarIcon: NSImage?
    private var menuBarIconActive: NSImage?
    private var activityTimer: Timer?
    private var isClaudeActive = false

    func applicationDidFinishLaunching(_ notification: Notification) {
        setupNotifications()
        setupMenuBar()
        setupMainMenu()
        setupWindow()
        startActivityPolling()
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

    // MARK: - Notifications

    private func setupNotifications() {
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        center.requestAuthorization(options: [.alert, .sound]) { _, _ in }
    }

    // Handle messages from the webview
    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.name == "notify", let body = message.body as? [String: Any],
              let title = body["title"] as? String else { return }

        let content = UNMutableNotificationContent()
        content.title = title
        if let subtitle = body["body"] as? String { content.body = subtitle }
        content.sound = .default

        let request = UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)
        UNUserNotificationCenter.current().add(request)
    }

    // MARK: - WKUIDelegate (external links)

    // Handle target="_blank" links by opening in the system browser
    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration, for navigationAction: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        if let url = navigationAction.request.url {
            NSWorkspace.shared.open(url)
        }
        return nil
    }

    // Clicking a notification brings the app to focus
    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse, withCompletionHandler completionHandler: @escaping () -> Void) {
        showWindow()
        completionHandler()
    }

    // Show notifications even when app is frontmost (for testing)
    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification, withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .sound])
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
                menuBarIcon = img
                menuBarIconActive = makeActiveIcon(from: img)
                button.image = img
            }
            button.action = #selector(handleMenuBarClick(_:))
            button.target = self
            button.sendAction(on: [.leftMouseUp, .rightMouseUp])
        }
    }

    /// Create a non-template copy of the icon with a green activity dot.
    private func makeActiveIcon(from base: NSImage) -> NSImage {
        let size = base.size
        let icon = NSImage(size: size, flipped: false) { rect in
            // Draw the base icon in menu bar color (black for light mode)
            NSColor.black.setFill()
            base.draw(in: rect, from: .zero, operation: .sourceOver, fraction: 1.0)

            // Draw green dot (bottom-right corner)
            let dotSize: CGFloat = 5
            let dotRect = NSRect(
                x: rect.width - dotSize - 0.5,
                y: 0.5,
                width: dotSize,
                height: dotSize
            )
            NSColor(red: 0.3, green: 0.8, blue: 0.4, alpha: 1.0).setFill()
            NSBezierPath(ovalIn: dotRect).fill()
            return true
        }
        // Non-template so the green dot retains its color
        icon.isTemplate = false
        return icon
    }

    @objc private func handleMenuBarClick(_ sender: NSStatusBarButton) {
        guard let event = NSApp.currentEvent else { return }
        if event.type == .rightMouseUp {
            showRepoMenu(from: sender)
        } else {
            showWindow()
        }
    }

    private func showRepoMenu(from sender: NSStatusBarButton) {
        // Fetch synchronously — it's localhost so sub-millisecond
        let repos = fetchReposSync()
        let menu = buildRepoMenu(repos: repos)
        statusItem.menu = menu
        sender.performClick(nil)
        DispatchQueue.main.async { self.statusItem.menu = nil }
    }

    private func buildRepoMenu(repos: [[String: Any]]) -> NSMenu {
        let menu = NSMenu()

        // Group repos by squad (empty squad = top-level)
        var grouped: [(squad: String, repos: [[String: Any]])] = []
        var bySquad: [String: [[String: Any]]] = [:]
        var insertOrder: [String] = []
        for repo in repos {
            let squad = repo["squad"] as? String ?? ""
            if bySquad[squad] == nil { insertOrder.append(squad) }
            bySquad[squad, default: []].append(repo)
        }
        for squad in insertOrder {
            grouped.append((squad: squad, repos: bySquad[squad]!))
        }

        for group in grouped {
            if group.squad.isEmpty {
                // Top-level repos
                for repo in group.repos {
                    addRepoMenuItem(to: menu, repo: repo)
                }
            } else {
                // Squad submenu
                let squadItem = NSMenuItem(title: group.squad, action: nil, keyEquivalent: "")
                squadItem.image = folderIcon(size: 14)
                let squadMenu = NSMenu()
                for repo in group.repos {
                    addRepoMenuItem(to: squadMenu, repo: repo)
                }
                squadItem.submenu = squadMenu
                menu.addItem(squadItem)
            }
        }

        if repos.isEmpty {
            menu.addItem(withTitle: "No repos configured", action: nil, keyEquivalent: "")
        }

        menu.addItem(.separator())
        menu.addItem(withTitle: "Quit cmdr", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "")

        return menu
    }

    private func addRepoMenuItem(to menu: NSMenu, repo: [String: Any]) {
        let name = repo["name"] as? String ?? "unknown"
        let path = repo["path"] as? String ?? ""
        let displayName = name.split(separator: "/").last.map(String.init) ?? name

        let item = NSMenuItem(title: displayName, action: nil, keyEquivalent: "")
        item.image = folderIcon(size: 14)
        let submenu = FolderSubmenu(path: path)
        item.submenu = submenu
        menu.addItem(item)
    }

    private func folderIcon(size: CGFloat) -> NSImage {
        let img = NSWorkspace.shared.icon(for: .folder)
        img.size = NSSize(width: size, height: size)
        return img
    }

    @objc private func showWindow() {
        window.makeKeyAndOrderFront(nil)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }

    // MARK: - Activity Polling

    private func startActivityPolling() {
        activityTimer = Timer.scheduledTimer(withTimeInterval: 5.0, repeats: true) { [weak self] _ in
            self?.pollActivity()
        }
    }

    private func pollActivity() {
        guard let url = URL(string: "http://127.0.0.1:7369/api/claude/tasks") else { return }
        URLSession.shared.dataTask(with: url) { [weak self] data, _, _ in
            guard let self = self, let data = data,
                  let tasks = try? JSONSerialization.jsonObject(with: data) as? [[String: Any]] else { return }

            let activeStatuses: Set<String> = ["running", "pending", "refactoring", "implementing"]
            let hasActive = tasks.contains { task in
                guard let status = task["status"] as? String else { return false }
                return activeStatuses.contains(status)
            }

            DispatchQueue.main.async {
                if hasActive != self.isClaudeActive {
                    self.isClaudeActive = hasActive
                    self.statusItem.button?.image = hasActive ? self.menuBarIconActive : self.menuBarIcon
                }
            }
        }.resume()
    }

    // MARK: - API Helpers

    private func fetchReposSync() -> [[String: Any]] {
        guard let url = URL(string: "http://127.0.0.1:7369/api/repos") else { return [] }
        guard let data = try? Data(contentsOf: url) else { return [] }
        return (try? JSONSerialization.jsonObject(with: data) as? [[String: Any]]) ?? []
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
        let noCtxMenu = WKUserScript(source: "document.addEventListener('contextmenu', e => e.preventDefault());", injectionTime: .atDocumentStart, forMainFrameOnly: false)
        config.userContentController.addUserScript(noCtxMenu)
        // Block backspace/delete from triggering back navigation (SPA has no history to navigate)
        let noBackNav = WKUserScript(source: """
            document.addEventListener('keydown', function(e) {
                if ((e.key === 'Backspace' || e.key === 'Delete') &&
                    !['INPUT','TEXTAREA'].includes(e.target.tagName) &&
                    !e.target.isContentEditable) {
                    e.preventDefault();
                }
            });
            """, injectionTime: .atDocumentStart, forMainFrameOnly: false)
        config.userContentController.addUserScript(noBackNav)
        // Register message handler for native notifications
        config.userContentController.add(self, name: "notify")
        webView = WKWebView(frame: container.bounds, configuration: config)
        webView.uiDelegate = self
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

// MARK: - Lazy folder submenu

/// A submenu that lazily populates its contents from the filesystem when opened.
/// Each subdirectory gets its own FolderSubmenu for recursive drill-down.
class FolderSubmenu: NSMenu, NSMenuDelegate {
    let path: String
    private var populated = false

    init(path: String) {
        self.path = path
        super.init(title: path.split(separator: "/").last.map(String.init) ?? path)
        self.delegate = self
        // Placeholder so macOS shows the submenu arrow
        addItem(withTitle: "Loading…", action: nil, keyEquivalent: "")
    }

    required init(coder: NSCoder) { fatalError() }

    func menuWillOpen(_ menu: NSMenu) {
        guard !populated else { return }
        populated = true
        removeAllItems()

        // "Show in Finder" action at top
        let showItem = NSMenuItem(title: "Show in Finder", action: #selector(FolderMenuActions.openInFinder(_:)), keyEquivalent: "")
        showItem.target = FolderMenuActions.shared
        showItem.representedObject = path
        showItem.image = NSImage(systemSymbolName: "folder", accessibilityDescription: nil)
        addItem(showItem)

        // "Open in Terminal" action
        let termItem = NSMenuItem(title: "Open in Terminal", action: #selector(FolderMenuActions.openInTerminal(_:)), keyEquivalent: "")
        termItem.target = FolderMenuActions.shared
        termItem.representedObject = path
        termItem.image = NSImage(systemSymbolName: "terminal", accessibilityDescription: nil)
        addItem(termItem)

        addItem(.separator())

        // List directory contents
        guard let entries = try? FileManager.default.contentsOfDirectory(atPath: path) else {
            addItem(withTitle: "(empty)", action: nil, keyEquivalent: "")
            return
        }

        let visible = entries.filter { !$0.hasPrefix(".") }
        let ignored = gitIgnored(in: path, entries: visible)
        let sorted = visible.filter { !ignored.contains($0) }
            .sorted { $0.localizedCaseInsensitiveCompare($1) == .orderedAscending }

        // Directories first, then files
        var dirs: [(String, String)] = []
        var files: [(String, String)] = []
        for entry in sorted {
            let fullPath = (path as NSString).appendingPathComponent(entry)
            var isDir: ObjCBool = false
            FileManager.default.fileExists(atPath: fullPath, isDirectory: &isDir)
            if isDir.boolValue {
                dirs.append((entry, fullPath))
            } else {
                files.append((entry, fullPath))
            }
        }

        for (name, fullPath) in dirs {
            let item = NSMenuItem(title: name, action: nil, keyEquivalent: "")
            item.image = NSWorkspace.shared.icon(for: .folder)
            item.image?.size = NSSize(width: 14, height: 14)
            item.submenu = FolderSubmenu(path: fullPath)
            addItem(item)
        }

        if !dirs.isEmpty && !files.isEmpty {
            addItem(.separator())
        }

        for (name, fullPath) in files {
            let item = NSMenuItem(title: name, action: #selector(FolderMenuActions.openFile(_:)), keyEquivalent: "")
            item.target = FolderMenuActions.shared
            item.representedObject = fullPath
            item.image = NSWorkspace.shared.icon(forFile: fullPath)
            item.image?.size = NSSize(width: 14, height: 14)
            addItem(item)
        }

        if dirs.isEmpty && files.isEmpty {
            addItem(withTitle: "(empty)", action: nil, keyEquivalent: "")
        }
    }
}

/// Ask git which entries are ignored. Uses `git check-ignore` so it respects
/// .gitignore, .git/info/exclude, nested ignores, and global ignores.
/// Returns a set of ignored entry names. Falls back to empty if not a git repo.
func gitIgnored(in dir: String, entries: [String]) -> Set<String> {
    guard !entries.isEmpty else { return [] }
    let task = Process()
    task.executableURL = URL(fileURLWithPath: "/usr/bin/git")
    task.arguments = ["-C", dir, "check-ignore"] + entries
    task.currentDirectoryURL = URL(fileURLWithPath: dir)
    let pipe = Pipe()
    task.standardOutput = pipe
    task.standardError = FileHandle.nullDevice
    do { try task.run() } catch { return [] }
    task.waitUntilExit()
    let data = pipe.fileHandleForReading.readDataToEndOfFile()
    let output = String(data: data, encoding: .utf8) ?? ""
    var ignored = Set<String>()
    for line in output.split(separator: "\n") {
        let name = String(line).split(separator: "/").last.map(String.init) ?? String(line)
        ignored.insert(name)
    }
    return ignored
}

/// Singleton target for menu item actions (avoids retain issues with NSMenuItem targets).
class FolderMenuActions: NSObject {
    static let shared = FolderMenuActions()

    @objc func openInFinder(_ sender: NSMenuItem) {
        guard let path = sender.representedObject as? String else { return }
        NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: path)
    }

    @objc func openInTerminal(_ sender: NSMenuItem) {
        guard let path = sender.representedObject as? String else { return }
        let config = ["--args", "--working-directory=\(path)"]
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/usr/bin/open")
        task.arguments = ["-na", "Ghostty"] + config
        try? task.run()
    }

    @objc func openFile(_ sender: NSMenuItem) {
        guard let path = sender.representedObject as? String else { return }
        NSWorkspace.shared.open(URL(fileURLWithPath: path))
    }
}

// MARK: - Main

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.run()
