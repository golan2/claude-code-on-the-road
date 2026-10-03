package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golan2/claude-code-on-the-road/internal/applog"
	"github.com/golan2/claude-code-on-the-road/internal/claudecode"
	"github.com/golan2/claude-code-on-the-road/internal/config"
	"github.com/golan2/claude-code-on-the-road/internal/response"
	"github.com/golan2/claude-code-on-the-road/internal/session"
	"github.com/golan2/claude-code-on-the-road/internal/shellexec"
	"github.com/golan2/claude-code-on-the-road/internal/skillscopy"
)

type requestPayload struct {
	Prompt         string   `json:"prompt"`
	Workdir        string   `json:"workdir,omitempty"`
	PermissionMode string   `json:"permissionMode,omitempty"`
	Skills         []string `json:"skills,omitempty"`
}

// inFlightSessions keeps track of all Claude Code invocations that are in progress and awaiting a response.
// The mutex prevents having 2 Claude Code invocations in flight for the same folder at once, which would
// cause confusion when responses come back. Unlike inFlightKeyed below, a folder's mark here is a real
// concurrency gate: it stays set for the entire duration of processRequest, genuinely serializing Claude
// Code requests one at a time per folder — not just protecting a map.
type inFlightSessions struct {
	mu         sync.Mutex
	processing map[string]bool // a set of all in flight sessions folder full-paths
}

// inFlightKeyed tracks an arbitrary set of string keys currently being
// processed. It backs exec-request and config-request de-duplication: unlike
// regular requests, "pending" for these is derived purely from "no response
// file yet" (session.PendingExecRequests / PendingConfigRequests), with no
// separate marker for "already dispatched, still running". Without this, an
// exec-request whose command runs longer than one poll tick gets re-dispatched
// as a brand-new goroutine on every subsequent tick that still finds no
// response file — running the same command multiple times concurrently,
// each invocation racing the others to write the final response.
//
// Its mutex is purely bookkeeping, not a concurrency gate the way
// inFlightSessions' is: tryMark/unmark only ever hold it for the instant it
// takes to check-and-set (or delete) one map entry, never while the exec
// command or config lookup itself runs. It never blocks two different
// ordinals, or the same ordinal in two different folders, from running fully
// concurrently — it only ever prevents the exact same (folder, ordinal) pair
// from being dispatched twice.
type inFlightKeyed struct {
	mu         sync.Mutex
	processing map[string]bool
}

func (s *inFlightKeyed) tryMark(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.processing[key] {
		return false
	}
	s.processing[key] = true
	return true
}

func (s *inFlightKeyed) unmark(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.processing, key)
}

// execConfigKey builds the de-duplication key for one ordinal within one
// session folder, shared by exec-request and config-request dispatch.
func execConfigKey(folder, ordinal string) string {
	return folder + ":" + ordinal
}

func main() {
	cfg, err := config.Load("config.json")
	if err != nil {
		log.Printf("ERROR: %v", err)
		os.Exit(1)
	}

	logFile, err := applog.Init(cfg.LogFile)
	if err != nil {
		// Not fatal — GoApp keeps running with stdout-only logging rather
		// than refusing to start over a log file it can't open.
		log.Printf("ERROR: %v", err)
	} else {
		defer logFile.Close()
	}
	log.Printf("GoApp starting up (pid %d), logging to %s", os.Getpid(), cfg.LogFile)

	writeStartupMarker(cfg.WatchFolder)
	syncInstructions(cfg.WatchFolder)

	inFlight := &inFlightSessions{processing: make(map[string]bool)}
	inFlightExecs := &inFlightKeyed{processing: make(map[string]bool)}
	inFlightConfigs := &inFlightKeyed{processing: make(map[string]bool)}
	sem := make(chan struct{}, cfg.MaxConcurrentSessions)

	printRecentSessions(cfg.WatchFolder)

	runSkillsCopy(cfg)
	go func() {
		defer recoverPanic("skillsCopyLoop")
		skillsTicker := time.NewTicker(time.Duration(cfg.SkillsCopyIntervalSeconds) * time.Second)
		defer skillsTicker.Stop()
		for range skillsTicker.C {
			runSkillsCopy(cfg)
		}
	}()

	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		pollOnce(cfg, inFlight, inFlightExecs, inFlightConfigs, sem)
	}
}

