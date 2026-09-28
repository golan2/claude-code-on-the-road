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
	defaultLogFile               = "goapp.log"
	defaultExecAckDelaySeconds   = 5
)

// SimpleRepository is one entry of the simpleRepositories config list,
// returned verbatim as the value of a "simple_repositories" config-request.
type SimpleRepository struct {
	Nickname string `json:"nickname"`
	Path     string `json:"path"`
}

// Config holds the application configuration loaded from a JSON file.
//
//   - WatchFolder: absolute path to the Drive-synced root folder GoApp polls for session folders.
//   - PollIntervalSeconds: how often, in seconds, GoApp scans for new requests.
//   - TimeoutMinutes: how long a Claude Code or exec-request subprocess may run before being killed.
//   - PermissionMode: the default Claude Code permission mode used when a request doesn't specify one.
//   - MaxConcurrentSessions: the maximum number of Claude Code invocations allowed to run at once.
//   - ExecOutputMaxChars: the cap, in characters, on exec-response output before it is truncated.
//   - SimpleRepositories: backs the "simple_repositories" config-request key; left nil (not defaulted
//     to an empty slice) when absent from the config file, so "never configured" can be told apart
//     from "configured but empty".
//   - Acronyms: backs the "acronyms" config-request key; independent of SimpleRepositories, left nil
//     (not defaulted to an empty map) when absent from the config file for the same reason.
//   - LogFile: path (relative to the working directory GoApp is run from, same convention as the
//     config file path itself) of the append-only log file GoApp writes every event to, in addition
//     to stdout. Defaults to "goapp.log" when absent from the config file.
//   - ExecAckDelaySeconds: how long, in seconds, an exec-request's command must still be running
//     before GoApp writes its NNNNN_ack.json. A command that finishes within this window gets no
//     ack at all — its NNNNN_exec_response.json is the only signal PC needs, since it arrives just
//     as fast. Defaults to 5 when absent from the config file.
type Config struct {
	WatchFolder           string             `json:"watchFolder"`
	PollIntervalSeconds   int                `json:"pollIntervalSeconds"`
	TimeoutMinutes        int                `json:"timeoutMinutes"`
	PermissionMode        string             `json:"permissionMode"`
	MaxConcurrentSessions int                `json:"maxConcurrentSessions"`
	ExecOutputMaxChars    int                `json:"execOutputMaxChars"`
	SimpleRepositories    []SimpleRepository `json:"simpleRepositories"`
	Acronyms              map[string]string  `json:"acronyms"`
	LogFile               string             `json:"logFile"`
	ExecAckDelaySeconds   int                `json:"execAckDelaySeconds"`
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
	if cfg.LogFile == "" {
		cfg.LogFile = defaultLogFile
	}
	if cfg.ExecAckDelaySeconds == 0 {
		cfg.ExecAckDelaySeconds = defaultExecAckDelaySeconds
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
