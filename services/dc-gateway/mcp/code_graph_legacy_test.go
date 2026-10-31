package mcp

import "testing"

func TestNativeGraphSupersedesUnsafeHeuristicGraph(t *testing.T) {
	r := &Registry{tools: []ToolDefinition{
		{Name: "get_architecture"}, {Name: "index_codebase"}, {Name: "query_symbols"},
		{Name: "trace_call_path"}, {Name: "assess_impact"}, {Name: "correlate_error"},
		{Name: "manage_adr"}, {Name: "crg_query_graph_tool"}, {Name: "read_file"},
	}}
	r.handlers = map[string]ToolHandler{}
	for _, tool := range r.tools {
		r.handlers[tool.Name] = nil
	}
	r.suppressLegacyGraph()
	for _, tool := range r.tools {
		switch tool.Name {
		case "get_architecture", "index_codebase", "query_symbols", "trace_call_path", "assess_impact", "correlate_error":
			t.Fatalf("legacy heuristic graph still advertised: %s", tool.Name)
		}
	}
	if len(r.tools) != 3 {
		t.Fatalf("expected native, ADR and file tools; got %d", len(r.tools))
	}
	if _, ok := r.handlers["get_architecture"]; ok {
		t.Fatal("unsafe legacy handler still callable")
	}
}
