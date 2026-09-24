package config

import (
	"encoding/json"
	"fmt"
	"os"
)

const (
	defaultPollIntervalSeconds   = 5
	defaultTimeoutMinutes        = 10
	defaultPermissionMode        = "bypassPermissions"
	defaultMaxConcurrentSessions = 10
	defaultExecOutputMaxChars    = 30000
)

// Config holds the application configuration loaded from a JSON file.
type Config struct {
	WatchFolder           string `json:"watchFolder"`
	PollIntervalSeconds   int    `json:"pollIntervalSeconds"`
	TimeoutMinutes        int    `json:"timeoutMinutes"`
	PermissionMode        string `json:"permissionMode"`
	MaxConcurrentSessions int    `json:"maxConcurrentSessions"`
	ExecOutputMaxChars    int    `json:"execOutputMaxChars"`
}

// Load reads the JSON configuration at path, applies defaults, and validates it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %q: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %q: %w", path, err)
	}

	applyDefaults(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config in %q: %w", path, err)
	}

	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.PollIntervalSeconds == 0 {
		cfg.PollIntervalSeconds = defaultPollIntervalSeconds
	}
	if cfg.TimeoutMinutes == 0 {
		cfg.TimeoutMinutes = defaultTimeoutMinutes
	}
	if cfg.PermissionMode == "" {
		cfg.PermissionMode = defaultPermissionMode
	}
	if cfg.MaxConcurrentSessions == 0 {
		cfg.MaxConcurrentSessions = defaultMaxConcurrentSessions
	}
	if cfg.ExecOutputMaxChars == 0 {
		cfg.ExecOutputMaxChars = defaultExecOutputMaxChars
	}
}

func validate(cfg *Config) error {
	if cfg.WatchFolder == "" {
		return fmt.Errorf("watchFolder is required")
	}

	info, err := os.Stat(cfg.WatchFolder)
	if err != nil {
		return fmt.Errorf("watchFolder %q does not exist: %w", cfg.WatchFolder, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("watchFolder %q is not a directory", cfg.WatchFolder)
	}

	return nil
}
