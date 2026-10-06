package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"sync"
	"syscall"
	"time"
)

// InvokeParams holds the parameters for invoking the claude CLI.
type InvokeParams struct {
	ClaudePath      string // absolute path of the claude binary to spawn
	Prompt          string
	Workdir         string           // subprocess working directory (cwd)
	PermissionMode  string           // passed as --permission-mode
	AddDirs         []string         // each passed as its own --add-dir
	ResumeSessionID string           // if non-empty, pass --resume <value>; if empty, brand-new session
	Timeout         time.Duration    // hard wall-clock limit for the subprocess
	Progress        *ProgressTracker // optional; updated live as the subprocess streams events
	OnLaunched      func()           // optional; called synchronously the moment cmd.Start() succeeds
}

// InvokeResult holds the outcome of a claude CLI invocation.
type InvokeResult struct {
	Stdout   string // the final "result" event, shaped like --output-format json's single object
	Stderr   string
	ExitCode int
	TimedOut bool
}

// Progress is a point-in-time snapshot of an in-flight invocation.
type Progress struct {
	ElapsedSeconds float64 `json:"elapsedSeconds"`
	LastToolCall   string  `json:"lastToolCall,omitempty"`
	PartialOutput  string  `json:"partialOutput,omitempty"`
}

// ProgressTracker accumulates progress from a streaming claude invocation.
// Snapshot is safe to call concurrently with the in-flight Invoke call that
// owns the tracker, so a caller can poll it on a timer to write status files.
type ProgressTracker struct {
	mu            sync.Mutex
	startedAt     time.Time
	lastToolCall  string
	partialOutput string
	invokedSkills map[string]bool
}

// NewProgressTracker starts a tracker with its clock running from now.
func NewProgressTracker() *ProgressTracker {
	return &ProgressTracker{startedAt: time.Now()}
}

// Snapshot returns the current progress.
func (t *ProgressTracker) Snapshot() Progress {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Progress{
		ElapsedSeconds: time.Since(t.startedAt).Seconds(),
		LastToolCall:   t.lastToolCall,
		PartialOutput:  t.partialOutput,
	}
}

func (t *ProgressTracker) recordToolCall(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastToolCall = name
}

func (t *ProgressTracker) appendText(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.partialOutput += text
}

func (t *ProgressTracker) recordSkillInvocation(name string) {
	if name == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.invokedSkills == nil {
		t.invokedSkills = make(map[string]bool)
	}
	t.invokedSkills[name] = true
}

// InvokedSkills returns the names of every skill the Skill tool was actually
// called with during this invocation, gathered live from the "skill" field of
// each Skill tool_use event as it streamed by — not from the prompt text sent
// to claude, so it reflects what genuinely happened rather than what was
// merely requested. Order is not meaningful. Safe to call once the
// invocation has finished; the underlying map is no longer written to at
// that point.
func (t *ProgressTracker) InvokedSkills() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	names := make([]string, 0, len(t.invokedSkills))
	for name := range t.invokedSkills {
		names = append(names, name)
	}
	return names
}

// streamEvent is the subset of the claude CLI's --output-format stream-json
// event shape needed to track tool calls, assistant text, and which skills
// were actually invoked (via the Skill tool's own "skill" input field).
type streamEvent struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Name  string `json:"name"`
			Input struct {
				Skill string `json:"skill"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// Invoke runs the external claude CLI binary as a subprocess and captures its
// output. It enforces a hard wall-clock timeout via context.WithTimeout, and
// on timeout it kills the entire process group to avoid orphaned children.
//
// Stdout is read as --output-format stream-json (newline-delimited events) so
// that, when params.Progress is set, the tracker can be updated live with the
// most recent tool call and partial assistant text while the subprocess is
// still running. The final "result" event is kept as InvokeResult.Stdout,
// which is shaped like a single --output-format json object, so downstream
// parsing (session ID, etc.) is unchanged.
//
// A non-zero exit code from the subprocess is NOT returned as a Go error —
// the caller decides what exit codes mean. A non-nil error is only returned
// when the process cannot be started at all (e.g. binary not found, invalid
// working directory).
func Invoke(params InvokeParams) (*InvokeResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), params.Timeout)
	defer cancel()

	args := buildArgs(params)

	cmd := exec.CommandContext(ctx, params.ClaudePath, args...)
	cmd.Dir = params.Workdir
	cmd.Env = claudeEnv()

	// Put the child in its own process group so we can kill the whole tree on
	// timeout (claude may spawn subprocesses like bash tool calls).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claudecode: failed to attach stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claudecode: failed to start claude process: %w", err)
	}

	if params.OnLaunched != nil {
		params.OnLaunched()
	}

	var finalLine string
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			finalLine = line
			handleStreamLine(line, params.Progress)
		}
	}()

	runErr := cmd.Wait()
	<-scanDone // drain the scanner goroutine so finalLine is fully populated

	if ctx.Err() == context.DeadlineExceeded {
		killProcessGroup(cmd)
		return &InvokeResult{
			Stdout:   finalLine,
			Stderr:   stderrBuf.String(),
			ExitCode: -1,
			TimedOut: true,
		}, nil
	}

	if runErr != nil {
		// The process ran but exited non-zero. This is a normal outcome, not a
		// Go error — extract the real exit code.
		return &InvokeResult{
			Stdout:   finalLine,
			Stderr:   stderrBuf.String(),
			ExitCode: cmd.ProcessState.ExitCode(),
			TimedOut: false,
		}, nil
	}

	return &InvokeResult{
		Stdout:   finalLine,
		Stderr:   stderrBuf.String(),
		ExitCode: 0,
		TimedOut: false,
	}, nil
}

// handleStreamLine parses a single stream-json event line and, when progress
// tracking is enabled, records any new tool call or assistant text delta.
func handleStreamLine(line string, progress *ProgressTracker) {
	if progress == nil {
		return
	}
	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}
	if ev.Type != "assistant" {
		return
	}
	for _, block := range ev.Message.Content {
		switch block.Type {
		case "tool_use":
			progress.recordToolCall(block.Name)
			if block.Name == "Skill" {
				progress.recordSkillInvocation(block.Input.Skill)
			}
		case "text":
			progress.appendText(block.Text)
		}
	}
}

// claudeEnv returns the subprocess environment: GoApp's own, with USER forced
// to the real OS account name. On macOS Claude Code looks up its login
// credential in the keychain under the account named by $USER, so a GoApp
// launched from an environment where USER differs from the real account
// (e.g. USER=izik inherited from an IDE while the keychain item belongs to
// izikgolan) gets "Not logged in · Please run /login" from every spawned
// claude. exec.Cmd uses the last value when a key is repeated.
func claudeEnv() []string {
	env := os.Environ()
	if u, err := user.Current(); err == nil && u.Username != "" {
		env = append(env, "USER="+u.Username)
	}
	return env
}

// buildArgs constructs the CLI argument slice from the given params.
func buildArgs(params InvokeParams) []string {
	args := []string{
		"-p", params.Prompt,
		"--output-format", "stream-json",
		"--verbose", // required by the claude CLI when --print is combined with stream-json
		"--permission-mode", params.PermissionMode,
	}
	for _, dir := range params.AddDirs {
		args = append(args, "--add-dir", dir)
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