// startupMarkerFileName is written directly into the watch folder root (not
// into any session folder) so PhoneClaude (and the user) can tell if GoApp is alive
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
		log.Printf("ERROR: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(watchFolder, startupMarkerFileName), data, 0o644); err != nil {
		log.Printf("ERROR: %v", err)
	}
}

// instructionsFileName is used both for the local copy read from the repo
// root (the working directory GoApp is run from, same convention as
// config.Load("config.json")) and for the synced copy written into the
// Drive root folder — never inside any session folder.
const instructionsFileName = "instructions.md"

// instructionsVersionPattern matches the "<!-- version: N -->" header that
// must be the literal first line of the instructions file. GoApp only
// overwrites the Drive copy when the local file's version is strictly
// greater than the Drive copy's version, so a stale/old binary can never
// clobber a newer instructions file already synced by a newer binary
// running elsewhere. The version is plain content metadata maintained by
// hand — whoever edits the file's content bumps this number by 1; there is
// no build-time injection or separate version file.
var instructionsVersionPattern = regexp.MustCompile(`^<!-- version: (\d+) -->`)

// parseInstructionsVersion extracts the version number from the first line
// of an instructions file's content. ok is false if the first line doesn't
// match the expected header format.
func parseInstructionsVersion(data []byte) (version int, ok bool) {
	firstLine := data
	if idx := bytes.IndexByte(data, '\n'); idx != -1 {
		firstLine = data[:idx]
	}
	m := instructionsVersionPattern.FindSubmatch(firstLine)
	if m == nil {
		return 0, false
	}
	v, err := strconv.Atoi(string(m[1]))
	if err != nil {
		return 0, false
	}
	return v, true
}

// legacyInstructionsFileNames lists former Drive-root filenames for the
// synced instructions file (instructions.md was previously named
// "instructions"). GoApp has no Google Drive API integration — "Drive" here
// is just a local folder that Google Drive Desktop mirrors in the
// background, so a Drive file's identity is tied entirely to its local
// path. A plain os.WriteFile to a path that has never existed before is
// indistinguishable, from Drive Desktop's point of view, from adding a
// brand-new unrelated file: it has no way to know "this is the same
// document, just renamed" unless the rename actually happens as a rename()
// on the local filesystem it's watching. migrateLegacyInstructionsFile
// exists to make that real rename happen, so Drive preserves the existing
// file's identity instead of GoApp accidentally creating an orphaned
// duplicate. Whenever instructionsFileName changes again in the future,
// prepend the previous name here so this migration keeps working.
var legacyInstructionsFileNames = []string{"instructions"}

// migrateLegacyInstructionsFile checks watchFolder for the instructions file
// under any of its former names and, if found, performs a real os.Rename to
// newPath (never a delete+create), so Google Drive Desktop's sync mirrors it
// as a rename and preserves that file's existing Drive file ID.
func migrateLegacyInstructionsFile(watchFolder, newPath string) {
	for _, legacyName := range legacyInstructionsFileNames {
		legacyPath := filepath.Join(watchFolder, legacyName)
		if _, err := os.Stat(legacyPath); err != nil {
			continue
		}
		if err := os.Rename(legacyPath, newPath); err != nil {
			log.Printf("ERROR: %v", fmt.Errorf("migrate legacy instructions file %s to %s: %w", legacyPath, newPath, err))
			continue
		}
		log.Printf("Migrated Drive instructions file from legacy name %q to %q, preserving its Drive file identity\n", legacyName, instructionsFileName)
		return
	}
}

