package multiplex

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/desktop-commander/dc-gateway/process"
)

func TestDashboardMergesMCPWorkerSessionsAndOutput(t *testing.T) {
	socket := shortWorkerTestSocket(t)
	workerSupervisor := process.NewSupervisor()
	hub := NewMultiplexHub(workerSupervisor, socket)
	if err := hub.Start(); err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	monitor := NewWorkerMonitor(socket)
	monitor.Start()
	defer monitor.Close()
	awaitWorkerMonitor(t, 3*time.Second, monitor.Connected)

	session, err := workerSupervisor.StartProcess("printf 'remote-dashboard-proof\\n'", 1500, "")
	if err != nil {
		t.Fatal(err)
	}
	awaitWorkerMonitor(t, 3*time.Second, func() bool {
		lines, ok := monitor.Snapshot(session.PID)
		return ok && len(lines) > 0
	})
	dashboard := &Server{supervisor: process.NewSupervisor(), worker: monitor}
	sessionsRec := httptest.NewRecorder()
	dashboard.handleListSessions(sessionsRec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if sessionsRec.Code != http.StatusOK {
		t.Fatalf("sessions HTTP %d", sessionsRec.Code)
	}
	if sessionsRec.Header().Get("X-A2M-Worker-Monitor") != "connected" {
		t.Fatal("no live worker status")
	}
	var sessions []process.SessionSummary
	if err := json.Unmarshal(sessionsRec.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range sessions {
		if item.PID == session.PID && item.Source == "mcp-worker" {
			found = true
		}
	}
	if !found {
		t.Fatalf("MCP worker PID %d absent from desktop sessions", session.PID)
	}

	outputRec := httptest.NewRecorder()
	path := fmt.Sprintf("/api/output?pid=%d&source=mcp-worker&limit=20", session.PID)
	dashboard.handleProcessOutput(outputRec, httptest.NewRequest(http.MethodGet, path, nil))
	if outputRec.Code != http.StatusOK || !strings.Contains(outputRec.Body.String(), "remote-dashboard-proof") {
		t.Fatalf("remote stdout not visible: HTTP %d %s", outputRec.Code, outputRec.Body.String())
	}
	killRec := httptest.NewRecorder()
	dashboard.handleKill(killRec, httptest.NewRequest(http.MethodPost, "/api/kill?pid=999999&source=mcp-worker", nil))
	if killRec.Code != http.StatusNotFound {
		t.Fatalf("unknown remote PID must fail closed, got %d", killRec.Code)
	}
}

func TestDashboardUnavailableWorkerLeavesLocalSessionsAvailable(t *testing.T) {
	monitor := NewWorkerMonitor(shortWorkerTestSocket(t))
	dashboard := &Server{supervisor: process.NewSupervisor(), worker: monitor}
	rec := httptest.NewRecorder()
	dashboard.handleListSessions(rec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("local sessions HTTP %d", rec.Code)
	}
	if rec.Header().Get("X-A2M-Worker-Monitor") != "unavailable" {
		t.Fatal("missing disconnected worker header")
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("unexpected sessions: %s", rec.Body.String())
	}
}
