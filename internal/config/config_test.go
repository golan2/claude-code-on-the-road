package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes a config file that passes validation, with extra keys
// merged in, and returns its path.
func writeConfig(t *testing.T, extra map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	claude := filepath.Join(dir, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"watchFolder":   dir,
		"skillsCopyDir": filepath.Join(dir, "skills"),
		"claudePath":    claude,
	}
	for k, v := range extra {
		body[k] = v
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadTimeout(t *testing.T) {
	t.Run("when_only_timeoutMinutes_then_falls_back_and_warns", func(t *testing.T) {
		cfg, err := Load(writeConfig(t, map[string]any{"timeoutMinutes": 7}))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DefaultTimeoutMinutes != 7 {
			t.Errorf("DefaultTimeoutMinutes = %d, want 7", cfg.DefaultTimeoutMinutes)
		}
		if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "deprecated") {
			t.Errorf("Warnings = %q, want one deprecation warning", cfg.Warnings)
		}
	})

	t.Run("when_defaultTimeout_only_then_used_without_warning", func(t *testing.T) {
		cfg, err := Load(writeConfig(t, map[string]any{"defaultTimeout": 5}))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DefaultTimeoutMinutes != 5 {
			t.Errorf("DefaultTimeoutMinutes = %d, want 5", cfg.DefaultTimeoutMinutes)
		}
		if len(cfg.Warnings) != 0 {
			t.Errorf("Warnings = %q, want none", cfg.Warnings)
		}
	})

	t.Run("when_neither_key_then_default_is_10", func(t *testing.T) {
		cfg, err := Load(writeConfig(t, nil))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DefaultTimeoutMinutes != 10 {
			t.Errorf("DefaultTimeoutMinutes = %d, want 10", cfg.DefaultTimeoutMinutes)
		}
	})

	t.Run("when_both_keys_then_defaultTimeout_wins_and_warns", func(t *testing.T) {
		cfg, err := Load(writeConfig(t, map[string]any{"defaultTimeout": 5, "timeoutMinutes": 7}))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DefaultTimeoutMinutes != 5 {
			t.Errorf("DefaultTimeoutMinutes = %d, want 5", cfg.DefaultTimeoutMinutes)
		}
		if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "ignored") {
			t.Errorf("Warnings = %q, want one ignored-key warning", cfg.Warnings)
		}
	})

	t.Run("when_defaultTimeout_negative_then_load_fails", func(t *testing.T) {
		if _, err := Load(writeConfig(t, map[string]any{"defaultTimeout": -1})); err == nil {
			t.Error("want error for negative defaultTimeout")
		}
	})
}