// syncInstructions reads the local instructions file (relative path, same
// cwd-relative convention config.Load("config.json") already uses) and
// syncs its content into watchFolder/instructions.md — the Drive ROOT
// folder, not any session folder. It always writes to that exact same path
// via os.WriteFile (never removes and recreates it), so Google Drive
// Desktop's sync preserves that file's Drive-side identity across restarts.
// If the Drive copy doesn't exist yet at the current name but does exist
// under a former name, it's migrated in place first (see
// migrateLegacyInstructionsFile) rather than treated as a fresh create.
//
// On first-ever run (no Drive copy under the current or any legacy name),
// it creates the file. On later runs, it only overwrites the existing Drive
// copy if the local file's version header is strictly greater than the
// Drive copy's — this is the guard against an accidentally-stale binary
// clobbering a newer instructions file synced by a newer binary. If the
// local file has no valid version header at all, the sync is skipped
// entirely and a warning is logged, since there would be no safe way to
// compare versions.
func syncInstructions(watchFolder string) {
	localData, err := os.ReadFile(instructionsFileName)
	if err != nil {
		log.Printf("ERROR: %v", fmt.Errorf("read local %s: %w", instructionsFileName, err))
		return
	}
	localVersion, ok := parseInstructionsVersion(localData)
	if !ok {
		log.Printf("WARNING: local %s is missing a leading \"<!-- version: N -->\" header; skipping Drive sync\n", instructionsFileName)
		return
	}

	drivePath := filepath.Join(watchFolder, instructionsFileName)
	if _, err := os.Stat(drivePath); errors.Is(err, fs.ErrNotExist) {
		migrateLegacyInstructionsFile(watchFolder, drivePath)
	}

	driveData, err := os.ReadFile(drivePath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("ERROR: %v", fmt.Errorf("read drive %s: %w", instructionsFileName, err))
			return
		}
		if err := os.WriteFile(drivePath, localData, 0o644); err != nil {
			log.Printf("ERROR: %v", err)
			return
		}
		log.Printf("Synced instructions to Drive root (created, version %d)\n", localVersion)
		return
	}

	if driveVersion, ok := parseInstructionsVersion(driveData); ok && driveVersion >= localVersion {
		log.Printf("Instructions in Drive root are already up to date (version %d)\n", driveVersion)
		return
	}

	if err := os.WriteFile(drivePath, localData, 0o644); err != nil {
		log.Printf("ERROR: %v", err)
		return
	}
	log.Printf("Synced instructions to Drive root (updated to version %d)\n", localVersion)
}

// runSkillsCopy performs one skillscopy.Sync pass using cfg's skill settings.
// Called once at startup (before any session is ever launched, so the very
// first Claude Code invocation already sees a populated SkillsCopyDir) and
// then on cfg.SkillsCopyIntervalSeconds thereafter. A nil cfg.SkillPaths
// (never configured) makes Sync a no-op, not an error.
func runSkillsCopy(cfg *config.Config) {
	err := skillscopy.Sync(skillscopy.Config{
		SkillPaths: cfg.SkillPaths,
		CopyDir:    cfg.SkillsCopyDir,
	}, log.Printf)
	if err != nil {
		log.Printf("ERROR: %v", err)
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
		log.Printf("ERROR: %v", err)
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
			log.Printf("ERROR: %v", err)
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
		log.Println("Recent sessions: none found")
		return
	}
	log.Println("Recent sessions:")
	for _, a := range activities {
		log.Printf("  %s - last activity %s\n", a.name, a.latest.Format(time.RFC3339))
	}
}

