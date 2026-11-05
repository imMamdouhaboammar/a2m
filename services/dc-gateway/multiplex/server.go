package multiplex

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/auth"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/mcp"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/process"
)

type sseSession struct {
	id     string
	respCh chan mcp.Response
}

type Server struct {
	registry     *mcp.Registry
	supervisor   *process.Supervisor
	hub          *MultiplexHub
	worker       *WorkerMonitor
	authMgr      *auth.Authenticator
	sessions     map[string]*sseSession
	tunnelCmd    *exec.Cmd
	tunnelState  string
	tunnelError  string
	connectedAt  time.Time
	tunnelCmd2   *exec.Cmd
	tunnelState2 string
	tunnelError2 string
	connectedAt2 time.Time
	mu           sync.RWMutex
}

func NewServer(registry *mcp.Registry, supervisor *process.Supervisor, hub *MultiplexHub, authMgr *auth.Authenticator) *Server {
	s := &Server{
		registry:     registry,
		supervisor:   supervisor,
		hub:          hub,
		authMgr:      authMgr,
		sessions:     make(map[string]*sseSession),
		tunnelState:  "Disconnected",
		tunnelState2: "Disconnected",
	}

	// Check if tunnel-client daemons are already active on ports 8080 and 8081
	go s.probeInitialTunnelState()

	return s
}

func (s *Server) probeInitialTunnelState() {
	client := http.Client{Timeout: 500 * time.Millisecond}
	resp1, err1 := client.Get("http://127.0.0.1:8080/readyz")
	if err1 == nil && resp1.StatusCode == http.StatusOK {
		_ = resp1.Body.Close()
		s.mu.Lock()
		s.tunnelState = "Connected"
		s.connectedAt = time.Now()
		s.mu.Unlock()
	}

	resp2, err2 := client.Get("http://127.0.0.1:8081/readyz")
	if err2 == nil && resp2.StatusCode == http.StatusOK {
		_ = resp2.Body.Close()
		s.mu.Lock()
		s.tunnelState2 = "Connected"
		s.connectedAt2 = time.Now()
		s.mu.Unlock()
	}
}

func isTrustedOrigin(origin string) bool {
	if origin == "" {
		return true // Non-browser clients (direct curl, CLI, MCP agents) omit Origin
	}
	if origin == "null" {
		return false
	}
	lower := strings.ToLower(origin)
	if strings.HasPrefix(lower, "http://localhost:") ||
		lower == "http://localhost" ||
		strings.HasPrefix(lower, "http://127.0.0.1:") ||
		lower == "http://127.0.0.1" ||
		strings.HasPrefix(lower, "http://[::1]:") ||
		lower == "http://[::1]" ||
		strings.HasPrefix(lower, "https://localhost:") ||
		lower == "https://localhost" ||
		strings.HasPrefix(lower, "https://127.0.0.1:") ||
		lower == "https://127.0.0.1" ||
		strings.HasPrefix(lower, "vscode-webview://") ||
		strings.HasPrefix(lower, "tauri://") {
		return true
	}
	return false
}

func isTrustedHost(host string) bool {
	if host == "" {
		return false
	}
	h := host
	if strings.HasPrefix(h, "[") {
		if closeBracket := strings.Index(h, "]"); closeBracket != -1 {
			h = h[1:closeBracket]
		}
	} else if colon := strings.Index(h, ":"); colon != -1 {
		h = h[:colon]
	}
	lower := strings.ToLower(h)
	return lower == "localhost" || lower == "127.0.0.1" || lower == "::1" || lower == "[::1]" || strings.HasSuffix(lower, ".openai.com") || lower == "openai.com"
}

func (s *Server) securityMiddleware(next http.HandlerFunc, allowMethods ...string) http.HandlerFunc {
	methodMap := make(map[string]bool)
	for _, m := range allowMethods {
		methodMap[m] = true
	}
	methodMap[http.MethodOptions] = true

	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Host Validation (DNS rebinding defense)
		if !isTrustedHost(r.Host) {
			http.Error(w, "Forbidden: Untrusted Host header", http.StatusForbidden)
			return
		}

		// 2. Origin & CORS Defense
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !isTrustedOrigin(origin) {
				http.Error(w, "Forbidden: Untrusted Origin", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(allowMethods, ", "))
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Auth-Token, X-Session-ID")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		// 3. Strict HTTP Method
		if !methodMap[r.Method] {
			http.Error(w, fmt.Sprintf("Method %s not allowed", r.Method), http.StatusMethodNotAllowed)
			return
		}

		next(w, r)
	}
}

