package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEffectiveTimeout(t *testing.T) {
	const defaultMinutes = 10
	two := 2.0
	half := 0.5
	zero := 0.0
	negative := -1.0
	huge := 1e300
	tiny := 1e-12

	t.Run("when_absent_then_default_applies", func(t *testing.T) {
		got, err := effectiveTimeout(defaultMinutes, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != 10*time.Minute {
			t.Errorf("got %v, want 10m0s", got)
		}
	})

	t.Run("when_positive_then_overrides_default", func(t *testing.T) {
		got, err := effectiveTimeout(defaultMinutes, &two)
		if err != nil {
			t.Fatal(err)
		}
		if got != 2*time.Minute {
			t.Errorf("got %v, want 2m0s", got)
		}
	})

	t.Run("when_fractional_then_exact", func(t *testing.T) {
		got, err := effectiveTimeout(defaultMinutes, &half)
		if err != nil {
			t.Fatal(err)
		}
		if got != 30*time.Second {
			t.Errorf("got %v, want 30s", got)
		}
	})

	t.Run("when_zero_then_error", func(t *testing.T) {
		if _, err := effectiveTimeout(defaultMinutes, &zero); err == nil {
			t.Error("want error for zero timeout")
		}
	})

	t.Run("when_negative_then_error", func(t *testing.T) {
		if _, err := effectiveTimeout(defaultMinutes, &negative); err == nil {
			t.Error("want error for negative timeout")
		}
	})

	t.Run("when_too_large_for_duration_then_error", func(t *testing.T) {
		if _, err := effectiveTimeout(defaultMinutes, &huge); err == nil {
			t.Error("want error for timeout too large to represent")
		}
	})

	t.Run("when_too_small_for_duration_then_error", func(t *testing.T) {
		if _, err := effectiveTimeout(defaultMinutes, &tiny); err == nil {
			t.Error("want error for timeout that truncates to zero")
		}
	})
}

func TestTimeoutParsing(t *testing.T) {
	dir := t.TempDir()
	write := func(t *testing.T, name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("when_cc_request_has_timeout_then_parsed", func(t *testing.T) {
		payload, err := loadRequest(write(t, "cc.json", `{"prompt":"p","timeout":3}`))
		if err != nil {
			t.Fatal(err)
		}
		if payload.Timeout == nil || *payload.Timeout != 3 {
			t.Errorf("Timeout = %v, want 3", payload.Timeout)
		}
	})

	t.Run("when_cc_request_lacks_timeout_then_nil", func(t *testing.T) {
		payload, err := loadRequest(write(t, "cc_none.json", `{"prompt":"p"}`))
		if err != nil {
			t.Fatal(err)
		}
		if payload.Timeout != nil {
			t.Errorf("Timeout = %v, want nil", *payload.Timeout)
		}
	})

	t.Run("when_exec_request_has_fractional_timeout_then_parsed", func(t *testing.T) {
		payload, err := loadExecRequest(write(t, "exec.json", `{"command":"echo hi","timeout":0.5}`))
		if err != nil {
			t.Fatal(err)
		}
		if payload.Timeout == nil || *payload.Timeout != 0.5 {
			t.Errorf("Timeout = %v, want 0.5", payload.Timeout)
		}
	})

	t.Run("when_timeout_is_not_a_number_then_load_fails", func(t *testing.T) {
		if _, err := loadExecRequest(write(t, "exec_bad.json", `{"command":"echo hi","timeout":"ten"}`)); err == nil {
			t.Error("want error for a non-numeric timeout")
		}
	})
}

func TestTimeoutTag(t *testing.T) {
	const defaultMinutes = 10

	t.Run("when_equal_to_default_then_empty", func(t *testing.T) {
		if got := timeoutTag(10*time.Minute, defaultMinutes); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("when_shorter_than_default_then_empty", func(t *testing.T) {
		if got := timeoutTag(2*time.Minute, defaultMinutes); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("when_longer_than_default_then_tag_in_minutes", func(t *testing.T) {
		if got, want := timeoutTag(30*time.Minute, defaultMinutes), " [TO=30min]"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("when_longer_and_fractional_then_fraction_kept", func(t *testing.T) {
		if got, want := timeoutTag(90*time.Second+10*time.Minute, defaultMinutes), " [TO=11.5min]"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}
