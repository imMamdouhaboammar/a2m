package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Role string

const (
	RoleAdmin     Role = "admin"
	RoleWorkspace Role = "workspace"
	RoleReadOnly  Role = "read_only"
)

type AuthStatus struct {
	Enabled              bool     `json:"enabled"`
	AuthRequired         bool     `json:"authRequired"`
	Authenticated        bool     `json:"authenticated"`
	RemoteAccess         bool     `json:"remote_access"`
	MachineAccessEnabled bool     `json:"machine_access_enabled"`
	Role                 Role     `json:"role"`
	AllowedTools         []string `json:"allowedTools,omitempty"`
	StatusMessage        string   `json:"status_message,omitempty"`
}

type Authenticator struct {
	enabled       bool
	token         string
	role          Role
	authenticated bool
	sessions      map[string]*SessionAuth
	mu            sync.RWMutex
}

type SessionAuth struct {
	SessionID     string `json:"sessionId"`
	Authenticated bool   `json:"authenticated"`
	Role          Role   `json:"role"`
}

var readOnlyAllowedTools = map[string]bool{
	"read_file":               true,
	"read_multiple_files":     true,
	"list_directory":          true,
	"get_file_info":           true,
	"start_search":            true,
	"get_more_search_results": true,
	"stop_search":             true,
	"list_searches":           true,
	"list_sessions":           true,
	"list_processes":          true,
	"read_process_output":     true,
	"get_config":              true,
	"get_usage_stats":         true,
	"get_recent_tool_calls":   true,
	"excel_read_sheet":        true,
	"excel_list_sheets":       true,
	"docx_read":               true,
	"pdf_extract_text":        true,
	"auth_status":             true,
	"auth_login":              true,
	"get_architecture":        true,
	"query_symbols":           true,
	"trace_call_path":         true,
	"assess_impact":           true,
	"correlate_error":         true,
	"capabilities_list":       true,
	"skills_find":             true,
	"skills_read":             true,
	"mcp_servers_list":        true,
}

var workspaceBlockedTools = map[string]bool{
	"force_terminate":  true,
	"set_config_value": true,
}

func NewAuthenticator(token string, role Role, requireAuth bool) *Authenticator {
	envToken := os.Getenv("DC_AUTH_TOKEN")
	if envToken != "" {
		token = envToken
		requireAuth = true
	}

	envRole := os.Getenv("DC_AUTH_ROLE")
	if envRole != "" {
		role = Role(strings.ToLower(envRole))
	}
	if role == "" {
		role = RoleAdmin
	}

	envRequire := os.Getenv("DC_REQUIRE_AUTH")
	if envRequire == "true" || envRequire == "1" {
		requireAuth = true
	}

	// If auth is required but no token provided, generate or load from local secure keyfile
	if requireAuth && token == "" {
		token = loadOrGenerateLocalToken()
	}

	a := &Authenticator{
		enabled:       requireAuth,
		token:         token,
		role:          role,
		authenticated: !requireAuth, // If auth not required, default to authenticated
		sessions:      make(map[string]*SessionAuth),
	}

	return a
}

func (a *Authenticator) IsEnabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.enabled
}

func (a *Authenticator) IsAuthenticated() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.authenticated
}

func (a *Authenticator) IsSessionAuthenticated(sessionID string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.enabled {
		return true
	}
	if sessionID == "" {
		return a.authenticated
	}
	if s, ok := a.sessions[sessionID]; ok && s != nil {
		return s.Authenticated
	}
	return false
}

func (a *Authenticator) GetRole() Role {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.role
}

func (a *Authenticator) Authenticate(candidateToken string) bool {
	return a.AuthenticateSession("", candidateToken)
}

func (a *Authenticator) AuthenticateSession(sessionID string, candidateToken string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.enabled {
		if sessionID != "" {
			a.sessions[sessionID] = &SessionAuth{
				SessionID:     sessionID,
				Authenticated: true,
				Role:          a.role,
			}
		} else {
			a.authenticated = true
		}
		return true
	}

	if candidateToken == "" {
		return false
	}

	// Constant-time byte slice comparison to eliminate timing attacks
	srcBytes := []byte(a.token)
	candBytes := []byte(candidateToken)
	if subtle.ConstantTimeCompare(srcBytes, candBytes) == 1 {
		if sessionID != "" {
			a.sessions[sessionID] = &SessionAuth{
				SessionID:     sessionID,
				Authenticated: true,
				Role:          a.role,
			}
		} else {
			a.authenticated = true
		}
		return true
	}

	return false
}

