package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type ServerConfig struct {
	AllowedDirectories   []string `json:"allowedDirectories"`
	CapabilityWorkspace  string   `json:"capabilityWorkspace,omitempty"`
	CapabilityWorkspaces []string `json:"capabilityWorkspaces,omitempty"`
	BlockedCommands      []string `json:"blockedCommands"`
	CommandTimeoutMs     int      `json:"commandTimeoutMs"`
	FileReadLineLimit    int      `json:"fileReadLineLimit"`
	MaxSearchResults     int      `json:"maxSearchResults"`
	Shell                string   `json:"shell"`
}

type ConfigManager struct {
	config     ServerConfig
	configPath string
	mu         sync.RWMutex
}

func NewConfigManager(customPath string) *ConfigManager {
	if customPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			homeDir = "."
		}
		customPath = filepath.Join(homeDir, ".a2m", "config.json")
	}

	cm := &ConfigManager{
		configPath: customPath,
		config: ServerConfig{
			AllowedDirectories: []string{},
			BlockedCommands: []string{
				"rm -rf /",
				":(){ :|:& };:",
			},
			CommandTimeoutMs:  30000,
			FileReadLineLimit: 1000,
			MaxSearchResults:  1000,
			Shell:             "",
		},
	}

	_ = cm.load()
	return cm
}

func (cm *ConfigManager) load() error {
	data, err := os.ReadFile(cm.configPath)
	if err == nil {
		_ = json.Unmarshal(data, &cm.config)
	}
	if envAllowed := os.Getenv("A2M_ALLOWED_DIRECTORIES"); envAllowed != "" {
		existing := make(map[string]bool)
		for _, dir := range cm.config.AllowedDirectories {
			existing[filepath.Clean(dir)] = true
		}
		for _, part := range strings.Split(envAllowed, ":") {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" && !existing[filepath.Clean(trimmed)] {
				cm.config.AllowedDirectories = append(cm.config.AllowedDirectories, trimmed)
				existing[filepath.Clean(trimmed)] = true
			}
		}
	}
	return err
}

func (cm *ConfigManager) Save() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	dir := filepath.Dir(cm.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cm.config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(cm.configPath, data, 0644)
}

func (cm *ConfigManager) GetConfig() ServerConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.config
}

func (cm *ConfigManager) SetValue(key string, val interface{}) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	switch key {
	case "commandTimeoutMs":
		if v, ok := val.(float64); ok {
			cm.config.CommandTimeoutMs = int(v)
		} else if v, ok := val.(int); ok {
			cm.config.CommandTimeoutMs = v
		} else {
			return fmt.Errorf("invalid type for commandTimeoutMs")
		}
	case "fileReadLineLimit":
		if v, ok := val.(float64); ok {
			cm.config.FileReadLineLimit = int(v)
		} else if v, ok := val.(int); ok {
			cm.config.FileReadLineLimit = v
		} else {
			return fmt.Errorf("invalid type for fileReadLineLimit")
		}
	case "maxSearchResults":
		if v, ok := val.(float64); ok {
			cm.config.MaxSearchResults = int(v)
		} else if v, ok := val.(int); ok {
			cm.config.MaxSearchResults = v
		} else {
			return fmt.Errorf("invalid type for maxSearchResults")
		}
	case "shell":
		if s, ok := val.(string); ok {
			cm.config.Shell = s
		} else {
			return fmt.Errorf("invalid type for shell")
		}
	case "blockedCommands":
		if arr, ok := val.([]interface{}); ok {
			var res []string
			for _, item := range arr {
				if s, ok := item.(string); ok {
					res = append(res, s)
				}
			}
			cm.config.BlockedCommands = res
		} else if arr, ok := val.([]string); ok {
			cm.config.BlockedCommands = arr
		} else {
			return fmt.Errorf("invalid type for blockedCommands")
		}
	case "capabilityWorkspace":
		workspace, ok := val.(string)
		if !ok {
			return fmt.Errorf("invalid type for capabilityWorkspace")
		}
		if workspace != "" {
			canonical, err := filepath.EvalSymlinks(workspace)
			if err != nil {
				return fmt.Errorf("capabilityWorkspace must be an existing directory: %w", err)
			}
			info, err := os.Stat(canonical)
			if err != nil || !info.IsDir() {
				return fmt.Errorf("capabilityWorkspace must be a directory")
			}
			workspace = canonical
		}
		cm.config.CapabilityWorkspace = workspace
	case "capabilityWorkspaces":
		var requested []string
		switch typed := val.(type) {
		case []string:
			requested = typed
		case []interface{}:
			for _, item := range typed {
				s, ok := item.(string)
				if !ok {
					return fmt.Errorf("capabilityWorkspaces entries must be strings")
				}
				requested = append(requested, s)
			}
		default:
			return fmt.Errorf("invalid type for capabilityWorkspaces")
		}
		if len(requested) > 16 {
			return fmt.Errorf("capabilityWorkspaces exceeds 16 approved roots")
		}
		canonicalRoots := make([]string, 0, len(requested))
		seen := make(map[string]bool, len(requested))
		for _, requestedRoot := range requested {
			if !filepath.IsAbs(requestedRoot) {
				return fmt.Errorf("capabilityWorkspaces entries must be absolute")
			}
			canonical, err := filepath.EvalSymlinks(requestedRoot)
			if err != nil {
				return fmt.Errorf("capabilityWorkspaces root unavailable: %w", err)
			}
			info, err := os.Stat(canonical)
			if err != nil || !info.IsDir() || seen[canonical] {
				return fmt.Errorf("capabilityWorkspaces requires unique existing directories")
			}
			seen[canonical] = true
			canonicalRoots = append(canonicalRoots, canonical)
		}
		cm.config.CapabilityWorkspaces = canonicalRoots
	case "allowedDirectories":
		if arr, ok := val.([]interface{}); ok {
			var res []string
			for _, item := range arr {
				if s, ok := item.(string); ok {
					res = append(res, s)
				}
			}
			cm.config.AllowedDirectories = res
		} else if arr, ok := val.([]string); ok {
			cm.config.AllowedDirectories = arr
		} else {
			return fmt.Errorf("invalid type for allowedDirectories")
		}
	default:
		return fmt.Errorf("unknown configuration key: %s", key)
	}

	return nil
}
