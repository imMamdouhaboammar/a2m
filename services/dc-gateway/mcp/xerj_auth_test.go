package mcp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestXerjDaemonHealthUsesExplicitAuthorization(t *testing.T) {
	t.Setenv("XERJ_AUTH", "ApiKey local-test-key")
	t.Setenv("XERJ_API_KEY", "")
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "ApiKey local-test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer node.Close()
	if !checkXerjDaemonHealthy(node.URL) {
		t.Fatal("authenticated XERJ node was reported unhealthy")
	}
}

func TestXerjCommandEnvironmentPreservesOnlyProviderAuthorization(t *testing.T) {
	t.Setenv("XERJ_AUTH", "")
	t.Setenv("XERJ_API_KEY", "local-provider-key")
	t.Setenv("CONTROL_PLANE_API_KEY", "gateway-secret-do-not-forward")
	env := strings.Join(xerjCommandEnvironment("http://127.0.0.1:9200"), "\n")
	for _, required := range []string{
		"XERJ_API_KEY=local-provider-key",
		"XERJ_AUTH=ApiKey local-provider-key",
		"XERJ_URL=http://127.0.0.1:9200",
	} {
		if !strings.Contains(env, required) {
			t.Fatalf("missing expected XERJ environment key: %s", strings.SplitN(required, "=", 2)[0])
		}
	}
	if strings.Contains(env, "gateway-secret-do-not-forward") {
		t.Fatal("gateway control-plane secret leaked to XERJ process")
	}
}

func TestXerjCommandResultHonorsExitStatus(t *testing.T) {
	failed := xerjCommandResult([]byte("node is unreachable"), errors.New("exit status 1"))
	if !failed.IsError {
		t.Fatal("CLI command failure with nonempty output was reported as success")
	}
	succeeded := xerjCommandResult([]byte("indexed successfully"), nil)
	if succeeded.IsError {
		t.Fatal("successful CLI command was reported as an error")
	}
}

func TestXerjAuthorizationNotInheritedByUnrelatedGraphProcesses(t *testing.T) {
	t.Setenv("XERJ_AUTH", "ApiKey private-provider-credential")
	graphEnv := strings.Join(safeGraphEnv(), "\n")
	if strings.Contains(graphEnv, "private-provider-credential") {
		t.Fatal("XERJ credential leaked to a non-XERJ graph subprocess")
	}
	xerjEnv := strings.Join(xerjCommandEnvironment("http://127.0.0.1:9200"), "\n")
	if !strings.Contains(xerjEnv, "XERJ_AUTH=ApiKey private-provider-credential") {
		t.Fatal("XERJ credential was not forwarded to its intended subprocess")
	}
}
