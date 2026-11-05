# A2M Desktop v2 | Local macOS Control UI

The A2M Desktop v2 frontend is a dependency-free HTML/CSS/JavaScript utility interface for the native A2M macOS application. It is served by the local Go Gateway and displayed in AppKit's WKWebView.

## Live integration

The Go Gateway serves:

| Path | Purpose |
| --- | --- |
| `/` | A2M Desktop v2 UI |
| `/styles.css` and `/app.js` | Local assets, no CDN |
| `/api/status` | Tunnel-daemon readiness, device metadata, running gateway `appVersion` and reported services |
| `/api/sessions` | Tracked terminal process sessions |
| `/api/output?pid=<PID>&limit=250` | Recent process output without modifying agent read cursors |
| `/api/stream` | Live server-sent terminal events (multiple subscribers supported) |
| `/api/connect` and `/api/disconnect` | Existing tunnel controls (POST) |
| `/api/kill?pid=<PID>` | Terminate a selected tracked process (POST, confirmed in UI) |
| `/api/diagnostics` | Redacted gateway diagnostics export |

The sidebar always shows the version reported by the **running gateway process** via `appVersion`; the native macOS window title reads the version of the **running app bundle** from `Info.plist`. Both values are supplied by the same build-time `VERSION` file, and an old running gateway is not mistaken for a newly installed bundle. Settings displays the detected capability inventory, including tool/CLI state and MCP approval separately.

The app navigates between Overview, Live Terminal, Activity, Access & Pairing and Settings. The current Activity view contains **only observed process stream events since opening the UI**. It cannot identify individual AI clients or claim a tool-call audit. The Access & Pairing view displays the gateway's reported auth state. Client inventory, pairing changes and filesystem policy editing are unavailable until implemented as backend APIs.

## Running

Build the native desktop bundle from the repository root:

```sh
./scripts/build_desktop.sh
```

The script copies this directory into `A2M.app/Contents/Resources/UI/a2m-desktop-v2`. The gateway discovers assets next to the executable in the app bundle. To run against a development UI directory, set `A2M_UI_DIR` to its absolute path before starting the Go Gateway.

The native app loads `http://127.0.0.1:8765/?embedded=1`, removing the HTML mock titlebar so only macOS's actual window chrome remains.

Open the standalone HTML file only for layout review: `file://` mode does not connect to the Gateway or simulate live data.

## Data and security boundaries

- A2M only reflects status actually returned by the Go Gateway. Tunnel-daemon readiness is **not** proof of remote-client authentication.
- Process logs are written as text nodes, not HTML. Terminal monitoring never sends commands or stdin.
- The Stop button only targets a PID returned in the local session inventory and requires confirmation.
- Local gateway authentication and remote tunnel pairing are separate concerns. The UI deliberately does not invent a pairing workflow or protection claims.
- The current Gateway `/api/status` hardcodes sidecar `running` labels rather than actively probing every sidecar. Treat these labels as *gateway-reported*, not verified health checks.
- If the tunnel was started outside A2M, regular Disconnect returns an explicit conflict instead of claiming success. A2M does not silently kill unrelated tunnel-client processes.
- The browser app uses same-origin endpoints, explicit HTTP method separation, a strict Content Security Policy, and no third-party frontend dependencies.
- Changing the native app bundle does not automatically replace an already-running Gateway process. Restart that service during a safe maintenance window to use the new UI on port 8765.

## Styling

Paper `#F7F7F5`, Forest `#1A3C2B`, muted graphite, 1px hairlines and dark terminal `#151A17`. The UI targets a 1180×760 macOS window with a 1000×680 minimum.
