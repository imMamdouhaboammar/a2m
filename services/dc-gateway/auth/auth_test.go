package auth

import (
	"strings"
	"testing"
)

func TestAuthDisabledDefault(t *testing.T) {
	auth := NewAuthenticator("", RoleAdmin, false)
	if auth.IsEnabled() {
		t.Errorf("expected auth to be disabled")
	}
	if !auth.IsAuthenticated() {
		t.Errorf("expected session to be authenticated by default when auth disabled")
	}

	// Any tool should be authorized
	if err := auth.AuthorizeTool("start_process"); err != nil {
		t.Errorf("start_process should be authorized: %v", err)
	}
	if err := auth.AuthorizeTool("write_file"); err != nil {
		t.Errorf("write_file should be authorized: %v", err)
	}

	st := auth.GetSessionStatus("new-session-123")
	if !st.Authenticated || !st.MachineAccessEnabled || st.AuthRequired {
		t.Errorf("expected session status to report authenticated and machine access enabled when auth is disabled, got %+v", st)
	}
	if st.StatusMessage == "" || strings.Contains(st.StatusMessage, "Authentication required") {
		t.Errorf("status message should not require auth when disabled: %s", st.StatusMessage)
	}

	roAuth := NewAuthenticator("", RoleReadOnly, false)
	roStatus := roAuth.GetSessionStatus("sess-ro")
	if !strings.Contains(roStatus.StatusMessage, "read-only") {
		t.Errorf("expected read-only in status message, got: %s", roStatus.StatusMessage)
	}
}

func TestAuthTokenVerificationAndGating(t *testing.T) {
	secretToken := "dc_test_secret_12345"
	auth := NewAuthenticator(secretToken, RoleAdmin, true)

	if !auth.IsEnabled() {
		t.Fatalf("expected auth to be enabled")
	}
	if auth.IsAuthenticated() {
		t.Fatalf("expected session to NOT be authenticated initially")
	}

	// Tools should be blocked before login
	err := auth.AuthorizeTool("start_process")
	if err == nil {
		t.Fatalf("expected start_process to be rejected before authentication")
	}

	// Auth tools should always be allowed
	if err := auth.AuthorizeTool("auth_status"); err != nil {
		t.Errorf("auth_status should be allowed even unauthenticated: %v", err)
	}
	if err := auth.AuthorizeTool("auth_login"); err != nil {
		t.Errorf("auth_login should be allowed even unauthenticated: %v", err)
	}

	// Wrong token should fail
	if auth.Authenticate("wrong_token") {
		t.Errorf("wrong token should not authenticate")
	}
	if auth.IsAuthenticated() {
		t.Errorf("auth status should remain false after failed attempt")
	}

	// Correct token should succeed
	if !auth.Authenticate(secretToken) {
		t.Errorf("correct token should authenticate")
	}
	if !auth.IsAuthenticated() {
		t.Errorf("auth status should be true after successful login")
	}

	// Now tools should be authorized
	if err := auth.AuthorizeTool("start_process"); err != nil {
		t.Errorf("start_process should be authorized after login: %v", err)
	}
	if err := auth.AuthorizeTool("write_file"); err != nil {
		t.Errorf("write_file should be authorized after login: %v", err)
	}
}

func TestReadOnlyRoleGating(t *testing.T) {
	auth := NewAuthenticator("", RoleReadOnly, false)

	// Allowed read tools
	readTools := []string{
		"read_file",
		"read_multiple_files",
		"list_directory",
		"get_file_info",
		"start_search",
		"get_more_search_results",
		"stop_search",
		"list_searches",
		"read_process_output",
		"excel_read_sheet",
		"docx_read",
		"pdf_extract_text",
	}

	for _, tool := range readTools {
		if err := auth.AuthorizeTool(tool); err != nil {
			t.Errorf("expected read tool '%s' to be allowed under read_only role: %v", tool, err)
		}
	}

	// Prohibited mutation tools
	mutatingTools := []string{
		"start_process",
		"interact_with_process",
		"kill_process",
		"force_terminate",
		"write_file",
		"edit_block",
		"create_directory",
		"move_file",
		"set_config_value",
		"excel_edit_range",
		"docx_edit",
	}

	for _, tool := range mutatingTools {
		if err := auth.AuthorizeTool(tool); err == nil {
			t.Errorf("expected mutating tool '%s' to be BLOCKED under read_only role", tool)
		}
	}
}

func TestWorkspaceRoleGating(t *testing.T) {
	auth := NewAuthenticator("", RoleWorkspace, false)

	// Workspace role allows normal dev tools
	allowed := []string{
		"read_file",
		"write_file",
		"edit_block",
		"start_process",
		"kill_process",
	}
	for _, tool := range allowed {
		if err := auth.AuthorizeTool(tool); err != nil {
			t.Errorf("tool '%s' should be allowed in workspace role: %v", tool, err)
		}
	}

	// Workspace role blocks destructive root operations
	blocked := []string{
		"force_terminate",
		"set_config_value",
	}
	for _, tool := range blocked {
		if err := auth.AuthorizeTool(tool); err == nil {
			t.Errorf("tool '%s' should be BLOCKED in workspace role", tool)
		}
	}
}

func TestCrossSessionIsolation(t *testing.T) {
	secretToken := "secret_token_session_xyz"
	auth := NewAuthenticator(secretToken, RoleAdmin, true)

	sessionA := "session_client_A"
	sessionB := "session_client_B"

	// Both sessions initially unauthenticated
	if err := auth.AuthorizeSessionTool(sessionA, "start_process"); err == nil {
		t.Fatalf("session A should be rejected before login")
	}
	if err := auth.AuthorizeSessionTool(sessionB, "start_process"); err == nil {
		t.Fatalf("session B should be rejected before login")
	}

	// Authenticate Session A ONLY
	if !auth.AuthenticateSession(sessionA, secretToken) {
		t.Fatalf("session A should authenticate with valid token")
	}

	// Verify Session A is authenticated
	if err := auth.AuthorizeSessionTool(sessionA, "start_process"); err != nil {
		t.Errorf("session A should be authorized after login: %v", err)
	}

	// Verify Session B is STILL REJECTED (Zero cross-session leak)
	if err := auth.AuthorizeSessionTool(sessionB, "start_process"); err == nil {
		t.Fatalf("SECURITY VIOLATION: session B was incorrectly authorized when session A logged in")
	}

	// Verify empty session is STILL REJECTED (Zero unauthenticated leak)
	if err := auth.AuthorizeSessionTool("", "start_process"); err == nil {
		t.Fatalf("SECURITY VIOLATION: empty session ID was incorrectly authorized when session A logged in")
	}

	// Verify session revocation / removal
	auth.RemoveSession(sessionA)
	if err := auth.AuthorizeSessionTool(sessionA, "start_process"); err == nil {
		t.Fatalf("session A should be rejected after RemoveSession")
	}
}