func (a *Authenticator) RemoveSession(sessionID string) {
	if sessionID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, sessionID)
}

func (a *Authenticator) ValidateToken(candidateToken string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.enabled {
		return true
	}
	if candidateToken == "" {
		return false
	}
	srcBytes := []byte(a.token)
	candBytes := []byte(candidateToken)
	return subtle.ConstantTimeCompare(srcBytes, candBytes) == 1
}

func (a *Authenticator) AuthorizeTool(toolName string) error {
	return a.AuthorizeSessionTool("", toolName)
}

func (a *Authenticator) AuthorizeSessionTool(sessionID string, toolName string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// Auth management tools are always accessible
	if toolName == "auth_status" || toolName == "auth_login" {
		return nil
	}

	var isAuth bool
	role := a.role
	if sessionID != "" {
		if s, ok := a.sessions[sessionID]; ok && s != nil {
			isAuth = s.Authenticated
			if s.Role != "" {
				role = s.Role
			}
		} else {
			isAuth = false
		}
	} else {
		isAuth = a.authenticated
	}

	// 1. Check authentication status
	if a.enabled && !isAuth {
		return fmt.Errorf("authentication required: session is not authenticated (call 'auth_login' with valid token)")
	}

	// 2. Role-based capabilities
	switch role {
	case RoleAdmin:
		return nil

	case RoleReadOnly:
		if !readOnlyAllowedTools[toolName] && !strings.HasPrefix(toolName, "crg_") {
			return fmt.Errorf("permission denied: tool '%s' is prohibited under '%s' role", toolName, role)
		}
		return nil

	case RoleWorkspace:
		if workspaceBlockedTools[toolName] {
			return fmt.Errorf("permission denied: tool '%s' is prohibited under '%s' role", toolName, role)
		}
		return nil

	default:
		return nil
	}
}

func (a *Authenticator) GetStatus() AuthStatus {
	return a.GetSessionStatus("")
}

func (a *Authenticator) GetSessionStatus(sessionID string) AuthStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()

	isAuth := a.authenticated
	role := a.role
	if sessionID != "" {
		if s, ok := a.sessions[sessionID]; ok && s != nil {
			isAuth = s.Authenticated
			if s.Role != "" {
				role = s.Role
			}
		} else {
			isAuth = false
		}
	}

	statusMsg := "Machine access is active and authorized. All requested tools, terminal commands, and filesystem operations are enabled."
	if !isAuth {
		statusMsg = "Authentication required: call 'auth_login' with the access token to enable full machine access."
	}

	return AuthStatus{
		Enabled:              isAuth,
		AuthRequired:         a.enabled,
		Authenticated:        isAuth,
		RemoteAccess:         isAuth,
		MachineAccessEnabled: isAuth,
		Role:                 role,
		StatusMessage:        statusMsg,
	}
}

func GenerateSecureToken() string {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return "dc_fallback_token_" + hex.EncodeToString([]byte("random"))
	}
	return "dc_sec_" + hex.EncodeToString(bytes)
}

func loadOrGenerateLocalToken() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	authDir := filepath.Join(homeDir, ".a2m")
	tokenPath := filepath.Join(authDir, "auth_token")

	// Read existing token if available
	if data, err := os.ReadFile(tokenPath); err == nil {
		trimmed := strings.TrimSpace(string(data))
		if trimmed != "" {
			return trimmed
		}
	}

	// Generate fresh token
	freshToken := GenerateSecureToken()
	_ = os.MkdirAll(authDir, 0700)
	_ = os.WriteFile(tokenPath, []byte(freshToken+"\n"), 0600)
	fmt.Fprintf(os.Stderr, "[a2m] Generated secure local auth token at: %s\n", tokenPath)
	return freshToken
}
