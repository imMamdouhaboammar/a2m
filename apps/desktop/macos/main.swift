import Cocoa
import WebKit

class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate {
    var window: NSWindow!
    var webView: WKWebView!
    var statusItem: NSStatusItem!
    var sidecarProcess: Process?
    var isQuitting = false
    var gatewayRecoveryAttempts = 0
    var dashboardProbeGeneration = 0
    let gatewayPort: Int = {
        let configured = ProcessInfo.processInfo.environment["A2M_GATEWAY_PORT"] ?? ""
        if let value = Int(configured), (1024...65535).contains(value) { return value }
        return 8765
    }()

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)

        ensureSidecarRunning()

        // 1. Setup Status Item (System Tray)
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        if let button = statusItem.button {
            button.title = "A2M"
        }
        setupStatusMenu()

        // 2. Setup Main Window
        let rect = NSRect(x: 0, y: 0, width: 1180, height: 760)
        window = NSWindow(
            contentRect: rect,
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "A2M Desktop | Agent to Machine"
        window.minSize = NSSize(width: 1000, height: 680)
        window.center()
        window.delegate = self
        window.appearance = NSAppearance(named: .aqua)
        window.backgroundColor = NSColor(red: 247/255.0, green: 247/255.0, blue: 245/255.0, alpha: 1.0)

        // 3. Setup WKWebView
        let config = WKWebViewConfiguration()
        webView = WKWebView(frame: window.contentView!.bounds, configuration: config)
        webView.navigationDelegate = self
        webView.autoresizingMask = [.width, .height]
        window.contentView?.addSubview(webView)

        showStartingPage()
        beginDashboardReadiness()

        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    func setupStatusMenu() {
        let menu = NSMenu()
        let titleItem = NSMenuItem(title: "A2M Desktop v1.0.0 (Agent to Machine)", action: nil, keyEquivalent: "")
        titleItem.isEnabled = false
        menu.addItem(titleItem)
        menu.addItem(NSMenuItem.separator())

        let openItem = NSMenuItem(title: "Show Dashboard", action: #selector(showWindow), keyEquivalent: "o")
        openItem.target = self
        menu.addItem(openItem)

        let terminalItem = NSMenuItem(title: "Open Live Terminal", action: #selector(openTerminal), keyEquivalent: "t")
        terminalItem.target = self
        menu.addItem(terminalItem)

        menu.addItem(NSMenuItem.separator())

        let quitItem = NSMenuItem(title: "Quit A2M Desktop", action: #selector(quitApp), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)

        statusItem.menu = menu
    }

    func ensureSidecarRunning() {
        // Quick HTTP probe on the loopback gateway.
        let url = URL(string: "http://127.0.0.1:\(gatewayPort)/api/status")!
        var request = URLRequest(url: url)
        request.timeoutInterval = 0.5

        let semaphore = DispatchSemaphore(value: 0)
        var isRunning = false
        let task = URLSession.shared.dataTask(with: request) { _, response, _ in
            if let httpResp = response as? HTTPURLResponse, httpResp.statusCode == 200 {
                isRunning = true
            }
            semaphore.signal()
        }
        task.resume()
        _ = semaphore.wait(timeout: .now() + 0.6)

        if isRunning {
            print("[A2M Controller] Gateway already running on port 8765")
            return
        }

        // Spawn gateway sidecar
        let currentDir = FileManager.default.currentDirectoryPath
        var candidates = [
            Bundle.main.bundlePath + "/Contents/MacOS/a2m",
            Bundle.main.bundlePath + "/Contents/MacOS/desktop-commander",
            currentDir + "/bin/a2m",
            currentDir + "/bin/desktop-commander",
            "/usr/local/bin/a2m",
            "/opt/homebrew/bin/a2m"
        ]
        if let resourcePath = Bundle.main.resourcePath {
            candidates.insert(resourcePath + "/a2m", at: 0)
        }

        for path in candidates {
            if FileManager.default.isExecutableFile(atPath: path) {
                let proc = Process()
                proc.executableURL = URL(fileURLWithPath: path)
                proc.arguments = ["server", "--host", "127.0.0.1", "--port", String(gatewayPort)]
                do {
                    proc.terminationHandler = { [weak self] terminated in
                        DispatchQueue.main.async {
                            guard let self = self, self.sidecarProcess === terminated else { return }
                            self.sidecarProcess = nil
                            self.scheduleGatewayRecovery()
                        }
                    }
                    try proc.run()
                    sidecarProcess = proc
                    print("[A2M Controller] Spawned sidecar gateway from \(path)")
                    DispatchQueue.main.asyncAfter(deadline: .now() + 60) { [weak self, weak proc] in
                        guard let self = self, let proc = proc else { return }
                        if self.sidecarProcess === proc && proc.isRunning {
                            self.gatewayRecoveryAttempts = 0
                        }
                    }
                    Thread.sleep(forTimeInterval: 0.5)
                    break
                } catch {
                    print("[A2M Controller] Failed to spawn sidecar: \(error)")
                }
            }
        }
    }

    func scheduleGatewayRecovery() {
        guard !isQuitting, gatewayRecoveryAttempts < 5 else { return }
        gatewayRecoveryAttempts += 1
        let delay = Double(min(30, 1 << gatewayRecoveryAttempts))
        print("[A2M Controller] Gateway exited; recovery attempt \(gatewayRecoveryAttempts) in \(delay)s")
        DispatchQueue.main.asyncAfter(deadline: .now() + delay) { [weak self] in
            guard let self = self, !self.isQuitting else { return }
            self.ensureSidecarRunning()
            self.beginDashboardReadiness()
        }
    }

    func showStartingPage() {
        // A native bundle should never present an unexplained blank WKWebView
        // while its own local Gateway is still indexing or starting.
        let html = """
        <!doctype html><html lang="en"><head><meta charset="utf-8">
        <meta name="viewport" content="width=device-width, initial-scale=1">
        <style>
        * { box-sizing: border-box; }
        body { margin:0; min-height:100vh; display:grid; place-items:center;
               font-family:-apple-system, BlinkMacSystemFont, sans-serif;
               background:#f5f7f5; color:#172f25; }
        .card { max-width:540px; padding:48px; text-align:center; }
        .brand { font-size:38px; font-weight:800; letter-spacing:-2px; }
        h1 { font-size:27px; margin:24px 0 12px; }
        p { color:#63726a; font-size:16px; line-height:1.55; }
        .dot { display:inline-block; width:11px; height:11px; margin-right:8px;
               border-radius:50%; background:#36c96e; animation:pulse 1.5s infinite; }
        @keyframes pulse { 50% { opacity:.3; } }
        </style></head><body><div class="card">
        <div class="brand">A2M</div><h1>Preparing your local gateway</h1>
        <p><span class="dot"></span>Starting services and checking the connection.<br>
        Your dashboard will open automatically when the Gateway is ready.</p>
        </div></body></html>
        """
        webView.loadHTMLString(html, baseURL: nil)
    }

    func beginDashboardReadiness() {
        dashboardProbeGeneration += 1
        probeGateway(generation: dashboardProbeGeneration)
    }

    func probeGateway(generation: Int) {
        guard !isQuitting, generation == dashboardProbeGeneration else { return }
        let url = URL(string: "http://127.0.0.1:\(gatewayPort)/api/status")!
        var request = URLRequest(url: url)
        request.timeoutInterval = 2
        URLSession.shared.dataTask(with: request) { [weak self] _, response, _ in
            DispatchQueue.main.async {
                guard let self = self, !self.isQuitting,
                      generation == self.dashboardProbeGeneration else { return }
                if let http = response as? HTTPURLResponse, http.statusCode == 200 {
                    self.loadDashboard()
                } else {
                    DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { [weak self] in
                        self?.probeGateway(generation: generation)
                    }
                }
            }
        }.resume()
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!,
                 withError error: Error) {
        guard !isQuitting else { return }
        let failure = error as NSError
        if failure.domain == NSURLErrorDomain && failure.code == NSURLErrorCancelled { return }
        print("[A2M Controller] Dashboard navigation failed; waiting for local Gateway")
        showStartingPage()
        beginDashboardReadiness()
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!,
                 withError error: Error) {
        self.webView(webView, didFailProvisionalNavigation: navigation, withError: error)
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        if let url = webView.url, url.host == "127.0.0.1",
           url.port == gatewayPort, url.path == "/" {
            NSLog("[A2M Controller] Dashboard WKWebView navigation complete")
        }
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        guard !isQuitting else { return }
        showStartingPage()
        beginDashboardReadiness()
    }

    func loadDashboard() {
        let url = URL(string: "http://127.0.0.1:\(gatewayPort)/?embedded=1")!
        let request = URLRequest(url: url)
        webView.load(request)
    }

    @objc func showWindow() {
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    @objc func openTerminal() {
        showWindow()
        webView.evaluateJavaScript("a2mShowView('terminal')", completionHandler: nil)
    }

    @objc func quitApp() {
        isQuitting = true
        sidecarProcess?.terminate()
        NSApp.terminate(nil)
    }

    func windowShouldClose(_ sender: NSWindow) -> Bool {
        // Hide window instead of terminating so background connection stays active
        window.orderOut(nil)
        return false
    }

    func applicationWillTerminate(_ notification: Notification) {
        isQuitting = true
        sidecarProcess?.terminate()
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.run()
