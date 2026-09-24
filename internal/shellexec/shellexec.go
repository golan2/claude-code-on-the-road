package shellexec

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// InvokeParams holds the parameters for invoking a shell command.
type InvokeParams struct {
	Command    string
	Workdir    string
	Timeout    time.Duration
	OnLaunched func() // called synchronously the moment cmd.Start() succeeds
}

// InvokeResult holds the outcome of a shell command invocation.
type InvokeResult struct {
	Output   string // merged stdout+stderr, in production order
	ExitCode int    // -1 if TimedOut
	TimedOut bool
}

// Invoke runs a command through a shell subprocess and captures its combined
// stdout and stderr. It enforces a hard wall-clock timeout via context.WithTimeout,
// and on timeout it kills the entire process group to avoid orphaned children.
//
// A non-zero exit code from the subprocess is NOT returned as a Go error — the
// caller decides what exit codes mean. A non-nil error is only returned when the
// process cannot be started at all (e.g. shell not found, invalid working directory).
func Invoke(params InvokeParams) (*InvokeResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), params.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", params.Command)
	cmd.Dir = params.Workdir

	// Put the child in its own process group so we can kill the whole tree on
	// timeout (the shell may spawn subprocesses for pipelines, globs, etc.).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf // same writer value: os/exec synchronizes writes safely

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("shellexec: failed to start shell command: %w", err)
	}

	if params.OnLaunched != nil {
		params.OnLaunched()
	}

	runErr := cmd.Wait()

	if ctx.Err() == context.DeadlineExceeded {
		killProcessGroup(cmd)
		return &InvokeResult{
			Output:   buf.String(),
			ExitCode: -1,
			TimedOut: true,
		}, nil
	}

	if runErr != nil {
		// The process ran but exited non-zero. This is a normal outcome, not a
		// Go error — extract the real exit code.
		return &InvokeResult{
			Output:   buf.String(),
			ExitCode: cmd.ProcessState.ExitCode(),
			TimedOut: false,
		}, nil
	}

	return &InvokeResult{
		Output:   buf.String(),
		ExitCode: 0,
		TimedOut: false,
	}, nil
}

// killProcessGroup sends SIGKILL to the entire process group of cmd to ensure
// no orphaned child processes survive after a timeout.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Negative pid targets the whole process group.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
