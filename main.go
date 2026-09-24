package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/golan2/claude-code-on-the-road/internal/claudecode"
	"github.com/golan2/claude-code-on-the-road/internal/config"
	"github.com/golan2/claude-code-on-the-road/internal/response"
	"github.com/golan2/claude-code-on-the-road/internal/session"
)

type requestPayload struct {
	Prompt         string `json:"prompt"`
	Workdir        string `json:"workdir,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
}

// inFlightSessions keeps track of all Claude Code invocations that are in progress and awaiting a response.
// The mutex prevents having 2 Claude Code invocations in flight for the same folder at once, which would
// cause confusion when responses come back.
type inFlightSessions struct {
	mu         sync.Mutex
	processing map[string]bool // a set of all in flight sessions folder full-paths
}

func main() {
	cfg, err := config.Load("config.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	inFlight := &inFlightSessions{processing: make(map[string]bool)}
	sem := make(chan struct{}, cfg.MaxConcurrentSessions)
	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		pollOnce(cfg, inFlight, sem)
	}
}

func pollOnce(cfg *config.Config, inFlight *inFlightSessions, sem chan struct{}) {
	folders, err := session.DiscoverSessionFolders(cfg.WatchFolder)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	for _, folder := range folders {
		if !inFlight.tryMark(folder) {
			continue
		}

		ordinal, requestFilePath, found, err := session.NextPendingRequest(folder)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			inFlight.unmark(folder)
			continue
		}
		if !found {
			inFlight.unmark(folder)
			continue
		}

		go func(folder, ordinal, requestFilePath string) {
			sem <- struct{}{}
			defer func() {
				<-sem
				inFlight.unmark(folder)
			}()
			processRequest(cfg, folder, ordinal, requestFilePath)
		}(folder, ordinal, requestFilePath)
	}
}

func (s *inFlightSessions) tryMark(folder string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.processing[folder] {
		return false
	}
	s.processing[folder] = true
	return true
}

func (s *inFlightSessions) unmark(folder string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.processing, folder)
}

func processRequest(cfg *config.Config, folder, ordinal, requestFilePath string) {
	sessionName := filepath.Base(folder)
	fmt.Printf("Request file [%s] for session [%s] sent to Claude Code\n", ordinal, sessionName)

	payload, err := loadRequest(requestFilePath)
	if err != nil {
		writeRequestError(requestFilePath, ordinal, sessionName, err.Error())
		return
	}

	conf, err := session.LoadSessionConf(folder)
	if err != nil {
		writeRequestError(requestFilePath, ordinal, sessionName, fmt.Sprintf("invalid __session_conf.json: %v", err))
		return
	}

	isNewSession := conf == nil
	var workdir, permissionMode, resumeSessionID string
	if isNewSession {
		if payload.Workdir == "" {
			writeRequestError(requestFilePath, ordinal, sessionName, "workdir is required on the first request of a new session")
			return
		}
		workdir = payload.Workdir
		permissionMode = payload.PermissionMode
		if permissionMode == "" {
			permissionMode = cfg.PermissionMode
		}
	} else {
		workdir = conf.Workdir
		permissionMode = conf.PermissionMode
		if payload.PermissionMode != "" {
			permissionMode = payload.PermissionMode
		}
		resumeSessionID = conf.SessionID
	}

	invokeResult, err := claudecode.Invoke(claudecode.InvokeParams{
		Prompt:          payload.Prompt,
		Workdir:         workdir,
		PermissionMode:  permissionMode,
		AddDir:          folder,
		ResumeSessionID: resumeSessionID,
		Timeout:         time.Duration(cfg.TimeoutMinutes) * time.Minute,
	})

	var resp *response.Response
	if err != nil {
		resp = &response.Response{
			Outcome: response.OutcomeClaudeCodeError,
			Error:   &response.ErrorDetail{Message: err.Error()},
		}
	} else {
		resp = response.Build(invokeResult)
	}

	if err := response.Write(session.ResponsePathFor(requestFilePath), resp); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}

	if isNewSession && resp.SessionID != "" {
		err = session.SaveSessionConf(folder, &session.SessionConf{
			SessionID:      resp.SessionID,
			Workdir:        workdir,
			PermissionMode: permissionMode,
		})
	} else if !isNewSession && permissionMode != conf.PermissionMode {
		err = session.SaveSessionConf(folder, &session.SessionConf{
			SessionID:      conf.SessionID,
			Workdir:        conf.Workdir,
			PermissionMode: permissionMode,
		})
	} else {
		err = nil
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}

	printCompletion(resp.Outcome, ordinal, sessionName)
}

func loadRequest(path string) (*requestPayload, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read request file: %w", err)
	}

	var payload requestPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("malformed request JSON: %w", err)
	}
	if payload.Prompt == "" {
		return nil, fmt.Errorf("request prompt is missing or empty")
	}
	return &payload, nil
}

func writeRequestError(requestFilePath, ordinal, sessionName, message string) {
	resp := &response.Response{
		Outcome: response.OutcomeClaudeCodeError,
		Error:   &response.ErrorDetail{Message: message},
	}
	if err := response.Write(session.ResponsePathFor(requestFilePath), resp); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Printf("ERROR: Response file [%s] for session [%s] was written to folder.\n", ordinal, sessionName)
}

func printCompletion(outcome, ordinal, sessionName string) {
	switch outcome {
	case response.OutcomeSuccess:
		fmt.Printf("Response file [%s] for session [%s] was written to folder.\n", ordinal, sessionName)
	case response.OutcomeClaudeCodeError:
		fmt.Printf("ERROR: Response file [%s] for session [%s] was written to folder.\n", ordinal, sessionName)
	case response.OutcomeTimeout:
		fmt.Printf("TIMEOUT: Response file [%s] for session [%s] was written to folder.\n", ordinal, sessionName)
	}
}
