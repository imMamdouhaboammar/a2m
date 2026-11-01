package multiplex

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/auth"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/config"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/ipc"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/mcp"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/process"
)

func TestMultiplexHubLifecycleAndWatch(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dc_multiplex_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sockPath := filepath.Join(tempDir, "hub_test.sock")
	supervisor := process.NewSupervisor()
	hub := NewMultiplexHub(supervisor, sockPath)

	if err := hub.Start(); err != nil {
		t.Fatalf("failed to start hub: %v", err)
	}
	defer hub.Close()

	// 1. Connect client to control socket
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to connect to hub socket: %v", err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)

	// 2. Request session list (should be empty initially)
	_, _ = conn.Write([]byte(`{"action":"list"}` + "\n"))
	listLine, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read list response: %v", err)
	}
	if !strings.Contains(string(listLine), "[]") {
		t.Errorf("expected empty sessions array, got: %s", string(listLine))
	}

	// 3. Register as watcher
	_, _ = conn.Write([]byte(`{"action":"watch"}` + "\n"))
	handshake, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read watch handshake: %v", err)
	}
	if !strings.Contains(string(handshake), "watching_all_sessions") {
		t.Errorf("unexpected handshake: %s", string(handshake))
	}

	// 4. Broadcast an event from supervisor
	hub.Broadcast(SessionEvent{
		Type:      EventOutput,
		PID:       4242,
		Command:   "echo 'live multiplex test'",
		Stream:    "stdout",
		Text:      "multiplex broadcast line received",
		Timestamp: time.Now(),
	})

	// 5. Watcher should receive the broadcast line
	evLine, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read broadcast event: %v", err)
	}

	var ev SessionEvent
	if err := json.Unmarshal(evLine, &ev); err != nil {
		t.Fatalf("failed to unmarshal broadcast event: %v", err)
	}
	if ev.PID != 4242 || ev.Text != "multiplex broadcast line received" {
		t.Errorf("unexpected event payload: %+v", ev)
	}
}

func TestHTTPServerDashboardAndSSEEndpoints(t *testing.T) {
	supervisor := process.NewSupervisor()
	hub := NewMultiplexHub(supervisor, "")
	cfgMgr := config.NewConfigManager("")
	authMgr := auth.NewAuthenticator("", auth.RoleAdmin, false)
	fsClient := ipc.NewFsEngineClient("")
	registry := mcp.NewRegistry(supervisor, fsClient, cfgMgr, authMgr)

	srv := NewServer(registry, supervisor, hub, authMgr)

	// 1. Test Dashboard HTML GET /
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.handleDashboard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for dashboard, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "A2M Desktop") || !strings.Contains(rec.Body.String(), "Your machine, under your control") {
		t.Errorf("dashboard HTML missing live A2M interface")
	}

	// 2. Test Sessions API GET /api/sessions
	reqSess := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	recSess := httptest.NewRecorder()
	srv.handleListSessions(recSess, reqSess)

	if recSess.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /api/sessions, got %d", recSess.Code)
	}
	if !strings.Contains(recSess.Body.String(), "[]") {
		t.Errorf("expected empty sessions list, got: %s", recSess.Body.String())
	}

	// 3. Test Status API GET /api/status
	reqStatus := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	recStatus := httptest.NewRecorder()
	srv.handleStatus(recStatus, reqStatus)
	if recStatus.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /api/status, got %d", recStatus.Code)
	}
	if !strings.Contains(recStatus.Body.String(), "connectionState") {
		t.Errorf("missing connectionState in status response")
	}

	// Connect/disconnect require an isolated fake tunnel in tests.
	// Never call the real handlers here: they control the live user tunnel-client.

	// 5. Test Diagnostics Export GET /api/diagnostics
	reqDiag := httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil)
	recDiag := httptest.NewRecorder()
	srv.handleDiagnostics(recDiag, reqDiag)
	if recDiag.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /api/diagnostics, got %d", recDiag.Code)
	}
	if !strings.Contains(recDiag.Body.String(), "tokensMasked") {
		t.Errorf("expected redacted tokens in diagnostics export")
	}
}

