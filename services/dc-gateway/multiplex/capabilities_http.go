package multiplex

import (
	"encoding/json"
	"net/http"
)

// The macOS settings page consumes the same real capability registry as MCP.
// It never receives environment variables, server launch arguments or tokens.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	read := func(name string) interface{} {
		result := s.registry.Dispatch(name, json.RawMessage("{}"))
		if result.IsError || len(result.Content) == 0 {
			return map[string]string{"error": "unavailable"}
		}
		var decoded interface{}
		if err := json.Unmarshal([]byte(result.Content[0].Text), &decoded); err != nil {
			return map[string]string{"error": "invalid adapter response"}
		}
		return decoded
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"capabilities": read("capabilities_list"),
		"mcp_servers":  read("mcp_servers_list"),
		"scope":        "live gateway capability registry; third-party tools need operator-owned per-tool approvals",
	})
}
