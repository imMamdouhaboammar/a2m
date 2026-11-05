package main

import (
	"encoding/json"
	"os/exec"
	"testing"
)

func TestHealthCommandReportsOnDemandReadiness(t *testing.T) {
	cmd := exec.Command("go", "run", ".", "health")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("health command failed: %v; output: %s", err, out)
	}
	var health struct {
		Service string `json:"service"`
		Status  string `json:"status"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(out, &health); err != nil {
		t.Fatalf("health response is not JSON: %v; output: %s", err, out)
	}
	if health.Service != "a2m-docs" || health.Status != "ready" || health.Mode != "on-demand" {
		t.Fatalf("unexpected document service health: %+v", health)
	}
}
