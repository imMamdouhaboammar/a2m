package multiplex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/desktop-commander/dc-gateway/mcp"
)

func TestCapabilityHTTPReportsRealRegistryWithoutSecrets(t *testing.T) {
	s := &Server{registry: mcp.NewRegistry(nil, nil, nil, nil)}
	req := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil)
	record := httptest.NewRecorder()
	s.handleCapabilities(record, req)
	if record.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d: %s", record.Code, record.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	caps, ok := body["capabilities"].(map[string]interface{})
	if !ok || caps["policy"] == nil {
		t.Fatalf("missing capability policy: %+v", body)
	}
	if _, ok := body["mcp_servers"]; !ok {
		t.Fatal("missing discovered MCP inventory")
	}
	if strings.Contains(record.Body.String(), "SENTRY_AUTH_TOKEN") {
		t.Fatal("secrets exposed via status API")
	}
}
