package multiplex

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseMCPToolCallsUsesOnlyRealToolCount(t *testing.T) {
	metrics := strings.Join([]string{
		`command_end_to_end_latency_milliseconds_bucket{latency_type="enqueue_to_response",request_method="tools/call",tunnel_id="tunnel_live",le="+Inf"} 999`,
		`command_end_to_end_latency_milliseconds_count{latency_type="poll_to_response",request_method="tools/call",tunnel_id="tunnel_live"} 51`,
		`command_end_to_end_latency_milliseconds_count{latency_type="enqueue_to_response",request_method="tools/call",tunnel_id="tunnel_other"} 100`,
		`command_end_to_end_latency_milliseconds_count{latency_type="enqueue_to_response",request_method="initialize",tunnel_id="tunnel_live"} 22`,
		`command_end_to_end_latency_milliseconds_count{latency_type="enqueue_to_response",request_method="tools/call",tunnel_id="tunnel_live"} 7`,
	}, "\n")
	count := parseMCPToolCalls(metrics, "tunnel_live")
	if count == nil || *count != 7 {
		t.Fatalf("expected 7 true tool calls, got %v", count)
	}
	if parseMCPToolCalls("unrelated_counter 3", "tunnel_live") != nil {
		t.Fatal("missing tool metrics must be unknown, not fabricated zero")
	}
}

func TestObserveTunnelClientReadsDaemonMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			fmt.Fprint(w, "ready")
		case "/api/status":
			fmt.Fprint(w, `{"control_plane_tunnel_id":"tunnel_actual","uptime_seconds":412}`)
		case "/metrics":
			fmt.Fprint(w, `command_end_to_end_latency_milliseconds_count{latency_type="enqueue_to_response",request_method="tools/call",tunnel_id="tunnel_actual"} 9`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	result := observeTunnelClientAt(srv.URL)
	if !result.Ready || result.TunnelID != "tunnel_actual" || result.Uptime != 412 ||
		result.MCPToolCalls == nil || *result.MCPToolCalls != 9 {
		t.Fatalf("unexpected observed tunnel status: %+v", result)
	}
}

func TestSanitizeTunnelLogNeverReturnsOriginalPayload(t *testing.T) {
	event, ok := sanitizeTunnelEvent(tunnelLogEntry{
		Seq: 12, Time: "2026-10-30T00:00:00Z",
		Message: "dispatcher forwarded command to secret-command argument=token-value",
	})
	if !ok || event.Operation != "MCP request forwarded" ||
		strings.Contains(fmt.Sprintf("%+v", event), "secret-command") {
		t.Fatalf("operator event leaked raw log data: %+v", event)
	}
	if _, ok := sanitizeTunnelEvent(tunnelLogEntry{Message: "arbitrary payload includes personal data"}); ok {
		t.Fatal("unrecognized log events must never be forwarded to the UI")
	}
}

func TestTunnelActivityAtReturnsOnlyRedactedEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"events":[{"seq":1,"time":"2026-10-30T00:00:00Z","message":"dispatcher forwarded command to private-target","attrs":{"api_key":"never expose"}},{"seq":2,"time":"2026-10-30T00:00:01Z","message":"unknown private data"}]}`)
	}))
	defer srv.Close()
	events, err := tunnelActivityAt(srv.URL)
	if err != nil || len(events) != 1 || events[0].Operation != "MCP request forwarded" {
		t.Fatalf("unexpected sanitized activity: events=%+v err=%v", events, err)
	}
}
