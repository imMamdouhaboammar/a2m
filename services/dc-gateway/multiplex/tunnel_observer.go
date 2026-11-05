package multiplex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The tunnel-client daemon and the A2M dashboard gateway can be different
// processes. Observe the daemon's own loopback operator API instead of
// inventing connection metadata or equating the dashboard with MCP traffic.
type tunnelObservation struct {
	Ready        bool   `json:"ready"`
	TunnelID     string `json:"tunnelId,omitempty"`
	Uptime       int    `json:"uptimeSeconds"`
	MCPToolCalls *int   `json:"mcpToolCalls"`
	Source       string `json:"source"`
}

type tunnelLogEntry struct {
	Seq     int64  `json:"seq"`
	Time    string `json:"time"`
	Message string `json:"message"`
}

type tunnelActivityEntry struct {
	Seq       int64  `json:"seq"`
	Timestamp string `json:"timestamp"`
	Operation string `json:"operation"`
	Source    string `json:"source"`
}

// The operator API is bound on loopback by tunnel-client. Do not forward
// arbitrary remote URLs or any original log payload to the web UI.
const tunnelAdminBase = "http://127.0.0.1:8080"

func readTunnelEndpoint(client *http.Client, base, path string, maxBytes int64) ([]byte, error) {
	resp, err := client.Get(strings.TrimSuffix(base, "/") + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxBytes)
	}
	return body, nil
}

func parseMCPToolCalls(metrics string, tunnelID string) *int {
	// Prometheus counters are cumulative within the running daemon. Count
	// just successful enqueue-to-response tool requests, not histogram
	// buckets or duplicate latency series.
	const prefix = "command_end_to_end_latency_milliseconds_count{"
	total := 0
	found := false
	scanner := bufio.NewScanner(strings.NewReader(metrics))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, prefix) ||
			!strings.Contains(line, `request_method="tools/call"`) ||
			!strings.Contains(line, `latency_type="enqueue_to_response"`) {
			continue
		}
		if tunnelID != "" && !strings.Contains(line, `tunnel_id="`+tunnelID+`"`) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil || value < 0 {
			continue
		}
		total += int(value)
		found = true
	}
	if !found {
		return nil
	}
	return &total
}

func observeTunnelClientAt(base string) tunnelObservation {
	obs := tunnelObservation{Source: "tunnel-client loopback operator API"}
	client := &http.Client{Timeout: 450 * time.Millisecond}
	_, err := readTunnelEndpoint(client, base, "/readyz", 128)
	if err != nil {
		return obs
	}
	obs.Ready = true
	body, err := readTunnelEndpoint(client, base, "/api/status", 64*1024)
	if err == nil {
		var status struct {
			TunnelID string `json:"control_plane_tunnel_id"`
			Uptime   int    `json:"uptime_seconds"`
		}
		if json.Unmarshal(body, &status) == nil {
			obs.TunnelID = status.TunnelID
			obs.Uptime = status.Uptime
		}
	}
	metrics, err := readTunnelEndpoint(client, base, "/metrics", 1024*1024)
	if err == nil {
		obs.MCPToolCalls = parseMCPToolCalls(string(metrics), obs.TunnelID)
	}
	return obs
}

func observeTunnelClient() tunnelObservation {
	return observeTunnelClientAt(tunnelAdminBase)
}

func sanitizeTunnelEvent(entry tunnelLogEntry) (tunnelActivityEntry, bool) {
	var operation string
	switch {
	case strings.HasPrefix(entry.Message, "dispatcher forwarded command to"):
		operation = "MCP request forwarded"
	case strings.HasPrefix(entry.Message, "dispatcher acknowledged notification with"):
		operation = "MCP notification acknowledged"
	default:
		return tunnelActivityEntry{}, false
	}
	return tunnelActivityEntry{
		Seq: entry.Seq, Timestamp: entry.Time, Operation: operation,
		Source: "tunnel-client",
	}, true
}

func tunnelActivityAt(base string) ([]tunnelActivityEntry, error) {
	client := &http.Client{Timeout: 450 * time.Millisecond}
	body, err := readTunnelEndpoint(client, base, "/api/logs?limit=80", 256*1024)
	if err != nil {
		return nil, err
	}
	var source struct {
		Events []tunnelLogEntry `json:"events"`
	}
	if err := json.Unmarshal(body, &source); err != nil {
		return nil, err
	}
	result := make([]tunnelActivityEntry, 0, len(source.Events))
	for _, entry := range source.Events {
		if event, valid := sanitizeTunnelEvent(entry); valid {
			result = append(result, event)
		}
	}
	return result, nil
}

func (s *Server) handleTunnelActivity(w http.ResponseWriter, r *http.Request) {
	events, err := tunnelActivityAt(tunnelAdminBase)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"available": false, "events": []tunnelActivityEntry{},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"available": true, "events": events,
	})
}

func observableSidecars() map[string]string {
	return probeSidecarsAt(
		"/tmp/a2m-ipc.sock",
		sidecarExecutable("dc-fs-engine", "a2m-fs"),
		sidecarExecutable("dc-doc-service", "a2m-docs"),
	)
}

func observedPlatform() string {
	return runtime.GOOS + " " + runtime.GOARCH
}
