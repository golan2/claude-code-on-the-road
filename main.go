package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golan2/claude-code-on-the-road/internal/claudecode"
	"github.com/golan2/claude-code-on-the-road/internal/config"
	"github.com/golan2/claude-code-on-the-road/internal/response"
	"github.com/golan2/claude-code-on-the-road/internal/session"
	"github.com/golan2/claude-code-on-the-road/internal/shellexec"
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

// inFlightExecs keeps track of individual exec-request ordinals that are
// already being handled, so the same exec-request is not dispatched twice.
type inFlightExecs struct {
	mu         sync.Mutex
	processing map[string]bool // keys are "folder|ordinal"
}

func (e *inFlightExecs) tryMark(folder, ordinal string) bool {
	key := folder + "|" + ordinal
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.processing[key] {
		return false
	}
	e.processing[key] = true
	return true
}

func (e *inFlightExecs) unmark(folder, ordinal string) {
	key := folder + "|" + ordinal
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.processing, key)
}

func main() {
	cfg, err := config.Load("config.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	writeStartupMarker(cfg.WatchFolder)

	inFlight := &inFlightSessions{processing: make(map[string]bool)}
	inFlightExec := &inFlightExecs{processing: make(map[string]bool)}
	sem := make(chan struct{}, cfg.MaxConcurrentSessions)

	printRecentSessions(cfg.WatchFolder)
	recoverStuckSessions(cfg, inFlight, sem)

	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		pollOnce(cfg, inFlight, inFlightExec, sem)
	}
}

// startupMarkerFileName is written directly into the watch folder root (not
// into any session folder) so PhoneClaude and Izik can tell GoApp is alive
// and see when it last started.
const startupMarkerFileName = "goapp_started.json"

type startupMarker struct {
	StartedAt string `json:"startedAt"`
	PID       int    `json:"pid"`
}

