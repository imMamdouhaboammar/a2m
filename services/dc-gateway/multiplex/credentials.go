package multiplex

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	keychainService = "a2m-desktop"
	keychainAccount = "control-plane-api-key"
)

var (
	credMu sync.Mutex
)

// CredentialStatus represents the public state of the tunnel credentials
// without exposing sensitive token values.
type CredentialStatus struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source"` // "keychain", "environment", or "none"
}

// GetControlPlaneAPIKey securely retrieves the tunnel control plane API key.
// Precedence:
// 1. Environment variable CONTROL_PLANE_API_KEY
// 2. macOS Keychain (service "a2m-desktop", account "control-plane-api-key")
func GetControlPlaneAPIKey() (string, string, error) {
	credMu.Lock()
	defer credMu.Unlock()

	// 1. Check environment variable
	if envKey := strings.TrimSpace(os.Getenv("CONTROL_PLANE_API_KEY")); envKey != "" {
		return envKey, "environment", nil
	}

	// 2. Check local .env file
	if dotEnvKey := readKeyFromDotEnv(); dotEnvKey != "" {
		return dotEnvKey, "dotenv", nil
	}

	// 3. On macOS, query Keychain
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("security", "find-generic-password", "-s", keychainService, "-a", keychainAccount, "-w")
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil {
			key := strings.TrimSpace(out.String())
			if key != "" {
				return key, "keychain", nil
			}
		}
	}

	return "", "none", nil
}

// SaveControlPlaneAPIKey stores the API key in the macOS Keychain.
func SaveControlPlaneAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return fmt.Errorf("API key cannot be empty")
	}

	credMu.Lock()
	defer credMu.Unlock()

	if runtime.GOOS == "darwin" {
		cmd := exec.Command("security", "add-generic-password", "-U", "-s", keychainService, "-a", keychainAccount, "-w", apiKey)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to save credential to macOS Keychain: %w", err)
		}
		return nil
	}

	// On non-darwin (e.g. tests or Linux), fallback to process environment
	return os.Setenv("CONTROL_PLANE_API_KEY", apiKey)
}

// DeleteControlPlaneAPIKey removes the API key from the macOS Keychain.
func DeleteControlPlaneAPIKey() error {
	credMu.Lock()
	defer credMu.Unlock()

	if runtime.GOOS == "darwin" {
		cmd := exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", keychainAccount)
		_ = cmd.Run() // ignore error if already removed
	}

	_ = os.Unsetenv("CONTROL_PLANE_API_KEY")
	return nil
}

// FindTunnelClientBinary resolves the tunnel-client binary from known paths.
func FindTunnelClientBinary() (string, error) {
	if configured := os.Getenv("A2M_TUNNEL_CLIENT_PATH"); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return configured, nil
		}
	}

	candidates := []string{
		"/opt/homebrew/bin/tunnel-client",
		"/usr/local/bin/tunnel-client",
	}

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "tunnel-client"),
			filepath.Join(home, "go", "bin", "tunnel-client"),
		)
	}

	// Also check PATH
	if pathBin, err := exec.LookPath("tunnel-client"); err == nil {
		candidates = append([]string{pathBin}, candidates...)
	}

	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("tunnel-client binary not found in PATH or standard macOS locations (/opt/homebrew/bin/tunnel-client)")
}

// FindDesktopCommanderBinary resolves the desktop-commander / a2m CLI wrapper.
func FindDesktopCommanderBinary() (string, error) {
	if configured := os.Getenv("A2M_MCP_BINARY"); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return configured, nil
		}
	}

	// Check same directory as current running executable (e.g. inside A2M.app/Contents/MacOS)
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, name := range []string{"desktop-commander", "a2m", "a2m-gateway"} {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
				return candidate, nil
			}
		}
	}

	candidates := []string{}

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "desktop-commander"),
			filepath.Join(home, "go", "bin", "desktop-commander"),
		)
	}

	if pathBin, err := exec.LookPath("desktop-commander"); err == nil {
		candidates = append([]string{pathBin}, candidates...)
	}

	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("desktop-commander binary not found")
}

func readKeyFromDotEnv() string {
	candidates := []string{".env", "../../.env", "../../../.env"}
	if configured := strings.TrimSpace(os.Getenv("A2M_ENV_FILE")); configured != "" {
		candidates = append([]string{configured}, candidates...)
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".env"))
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "..", "Resources", ".env"))
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if strings.TrimSpace(parts[0]) == "CONTROL_PLANE_API_KEY" {
				val := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
				if val != "" {
					return val
				}
			}
		}
	}
	return ""
}
