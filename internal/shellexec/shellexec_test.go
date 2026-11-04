package shellexec

import (
	"testing"
	"time"
)

func TestShellExec_Invoke(t *testing.T) {
	const timeout = 100 * time.Millisecond
	const waitDelay = 50 * time.Millisecond
	t.Run("when_grandchild_holds_stdout_then_timeout_returns_promptly", func(t *testing.T) {
		start := time.Now()
		result, err := Invoke(InvokeParams{Command: "sleep 10 & wait", Timeout: timeout, WaitDelay: waitDelay})
		if err != nil {
			t.Fatal(err)
		}
		if !result.TimedOut || result.ExitCode != -1 || time.Since(start) > 2*time.Second {
			t.Fatalf("result = %+v, elapsed = %s", result, time.Since(start))
		}
	})
	t.Run("when_command_fails_then_exit_code_and_output_preserved", func(t *testing.T) {
		result, err := Invoke(InvokeParams{Command: "printf failed; exit 4", Timeout: time.Second, WaitDelay: waitDelay})
		if err != nil {
			t.Fatal(err)
		}
		if result.ExitCode != 4 || result.Output != "failed" || result.TimedOut {
			t.Fatalf("result = %+v", result)
		}
	})
}
