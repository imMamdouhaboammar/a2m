package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/auth"
	"github.com/imMamdouhaboammar/a2m/services/dc-gateway/config"
)

func testRemoteRegistry(t *testing.T, url string) *Registry {
	t.Helper()
	root := t.TempDir()
	provider := filepath.Join(root, "providers.json")
	approval := filepath.Join(root, "approvals.json")
	servers := map[string]interface{}{"servers": map[string]interface{}{"fixture": map[string]interface{}{
		"url": url, "headers": map[string]string{"Authorization": "Bearer test-user-token"},
	}}}
	b, _ := json.Marshal(servers)
	if err := os.WriteFile(provider, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(approval, []byte("{\"servers\":[{\"name\":\"fixture\",\"source\":\"user\",\"tools\":[\"*\"]}]}"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("A2M_MCP_DISCOVERY_CONFIG", provider)
	t.Setenv("A2M_MCP_APPROVALS_FILE", approval)
	cfg := config.NewConfigManager(filepath.Join(root, "gateway.json"))
	if err := cfg.SetValue("capabilityWorkspace", root); err != nil {
		t.Fatal(err)
	}
	r := &Registry{handlers: make(map[string]ToolHandler), sessionHandlers: make(map[string]SessionToolHandler),
		callCounts: make(map[string]int), configMgr: cfg, auth: auth.NewAuthenticator("", auth.RoleAdmin, false)}
	r.registerExternalMCP()
	t.Cleanup(r.Close)
	return r
}

func TestRemoteMCPExportsAndInvokesActualApprovedHTTPSCompatibleTools(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer test-user-token" {
			w.WriteHeader(401)
			return
		}
		if req.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var r struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if json.NewDecoder(req.Body).Decode(&r) != nil {
			w.WriteHeader(400)
			return
		}
		if r.ID == 0 {
			w.WriteHeader(202)
			return
		}
		var result interface{}
		switch r.Method {
		case "initialize":
			result = map[string]interface{}{"protocolVersion": "2025-06-18", "capabilities": map[string]interface{}{"tools": map[string]interface{}{}}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
		case "tools/list":
			result = map[string]interface{}{"tools": []ToolDefinition{
				{Name: "probe", Description: "Probe real remote MCP", InputSchema: map[string]interface{}{"type": "object"}},
				{Name: "operate", Description: "Operate real remote MCP", InputSchema: map[string]interface{}{"type": "object"}},
			}}
		case "tools/call":
			calls++
			result = TextResult("remote-backed-call")
		default:
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": r.ID, "result": result})
	}))
	defer srv.Close()
	r := testRemoteRegistry(t, srv.URL)
	if got := len(r.ListTools()); got != 2 {
		t.Fatalf("remote tools/list not exported, got %d", got)
	}
	result := r.Dispatch("ext_user_fixture__probe", json.RawMessage("{}"))
	if result.IsError || len(result.Content) == 0 || result.Content[0].Text != "remote-backed-call" || calls != 1 {
		t.Fatalf("remote tools/call was not actually forwarded: %+v calls=%d", result, calls)
	}
}

func TestRemoteMCPRejectsInsecureNonLoopbackEndpoint(t *testing.T) {
	if _, err := newRemoteMCPProxy(externalMCPLaunch{URL: "http://example.org/mcp"}); err == nil {
		t.Fatal("remote non-HTTPS endpoint accepted")
	}
}

func TestRemoteMCPParsesEventStreamResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var r struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if json.NewDecoder(req.Body).Decode(&r) != nil {
			w.WriteHeader(400)
			return
		}
		if r.ID == 0 {
			w.WriteHeader(202)
			return
		}
		var result interface{}
		switch r.Method {
		case "initialize":
			result = map[string]interface{}{"protocolVersion": "2025-06-18", "capabilities": map[string]interface{}{}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
		case "tools/list":
			result = map[string]interface{}{"tools": []ToolDefinition{{Name: "stream", Description: "SSE tool", InputSchema: map[string]interface{}{"type": "object"}}}}
		case "tools/call":
			result = TextResult("sse-call")
		}
		b, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": r.ID, "result": result})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, "event: message")
		fmt.Fprintln(w, "data: "+string(b))
		fmt.Fprintln(w)
	}))
	defer srv.Close()
	r := testRemoteRegistry(t, srv.URL)
	if len(r.ListTools()) != 1 {
		t.Fatalf("SSE list response unreadable: %+v", r.ListTools())
	}
	got := r.Dispatch("ext_user_fixture__stream", json.RawMessage("{}"))
	if got.IsError || !strings.Contains(got.Content[0].Text, "sse-call") {
		t.Fatalf("SSE call failed: %+v", got)
	}
}

func TestRemoteMCPNeverFollowsRedirectsToOtherHosts(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(200) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	r := testRemoteRegistry(t, srv.URL)
	if len(r.ListTools()) != 0 {
		t.Fatal("redirected MCP was advertised")
	}
}

func TestRemoteMCPPaginationExportsAllUpstreamPages(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var request struct {
			ID     int64
			Method string
			Params map[string]interface{}
		}
		if json.NewDecoder(req.Body).Decode(&request) != nil {
			w.WriteHeader(400)
			return
		}
		if request.ID == 0 {
			w.WriteHeader(202)
			return
		}
		var result interface{}
		switch request.Method {
		case "initialize":
			result = map[string]interface{}{"protocolVersion": "2025-06-18", "capabilities": map[string]interface{}{}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
		case "tools/list":
			pages++
			if request.Params["cursor"] == "second" {
				result = map[string]interface{}{"tools": []ToolDefinition{{Name: "later", InputSchema: map[string]interface{}{"type": "object"}}}}
			} else {
				result = map[string]interface{}{"tools": []ToolDefinition{{Name: "first", InputSchema: map[string]interface{}{"type": "object"}}}, "nextCursor": "second"}
			}
		default:
			result = TextResult("ok")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer srv.Close()
	r := testRemoteRegistry(t, srv.URL)
	if len(r.ListTools()) != 2 || pages != 2 {
		t.Fatalf("MCP tools pagination incomplete: %d tools, %d page requests", len(r.ListTools()), pages)
	}
}
