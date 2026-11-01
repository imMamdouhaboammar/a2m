package multiplex

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/desktop-commander/dc-gateway/process"
)

func TestDesktopUIAssetsAreServedFromProject(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(cwd, "../../../UI/a2m-desktop-v2")
	t.Setenv("A2M_UI_DIR", source)
	srv := &Server{}

	for _, item := range []struct {
		path    string
		handler http.HandlerFunc
		mime    string
		marker  string
	}{
		{"/", srv.handleDashboard, "text/html", "Your machine, under your control"},
		{"/styles.css", srv.handleUIStyles, "text/css", "--forest"},
		{"/app.js", srv.handleUIScript, "text/javascript", "refreshSessions"},
	} {
		rec := httptest.NewRecorder()
		item.handler(rec, httptest.NewRequest(http.MethodGet, item.path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", item.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), item.mime) {
			t.Errorf("%s: bad MIME", item.path)
		}
		if !strings.Contains(rec.Body.String(), item.marker) {
			t.Errorf("%s: missing content marker", item.path)
		}
		if rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: missing CSP", item.path)
		}
	}
}

func TestDesktopUIAssetsFailClosedWhenMissing(t *testing.T) {
	t.Setenv("A2M_UI_DIR", t.TempDir())
	srv := &Server{}
	rec := httptest.NewRecorder()
	srv.handleDashboard(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503; got %d", rec.Code)
	}
}

func TestDesktopOutputEndpointInputValidation(t *testing.T) {
	srv := &Server{supervisor: process.NewSupervisor()}
	for _, item := range []struct {
		query string
		want  int
	}{
		{"?pid=not-a-number", http.StatusBadRequest},
		{"?pid=-1", http.StatusBadRequest},
		{"?pid=42&limit=501", http.StatusBadRequest},
		{"?pid=42", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		srv.handleProcessOutput(rec, httptest.NewRequest(http.MethodGet, "/api/output"+item.query, nil))
		if rec.Code != item.want {
			t.Errorf("%s: status %d, want %d", item.query, rec.Code, item.want)
		}
	}
}

func TestDesktopKillEndpointRejectsInvalidProcess(t *testing.T) {
	srv := &Server{supervisor: process.NewSupervisor()}
	for _, item := range []struct {
		query string
		want  int
	}{
		{"?pid=invalid", http.StatusBadRequest},
		{"?pid=0", http.StatusBadRequest},
		{"?pid=999999", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		srv.handleKill(rec, httptest.NewRequest(http.MethodPost, "/api/kill"+item.query, nil))
		if rec.Code != item.want {
			t.Errorf("%s: status %d, want %d", item.query, rec.Code, item.want)
		}
	}
}