func TestHTTPServerSecurityPolicy(t *testing.T) {
	supervisor := process.NewSupervisor()
	hub := NewMultiplexHub(supervisor, "")
	cfgMgr := config.NewConfigManager("")
	secretToken := "secret_test_token_999"
	authMgr := auth.NewAuthenticator(secretToken, auth.RoleAdmin, true)
	fsClient := ipc.NewFsEngineClient("")
	defer fsClient.Close()
	registry := mcp.NewRegistry(supervisor, fsClient, cfgMgr, authMgr)

	srv := NewServer(registry, supervisor, hub, authMgr)

	// 1. Untrusted Origin Blocked (CORS drive-by defense)
	handler := srv.securityMiddleware(srv.handleStatus, http.MethodGet)
	reqBadOrigin := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	reqBadOrigin.Host = "localhost:8765"
	reqBadOrigin.Header.Set("Origin", "http://evil-attacker.com")
	recBadOrigin := httptest.NewRecorder()
	handler(recBadOrigin, reqBadOrigin)
	if recBadOrigin.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for untrusted Origin, got %d", recBadOrigin.Code)
	}

	// 2. Untrusted Host Blocked (DNS rebinding defense)
	reqBadHost := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	reqBadHost.Host = "rebound.attacker.com"
	recBadHost := httptest.NewRecorder()
	handler(recBadHost, reqBadHost)
	if recBadHost.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for untrusted Host, got %d", recBadHost.Code)
	}

	// 3. Valid Loopback Origin Allowed & Echoed
	reqGoodOrigin := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	reqGoodOrigin.Host = "localhost:8765"
	reqGoodOrigin.Header.Set("Origin", "http://localhost:3000")
	recGoodOrigin := httptest.NewRecorder()
	handler(recGoodOrigin, reqGoodOrigin)
	if recGoodOrigin.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for localhost Origin, got %d", recGoodOrigin.Code)
	}
	if recGoodOrigin.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("expected Access-Control-Allow-Origin to match loopback, got %s", recGoodOrigin.Header().Get("Access-Control-Allow-Origin"))
	}

	// 4. Method Enforcement: GET on mutating endpoint returns 405 Method Not Allowed
	stdinHandler := srv.requireAuthMiddleware(srv.handleStdin, http.MethodPost)
	reqWrongMethod := httptest.NewRequest(http.MethodGet, "/api/stdin", nil)
	reqWrongMethod.Host = "localhost:8765"
	recWrongMethod := httptest.NewRecorder()
	stdinHandler(recWrongMethod, reqWrongMethod)
	if recWrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 Method Not Allowed for GET /api/stdin, got %d", recWrongMethod.Code)
	}

	// 5. Unauthenticated sensitive endpoint returns 401 Unauthorized
	reqUnauth := httptest.NewRequest(http.MethodPost, "/api/stdin", strings.NewReader(`{"pid":1,"data":"test"}`))
	reqUnauth.Host = "localhost:8765"
	recUnauth := httptest.NewRecorder()
	stdinHandler(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for unauthenticated /api/stdin, got %d", recUnauth.Code)
	}

	// 6. Authenticated sensitive endpoint with Bearer token is allowed
	reqAuth := httptest.NewRequest(http.MethodPost, "/api/stdin", strings.NewReader(`{"pid":999999,"data":"test"}`))
	reqAuth.Host = "localhost:8765"
	reqAuth.Header.Set("Authorization", "Bearer "+secretToken)
	recAuth := httptest.NewRecorder()
	stdinHandler(recAuth, reqAuth)
	// PID 999999 doesn't exist so supervisor returns 500/error, but auth gate passed (not 401 or 403)
	if recAuth.Code == http.StatusUnauthorized || recAuth.Code == http.StatusForbidden {
		t.Fatalf("expected auth gate to pass with valid Bearer token, got %d", recAuth.Code)
	}
}
