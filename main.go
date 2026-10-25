package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/golan2/claude-code-on-the-road/internal/applog"
	"github.com/golan2/claude-code-on-the-road/internal/archiver"
	"github.com/golan2/claude-code-on-the-road/internal/claudecode"
	"github.com/golan2/claude-code-on-the-road/internal/config"
	"github.com/golan2/claude-code-on-the-road/internal/response"
	"github.com/golan2/claude-code-on-the-road/internal/session"
	"github.com/golan2/claude-code-on-the-road/internal/shellexec"
	"github.com/golan2/claude-code-on-the-road/internal/skillscopy"
	"github.com/golan2/claude-code-on-the-road/internal/startupmarker"
)

type requestPayload struct {
	Prompt         string `json:"prompt"`
	Workdir        string `json:"workdir,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	// Timeout, in minutes, overrides the default timeout for this request only.
	Timeout *float64 `json:"timeout,omitempty"`
	Brief   string   `json:"brief,omitempty"`
}

// fallbackWorkdir is where a request runs when its session has no workdir. /tmp
// is the least harmful place for a Claude Code or shell run that isn't tied to
// any repository; PC can notice and send a request carrying a proper workdir.
const fallbackWorkdir = "/tmp"

// effectiveWorkdir returns the directory a request should run in: the
// session's workdir, or fallbackWorkdir when the session has none.
func effectiveWorkdir(workdir string) string {
	if workdir == "" {
		return fallbackWorkdir
	}
	return workdir
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

	warnIfAnotherRunning(cfg.WatchFolder)
	startedAt := time.Now()
	startup := startupmarker.New(filepath.Join(cfg.WatchFolder, startupmarker.FileName), startedAt, os.Getpid())
	if err := startup.Write(startedAt); err != nil {
		log.Printf("ERROR: %v", err)
	}
	syncInstructions(cfg.WatchFolder)

	inFlight := &inFlightSessions{processing: make(map[string]bool)}
	inFlightExecs := &inFlightKeyed{processing: make(map[string]bool)}
	inFlightConfigs := &inFlightKeyed{processing: make(map[string]bool)}
	inFlightCancels := &inFlightKeyed{processing: make(map[string]bool)}
	sem := make(chan struct{}, cfg.MaxConcurrentSessions)

	printRecentSessions(cfg.WatchFolder)

	// Archive idle sessions before polling starts, so nothing is in flight.
	if cfg.ArchiveAfterDays > 0 {
		archiver.Run(cfg.WatchFolder, cfg.ArchiveAfterDays, time.Now(), log.Printf)
	}

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
		if err := startup.Write(time.Now()); err != nil {
			log.Printf("ERROR: %v", err)
		}
		pollOnce(cfg, inFlight, inFlightExecs, inFlightConfigs, inFlightCancels, sem)
	}
}

// warnIfAnotherRunning logs a WARNING when the marker left by the previous start
// names a different process that is still alive. Two GoApps polling the same
// folder each keep their own in-flight state, so both pick up and run every
// request, and every log line appears twice. It only warns; it never stops
// either process. It can only see the most recent earlier start, because each
// start overwrites the marker, and a reused PID is possible, which is why the
// other process's command line is printed for judgment.
func warnIfAnotherRunning(watchFolder string) {
	data, err := os.ReadFile(filepath.Join(watchFolder, startupmarker.FileName))
	if err != nil {
		return
	}
	var prev startupmarker.Marker
	if err := json.Unmarshal(data, &prev); err != nil || prev.PID <= 0 || prev.PID == os.Getpid() {
		return
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(prev.PID), "-o", "command=").Output()
	command := strings.TrimSpace(string(out))
	if err != nil || command == "" {
		return
	}
	if len(command) > 160 {
		command = command[:160] + "..."
	}
	log.Printf("WARNING: another GoApp may already be running: pid %d, started %s, command: %s", prev.PID, prev.StartedAt, command)
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

func pollOnce(cfg *config.Config, inFlight *inFlightSessions, inFlightExecs, inFlightConfigs, inFlightCancels *inFlightKeyed, sem chan struct{}) {
	folders, err := session.DiscoverSessionFolders(cfg.WatchFolder)
	if err != nil {
		log.Printf("ERROR: %v", err)
		return
	}
	for _, folder := range folders {
		// Cancel requests are checked before the folder's in-flight gate, since
		// the gate stays held for the whole Claude Code run they need to interrupt.
		cancelOrdinals, err := session.PendingCancelRequests(folder)
		if err != nil {
			log.Printf("ERROR: %v", err)
		}
		for _, ordinal := range cancelOrdinals {
			key := execConfigKey(folder, ordinal)
			if !inFlightCancels.tryMark(key) {
				continue
			}
			go func(folder, ordinal string) {
				defer recoverPanic(fmt.Sprintf("processCancelRequest[%s][cancel_%s]", filepath.Base(folder), ordinal))
				defer inFlightCancels.unmark(key)
				processCancelRequest(folder, ordinal)
			}(folder, ordinal)
		}

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

// maxBriefWords is the hard cap on PC's "brief" field. A brief over this
// length is trimmed down to its first maxBriefWords words rather than
// rejected — see trimBrief.
const maxBriefWords = 7

// trimBrief enforces maxBriefWords on brief, keeping only its first
// maxBriefWords whitespace-separated words. A brief already within the cap
// is returned unchanged.
func trimBrief(brief string) string {
	words := strings.Fields(brief)
	if len(words) <= maxBriefWords {
		return brief
	}
	return strings.Join(words[:maxBriefWords], " ")
}

// briefSuffix formats the optional "brief" field PC may set on any request
// as a trailing clause for the existing pickup/response log line — so every
// request produces exactly one log line, never a second one just for the
// brief. It also enforces maxBriefWords, trimming an over-length brief
// before it reaches the log. A missing brief doesn't fail the request; it's
// folded into that same line as an inline warning instead of a separate line.
func briefSuffix(brief string) string {
	if brief == "" {
		return " - WARNING: no brief provided"
	}
	return fmt.Sprintf(" - brief: %q", trimBrief(brief))
}

// effortLevels are the values claude --effort accepts.
var effortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// validateEffort accepts an empty effort (claude's own default) or one of
// effortLevels, and otherwise returns an error that lists the allowed values.
func validateEffort(effort string) error {
	if effort == "" || slices.Contains(effortLevels, effort) {
		return nil
	}
	return fmt.Errorf("unknown effort %q; allowed: %s", effort, strings.Join(effortLevels, ", "))
}

// effectiveTimeout returns how long one request's process may run: the
// request's own timeout (minutes, may be fractional) when present, otherwise
// the configured default. A present value must be positive and representable
// as a time.Duration; anything else is an error, so the request is rejected
// rather than run with a limit nobody asked for. There is no upper cap.
func effectiveTimeout(defaultMinutes int, requested *float64) (time.Duration, error) {
	if requested == nil {
		return time.Duration(defaultMinutes) * time.Minute, nil
	}
	if *requested <= 0 {
		return 0, fmt.Errorf("timeout must be a positive number of minutes (got %v)", *requested)
	}
	if *requested > float64(math.MaxInt64)/float64(time.Minute) {
		return 0, fmt.Errorf("timeout of %v minutes is too large", *requested)
	}
	timeout := time.Duration(*requested * float64(time.Minute))
	if timeout <= 0 {
		return 0, fmt.Errorf("timeout of %v minutes is too small", *requested)
	}
	return timeout, nil
}

// resolveModel maps a model's short name to the full id from cfg.Models. An
// empty name means "no model chosen" and resolves to "", so claude uses its
// own default. A name that isn't configured is an error that lists the
// allowed names, so a typo never reaches claude.
func resolveModel(cfg *config.Config, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	if len(cfg.Models) == 0 {
		return "", fmt.Errorf("model %q was requested but no models are configured in config.json", name)
	}
	names := make([]string, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		if m.Name == name {
			return m.ID, nil
		}
		names = append(names, m.Name)
	}
	return "", fmt.Errorf("unknown model %q; allowed: %s", name, strings.Join(names, ", "))
}

func processRequest(cfg *config.Config, folder, ordinal, requestFilePath string) {
	sessionName := filepath.Base(folder)
	if _, err := os.Stat(session.PidPathFor(folder, ordinal)); err == nil {
		log.Printf("[%s][%s_cc][INFO] - already started but unanswered (started by a previous run), reprocessing\n", sessionName, ordinal)
	}

	payload, err := loadRequest(requestFilePath)
	if err != nil {
		writeRequestError(requestFilePath, ordinal, sessionName, "", err.Error())
		return
	}
	timeout, err := effectiveTimeout(cfg.TimeoutMinutes, payload.Timeout)
	if err != nil {
		writeRequestError(requestFilePath, ordinal, sessionName, payload.Brief, err.Error())
		return
	}
	log.Printf("[%s][%s_cc][REQ] - timeout %s%s\n", sessionName, ordinal, timeout, briefSuffix(payload.Brief))

	conf, err := session.LoadSessionConf(folder)
	if err != nil {
		writeRequestError(requestFilePath, ordinal, sessionName, payload.Brief, fmt.Sprintf("invalid __session_conf.json: %v", err))
		return
	}

	isNewSession := conf == nil
	var confWorkdir, permissionMode, resumeSessionID string
	if isNewSession {
		confWorkdir = payload.Workdir
		permissionMode = payload.PermissionMode
		if permissionMode == "" {
			permissionMode = cfg.PermissionMode
		}
	} else {
		confWorkdir = conf.Workdir
		if confWorkdir == "" {
			// The session has no workdir yet (it started without one); a request
			// that carries one sets it. Once set it is never changed.
			confWorkdir = payload.Workdir
		}
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
	}
	// The model is sticky for the session, like permissionMode: a request's
	// model replaces the stored one. The stored value is the short name, so a
	// version change in config.json takes effect on the next request.
	modelName := payload.Model
	if modelName == "" && !isNewSession {
		modelName = conf.Model
	}
	modelID, err := resolveModel(cfg, modelName)
	if err != nil {
		writeRequestError(requestFilePath, ordinal, sessionName, payload.Brief, err.Error())
		return
	}
	effort := payload.Effort
	if effort == "" && !isNewSession {
		effort = conf.Effort
	}
	if err := validateEffort(effort); err != nil {
		writeRequestError(requestFilePath, ordinal, sessionName, payload.Brief, err.Error())
		return
	}
	workdir := effectiveWorkdir(confWorkdir)
	if confWorkdir == "" {
		log.Printf("[%s][%s_cc][INFO] - no workdir set for this session, running in %s\n", sessionName, ordinal, fallbackWorkdir)
	}

	tracker := claudecode.NewProgressTracker()
	statusDone := make(chan struct{})
	var finished atomic.Bool
	go func() {
		defer recoverPanic(fmt.Sprintf("runStatusRequestWatcher[%s][request_%s]", sessionName, ordinal))
		runStatusRequestWatcher(folder, ordinal, &finished, tracker, statusDone)
	}()

	invokeResult, err := claudecode.Invoke(claudecode.InvokeParams{
		ClaudePath:      cfg.ClaudePath,
		Prompt:          payload.Prompt,
		Workdir:         workdir,
		PermissionMode:  permissionMode,
		Model:           modelID,
		Effort:          effort,
		AddDirs:         []string{folder, cfg.SkillsCopyDir},
		ResumeSessionID: resumeSessionID,
		Timeout:         timeout,
		Progress:        tracker,
		OnLaunched: func(pid int) {
			if err := writePidFile(session.PidPathFor(folder, ordinal), pid); err != nil {
				log.Printf("ERROR: %v", err)
			}
		},
	})
	finished.Store(true)
	close(statusDone)

	if err == nil {
		if invoked := tracker.InvokedSkills(); len(invoked) > 0 {
			log.Printf("[%s][%s_cc][INFO] - skills invoked: %s\n", sessionName, ordinal, strings.Join(skillscopy.ShortenForLog(invoked), ", "))
		}
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
			Workdir:        confWorkdir,
			PermissionMode: permissionMode,
			Model:          modelName,
			Effort:         effort,
		})
	case !isNewSession && (permissionMode != conf.PermissionMode || gainedSessionID || confWorkdir != conf.Workdir || modelName != conf.Model || effort != conf.Effort):
		sessionID := conf.SessionID
		if gainedSessionID {
			sessionID = resp.SessionID
		}
		err = session.SaveSessionConf(folder, &session.SessionConf{
			SessionID:      sessionID,
			Workdir:        confWorkdir,
			PermissionMode: permissionMode,
			Model:          modelName,
			Effort:         effort,
		})
	default:
		err = nil
	}
	if err != nil {
		log.Printf("ERROR: %v", err)
	}

	printCompletion(resp.Outcome, ordinal, sessionName, payload.Brief)
}

type pidFile struct {
	PID int `json:"pid"`
}

func writePidFile(path string, pid int) error {
	data, err := json.Marshal(pidFile{PID: pid})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

type cancelResponse struct {
	Status  string `json:"status"`
	PID     int    `json:"pid,omitempty"`
	Message string `json:"message,omitempty"`
}

func processCancelRequest(folder, ordinal string) {
	sessionName := filepath.Base(folder)
	resp := cancelClaudeCode(folder, ordinal)

	data, err := json.MarshalIndent(resp, "", "  ")
	if err == nil {
		err = os.WriteFile(session.CancelResponsePathFor(folder, ordinal), data, 0o644)
	}
	if err != nil {
		log.Printf("ERROR: %v", err)
		return
	}

	if resp.Status == "success" {
		log.Printf("[%s][%s_cancel][RES] [ok: killed process group of pid %d]\n", sessionName, ordinal, resp.PID)
	} else {
		log.Printf("ERROR: [%s][%s_cancel][RES] [error: %s]\n", sessionName, ordinal, resp.Message)
	}
}

// cancelClaudeCode kills the process recorded in the request's NNNNN_pid.json
// (a Claude Code request's process, or an exec request's shell). It refuses once the request has a cc_response, since by then
// the PID may already belong to an unrelated process.
func cancelClaudeCode(folder, ordinal string) cancelResponse {
	// Ordinals are unique across request types, so the ordinal identifies
	// either a Claude Code request or an exec request.
	responsePath := session.CCResponsePathFor(folder, ordinal)
	execRequestPath := filepath.Join(folder, ordinal+"_exec_request.json")
	if _, err := os.Stat(execRequestPath); err == nil {
		responsePath = session.ExecResponsePathFor(execRequestPath)
	}
	if _, err := os.Stat(responsePath); err == nil {
		return cancelResponse{Status: "error", Message: "request already finished"}
	}

	data, err := os.ReadFile(session.PidPathFor(folder, ordinal))
	if errors.Is(err, fs.ErrNotExist) {
		return cancelResponse{Status: "error", Message: "PID file not found"}
	}
	if err != nil {
		return cancelResponse{Status: "error", Message: fmt.Sprintf("read PID file: %v", err)}
	}

	var pf pidFile
	if err := json.Unmarshal(data, &pf); err != nil || pf.PID <= 0 {
		return cancelResponse{Status: "error", Message: "PID file is invalid"}
	}

	// Claude Code and exec shells each run in their own process group (see
	// claudecode.Invoke and shellexec.Invoke), and their subprocesses inherit stdout. Killing only the PID leaves them holding
	// the pipe open, so the request would never get its cc_response.
	if err := syscall.Kill(-pf.PID, syscall.SIGKILL); err != nil {
		return cancelResponse{Status: "error", Message: fmt.Sprintf("kill -9 -%d failed: %v", pf.PID, err)}
	}
	return cancelResponse{Status: "success", PID: pf.PID}
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

func writeRequestError(requestFilePath, ordinal, sessionName, brief, message string) {
	resp := &response.Response{
		Outcome: response.OutcomeClaudeCodeError,
		Error:   &response.ErrorDetail{Message: message},
	}
	if err := response.Write(session.ResponsePathFor(requestFilePath), resp); err != nil {
		log.Printf("ERROR: %v", err)
	}
	log.Printf("ERROR: [%s][%s_cc][RES] [error: %s]%s\n", sessionName, ordinal, message, briefSuffix(brief))
}

func printCompletion(outcome, ordinal, sessionName, brief string) {
	suffix := briefSuffix(brief)
	switch outcome {
	case response.OutcomeSuccess:
		log.Printf("[%s][%s_cc][RES] [success]%s\n", sessionName, ordinal, suffix)
	case response.OutcomeClaudeCodeError:
		log.Printf("ERROR: [%s][%s_cc][RES] [error]%s\n", sessionName, ordinal, suffix)
	case response.OutcomeTimeout:
		log.Printf("TIMEOUT: [%s][%s_cc][RES] [timeout]%s\n", sessionName, ordinal, suffix)
	}
}

type execRequestPayload struct {
	Command string `json:"command"`
	Workdir string `json:"workdir,omitempty"`
	// Timeout, in minutes, overrides the default timeout for this command only.
	Timeout *float64 `json:"timeout,omitempty"`
	Brief   string   `json:"brief,omitempty"`
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

	payload, err := loadExecRequest(execRequestFilePath)
	if err != nil {
		writeExecError(execRequestFilePath, ordinal, sessionName, "", err.Error())
		return
	}
	timeout, err := effectiveTimeout(cfg.TimeoutMinutes, payload.Timeout)
	if err != nil {
		writeExecError(execRequestFilePath, ordinal, sessionName, payload.Brief, err.Error())
		return
	}
	log.Printf("[%s][%s_exec][REQ] - timeout %s%s\n", sessionName, ordinal, timeout, briefSuffix(payload.Brief))

	conf, err := session.LoadSessionConf(folder)
	if err != nil {
		writeExecError(execRequestFilePath, ordinal, sessionName, payload.Brief, fmt.Sprintf("invalid __session_conf.json: %v", err))
		return
	}
	if conf == nil {
		// No session has ever been established in this folder — support a
		// no-CC session: one that only ever runs exec-requests and never
		// invokes Claude Code at all. __session_conf.json is created with no
		// SessionID (there is no Claude Code session yet, and there may never
		// be one), and with whatever workdir the request carried — possibly
		// none, since not every command relates to a working directory.
		conf = &session.SessionConf{Workdir: payload.Workdir}
		if err := session.SaveSessionConf(folder, conf); err != nil {
			writeExecError(execRequestFilePath, ordinal, sessionName, payload.Brief, fmt.Sprintf("failed to save __session_conf.json: %v", err))
			return
		}
		log.Printf("[%s][%s_exec][INFO] - initialized new no-CC session\n", sessionName, ordinal)
	} else if conf.Workdir == "" && payload.Workdir != "" {
		conf.Workdir = payload.Workdir
		if err := session.SaveSessionConf(folder, conf); err != nil {
			writeExecError(execRequestFilePath, ordinal, sessionName, payload.Brief, fmt.Sprintf("failed to save __session_conf.json: %v", err))
			return
		}
	}
	if conf.Workdir == "" {
		log.Printf("[%s][%s_exec][INFO] - no workdir set for this session, running in %s\n", sessionName, ordinal, fallbackWorkdir)
	}

	result, err := shellexec.Invoke(shellexec.InvokeParams{
		Command: payload.Command,
		Workdir: effectiveWorkdir(conf.Workdir),
		Timeout: timeout,
		OnLaunched: func(pid int) {
			// The PID file is the "picked up" signal, exactly as for Claude Code
			// requests, and it is what makes the command cancellable from the
			// moment it starts.
			if err := writePidFile(session.PidPathFor(folder, ordinal), pid); err != nil {
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

	suffix := briefSuffix(payload.Brief)
	if resp.Error != "" {
		log.Printf("ERROR: [%s][%s_exec][RES] [error: %s]%s\n", sessionName, ordinal, resp.Error, suffix)
	} else {
		log.Printf("[%s][%s_exec][RES] [exit %d]%s\n", sessionName, ordinal, resp.ExitCode, suffix)
	}
}

func writeExecError(execRequestFilePath, ordinal, sessionName, brief, message string) {
	resp := &response.ExecResponse{
		ExitCode:  -1,
		Error:     message,
		Truncated: false,
	}
	if err := response.Write(session.ExecResponsePathFor(execRequestFilePath), resp); err != nil {
		log.Printf("ERROR: %v", err)
	}
	log.Printf("ERROR: [%s][%s_exec][RES] [error: %s]%s\n", sessionName, ordinal, message, briefSuffix(brief))
}

type configRequestPayload struct {
	Key   string `json:"key"`
	Brief string `json:"brief,omitempty"`
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

	payload, err := loadConfigRequest(configRequestFilePath)
	if err != nil {
		writeConfigError(configRequestFilePath, ordinal, sessionName, "", err.Error())
		return
	}
	log.Printf("[%s][%s_config][REQ]%s\n", sessionName, ordinal, briefSuffix(payload.Brief))

	resp := response.BuildConfig(cfg, payload.Key)
	if err := response.Write(session.ConfigResponsePathFor(configRequestFilePath), resp); err != nil {
		log.Printf("ERROR: %v", err)
	}

	suffix := briefSuffix(payload.Brief)
	if resp.Error != "" {
		log.Printf("ERROR: [%s][%s_config][RES] [error: %s]%s\n", sessionName, ordinal, resp.Error, suffix)
	} else {
		log.Printf("[%s][%s_config][RES] [ok]%s\n", sessionName, ordinal, suffix)
	}
}

func writeConfigError(configRequestFilePath, ordinal, sessionName, brief, message string) {
	resp := &response.ConfigResponse{Error: message}
	if err := response.Write(session.ConfigResponsePathFor(configRequestFilePath), resp); err != nil {
		log.Printf("ERROR: %v", err)
	}
	log.Printf("ERROR: [%s][%s_config][RES] [error: %s]%s\n", sessionName, ordinal, message, briefSuffix(brief))
}