func (s *Server) requireAuthMiddleware(next http.HandlerFunc, allowMethods ...string) http.HandlerFunc {
	authGate := func(w http.ResponseWriter, r *http.Request) {
		if s.authMgr != nil && s.authMgr.IsEnabled() {
			token := ""
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				token = strings.TrimPrefix(authHeader, "Bearer ")
			} else if tok := r.Header.Get("X-Auth-Token"); tok != "" {
				token = tok
			} else if tok := r.URL.Query().Get("token"); tok != "" {
				token = tok
			}
			if !s.authMgr.ValidateToken(token) {
				http.Error(w, "Unauthorized: valid authentication token required", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
	return s.securityMiddleware(authGate, allowMethods...)
}

func (s *Server) Start(addr string) error {
	// The MCP stdio worker is a separate process with its own supervisor.
	// Observe its existing local control socket without touching the live tunnel.
	if s.worker == nil {
		s.worker = NewWorkerMonitor("")
	}
	s.worker.Start()
	defer s.worker.Close()
	mux := http.NewServeMux()

	mux.HandleFunc("/sse", s.securityMiddleware(s.handleSSE, http.MethodGet))
	mux.HandleFunc("/message", s.securityMiddleware(s.handleMessage, http.MethodPost))
	mux.HandleFunc("/api/stream", s.securityMiddleware(s.handleWebLogStream, http.MethodGet))
	mux.HandleFunc("/api/sessions", s.securityMiddleware(s.handleListSessions, http.MethodGet))
	mux.HandleFunc("/api/output", s.securityMiddleware(s.handleProcessOutput, http.MethodGet))
	mux.HandleFunc("/api/status", s.securityMiddleware(s.handleStatus, http.MethodGet))
	mux.HandleFunc("/api/capabilities", s.securityMiddleware(s.handleCapabilities, http.MethodGet))
	mux.HandleFunc("/api/tunnel-activity", s.securityMiddleware(s.handleTunnelActivity, http.MethodGet))
	mux.HandleFunc("/api/connect", s.requireAuthMiddleware(s.handleConnect, http.MethodPost))
	mux.HandleFunc("/api/disconnect", s.requireAuthMiddleware(s.handleDisconnect, http.MethodPost))
	mux.HandleFunc("/api/credentials/tunnel", s.requireAuthMiddleware(s.handleTunnelCredential, http.MethodGet, http.MethodPost, http.MethodDelete))
	mux.HandleFunc("/api/diagnostics", s.securityMiddleware(s.handleDiagnostics, http.MethodGet))
	mux.HandleFunc("/api/stdin", s.requireAuthMiddleware(s.handleStdin, http.MethodPost))
	mux.HandleFunc("/api/kill", s.requireAuthMiddleware(s.handleKill, http.MethodPost))
	mux.HandleFunc("/api/memory/architecture", s.securityMiddleware(s.handleMemoryArchitecture, http.MethodGet))
	mux.HandleFunc("/api/memory/adrs", s.securityMiddleware(s.handleMemoryADRs, http.MethodGet))
	mux.HandleFunc("/styles.css", s.securityMiddleware(s.handleUIStyles, http.MethodGet))
	mux.HandleFunc("/app.js", s.securityMiddleware(s.handleUIScript, http.MethodGet))
	mux.HandleFunc("/", s.securityMiddleware(s.handleDashboard, http.MethodGet))

	fmt.Printf("\033[1;32m[A2M]\033[0m Multi-party HTTP & SSE server listening on http://%s\n", addr)
	fmt.Printf("  • MCP SSE Endpoint:     http://%s/sse\n", addr)
	fmt.Printf("  • Web Live Dashboard:   http://%s/\n", addr)

	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sessionID := generateID()
	sess := &sseSession{
		id:     sessionID,
		respCh: make(chan mcp.Response, 32),
	}

	s.mu.Lock()
	s.sessions[sessionID] = sess
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.sessions, sessionID)
		s.mu.Unlock()
		if s.authMgr != nil {
			s.authMgr.RemoveSession(sessionID)
		}
	}()

	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if xfh := r.Header.Get("X-Forwarded-Host"); xfh != "" {
		host = xfh
	}

	endpointMsg := fmt.Sprintf("%s://%s/message?sessionId=%s", scheme, host, sessionID)
	// Defeat proxy buffering with comment padding
	padding := strings.Repeat(" ", 2048)
	fmt.Fprintf(w, ": %s\n\n", padding)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointMsg)
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case resp, ok := <-sess.respCh:
			if !ok {
				return
			}
			bytes, err := json.Marshal(resp)
			if err == nil {
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(bytes))
				flusher.Flush()
			}
		}
	}
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")
	s.mu.RLock()
	sess, exists := s.sessions[sessionID]
	s.mu.RUnlock()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Error reading request body", http.StatusBadRequest)
		return
	}

	var req mcp.Request
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON-RPC: %v", err), http.StatusBadRequest)
		return
	}

	var resp mcp.Response
	switch req.Method {
	case "initialize":
		resp = mcp.Response{
			JsonRpc: "2.0",
			ID:      req.ID,
			Result: mcp.InitializeResult{
				ProtocolVersion: "2024-11-05",
				Capabilities: map[string]interface{}{
					"tools": map[string]interface{}{},
				},
				ServerInfo: mcp.ServerInfo{
					Name:    "io.github.a2m/a2m-mcp",
					Version: "1.1.0",
				},
				Instructions: "A2M (Agent to Machine) Multi-Party Server provides terminal execution, fast search, and surgical file editing under user control.",
			},
		}

	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
		return

	case "tools/list":
		resp = mcp.Response{
			JsonRpc: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"tools": s.registry.ListTools(),
			},
		}

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp = mcp.Response{
				JsonRpc: "2.0",
				ID:      req.ID,
				Error: &mcp.ErrorObject{
					Code:    -32602,
					Message: fmt.Sprintf("Invalid params: %v", err),
				},
			}
		} else {
			result := s.registry.DispatchWithSession(sessionID, params.Name, params.Arguments)
			resp = mcp.Response{
				JsonRpc: "2.0",
				ID:      req.ID,
				Result:  result,
			}
		}

	default:
		resp = mcp.Response{
			JsonRpc: "2.0",
			ID:      req.ID,
			Error: &mcp.ErrorObject{
				Code:    -32601,
				Message: fmt.Sprintf("Method not found: %s", req.Method),
			},
		}
	}

	if exists && sess != nil {
		select {
		case sess.respCh <- resp:
			w.WriteHeader(http.StatusAccepted)
			return
		default:
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleWebLogStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	eventCh := make(chan SessionEvent, 128)
	unsubscribe := s.supervisor.AddBroadcaster(func(pid int, cmd string, stream string, text string) {
		select {
		case eventCh <- SessionEvent{
			Source:    "dashboard",
			PID:       pid,
			Command:   cmd,
			Stream:    stream,
			Text:      text,
			Timestamp: time.Now(),
		}:
		default:
		}
	})
	defer unsubscribe()
	if s.worker != nil {
		unsubscribeWorker := s.worker.Subscribe(func(ev SessionEvent) {
			select {
			case eventCh <- ev:
			default:
			}
		})
		defer unsubscribeWorker()
	}

	// An idle stream must remain observable through proxy keepalive comments.
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case ev := <-eventCh:
			bytes, err := json.Marshal(ev)
			if err == nil {
				fmt.Fprintf(w, "event: output\ndata: %s\n\n", string(bytes))
				flusher.Flush()
			}
		}
	}
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions := s.supervisor.ListSessions()
	for i := range sessions {
		sessions[i].Source = "dashboard"
	}
	if s.worker != nil {
		workerSessions, err := s.worker.ListSessions()
		if err == nil {
			sessions = append(sessions, workerSessions...)
			w.Header().Set("X-A2M-Worker-Monitor", "connected")
		} else {
			w.Header().Set("X-A2M-Worker-Monitor", "unavailable")
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(sessions)
}

func isTunnelActive() bool {
	return isTunnelActivePort(8080)
}

func isTunnelActivePort(port int) bool {
	if port <= 0 {
		port = 8080
	}
	client := http.Client{Timeout: 200 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/readyz", port))
	if err == nil {
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	return false
}

// tunnelProfileName uses A2M branding by default while allowing users of
// existing local tunnel-client configurations to select their own profile.
func tunnelProfileName(slot int) string {
	if slot == 2 {
		if override := strings.TrimSpace(os.Getenv("A2M_TUNNEL_PROFILE_2")); override != "" {
			return override
		}
		if home, err := os.UserHomeDir(); err == nil {
			a2mPath := filepath.Join(home, ".config", "tunnel-client", "a2m-2.yaml")
			if _, err := os.Stat(a2mPath); err != nil {
				legacyPath := filepath.Join(home, ".config", "tunnel-client", "desktop-commander-2.yaml")
				if _, legErr := os.Stat(legacyPath); legErr == nil {
					return "desktop-commander-2"
				}
			}
		}
		return "a2m-2"
	}
	if override := strings.TrimSpace(os.Getenv("A2M_TUNNEL_PROFILE_1")); override != "" {
		return override
	}
	if home, err := os.UserHomeDir(); err == nil {
		a2mPath := filepath.Join(home, ".config", "tunnel-client", "a2m.yaml")
		if _, err := os.Stat(a2mPath); err != nil {
			legacyPath := filepath.Join(home, ".config", "tunnel-client", "desktop-commander.yaml")
			if _, legErr := os.Stat(legacyPath); legErr == nil {
				return "desktop-commander"
			}
		}
	}
	return "a2m"
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	// Observe both tunnel 1 (port 8080) and tunnel 2 (port 8081)
	obs1 := observeTunnelClientAt("http://127.0.0.1:8080")
	obs2 := observeTunnelClientAt("http://127.0.0.1:8081")

	s.mu.Lock()
	if obs1.Ready {
		s.tunnelState = "Connected"
		if s.connectedAt.IsZero() {
			s.connectedAt = time.Now()
		}
	} else if s.tunnelCmd == nil {
		s.tunnelState = "Disconnected"
		s.connectedAt = time.Time{}
	}

	if obs2.Ready {
		s.tunnelState2 = "Connected"
		if s.connectedAt2.IsZero() {
			s.connectedAt2 = time.Now()
		}
	} else if s.tunnelCmd2 == nil {
		s.tunnelState2 = "Disconnected"
		s.connectedAt2 = time.Time{}
	}

	state1 := s.tunnelState
	owner1 := "none"
	if s.tunnelCmd != nil {
		owner1 = "gateway"
	} else if obs1.Ready {
		owner1 = "external"
	}

	state2 := s.tunnelState2
	owner2 := "none"
	if s.tunnelCmd2 != nil {
		owner2 = "gateway"
	} else if obs2.Ready {
		owner2 = "external"
	}
	s.mu.Unlock()

	overallState := "Disconnected"
	if state1 == "Connected" || state2 == "Connected" {
		overallState = "Connected"
	} else if state1 == "Connecting" || state2 == "Connecting" {
		overallState = "Connecting"
	}

	primaryObs := obs1
	primaryOwner := owner1
	if !obs1.Ready && obs2.Ready {
		primaryObs = obs2
		primaryOwner = owner2
	}

	hostname, _ := os.Hostname()
	status := map[string]interface{}{
		"connectionState":   overallState,
		"tunnelOwner":       primaryOwner,
		"tunnelId":          primaryObs.TunnelID,
		"connectedDuration": primaryObs.Uptime,
		"tunnel":            primaryObs,
		"tunnels": map[string]interface{}{
			"1": map[string]interface{}{
				"id":            1,
				"name":          "Tunnel 1",
				"profile":       tunnelProfileName(1),
				"port":          8080,
				"state":         state1,
				"ready":         obs1.Ready,
				"owner":         owner1,
				"tunnelId":      obs1.TunnelID,
				"uptimeSeconds": obs1.Uptime,
				"mcpToolCalls":  obs1.MCPToolCalls,
			},
			"2": map[string]interface{}{
				"id":            2,
				"name":          "Tunnel 2",
				"profile":       tunnelProfileName(2),
				"port":          8081,
				"state":         state2,
				"ready":         obs2.Ready,
				"owner":         owner2,
				"tunnelId":      obs2.TunnelID,
				"uptimeSeconds": obs2.Uptime,
				"mcpToolCalls":  obs2.MCPToolCalls,
			},
		},
		"monitoringScope":        "dashboard and observed MCP worker processes; output retained since observer attachment",
		"workerMonitorConnected": s.worker != nil && s.worker.Connected(),
		"device": map[string]interface{}{
			"hostname":   hostname,
			"platform":   observedPlatform(),
			"status":     "gateway reachable",
			"toolsCount": len(s.registry.ListTools()),
		},
		"sidecars": observableSidecars(),
		"auth": map[string]interface{}{
			"enabled": s.authMgr.IsEnabled(),
			"role":    string(s.authMgr.GetRole()),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	tunnelBin, err := FindTunnelClientBinary()
	if err != nil {
		log.Printf("[Tunnel Manager] tunnel-client binary not found: %v", err)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ERROR",
			"message": "tunnel-client binary not found. Install it via Homebrew: brew install openai/tap/tunnel-client",
		})
		return
	}

	tunnelTarget := r.URL.Query().Get("tunnel")
	if tunnelTarget == "2" {
		s.connectTunnelSlot(w, r, tunnelBin, 2, tunnelProfileName(2), 8081)
		return
	}
	s.connectTunnelSlot(w, r, tunnelBin, 1, tunnelProfileName(1), 8080)
}

func (s *Server) connectTunnelSlot(w http.ResponseWriter, r *http.Request, tunnelBin string, tunnelNum int, profile string, port int) {
	mode := r.URL.Query().Get("mode")
	apiKey, credSource, _ := GetControlPlaneAPIKeyForTunnel(tunnelNum)

	if apiKey == "" && mode != "local" {
		log.Printf("[Tunnel Manager %d] CONFIGURATION_REQUIRED: API key not set (source: %s)", tunnelNum, credSource)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":               "CONFIGURATION_REQUIRED",
			"message":              fmt.Sprintf("API key for Tunnel %d is not configured.", tunnelNum),
			"helpUrl":              "https://platform.openai.com/settings/organization/api-keys",
			"localBridgeAvailable": tunnelNum == 1,
		})
		return
	}

	s.mu.Lock()
	if isTunnelActivePort(port) {
		if tunnelNum == 2 {
			s.tunnelState2 = "Connected"
			if s.connectedAt2.IsZero() {
				s.connectedAt2 = time.Now()
			}
		} else {
			s.tunnelState = "Connected"
			if s.connectedAt.IsZero() {
				s.connectedAt = time.Now()
			}
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "already_connected", "tunnel": strconv.Itoa(tunnelNum)})
		return
	}

	if tunnelNum == 2 {
		s.tunnelState2 = "Connecting"
	} else {
		s.tunnelState = "Connecting"
	}

	var cmd *exec.Cmd
	if (mode == "local" || apiKey == "") && tunnelNum == 1 {
		mcpBin, err := FindA2MBinary()
		if err != nil {
			s.tunnelState = "Disconnected"
			s.mu.Unlock()
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status":  "ERROR",
				"message": "A2M executable not found. Install the CLI or set A2M_MCP_BINARY to its full path.",
			})
			return
		}
		log.Printf("[Tunnel Manager 1] Spawning zero-config local in-memory tunnel proxy with mcp: %s", mcpBin)
		cmd = exec.Command(tunnelBin, "dev", "proxy", "--health-listen-addr", fmt.Sprintf("127.0.0.1:%d", port), "--mcp-command", mcpBin)
	} else {
		log.Printf("[Tunnel Manager %d] Spawning tunnel-client with profile %s", tunnelNum, profile)
		cmd = exec.Command(tunnelBin, "run", "--profile", profile)
		cmd.Env = append(os.Environ(), "CONTROL_PLANE_API_KEY="+apiKey, fmt.Sprintf("CONTROL_PLANE_API_KEY_%d=%s", tunnelNum, apiKey))
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[Tunnel Manager %d] could not spawn tunnel-client: %v", tunnelNum, err)
		if tunnelNum == 2 {
			s.tunnelState2 = "Disconnected"
		} else {
			s.tunnelState = "Disconnected"
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ERROR",
			"message": fmt.Sprintf("Failed to start tunnel %d: %v", tunnelNum, err),
		})
		return
	}

	if tunnelNum == 2 {
		s.tunnelCmd2 = cmd
		go func() {
			_ = cmd.Wait()
			s.mu.Lock()
			s.tunnelState2 = "Disconnected"
			s.tunnelCmd2 = nil
			s.connectedAt2 = time.Time{}
			s.mu.Unlock()
		}()
	} else {
		s.tunnelCmd = cmd
		go func() {
			_ = cmd.Wait()
			s.mu.Lock()
			s.tunnelState = "Disconnected"
			s.tunnelCmd = nil
			s.connectedAt = time.Time{}
			s.mu.Unlock()
		}()
	}
	s.mu.Unlock()

	// Wait up to 2.5s for readyz
	for i := 0; i < 25; i++ {
		time.Sleep(100 * time.Millisecond)
		if isTunnelActivePort(port) {
			s.mu.Lock()
			if tunnelNum == 2 {
				s.tunnelState2 = "Connected"
				s.connectedAt2 = time.Now()
			} else {
				s.tunnelState = "Connected"
				s.connectedAt = time.Now()
			}
			s.mu.Unlock()
			break
		}
	}

	obs := observeTunnelClientAt(fmt.Sprintf("http://127.0.0.1:%d", port))
	s.mu.RLock()
	var finalState string
	if tunnelNum == 2 {
		finalState = s.tunnelState2
	} else {
		finalState = s.tunnelState
	}
	s.mu.RUnlock()

	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":   finalState,
		"tunnelId": obs.TunnelID,
		"tunnel":   strconv.Itoa(tunnelNum),
	})
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	tunnelTarget := r.URL.Query().Get("tunnel")
	killAll := r.URL.Query().Get("killAll") == "true"

	if tunnelTarget == "2" {
		s.disconnectTunnelSlot(w, r, 2, 8081, killAll)
		return
	}
	if tunnelTarget == "1" {
		s.disconnectTunnelSlot(w, r, 1, 8080, killAll)
		return
	}
	if tunnelTarget == "all" {
		s.disconnectTunnelSlot(w, r, 1, 8080, killAll)
		s.disconnectTunnelSlot(w, r, 2, 8081, killAll)
		return
	}

	s.disconnectTunnelSlot(w, r, 1, 8080, killAll)
}

