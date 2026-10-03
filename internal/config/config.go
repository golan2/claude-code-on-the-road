package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultPollIntervalSeconds       = 5
	defaultTimeoutMinutes            = 10
	defaultPermissionMode            = "bypassPermissions"
	defaultMaxConcurrentSessions     = 10
	defaultExecOutputMaxChars        = 30000
	defaultLogFile                   = "goapp.log"
	defaultExecAckDelaySeconds       = 5
	defaultSkillsCopyIntervalSeconds = 300
)

// SimpleRepository is one entry of the simpleRepositories config list,
// returned verbatim as the value of a "simpleRepositories" config-request.
type SimpleRepository struct {
	Nickname string `json:"nickname"`
	Path     string `json:"path"`
}

// Config holds the application configuration loaded from a JSON file. Every
// field is queryable via a config-request whose key matches the field's JSON
// tag name (response.BuildConfig resolves this generically by reflection —
// adding a field here is enough to make it queryable, no other code needed);
// a request for a field left at its zero value fails with "not present" the
// same way a request for an unrecognized key fails with "unknown config key".
//
//   - WatchFolder: absolute path to the Drive-synced root folder GoApp polls for session folders.
//   - PollIntervalSeconds: how often, in seconds, GoApp scans for new requests.
//   - TimeoutMinutes: how long a Claude Code or exec-request subprocess may run before being killed.
//   - PermissionMode: the default Claude Code permission mode used when a request doesn't specify one.
//   - MaxConcurrentSessions: the maximum number of Claude Code invocations allowed to run at once.
//   - ExecOutputMaxChars: the cap, in characters, on exec-response output before it is truncated.
//   - SimpleRepositories: backs the "simpleRepositories" config-request key; left nil (not defaulted
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
//   - SkillPaths: a manually-curated list of root directories to copy skills from. Each entry may
//     start with "~/" for the user's home directory. Left nil (not defaulted to any path) when absent
//     from the config file — skill copying is simply skipped.
//   - SkillsCopyDir: the shared directory GoApp copies every discovered skill into, with
//     disable-model-invocation stripped in each copy, hierarchy preserved per SkillPaths root. Passed
//     to every Claude Code invocation as an extra --add-dir. No code-level default: it must be set in
//     the config file and must already exist on disk, or Load fails fast at startup. Also backs the
//     "skillsCopyDir" config-request key.
//   - SkillsCopyIntervalSeconds: how often, in seconds, GoApp re-scans SkillPaths for changed or
//     deleted skill files and re-copies/cleans up accordingly. Defaults to 300 (5 minutes).
type Config struct {
	WatchFolder               string             `json:"watchFolder"`
	PollIntervalSeconds       int                `json:"pollIntervalSeconds"`
	TimeoutMinutes            int                `json:"timeoutMinutes"`
	PermissionMode            string             `json:"permissionMode"`
	MaxConcurrentSessions     int                `json:"maxConcurrentSessions"`
	ExecOutputMaxChars        int                `json:"execOutputMaxChars"`
	SimpleRepositories        []SimpleRepository `json:"simpleRepositories"`
	Acronyms                  map[string]string  `json:"acronyms"`
	LogFile                   string             `json:"logFile"`
	ExecAckDelaySeconds       int                `json:"execAckDelaySeconds"`
	SkillPaths                []string           `json:"skillPaths"`
	SkillsCopyDir             string             `json:"skillsCopyDir"`
	SkillsCopyIntervalSeconds int                `json:"skillsCopyIntervalSeconds"`
}

// Load reads the JSON configuration at path, applies defaults, and validates
// it. A key in the file with no matching Config field fails Load outright
// (naming the offending key) rather than being silently ignored, since a
// typo'd or stale key would otherwise have no effect with no indication why.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %q: %w", path, err)
	}

	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
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
	cfg.SkillsCopyDir = expandHome(cfg.SkillsCopyDir)
	if cfg.SkillsCopyIntervalSeconds == 0 {
		cfg.SkillsCopyIntervalSeconds = defaultSkillsCopyIntervalSeconds
	}
	for i, p := range cfg.SkillPaths {
		cfg.SkillPaths[i] = expandHome(p)
	}
}

// expandHome replaces a leading "~" or "~/..." in path with the current
// user's home directory. Paths not starting with "~" are returned unchanged.
// If the home directory can't be determined, path is returned as-is rather
// than failing config load over it.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/"))
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

	if cfg.SkillsCopyDir == "" {
		return fmt.Errorf("skillsCopyDir is required")
	}

	skillsInfo, err := os.Stat(cfg.SkillsCopyDir)
	if err != nil {
		return fmt.Errorf("skillsCopyDir %q does not exist: %w", cfg.SkillsCopyDir, err)
	}
	if !skillsInfo.IsDir() {
		return fmt.Errorf("skillsCopyDir %q is not a directory", cfg.SkillsCopyDir)
	}

	return nil
}