func pollOnce(cfg *config.Config, inFlight *inFlightSessions, inFlightExecs, inFlightConfigs *inFlightKeyed, sem chan struct{}) {
	folders, err := session.DiscoverSessionFolders(cfg.WatchFolder)
	if err != nil {
		log.Printf("ERROR: %v", err)
		return
	}
	for _, folder := range folders {
		if inFlight.tryMark(folder) {
			ordinal, requestFilePath, found, err := session.NextPendingRequest(folder)
			if err != nil {
				log.Printf("ERROR: %v", err)
				inFlight.unmark(folder)
			} else if !found {
				inFlight.unmark(folder)
			} else {
				go func(folder, ordinal, requestFilePath string) {
					defer recoverPanic(fmt.Sprintf("processRequest[%s][request_%s]", filepath.Base(folder), ordinal))
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
			log.Printf("ERROR: %v", err)
			continue
		}
		for _, execReq := range execRequests {
			key := execConfigKey(folder, execReq.Ordinal)
			if !inFlightExecs.tryMark(key) {
				continue
			}
			go func(folder, ordinal, path string) {
				defer recoverPanic(fmt.Sprintf("processExecRequest[%s][exec_%s]", filepath.Base(folder), ordinal))
				defer inFlightExecs.unmark(key)
				processExecRequest(cfg, folder, ordinal, path)
			}(folder, execReq.Ordinal, execReq.Path)
		}

		configRequests, err := session.PendingConfigRequests(folder)
		if err != nil {
			log.Printf("ERROR: %v", err)
			continue
		}
		for _, configReq := range configRequests {
			key := execConfigKey(folder, configReq.Ordinal)
			if !inFlightConfigs.tryMark(key) {
				continue
			}
			go func(folder, ordinal, path string) {
				defer recoverPanic(fmt.Sprintf("processConfigRequest[%s][config_%s]", filepath.Base(folder), ordinal))
				defer inFlightConfigs.unmark(key)
				processConfigRequest(cfg, folder, ordinal, path)
			}(folder, configReq.Ordinal, configReq.Path)
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

// recoverPanic recovers from and logs a panic in the goroutine it's deferred
// in, stack trace included. An unrecovered panic in any goroutine kills the
// entire process — silently, from the user's point of view, since the only
// trace of it would have been a stray stderr line in a terminal window
// nobody is watching. That would explain symptoms like "I sent two requests
// and neither ever got a response": the first one's handler panicked, took
// the whole process down mid-flight, and polling simply never resumed.
// Wrapping every goroutine with this turns that into a logged, recoverable
// event instead of an invisible process death.
func recoverPanic(label string) {
	if r := recover(); r != nil {
		log.Printf("PANIC recovered in %s: %v\n%s", label, r, debug.Stack())
	}
}

func processRequest(cfg *config.Config, folder, ordinal, requestFilePath string) {
	sessionName := filepath.Base(folder)
	if _, err := os.Stat(session.AckPathFor(folder, ordinal)); err == nil {
		log.Printf("[%s][request_%s] - already acked but unanswered (started by a previous run), reprocessing\n", sessionName, ordinal)
	}
	log.Printf("[%s][request_%s] - sent to Claude Code\n", sessionName, ordinal)

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
	var skills []string
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
		skills = payload.Skills
	} else {
		workdir = conf.Workdir
		permissionMode = conf.PermissionMode
		if payload.PermissionMode != "" {
			permissionMode = payload.PermissionMode
		}
		if permissionMode == "" {
			// The existing __session_conf.json may have been created by an
			// exec-request bootstrapping a no-CC session, which never sets a
			// permission mode since it never invokes Claude Code. This is the
			// folder's first-ever Claude Code request, so fall back to the
			// configured default exactly as a brand-new session would.
			permissionMode = cfg.PermissionMode
		}
		resumeSessionID = conf.SessionID
		skills = conf.Skills
		if len(payload.Skills) > 0 {
			skills = payload.Skills
		}
	}

	prompt := payload.Prompt
	if len(skills) > 0 {
		prompt += fmt.Sprintf("\n\nFor this task, use the following skill(s): %s.", strings.Join(skills, ", "))
	}

	tracker := claudecode.NewProgressTracker()
	statusDone := make(chan struct{})
	var finished atomic.Bool
	go func() {
		defer recoverPanic(fmt.Sprintf("runStatusRequestWatcher[%s][request_%s]", sessionName, ordinal))
		runStatusRequestWatcher(folder, ordinal, &finished, tracker, statusDone)
	}()

	invokeResult, err := claudecode.Invoke(claudecode.InvokeParams{
		Prompt:          prompt,
		Workdir:         workdir,
		PermissionMode:  permissionMode,
		AddDirs:         []string{folder, cfg.SkillsCopyDir},
		ResumeSessionID: resumeSessionID,
		Timeout:         time.Duration(cfg.TimeoutMinutes) * time.Minute,
		Progress:        tracker,
		OnLaunched: func() {
			// The subprocess launch succeeded — drop the empty ack marker so
			// PhoneClaude knows the request was picked up, long before the
			// final response exists. Content is irrelevant; existence-only.
			if err := os.WriteFile(session.AckPathFor(folder, ordinal), []byte("{}"), 0o644); err != nil {
				log.Printf("ERROR: %v", err)
			}
		},
	})
	finished.Store(true)
	close(statusDone)

	if err == nil && len(skills) > 0 {
		logMissingSkillInvocations(sessionName, ordinal, skills, tracker.InvokedSkills())
	}

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
		log.Printf("ERROR: %v", err)
	}

	// A folder's __session_conf.json may already exist with no SessionID —
	// a no-CC session, bootstrapped by an exec-request, running its first
	// Claude Code request here for the first time. That case must persist
	// the newly-obtained SessionID too, same as a brand-new session, not
	// just a changed permission mode.
	gainedSessionID := !isNewSession && conf.SessionID == "" && resp.SessionID != ""
	switch {
	case isNewSession && resp.SessionID != "":
		err = session.SaveSessionConf(folder, &session.SessionConf{
			SessionID:      resp.SessionID,
			Workdir:        workdir,
			PermissionMode: permissionMode,
			Skills:         skills,
		})
	case !isNewSession && (permissionMode != conf.PermissionMode || !slices.Equal(skills, conf.Skills) || gainedSessionID):
		sessionID := conf.SessionID
		if gainedSessionID {
			sessionID = resp.SessionID
		}
		err = session.SaveSessionConf(folder, &session.SessionConf{
			SessionID:      sessionID,
			Workdir:        conf.Workdir,
			PermissionMode: permissionMode,
			Skills:         skills,
		})
	default:
		err = nil
	}
	if err != nil {
		log.Printf("ERROR: %v", err)
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
				log.Printf("ERROR: %v", err)
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

// logMissingSkillInvocations warns, per session/ordinal, about any name in
// requestedSkills that never showed up as a Skill tool_use event's "skill"
// input during the invocation (invokedSkills, from tracker.InvokedSkills()).
// The requested-skills instruction appended to the prompt (see processRequest)
// is a request to the model, not a guarantee — this is how GoApp notices, and
// surfaces via its own log rather than silently, on the rare occasion Claude
// Code doesn't actually invoke a skill it was asked to use.
func logMissingSkillInvocations(sessionName, ordinal string, requestedSkills, invokedSkills []string) {
	invoked := make(map[string]bool, len(invokedSkills))
	for _, name := range invokedSkills {
		invoked[name] = true
	}
	for _, name := range requestedSkills {
		if !invoked[name] {
			log.Printf("WARNING: [%s][request_%s] requested skill %q was not observed to be invoked in this response\n", sessionName, ordinal, name)
		}
	}
}

func writeStatusResponse(path string, p claudecode.Progress) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		log.Printf("ERROR: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("ERROR: %v", err)
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
		log.Printf("ERROR: %v", err)
	}
	log.Printf("ERROR: [%s][response_%s] - written to folder\n", sessionName, ordinal)
}

func printCompletion(outcome, ordinal, sessionName string) {
	switch outcome {
	case response.OutcomeSuccess:
		log.Printf("[%s][response_%s] - written to folder\n", sessionName, ordinal)
	case response.OutcomeClaudeCodeError:
		log.Printf("ERROR: [%s][response_%s] - written to folder\n", sessionName, ordinal)
	case response.OutcomeTimeout:
		log.Printf("TIMEOUT: [%s][response_%s] - written to folder\n", sessionName, ordinal)
	}
}

type execRequestPayload struct {
	Command string `json:"command"`
	Workdir string `json:"workdir,omitempty"`
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
	log.Printf("[%s][exec_%s] - running shell command\n", sessionName, ordinal)

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
		// No session has ever been established in this folder — support a
		// no-CC session: one that only ever runs exec-requests and never
		// invokes Claude Code at all. Same rule as a regular request's first
		// message: workdir is required exactly once, to bootstrap the
		// session, and __session_conf.json is created with no SessionID
		// (there is no Claude Code session yet, and there may never be one).
		if payload.Workdir == "" {
			writeExecError(execRequestFilePath, ordinal, sessionName, "workdir is required on the first request of a new session (no session configuration found yet)")
			return
		}
		conf = &session.SessionConf{Workdir: payload.Workdir}
		if err := session.SaveSessionConf(folder, conf); err != nil {
			writeExecError(execRequestFilePath, ordinal, sessionName, fmt.Sprintf("failed to save __session_conf.json: %v", err))
			return
		}
		log.Printf("[%s][exec_%s] - initialized new no-CC session with workdir %s\n", sessionName, ordinal, conf.Workdir)
	}

	result, err := shellexec.Invoke(shellexec.InvokeParams{
		Command:  payload.Command,
		Workdir:  conf.Workdir,
		Timeout:  time.Duration(cfg.TimeoutMinutes) * time.Minute,
		AckDelay: time.Duration(cfg.ExecAckDelaySeconds) * time.Second,
		OnLaunched: func() {
			// Only reached if the command is still running after AckDelay —
			// a fast exec-request just gets its exec-response, with no ack
			// at all, since the response is proof enough it was picked up.
			if err := os.WriteFile(session.AckPathFor(folder, ordinal), []byte("{}"), 0o644); err != nil {
				log.Printf("ERROR: %v", err)
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
		log.Printf("ERROR: %v", err)
	}

	if resp.Error != "" {
		log.Printf("ERROR: [%s][exec_response_%s] - %s\n", sessionName, ordinal, resp.Error)
	} else {
		log.Printf("[%s][exec_response_%s] - written to folder (exit %d)\n", sessionName, ordinal, resp.ExitCode)
	}
}

func writeExecError(execRequestFilePath, ordinal, sessionName, message string) {
	resp := &response.ExecResponse{
		ExitCode:  -1,
		Error:     message,
		Truncated: false,
	}
	if err := response.Write(session.ExecResponsePathFor(execRequestFilePath), resp); err != nil {
		log.Printf("ERROR: %v", err)
	}
	log.Printf("ERROR: [%s][exec_response_%s] - %s\n", sessionName, ordinal, message)
}

type configRequestPayload struct {
	Key string `json:"key"`
}

func loadConfigRequest(path string) (*configRequestPayload, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config request file: %w", err)
	}

	var payload configRequestPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("malformed config request JSON: %w", err)
	}
	if payload.Key == "" {
		return nil, fmt.Errorf("config request key is missing or empty")
	}
	return &payload, nil
}

// processConfigRequest answers a config-request synchronously — there is no
// subprocess to launch and therefore no ack file, just an immediate
// NNNNN_config_response.json written from the in-memory cfg.
func processConfigRequest(cfg *config.Config, folder, ordinal, configRequestFilePath string) {
	sessionName := filepath.Base(folder)
	log.Printf("[%s][config_%s] - resolving config key\n", sessionName, ordinal)

	payload, err := loadConfigRequest(configRequestFilePath)
	if err != nil {
		writeConfigError(configRequestFilePath, ordinal, sessionName, err.Error())
		return
	}

	resp := response.BuildConfig(cfg, payload.Key)
	if err := response.Write(session.ConfigResponsePathFor(configRequestFilePath), resp); err != nil {
		log.Printf("ERROR: %v", err)
	}

	if resp.Error != "" {
		log.Printf("ERROR: [%s][config_response_%s] - %s\n", sessionName, ordinal, resp.Error)
	} else {
		log.Printf("[%s][config_response_%s] - written to folder\n", sessionName, ordinal)
	}
}

func writeConfigError(configRequestFilePath, ordinal, sessionName, message string) {
	resp := &response.ConfigResponse{Error: message}
	if err := response.Write(session.ConfigResponsePathFor(configRequestFilePath), resp); err != nil {
		log.Printf("ERROR: %v", err)
	}
	log.Printf("ERROR: [%s][config_response_%s] - %s\n", sessionName, ordinal, message)
}