func (s *Server) disconnectTunnelSlot(w http.ResponseWriter, r *http.Request, tunnelNum int, port int, killAll bool) {
	s.mu.RLock()
	var owned *exec.Cmd
	if tunnelNum == 2 {
		owned = s.tunnelCmd2
	} else {
		owned = s.tunnelCmd
	}
	s.mu.RUnlock()

	active := isTunnelActivePort(port)
	external := owned == nil && active

	// The user must explicitly confirm ending a tunnel started elsewhere.
	if external && !killAll {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "externally_managed",
			"tunnel":  strconv.Itoa(tunnelNum),
			"message": fmt.Sprintf("An external tunnel-client owns Tunnel %d on port %d. Confirm external disconnect to stop that verified daemon.", tunnelNum, port),
		})
		return
	}

	var err error
	if external {
		err = stopExternalTunnelDaemonOnPort(port)
	} else if owned != nil && owned.Process != nil {
		err = owned.Process.Signal(os.Interrupt)
	}
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "disconnect_failed", "message": err.Error(), "tunnel": strconv.Itoa(tunnelNum),
		})
		return
	}
	for i := 0; i < 20 && isTunnelActivePort(port); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if isTunnelActivePort(port) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "still_connected", "message": fmt.Sprintf("Tunnel %d is still ready; disconnect was not confirmed.", tunnelNum), "tunnel": strconv.Itoa(tunnelNum),
		})
		return
	}

	s.mu.Lock()
	if tunnelNum == 2 {
		s.tunnelState2 = "Disconnected"
		s.connectedAt2 = time.Time{}
		s.tunnelCmd2 = nil
	} else {
		s.tunnelState = "Disconnected"
		s.connectedAt = time.Time{}
		s.tunnelCmd = nil
	}
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "disconnected", "tunnel": strconv.Itoa(tunnelNum)})
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	sessions := s.supervisor.ListSessions()

	diag := map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"device": map[string]string{
			"hostname": hostname,
			"platform": observedPlatform(),
			"release":  "A2M v1.1.0 Native (Agent to Machine)",
		},
		"sidecars":        observableSidecars(),
		"tunnel":          observeTunnelClient(),
		"monitoringScope": "dashboard gateway sessions only",
		"sessionsCount":   len(sessions),
		"security": map[string]interface{}{
			"authEnabled":  s.authMgr.IsEnabled(),
			"role":         string(s.authMgr.GetRole()),
			"tokensMasked": true,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=a2m-diagnostics.json")
	_ = json.NewEncoder(w).Encode(diag)
}

