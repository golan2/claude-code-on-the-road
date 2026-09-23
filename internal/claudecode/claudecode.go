package claudecode

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// InvokeParams holds the parameters for invoking the claude CLI.
type InvokeParams struct {
	Prompt          string
	Workdir         string        // subprocess working directory (cwd)
	PermissionMode  string        // passed as --permission-mode
	AddDir          string        // passed as --add-dir
	ResumeSessionID string        // if non-empty, pass --resume <value>; if empty, brand-new session
	Timeout         time.Duration // hard wall-clock limit for the subprocess
}

// InvokeResult holds the outcome of a claude CLI invocation.
type InvokeResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
}

// Invoke runs the external claude CLI binary as a subprocess and captures its
// output. It enforces a hard wall-clock timeout via context.WithTimeout, and
// on timeout it kills the entire process group to avoid orphaned children.
//
// A non-zero exit code from the subprocess is NOT returned as a Go error —
// the caller decides what exit codes mean. A non-nil error is only returned
// when the process cannot be started at all (e.g. binary not found, invalid
// working directory).
func Invoke(params InvokeParams) (*InvokeResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), params.Timeout)
	defer cancel()

	args := buildArgs(params)

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = params.Workdir

	// Put the child in its own process group so we can kill the whole tree on
	// timeout (claude may spawn subprocesses like bash tool calls).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claudecode: failed to start claude process: %w", err)
	}

	runErr := cmd.Wait()

	if ctx.Err() == context.DeadlineExceeded {
		killProcessGroup(cmd)
		return &InvokeResult{
			Stdout:   stdoutBuf.String(),
			Stderr:   stderrBuf.String(),
			ExitCode: -1,
			TimedOut: true,
		}, nil
	}

	if runErr != nil {
		// The process ran but exited non-zero. This is a normal outcome, not a
		// Go error — extract the real exit code.
		return &InvokeResult{
			Stdout:   stdoutBuf.String(),
			Stderr:   stderrBuf.String(),
			ExitCode: cmd.ProcessState.ExitCode(),
			TimedOut: false,
		}, nil
	}

	return &InvokeResult{
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: 0,
		TimedOut: false,
	}, nil
}

// buildArgs constructs the CLI argument slice from the given params.
func buildArgs(params InvokeParams) []string {
	args := []string{
		"-p", params.Prompt,
		"--output-format", "json",
		"--permission-mode", params.PermissionMode,
		"--add-dir", params.AddDir,
	}
	if params.ResumeSessionID != "" {
		args = append(args, "--resume", params.ResumeSessionID)
	}
	return args
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
