package response

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/golan2/claude-code-on-the-road/internal/claudecode"
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

func Write(path string, resp *Response) error {
	data, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write response to %q: %w", path, err)
	}
	return nil
}