func (s *Server) handleStdin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PID  int    `json:"pid"`
		Data string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := s.supervisor.InteractWithProcess(req.PID, req.Data, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.URL.Query().Get("pid"))
	if err != nil || pid <= 0 {
		http.Error(w, "valid pid required", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("source") == "mcp-worker" {
		if s.worker == nil {
			http.Error(w, "MCP worker monitor unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := s.worker.Kill(pid); err != nil {
			http.Error(w, "active MCP worker process not found or worker unavailable", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "stop_requested", "pid": pid, "source": "mcp-worker"})
		return
	}
	if err := s.supervisor.TerminateProcess(pid, true); err != nil {
		http.Error(w, "tracked running process not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "terminated", "pid": pid})
}

func (s *Server) handleMemoryArchitecture(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	res := s.registry.Dispatch("get_architecture", json.RawMessage(`{"path":"."}`))
	if len(res.Content) > 0 {
		_, _ = w.Write([]byte(res.Content[0].Text))
	} else {
		_, _ = w.Write([]byte(`{}`))
	}
}

func (s *Server) handleMemoryADRs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	res := s.registry.Dispatch("manage_adr", json.RawMessage(`{"action":"list"}`))
	if len(res.Content) > 0 {
		_, _ = w.Write([]byte(res.Content[0].Text))
	} else {
		_, _ = w.Write([]byte(`{"adrs":[]}`))
	}
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.serveUIAsset(w, "index.html", "text/html; charset=utf-8")
}

func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// handleTunnelCredential manages the OpenAI Runtime API key stored in macOS Keychain.
//
//	GET    /api/credentials/tunnel  → {"configured":bool,"source":"keychain|environment|none"}
//	POST   /api/credentials/tunnel  → body: {"api_key":"sk-..."} → {"status":"saved"}
//	DELETE /api/credentials/tunnel  → {"status":"deleted"}
//
// The actual key value is NEVER returned in responses.
func (s *Server) handleTunnelCredential(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		_, source, _ := GetControlPlaneAPIKey()
		configured := source != "none"
		_ = json.NewEncoder(w).Encode(CredentialStatus{Configured: configured, Source: source})

	case http.MethodPost:
		var body struct {
			APIKey string `json:"api_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.APIKey) == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "api_key field is required and must be non-empty"})
			return
		}
		if err := SaveControlPlaneAPIKey(body.APIKey); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		log.Printf("[Credentials] API key saved to Keychain successfully")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "saved"})

	case http.MethodDelete:
		if err := DeleteControlPlaneAPIKey(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		log.Printf("[Credentials] API key removed from Keychain")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>A2M Desktop — Agent to Machine (A2M Native Multi-Party Control)</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    :root {
      --bg-dark: #0d1117;
      --bg-card: #161b22;
      --bg-header: #12161d;
      --border: #30363d;
      --accent: #58a6ff;
      --text: #e6edf3;
      --text-muted: #8b949e;
      --green: #238636;
      --green-bright: #3fb950;
      --red: #da3633;
      --red-bright: #f85149;
      --yellow: #d29922;
    }
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, monospace; background: var(--bg-dark); color: var(--text); display: flex; flex-direction: column; height: 100vh; overflow: hidden; }
    header { background: var(--bg-header); border-bottom: 1px solid var(--border); padding: 10px 20px; display: flex; align-items: center; justify-content: space-between; }
    .brand { display: flex; align-items: center; gap: 10px; font-weight: 700; font-size: 15px; color: var(--text); }
    .brand-icon { width: 22px; height: 22px; background: #58a6ff; border-radius: 4px; display: flex; align-items: center; justify-content: center; font-size: 12px; font-weight: 900; color: #0d1117; }
    .nav-tabs { display: flex; gap: 4px; background: #090d13; padding: 3px; border-radius: 8px; border: 1px solid var(--border); }
    .tab-btn { background: transparent; border: none; color: var(--text-muted); padding: 6px 14px; border-radius: 6px; font-size: 12.5px; font-weight: 600; cursor: pointer; transition: 0.15s; }
    .tab-btn.active { background: var(--bg-card); color: var(--accent); }
    .header-actions { display: flex; align-items: center; gap: 12px; }
    .status-badge { display: flex; align-items: center; gap: 6px; font-size: 12px; font-weight: 600; padding: 4px 10px; border-radius: 12px; background: rgba(56, 139, 253, 0.15); border: 1px solid rgba(56, 139, 253, 0.4); }
    .status-badge.connected { background: rgba(63, 185, 80, 0.15); border-color: rgba(63, 185, 80, 0.4); color: var(--green-bright); }
    .status-badge.disconnected { background: rgba(248, 81, 73, 0.15); border-color: rgba(248, 81, 73, 0.4); color: var(--red-bright); }
    .status-dot { width: 8px; height: 8px; border-radius: 50%; background: currentColor; }
    .btn { padding: 6px 14px; border-radius: 6px; font-weight: 600; font-size: 12.5px; cursor: pointer; border: 1px solid transparent; transition: 0.15s; }
    .btn-connect { background: var(--green); color: #fff; }
    .btn-connect:hover { background: #2ea043; }
    .btn-disconnect { background: var(--red); color: #fff; }
    .btn-disconnect:hover { background: #b62324; }
    .btn-secondary { background: var(--bg-card); border-color: var(--border); color: var(--text); }
    .btn-secondary:hover { border-color: var(--accent); }
    .main-container { flex: 1; display: flex; overflow: hidden; position: relative; }
    .view-panel { display: none; width: 100%; height: 100%; flex-direction: column; }
    .view-panel.active { display: flex; }
    .dashboard-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 16px; padding: 20px; overflow-y: auto; }
    .card { background: var(--bg-card); border: 1px solid var(--border); border-radius: 8px; padding: 16px; display: flex; flex-direction: column; gap: 12px; }
    .card-title { font-size: 13.5px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.5px; }
    .card-row { display: flex; justify-content: space-between; align-items: center; font-size: 13px; border-bottom: 1px solid rgba(255,255,255,0.04); padding-bottom: 8px; }
    .card-row:last-child { border-bottom: none; padding-bottom: 0; }
    .pill { font-size: 11px; padding: 2px 8px; border-radius: 10px; font-weight: 600; background: #21262d; color: var(--accent); }
    .terminal-layout { flex: 1; display: flex; overflow: hidden; }
    .sessions-sidebar { width: 320px; background: #12161d; border-right: 1px solid var(--border); display: flex; flex-direction: column; }
    .sessions-header { padding: 12px 16px; font-size: 12px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; border-bottom: 1px solid var(--border); display: flex; justify-content: space-between; }
    .sessions-list { flex: 1; overflow-y: auto; padding: 8px; }
    .session-item { background: var(--bg-card); border: 1px solid var(--border); border-radius: 6px; padding: 10px; margin-bottom: 8px; cursor: pointer; transition: 0.15s; }
    .session-item:hover, .session-item.selected { border-color: var(--accent); background: #1c2128; }
    .session-item-cmd { font-family: monospace; font-size: 12px; color: #79c0ff; word-break: break-all; }
    .session-item-meta { display: flex; justify-content: space-between; font-size: 11px; color: var(--text-muted); margin-top: 6px; }
    .terminal-main { flex: 1; display: flex; flex-direction: column; background: #090d13; }
    .terminal-toolbar { padding: 8px 16px; background: #12161d; border-bottom: 1px solid var(--border); display: flex; align-items: center; justify-content: space-between; font-size: 12px; }
    .terminal-output { flex: 1; padding: 16px; font-family: "JetBrains Mono", Menlo, Consolas, monospace; font-size: 12px; line-height: 1.5; overflow-y: auto; color: #c9d1d9; }
    .terminal-line { margin-bottom: 2px; word-break: break-all; }
    .terminal-line.stdout { color: #79c0ff; }
    .terminal-line.stderr { color: #f85149; }
    .terminal-line-meta { color: #6e7681; margin-right: 8px; }
    .terminal-input-bar { padding: 10px 16px; background: #12161d; border-top: 1px solid var(--border); display: flex; gap: 8px; align-items: center; }
    input[type="text"] { flex: 1; background: #090d13; border: 1px solid var(--border); color: var(--text); padding: 7px 12px; border-radius: 6px; font-size: 12.5px; font-family: inherit; }
    input[type="text"]:focus { outline: none; border-color: var(--accent); }
  </style>
</head>
<body>
  <header>
    <div class="brand">
      <div class="brand-icon">⚡</div>
      <span>A2M Desktop</span>
      <span style="font-size: 11px; color: var(--text-muted); font-weight: normal; margin-left: 4px;">Agent to Machine</span>
    </div>

    <div class="nav-tabs">
      <button class="tab-btn active" onclick="switchTab('dashboard')">Dashboard</button>
      <button class="tab-btn" onclick="switchTab('terminal')">Live Terminal</button>
      <button class="tab-btn" onclick="switchTab('graph')">Knowledge Graph & Radar</button>
      <button class="tab-btn" onclick="switchTab('settings')">Settings</button>
    </div>

    <div class="header-actions">
      <div id="statusBadge" class="status-badge disconnected">
        <div class="status-dot"></div>
        <span id="statusText">Disconnected</span>
      </div>
      <button id="toggleConnectBtn" class="btn btn-connect" onclick="toggleConnection()">Connect</button>
    </div>
  </header>

  <div class="main-container">
    <!-- Tab 1: Dashboard -->
    <div id="tab-dashboard" class="view-panel active">
      <div class="dashboard-grid">
        <div class="card">
          <div class="card-title">Device & Core Health</div>
          <div class="card-row"><span>Host Device</span><span id="devHost" style="font-family: monospace;">Local Mac</span></div>
          <div class="card-row"><span>Platform</span><span class="pill">macOS Darwin arm64</span></div>
          <div class="card-row"><span>A2M Gateway (MCP Supervisor)</span><span class="pill" style="color: var(--green-bright);">Running</span></div>
          <div class="card-row"><span>A2M File Engine (Rust Search & FS)</span><span class="pill" style="color: var(--green-bright);">Running (UDS/TCP)</span></div>
          <div class="card-row"><span>A2M Document Service (Excel/Docx)</span><span class="pill" style="color: var(--green-bright);">Running</span></div>
          <div class="card-row"><span>Available Tools</span><span class="pill" id="toolsCount">38 Registered (Core + Graph)</span></div>
        </div>

        <div class="card">
          <div class="card-title">Secure Remote MCP Tunnel</div>
          <div class="card-row"><span>Connector Name</span><span style="font-weight: 600; color: #58a6ff;">Agent to Machine (A2M)</span></div>
          <div class="card-row"><span>Tunnel ID</span><span id="tunnelIdText" style="font-family: monospace; font-size: 11px;">tunnel_6ab2aa...</span></div>
          <div class="card-row"><span>OpenAI Control Plane</span><span class="pill" style="color: var(--green-bright);">Active</span></div>
          <div class="card-row"><span>Usage Quota</span><span style="color: var(--green-bright); font-weight: 600;">Unlimited (Zero Limits)</span></div>
          <div class="card-row"><span>Telemetry</span><span class="pill" style="color: #8b949e;">0% (100% Offline-First)</span></div>
        </div>

        <div class="card">
          <div class="card-title">Live Session Statistics</div>
          <div class="card-row"><span>Active Processes</span><span id="activeProcCount" style="font-weight: 700; color: #58a6ff;">0</span></div>
          <div class="card-row"><span>Connection Uptime</span><span id="uptimeDuration">--</span></div>
          <div class="card-row"><span>Memory Footprint</span><span style="font-family: monospace;">~14.2 MB</span></div>
          <div class="card-row"><span>Local SSE Endpoint</span><span style="font-family: monospace; font-size: 11px;">http://127.0.0.1:8765/sse</span></div>
        </div>
      </div>
    </div>

    <!-- Tab 2: Live Terminal Monitor -->
    <div id="tab-terminal" class="view-panel">
      <div class="terminal-layout">
        <div class="sessions-sidebar">
          <div class="sessions-header">
            <span>Process Sessions</span>
            <button class="btn btn-secondary" style="padding: 2px 6px; font-size: 10px;" onclick="refreshSessions()">Refresh</button>
          </div>
          <div id="sessionList" class="sessions-list">
            <div style="padding: 12px; color: var(--text-muted); font-size: 12px;">No active processes.</div>
          </div>
        </div>

        <div class="terminal-main">
          <div class="terminal-toolbar">
            <div style="display: flex; gap: 12px; align-items: center;">
              <span id="terminalTitle" style="font-weight: 600; color: #79c0ff;">Watching All Processes</span>
              <label style="display: flex; align-items: center; gap: 4px; color: var(--text-muted); cursor: pointer;">
                <input type="checkbox" id="autoScrollCheck" checked> Auto-scroll
              </label>
            </div>
            <div style="display: flex; gap: 8px;">
              <button class="btn btn-secondary" style="padding: 4px 10px; font-size: 11px;" onclick="clearTerminal()">Clear</button>
              <button class="btn btn-secondary" style="padding: 4px 10px; font-size: 11px;" onclick="copyTerminalOutput()">Copy Output</button>
            </div>
          </div>

          <div id="terminalStream" class="terminal-output"></div>

          <div class="terminal-input-bar">
            <input type="text" id="stdinInput" placeholder="Send input to active process stdin (e.g. y/n)..." onkeydown="if(event.key==='Enter') sendStdin()">
            <button class="btn btn-secondary" onclick="sendStdin()">Send</button>
            <button class="btn btn-disconnect" onclick="killSelectedProcess()">Kill Process</button>
          </div>
        </div>
      </div>
    </div>

    <!-- Tab 3: Knowledge Graph & Radar -->
    <div id="tab-graph" class="view-panel">
      <div class="dashboard-grid">
        <div class="card" style="grid-column: 1 / -1;">
          <div style="display: flex; justify-content: space-between; align-items: center;">
            <div class="card-title">Codebase Knowledge Graph & AST Memory</div>
            <button class="btn btn-secondary" onclick="triggerIndex()">Index Workspace Now</button>
          </div>
          <div style="display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; margin-top: 8px;">
            <div style="background: #0d1117; padding: 12px; border-radius: 6px; border: 1px solid var(--border);">
              <div style="font-size: 11px; color: var(--text-muted);">Total Files</div>
              <div id="kgFiles" style="font-size: 20px; font-weight: 700; color: #58a6ff; margin-top: 4px;">--</div>
            </div>
            <div style="background: #0d1117; padding: 12px; border-radius: 6px; border: 1px solid var(--border);">
              <div style="font-size: 11px; color: var(--text-muted);">Lines of Code</div>
              <div id="kgLines" style="font-size: 20px; font-weight: 700; color: #3fb950; margin-top: 4px;">--</div>
            </div>
            <div style="background: #0d1117; padding: 12px; border-radius: 6px; border: 1px solid var(--border);">
              <div style="font-size: 11px; color: var(--text-muted);">Symbols Indexed</div>
              <div id="kgSymbols" style="font-size: 20px; font-weight: 700; color: #d29922; margin-top: 4px;">--</div>
            </div>
            <div style="background: #0d1117; padding: 12px; border-radius: 6px; border: 1px solid var(--border);">
              <div style="font-size: 11px; color: var(--text-muted);">Relationships (Edges)</div>
              <div id="kgEdges" style="font-size: 20px; font-weight: 700; color: #bc8cff; margin-top: 4px;">--</div>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card-title">Language Distribution & Entrypoints</div>
          <div id="kgLanguages" style="display: flex; flex-direction: column; gap: 8px;"></div>
          <div style="margin-top: 12px; font-size: 12px; font-weight: 600; color: var(--text-muted);">Detected Entrypoints:</div>
          <div id="kgEntrypoints" style="font-family: monospace; font-size: 11px; color: #79c0ff; margin-top: 4px;"></div>
        </div>

        <div class="card">
          <div class="card-title">Agent Blast-Radius Live Radar</div>
          <div style="font-size: 12px; color: var(--text-muted);">Evaluate callers and downstream regression risk for any file or symbol:</div>
          <div style="display: flex; gap: 8px; margin-top: 8px;">
            <input type="text" id="radarFileInput" placeholder="File path (e.g. main.go)" style="flex: 2;">
            <input type="text" id="radarSymbolInput" placeholder="Symbol (optional)" style="flex: 1;">
            <button class="btn btn-secondary" onclick="checkImpact()">Check Risk</button>
          </div>
          <div id="radarResult" style="margin-top: 8px; padding: 10px; background: #0d1117; border-radius: 6px; border: 1px solid var(--border); font-size: 12px; display: none;"></div>
        </div>

        <div class="card" style="grid-column: 1 / -1;">
          <div class="card-title">Architecture Decision Records (ADRs)</div>
          <div id="adrList" style="display: flex; flex-direction: column; gap: 8px; max-height: 200px; overflow-y: auto;">
            <div style="color: var(--text-muted); font-size: 12px;">No ADRs recorded.</div>
          </div>
        </div>
      </div>
    </div>

    <!-- Tab 4: Pairing & Settings -->
    <div id="tab-settings" class="view-panel">
      <div class="dashboard-grid">
        <div class="card">
          <div class="card-title">Device Authorization & Security Role</div>
          <div class="card-row"><span>Active Security Role</span><span class="pill" id="settingsRole">Admin (Full Access)</span></div>
          <div class="card-row"><span>Enforce File Boundaries</span><span class="pill">Active</span></div>
          <div class="card-row"><span>Process Tree Cleanup</span><span class="pill">POSIX PGID Enabled</span></div>
          <div style="margin-top: 10px;">
            <button class="btn btn-secondary" style="width: 100%;" onclick="downloadDiagnostics()">Export Redacted Diagnostics JSON</button>
          </div>
        </div>
      </div>
    </div>
  </div>

  <script>
    let activeTab = 'dashboard';
    let selectedPID = 0;
    let sseSource = null;

    function switchTab(tab) {
      activeTab = tab;
      document.querySelectorAll('.tab-btn').forEach(btn => btn.classList.remove('active'));
      document.querySelectorAll('.view-panel').forEach(panel => panel.classList.remove('active'));
      event.target.classList.add('active');
      document.getElementById('tab-' + tab).classList.add('active');
      if (tab === 'graph') {
        loadGraphData();
      }
    }

    async function loadGraphData() {
      try {
        const archRes = await fetch('/api/memory/architecture');
        const arch = await archRes.json();
        if (arch.total_files !== undefined) {
          document.getElementById('kgFiles').textContent = arch.total_files.toLocaleString();
          document.getElementById('kgLines').textContent = arch.total_lines.toLocaleString();
          document.getElementById('kgSymbols').textContent = arch.total_symbols.toLocaleString();
          document.getElementById('kgEdges').textContent = arch.total_relationships.toLocaleString();

          const langDiv = document.getElementById('kgLanguages');
          langDiv.innerHTML = '';
          if (arch.languages) {
            for (const [lang, count] of Object.entries(arch.languages)) {
              const row = document.createElement('div');
              row.className = 'card-row';
              row.innerHTML = '<span>' + lang.toUpperCase() + '</span><span class="pill">' + count + ' files</span>';
              langDiv.appendChild(row);
            }
          }

          const epDiv = document.getElementById('kgEntrypoints');
          epDiv.innerHTML = arch.entrypoints && arch.entrypoints.length ? arch.entrypoints.join('<br>') : 'None detected';
        }

        const adrRes = await fetch('/api/memory/adrs');
        const adrData = await adrRes.json();
        const adrDiv = document.getElementById('adrList');
        adrDiv.innerHTML = '';
        if (adrData.adrs && adrData.adrs.length > 0) {
          adrData.adrs.forEach(adr => {
            const item = document.createElement('div');
            item.style.padding = '8px';
            item.style.background = '#0d1117';
            item.style.borderRadius = '4px';
            item.style.border = '1px solid var(--border)';
            item.innerHTML = '<div style="display:flex; justify-content:space-between; font-weight:600;">' +
              '<span>' + adr.id + ': ' + adr.title + '</span><span class="pill" style="color:var(--green-bright);">' + adr.status + '</span></div>' +
              '<div style="font-size:11.5px; color:var(--text-muted); margin-top:4px;">' + (adr.decision || adr.context) + '</div>';
            adrDiv.appendChild(item);
          });
        } else {
          adrDiv.innerHTML = '<div style="color: var(--text-muted); font-size: 12px;">No ADRs recorded.</div>';
        }
      } catch (e) {
        console.error('Failed to load knowledge graph data:', e);
      }
    }

    async function triggerIndex() {
      try {
        const res = await fetch('/message', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            jsonrpc: '2.0',
            id: Date.now(),
            method: 'tools/call',
            params: { name: 'index_codebase', arguments: { path: '.' } }
          })
        });
        await res.json();
        loadGraphData();
      } catch (e) {
        console.error('Failed to trigger indexing:', e);
      }
    }

    async function checkImpact() {
      const file = document.getElementById('radarFileInput').value.trim();
      const sym = document.getElementById('radarSymbolInput').value.trim();
      if (!file) return;

      const resDiv = document.getElementById('radarResult');
      resDiv.style.display = 'block';
      resDiv.innerHTML = '<span style="color:var(--text-muted);">Evaluating blast radius...</span>';

      try {
        const args = { file_path: file };
        if (sym) args.symbol_name = sym;
        const res = await fetch('/message', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            jsonrpc: '2.0',
            id: Date.now(),
            method: 'tools/call',
            params: { name: 'assess_impact', arguments: args }
          })
        });
        const data = await res.json();
        if (data.result && data.result.content && data.result.content[0]) {
          const impact = JSON.parse(data.result.content[0].text);
          let color = '#3fb950';
          if (impact.risk_level === 'MEDIUM') color = '#d29922';
          if (impact.risk_level === 'HIGH' || impact.risk_level === 'CRITICAL') color = '#f85149';

          resDiv.innerHTML = '<div style="display:flex; justify-content:space-between; align-items:center;">' +
            '<strong>Blast Radius Risk:</strong> <span class="pill" style="color:' + color + ';">' + impact.risk_level + '</span></div>' +
            '<div style="margin-top:4px;">Affected Callers: ' + impact.total_affected_callers + ' across ' + (impact.affected_files ? impact.affected_files.length : 0) + ' files.</div>';
        }
      } catch (e) {
        resDiv.innerHTML = '<span style="color:var(--red-bright);">Failed to assess impact: ' + e + '</span>';
      }
    }

    async function updateStatus() {
      try {
        const res = await fetch('/api/status');
        const data = await res.json();
        const badge = document.getElementById('statusBadge');
        const text = document.getElementById('statusText');
        const btn = document.getElementById('toggleConnectBtn');

        if (data.connectionState === 'Connected') {
          badge.className = 'status-badge connected';
          text.textContent = 'Connected (Live)';
          btn.textContent = 'Disconnect';
          btn.className = 'btn btn-disconnect';
        } else if (data.connectionState === 'Connecting') {
          badge.className = 'status-badge';
          text.textContent = 'Connecting...';
          btn.textContent = 'Connecting...';
        } else {
          badge.className = 'status-badge disconnected';
          text.textContent = 'Disconnected';
          btn.textContent = 'Connect';
          btn.className = 'btn btn-connect';
        }

        if (data.device && data.device.hostname) {
          document.getElementById('devHost').textContent = data.device.hostname;
        }
        if (data.connectedDuration) {
          const mins = Math.floor(data.connectedDuration / 60);
          const secs = data.connectedDuration % 60;
          document.getElementById('uptimeDuration').textContent = mins + 'm ' + secs + 's';
        }
      } catch (e) {
        console.error('Status fetch error:', e);
      }
    }

    async function toggleConnection() {
      const btn = document.getElementById('toggleConnectBtn');
      if (btn.textContent === 'Connect') {
        btn.textContent = 'Connecting...';
        await fetch('/api/connect', { method: 'POST' });
      } else {
        btn.textContent = 'Disconnecting...';
        await fetch('/api/disconnect', { method: 'POST' });
      }
      setTimeout(updateStatus, 800);
    }

    async function refreshSessions() {
      try {
        const res = await fetch('/api/sessions');
        const sessions = await res.json();
        const list = document.getElementById('sessionList');
        document.getElementById('activeProcCount').textContent = sessions.length;

        if (sessions.length === 0) {
          list.innerHTML = '<div style="padding: 12px; color: var(--text-muted); font-size: 12px;">No active processes.</div>';
          return;
        }

        list.innerHTML = '';
        sessions.forEach(sess => {
          const item = document.createElement('div');
          item.className = 'session-item' + (sess.pid === selectedPID ? ' selected' : '');
          item.onclick = () => selectSession(sess.pid, sess.command);
          item.innerHTML = '<div class="session-item-cmd">PID ' + sess.pid + ': ' + escapeHtml(sess.command) + '</div>' +
            '<div class="session-item-meta">' +
              '<span>State: <b>' + sess.state + '</b></span>' +
              '<span>Lines: ' + (sess.lineCount || 0) + '</span>' +
            '</div>';
          list.appendChild(item);
        });
      } catch (e) {}
    }

    function selectSession(pid, cmd) {
      selectedPID = pid;
      document.getElementById('terminalTitle').textContent = 'PID ' + pid + ': ' + cmd;
      refreshSessions();
    }

    function startTerminalStream() {
      if (sseSource) sseSource.close();
      sseSource = new EventSource('/api/stream');
      const streamDiv = document.getElementById('terminalStream');

      sseSource.addEventListener('output', e => {
        const data = JSON.parse(e.data);
        if (selectedPID > 0 && data.pid !== selectedPID) return;

        const line = document.createElement('div');
        line.className = 'terminal-line ' + (data.stream || 'stdout');
        line.innerHTML = '<span class="terminal-line-meta">[PID ' + data.pid + ']</span> ' + escapeHtml(data.text);
        streamDiv.appendChild(line);

        if (document.getElementById('autoScrollCheck').checked) {
          streamDiv.scrollTop = streamDiv.scrollHeight;
        }
      });
    }

    async function sendStdin() {
      const input = document.getElementById('stdinInput');
      const val = input.value;
      if (!val) return;
      if (selectedPID === 0) {
        alert('Please select an active process from the left panel to send stdin.');
        return;
      }
      await fetch('/api/stdin', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pid: selectedPID, data: val + '\n' })
      });
      input.value = '';
    }

    async function killSelectedProcess() {
      if (selectedPID === 0) {
        alert('Please select a running process to kill.');
        return;
      }
      if (confirm('Terminate process PID ' + selectedPID + '?')) {
        await fetch('/api/kill?pid=' + selectedPID, { method: 'POST' });
        setTimeout(refreshSessions, 300);
      }
    }

    function clearTerminal() {
      document.getElementById('terminalStream').innerHTML = '';
    }

    function copyTerminalOutput() {
      const text = document.getElementById('terminalStream').innerText;
      navigator.clipboard.writeText(text);
      alert('Terminal output copied to clipboard.');
    }

    function downloadDiagnostics() {
      window.location.href = '/api/diagnostics';
    }

    function escapeHtml(str) {
      return (str || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    }

    // Auto-refresh loops
    setInterval(updateStatus, 2000);
    setInterval(refreshSessions, 2000);
    updateStatus();
    refreshSessions();
    startTerminalStream();
  </script>
</body>
</html>`
