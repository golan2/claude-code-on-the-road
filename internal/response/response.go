package response

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/golan2/claude-code-on-the-road/internal/claudecode"
	"github.com/golan2/claude-code-on-the-road/internal/config"
	"github.com/golan2/claude-code-on-the-road/internal/shellexec"
)

const (
	OutcomeSuccess         = "success"
	OutcomeClaudeCodeError = "claude_code_error"
	OutcomeTimeout         = "timeout"
)

type ErrorDetail struct {
	Message  string `json:"message"`
	ExitCode int    `json:"exitCode,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

type Response struct {
	Outcome      string          `json:"outcome"`
	SessionID    string          `json:"sessionId,omitempty"`
	ClaudeResult json.RawMessage `json:"claudeResult,omitempty"`
	Error        *ErrorDetail    `json:"error,omitempty"`
}

type claudeOutput struct {
	SessionID string `json:"session_id"`
}

func Build(result *claudecode.InvokeResult) *Response {
	if result.TimedOut {
		return &Response{
			Outcome: OutcomeTimeout,
			Error: &ErrorDetail{
				Message: "claude process exceeded the configured timeout and was killed",
			},
		}
	}

	var output claudeOutput
	parseErr := json.Unmarshal([]byte(result.Stdout), &output)
	if result.ExitCode == 0 && parseErr == nil {
		return &Response{
			Outcome:      OutcomeSuccess,
			SessionID:    output.SessionID,
			ClaudeResult: json.RawMessage(result.Stdout),
		}
	}

	message := fmt.Sprintf("claude exited with code %d", result.ExitCode)
	if parseErr != nil && result.ExitCode == 0 {
		message = "claude produced invalid JSON output"
	}

	resp := &Response{
		Outcome: OutcomeClaudeCodeError,
		Error: &ErrorDetail{
			Message:  message,
			ExitCode: result.ExitCode,
			Stderr:   result.Stderr,
		},
	}
	if parseErr == nil {
		resp.SessionID = output.SessionID
		resp.ClaudeResult = json.RawMessage(result.Stdout)
	}
	return resp
}

type ExecResponse struct {
	ExitCode  int    `json:"exitCode"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"` // present only when the command could not be launched, or timed out
}

func BuildExec(result *shellexec.InvokeResult, maxOutputChars int) *ExecResponse {
	resp := &ExecResponse{
		ExitCode:  result.ExitCode,
		Output:    result.Output,
		Truncated: false,
	}

	if result.TimedOut {
		resp.Error = "command exceeded the configured timeout and was killed"
	}

	runes := []rune(resp.Output)
	if len(runes) > maxOutputChars {
		resp.Output = string(runes[:maxOutputChars])
		resp.Truncated = true
	}

	return resp
}

// ConfigResponse is the NNNNN_config_response.json payload. Key always
// echoes back the key from the matching config-request, so the response is
// self-describing without needing to cross-reference that file. Exactly one
// of Value or Error is set: Value on success, Error when the requested key
// is unknown or not present in config.json.
type ConfigResponse struct {
	Key   string `json:"key"`
	Value any    `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

// BuildConfig resolves a config-request's key against cfg by reflecting over
// cfg's fields and matching key against each field's JSON tag name — any
// field in config.Config is queryable this way, with no per-key code. It
// fails fast with a ConfigResponse.Error (never partial data or a retry) when
// key doesn't match any field's JSON tag, or when the matched field was left
// at its zero value (i.e. not present in config.json).
func BuildConfig(cfg *config.Config, key string) *ConfigResponse {
	v := reflect.ValueOf(cfg).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" || name != key {
			continue
		}
		fv := v.Field(i)
		if fv.IsZero() {
			return &ConfigResponse{Key: key, Error: fmt.Sprintf("config key %q is not present in config.json", key)}
		}
		return &ConfigResponse{Key: key, Value: fv.Interface()}
	}
	return &ConfigResponse{Key: key, Error: fmt.Sprintf("unknown config key %q", key)}
}

func Write(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write response to %q: %w", path, err)
	}
	return nil
}
