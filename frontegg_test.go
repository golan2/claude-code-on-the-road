package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFronteggTokenStatus(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	jwt := func(exp int64) string {
		enc := base64.RawURLEncoding.EncodeToString
		return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp))) + ".sig"
	}
	valid := jwt(now.Add(3 * time.Hour).Unix())
	expired := jwt(now.Add(-2 * time.Hour).Unix())
	run := func(t *testing.T, content string) (string, error) {
		t.Helper()
		path := filepath.Join(t.TempDir(), ".frontegg")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return fronteggTokenStatus(path, now)
	}

	for name, content := range map[string]string{
		"when_bare_jwt_then_valid":      valid + "\n",
		"when_json_token_then_valid":    `{"token":"` + valid + `"}`,
		"when_bearer_header_then_valid": "Authorization: Bearer " + valid,
		"when_bearer_only_then_valid":   "Bearer " + valid,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := run(t, content)
			if err != nil || !strings.Contains(out, "3h0m0s") {
				t.Fatalf("out = %q, err = %v", out, err)
			}
		})
	}
	t.Run("when_token_expired_then_error_says_how_long_ago", func(t *testing.T) {
		if _, err := run(t, expired); err == nil || !strings.Contains(err.Error(), "expired 2h0m0s ago") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("when_file_empty_then_error", func(t *testing.T) {
		if _, err := run(t, "  \n"); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("when_not_a_jwt_then_error", func(t *testing.T) {
		if _, err := run(t, "garbage"); err == nil || !strings.Contains(err.Error(), "JWT") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("when_file_missing_then_error", func(t *testing.T) {
		if _, err := fronteggTokenStatus(filepath.Join(t.TempDir(), "none"), now); err == nil {
			t.Fatal("want error")
		}
	})
}