// writeStartupMarker overwrites startupMarkerFileName in watchFolder with the
// current startup info. Any previous marker from an earlier run is replaced,
// since only the most recent start matters.
func writeStartupMarker(watchFolder string) {
	marker := startupMarker{
		StartedAt: time.Now().Format(time.RFC3339),
		PID:       os.Getpid(),
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if err := os.WriteFile(filepath.Join(watchFolder, startupMarkerFileName), data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// recentSessionsCount is how many of the most recently active session folders
// printRecentSessions reports at startup.
const recentSessionsCount = 3

// printRecentSessions scans every session folder under watchFolder and prints
// the recentSessionsCount most recently active ones (by the modification time
// of their most recently modified request/response/ack file) to stdout.
func printRecentSessions(watchFolder string) {
	folders, err := session.DiscoverSessionFolders(watchFolder)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	type sessionActivity struct {
		name   string
		latest time.Time
	}
	var activities []sessionActivity
	for _, folder := range folders {
		latest, ok, err := session.LatestActivity(folder)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		if !ok {
			continue
		}
		activities = append(activities, sessionActivity{name: filepath.Base(folder), latest: latest})
	}

	sort.Slice(activities, func(i, j int) bool {
		return activities[i].latest.After(activities[j].latest)
	})
	if len(activities) > recentSessionsCount {
		activities = activities[:recentSessionsCount]
	}

	if len(activities) == 0 {
		fmt.Println("Recent sessions: none found")
		return
	}
	fmt.Println("Recent sessions:")
	for _, a := range activities {
		fmt.Printf("  %s - last activity %s\n", a.name, a.latest.Format(time.RFC3339))
	}
}

// recoverStuckSessions scans every session folder under cfg.WatchFolder for a
// request left unanswered by a previous run (GoApp killed or crashed
// mid-processing) and reprocesses it through the normal request-processing
// path, exactly as if the poller had just discovered it. It dispatches
// through the same inFlight/sem machinery pollOnce uses, so the first regular
// poll tick correctly skips any folder already being recovered here.
func recoverStuckSessions(cfg *config.Config, inFlight *inFlightSessions, sem chan struct{}) {
	folders, err := session.DiscoverSessionFolders(cfg.WatchFolder)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	for _, folder := range folders {
		sessionName := filepath.Base(folder)

		stuck, err := session.FindStuckRequest(folder)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		if stuck.Anomaly {
			fmt.Fprintf(os.Stderr, "WARNING: [%s] unanswered request ordinals %v violate the sequential processing guarantee (expected at most the highest ordinal to be unanswered) — skipping automatic recovery for this session, investigate manually\n", sessionName, stuck.Unanswered)
			continue
		}
		if !stuck.Found {
			continue
		}

		fmt.Printf("[%s][request_%s] - stuck from a previous run, reprocessing\n", sessionName, stuck.Ordinal)

		if !inFlight.tryMark(folder) {
			continue
		}
		go func(folder, ordinal, requestFilePath string) {
			sem <- struct{}{}
			defer func() {
				<-sem
				inFlight.unmark(folder)
			}()
			processRequest(cfg, folder, ordinal, requestFilePath)
		}(folder, stuck.Ordinal, stuck.RequestFilePath)
	}
}

func pollOnce(cfg *config.Config, inFlight *inFlightSessions, inFlightExec *inFlightExecs, sem chan struct{}) {
	folders, err := session.DiscoverSessionFolders(cfg.WatchFolder)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	for _, folder := range folders {
		if inFlight.tryMark(folder) {
			ordinal, requestFilePath, found, err := session.NextPendingRequest(folder)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				inFlight.unmark(folder)
			} else if !found {
				inFlight.unmark(folder)
			} else {
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

		execRequests, err := session.PendingExecRequests(folder)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		for _, execReq := range execRequests {
			if !inFlightExec.tryMark(folder, execReq.Ordinal) {
				continue
			}
			go func(folder, ordinal, execRequestFilePath string) {
				defer inFlightExec.unmark(folder, ordinal)
				processExecRequest(cfg, folder, ordinal, execRequestFilePath)
			}(folder, execReq.Ordinal, execReq.Path)
		}
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
	fmt.Printf("[%s][request_%s] - sent to Claude Code\n", sessionName, ordinal)

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

	tracker := claudecode.NewProgressTracker()
	statusDone := make(chan struct{})
	var finished atomic.Bool
	go runStatusRequestWatcher(folder, ordinal, &finished, tracker, statusDone)

	invokeResult, err := claudecode.Invoke(claudecode.InvokeParams{
		Prompt:          payload.Prompt,
		Workdir:         workdir,
		PermissionMode:  permissionMode,
		AddDir:          folder,
		ResumeSessionID: resumeSessionID,
		Timeout:         time.Duration(cfg.TimeoutMinutes) * time.Minute,
		Progress:        tracker,
		OnLaunched: func() {
			// The subprocess launch succeeded — drop the empty ack marker so
			// PhoneClaude knows the request was picked up, long before the
			// final response exists. Content is irrelevant; existence-only.
			if err := os.WriteFile(session.AckPathFor(folder, ordinal), []byte("{}"), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		},
	})
	finished.Store(true)
	close(statusDone)

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

// statusRequestPollIntervalSeconds controls how often the session folder is
// polled for new NNNNN_status_request_MMM.json files while a request is in flight.
const statusRequestPollIntervalSeconds = 1

// runStatusRequestWatcher polls the session folder for new status_request marker
// files for the current ordinal. For each newly discovered counter, it writes a
// status_response snapshot from tracker unless the request has already finished.
func runStatusRequestWatcher(
	sessionFolderPath string,
	ordinal string,
	finished *atomic.Bool,
	tracker *claudecode.ProgressTracker,
	done <-chan struct{},
) {
	ticker := time.NewTicker(statusRequestPollIntervalSeconds * time.Second)
	defer ticker.Stop()

	handled := make(map[int]struct{})

	for {
		select {
		case <-ticker.C:
			counters, err := session.StatusRequestCounters(sessionFolderPath, ordinal)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				continue
			}
			for _, c := range counters {
				if _, ok := handled[c]; ok {
					continue
				}
				if finished.Load() {
					// No status_responses are written once the claude process has
					// finished; the final response will be written instead.
					return
				}
				handled[c] = struct{}{}
				writeStatusResponse(session.StatusResponsePathFor(sessionFolderPath, ordinal, c), tracker.Snapshot())
			}
		case <-done:
			return
		}
	}
}

func writeStatusResponse(path string, p claudecode.Progress) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
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
	fmt.Printf("ERROR: [%s][response_%s] - written to folder\n", sessionName, ordinal)
}

func printCompletion(outcome, ordinal, sessionName string) {
	switch outcome {
	case response.OutcomeSuccess:
		fmt.Printf("[%s][response_%s] - written to folder\n", sessionName, ordinal)
	case response.OutcomeClaudeCodeError:
		fmt.Printf("ERROR: [%s][response_%s] - written to folder\n", sessionName, ordinal)
	case response.OutcomeTimeout:
		fmt.Printf("TIMEOUT: [%s][response_%s] - written to folder\n", sessionName, ordinal)
	}
}

type execRequestPayload struct {
	Command string `json:"command"`
}

func loadExecRequest(path string) (*execRequestPayload, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read exec request file: %w", err)
	}

	var payload execRequestPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("malformed exec request JSON: %w", err)
	}
	if payload.Command == "" {
		return nil, fmt.Errorf("exec request command is missing or empty")
	}
	return &payload, nil
}

func processExecRequest(cfg *config.Config, folder, ordinal, execRequestFilePath string) {
	sessionName := filepath.Base(folder)
	fmt.Printf("[%s][exec_%s] - running shell command\n", sessionName, ordinal)

	payload, err := loadExecRequest(execRequestFilePath)
	if err != nil {
		writeExecError(execRequestFilePath, ordinal, sessionName, err.Error())
		return
	}

	conf, err := session.LoadSessionConf(folder)
	if err != nil {
		writeExecError(execRequestFilePath, ordinal, sessionName, fmt.Sprintf("invalid __session_conf.json: %v", err))
		return
	}
	if conf == nil {
		writeExecError(execRequestFilePath, ordinal, sessionName, "no session configuration found; exec requests require a prior regular request in this session")
		return
	}

	result, err := shellexec.Invoke(shellexec.InvokeParams{
		Command: payload.Command,
		Workdir: conf.Workdir,
		Timeout: time.Duration(cfg.TimeoutMinutes) * time.Minute,
		OnLaunched: func() {
			// Same ack mechanism as regular requests: drop an empty marker as
			// soon as the shell command subprocess has launched successfully.
			if err := os.WriteFile(session.AckPathFor(folder, ordinal), []byte("{}"), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		},
	})

	var resp *response.ExecResponse
	if err != nil {
		resp = &response.ExecResponse{
			ExitCode:  -1,
			Error:     err.Error(),
			Truncated: false,
		}
	} else {
		resp = response.BuildExec(result, cfg.ExecOutputMaxChars)
	}

	if err := response.Write(session.ExecResponsePathFor(execRequestFilePath), resp); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}

	if resp.Error != "" {
		fmt.Printf("ERROR: [%s][exec_response_%s] - %s\n", sessionName, ordinal, resp.Error)
	} else {
		fmt.Printf("[%s][exec_response_%s] - written to folder (exit %d)\n", sessionName, ordinal, resp.ExitCode)
	}
}

func writeExecError(execRequestFilePath, ordinal, sessionName, message string) {
	resp := &response.ExecResponse{
		ExitCode:  -1,
		Error:     message,
		Truncated: false,
	}
	if err := response.Write(session.ExecResponsePathFor(execRequestFilePath), resp); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Printf("ERROR: [%s][exec_response_%s] - %s\n", sessionName, ordinal, message)
}
