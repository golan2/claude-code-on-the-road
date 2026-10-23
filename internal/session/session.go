package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const sessionConfFileName = "__session_conf.json"

// archivedFolderName is the name of the top-level folder under the watchFolder
// root that holds archived session folders. Sessions are moved there externally
// (e.g. by PhoneClaude via Drive, at the user's explicit request) once finished.
// GoApp never creates or manages this folder — it only skips scanning into it.
const archivedFolderName = "_archived"

var (
	// requestFilePattern matches a Claude Code request file. NNNNN_cc_request.json
	// is the canonical name (parallel to NNNNN_exec_request.json and
	// NNNNN_config_request.json); plain NNNNN_request.json is accepted only for
	// backward compatibility with sessions and clients that predate the rename.
	requestFilePattern       = regexp.MustCompile(`^(\d{5})_(?:cc_)?request\.json$`)
	statusRequestFilePattern = regexp.MustCompile(`^(\d{5})_status_request_(\d{3})\.json$`)
	execRequestFilePattern   = regexp.MustCompile(`^(\d{5})_exec_request\.json$`)
	configRequestFilePattern = regexp.MustCompile(`^(\d{5})_config_request\.json$`)
	cancelRequestFilePattern = regexp.MustCompile(`^(\d{5})_cancel_request\.json$`)
	activityFilePattern      = regexp.MustCompile(`^\d{5}_(request|cc_request|response|cc_response|pid|ack|exec_request|exec_response|config_request|config_response|cancel_request|cancel_response|status_request_\d{3}|status_response_\d{3})\.json$`)
)

// SessionConf holds the per-session metadata stored in __session_conf.json.
type SessionConf struct {
	SessionID      string `json:"sessionId"`
	Workdir        string `json:"workdir"`
	PermissionMode string `json:"permissionMode"`
	Model          string `json:"model,omitempty"`  // short name from config models; empty = claude default
	Effort         string `json:"effort,omitempty"` // low|medium|high|xhigh|max; empty = claude default
}

// LoadSessionConf reads __session_conf.json from sessionFolderPath. It returns
// (nil, nil) when the file does not exist yet (brand-new session).
func LoadSessionConf(sessionFolderPath string) (*SessionConf, error) {
	data, err := os.ReadFile(filepath.Join(sessionFolderPath, sessionConfFileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session conf: %w", err)
	}
	var conf SessionConf
	if err := json.Unmarshal(data, &conf); err != nil {
		return nil, fmt.Errorf("parse session conf in %s: %w", sessionFolderPath, err)
	}
	return &conf, nil
}

// SaveSessionConf writes __session_conf.json into sessionFolderPath as indented JSON.
func SaveSessionConf(sessionFolderPath string, conf *SessionConf) error {
	data, err := json.MarshalIndent(conf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session conf: %w", err)
	}
	path := filepath.Join(sessionFolderPath, sessionConfFileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write session conf: %w", err)
	}
	return nil
}

// DiscoverSessionFolders returns the absolute paths of the session folders in
// root: its direct child directories that contain at least one request file
// (see dirHasRequestFile). The scan is flat — nothing deeper than a direct
// child is ever looked at — so a session folder must be created directly under
// the watch folder. The top-level "_archived" folder holds archived sessions
// and is never scanned. A child that can't be read is skipped silently.
func DiscoverSessionFolders(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("discover session folders under %s: %w", root, err)
	}
	var folders []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == archivedFolderName {
			continue
		}
		path := filepath.Join(root, e.Name())
		hasRequest, err := dirHasRequestFile(path)
		if err != nil || !hasRequest {
			continue
		}
		folders = append(folders, path)
	}
	return folders, nil
}

// dirHasRequestFile reports whether dir looks like a session folder at all —
// i.e. it has ever received any kind of request file. Any request type counts
// (Claude Code, exec or config): every request is valid on its own and gets a
// response, and none of them requires a prior request or a workdir to be
// handled.
func dirHasRequestFile(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if requestFilePattern.MatchString(name) ||
			execRequestFilePattern.MatchString(name) ||
			configRequestFilePattern.MatchString(name) {
			return true, nil
		}
	}
	return false, nil
}

// requestEntry is one Claude Code request file found in a session folder.
type requestEntry struct {
	Ordinal string
	Path    string
}

// listRequests returns every Claude Code request file directly in
// sessionFolderPath, sorted by ordinal. NNNNN_cc_request.json is canonical;
// the legacy NNNNN_request.json name is accepted only for backward
// compatibility. If both names exist for one ordinal, the canonical one wins.
func listRequests(sessionFolderPath string) ([]requestEntry, error) {
	entries, err := os.ReadDir(sessionFolderPath)
	if err != nil {
		return nil, fmt.Errorf("read session folder %s: %w", sessionFolderPath, err)
	}
	byOrdinal := make(map[string]requestEntry)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := requestFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if existing, ok := byOrdinal[m[1]]; ok && strings.HasSuffix(existing.Path, "_cc_request.json") {
			continue
		}
		byOrdinal[m[1]] = requestEntry{Ordinal: m[1], Path: filepath.Join(sessionFolderPath, e.Name())}
	}
	out := make([]requestEntry, 0, len(byOrdinal))
	for _, r := range byOrdinal {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ordinal < out[j].Ordinal })
	return out, nil
}

// NextPendingRequest returns the lowest-ordinal Claude Code request in
// sessionFolderPath (NNNNN_cc_request.json, or legacy NNNNN_request.json)
// that has no matching response (NNNNN_cc_response.json, or legacy NNNNN_response.json) yet.
func NextPendingRequest(sessionFolderPath string) (ordinal string, requestFilePath string, found bool, err error) {
	reqs, err := listRequests(sessionFolderPath)
	if err != nil {
		return "", "", false, err
	}
	for _, r := range reqs {
		if !hasResponse(r.Path) {
			return r.Ordinal, r.Path, true, nil
		}
	}
	return "", "", false, nil
}

// LatestActivity returns the modification time of the most recently modified
// activity file directly in sessionFolderPath: a NNNNN_request.json,
// NNNNN_response.json, NNNNN_ack.json, NNNNN_exec_request.json,
// NNNNN_exec_response.json, NNNNN_config_request.json,
// NNNNN_config_response.json, NNNNN_status_request_MMM.json, or
// NNNNN_status_response_MMM.json. ok is false if none of those files are
// present.
func LatestActivity(sessionFolderPath string) (latest time.Time, ok bool, err error) {
	entries, err := os.ReadDir(sessionFolderPath)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read session folder %s: %w", sessionFolderPath, err)
	}
	for _, e := range entries {
		if e.IsDir() || !activityFilePattern.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !ok || info.ModTime().After(latest) {
			latest = info.ModTime()
			ok = true
		}
	}
	return latest, ok, nil
}

// ResponsePathFor maps a request path to the response path GoApp writes for it:
// NNNNN_cc_request.json -> NNNNN_cc_response.json (canonical). A legacy
// NNNNN_request.json maps to the legacy NNNNN_response.json, so a client still
// using the old naming gets the old name back; that pairing exists only for
// backward compatibility.
func ResponsePathFor(requestFilePath string) string {
	dir := filepath.Dir(requestFilePath)
	name := filepath.Base(requestFilePath)
	m := requestFilePattern.FindStringSubmatch(name)
	if m == nil {
		return requestFilePath
	}
	if strings.HasSuffix(name, "_cc_request.json") {
		return filepath.Join(dir, m[1]+"_cc_response.json")
	}
	return filepath.Join(dir, m[1]+"_response.json")
}

// hasResponse reports whether a request has been answered. Either response
// spelling counts, so a request answered under the legacy NNNNN_response.json
// name is never reprocessed.
func hasResponse(requestFilePath string) bool {
	dir := filepath.Dir(requestFilePath)
	m := requestFilePattern.FindStringSubmatch(filepath.Base(requestFilePath))
	if m == nil {
		return false
	}
	for _, name := range []string{m[1] + "_cc_response.json", m[1] + "_response.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// AckPathFor returns the path for the NNNNN_ack.json marker for the given
// session folder and ordinal.
func AckPathFor(sessionFolderPath, ordinal string) string {
	return filepath.Join(sessionFolderPath, ordinal+"_ack.json")
}

// PidPathFor returns the path for the NNNNN_pid.json file that records the
// Claude Code process PID for the given session folder and ordinal.
func PidPathFor(sessionFolderPath, ordinal string) string {
	return filepath.Join(sessionFolderPath, ordinal+"_pid.json")
}

// CancelResponsePathFor returns the path for the NNNNN_cancel_response.json
// file answering a cancel request for the given session folder and ordinal.
func CancelResponsePathFor(sessionFolderPath, ordinal string) string {
	return filepath.Join(sessionFolderPath, ordinal+"_cancel_response.json")
}

// CCResponsePathFor returns the path for the NNNNN_cc_response.json file
// for the given session folder and ordinal.
func CCResponsePathFor(sessionFolderPath, ordinal string) string {
	return filepath.Join(sessionFolderPath, ordinal+"_cc_response.json")
}

// PendingCancelRequests returns the ordinals of all NNNNN_cancel_request.json
// files in sessionFolderPath that don't yet have a matching
// NNNNN_cancel_response.json, sorted ascending.
func PendingCancelRequests(sessionFolderPath string) ([]string, error) {
	entries, err := os.ReadDir(sessionFolderPath)
	if err != nil {
		return nil, fmt.Errorf("read session folder %s: %w", sessionFolderPath, err)
	}

	var pending []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := cancelRequestFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if _, err := os.Stat(CancelResponsePathFor(sessionFolderPath, m[1])); errors.Is(err, fs.ErrNotExist) {
			pending = append(pending, m[1])
		}
	}
	sort.Strings(pending)
	return pending, nil
}

// PendingExecRequest describes a single pending exec-request file.
type PendingExecRequest struct {
	Ordinal string
	Path    string
}

// PendingExecRequests returns all NNNNN_exec_request.json files in
// sessionFolderPath that don't yet have a matching NNNNN_exec_response.json,
// sorted by ascending ordinal.
func PendingExecRequests(sessionFolderPath string) ([]PendingExecRequest, error) {
	entries, err := os.ReadDir(sessionFolderPath)
	if err != nil {
		return nil, fmt.Errorf("read session folder %s: %w", sessionFolderPath, err)
	}

	var ordinals []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if m := execRequestFilePattern.FindStringSubmatch(e.Name()); m != nil {
			ordinals = append(ordinals, m[1])
		}
	}
	sort.Strings(ordinals)

	var pending []PendingExecRequest
	for _, ord := range ordinals {
		reqPath := filepath.Join(sessionFolderPath, ord+"_exec_request.json")
		if _, err := os.Stat(ExecResponsePathFor(reqPath)); errors.Is(err, fs.ErrNotExist) {
			pending = append(pending, PendingExecRequest{Ordinal: ord, Path: reqPath})
		}
	}
	return pending, nil
}

// ExecResponsePathFor maps a NNNNN_exec_request.json path to its
// NNNNN_exec_response.json path.
func ExecResponsePathFor(execRequestFilePath string) string {
	dir := filepath.Dir(execRequestFilePath)
	name := filepath.Base(execRequestFilePath)
	m := execRequestFilePattern.FindStringSubmatch(name)
	if m == nil {
		return execRequestFilePath
	}
	return filepath.Join(dir, m[1]+"_exec_response.json")
}

// PendingConfigRequest describes a single pending config-request file.
type PendingConfigRequest struct {
	Ordinal string
	Path    string
}

// PendingConfigRequests returns all NNNNN_config_request.json files in
// sessionFolderPath that don't yet have a matching NNNNN_config_response.json,
// sorted by ascending ordinal.
func PendingConfigRequests(sessionFolderPath string) ([]PendingConfigRequest, error) {
	entries, err := os.ReadDir(sessionFolderPath)
	if err != nil {
		return nil, fmt.Errorf("read session folder %s: %w", sessionFolderPath, err)
	}

	var ordinals []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if m := configRequestFilePattern.FindStringSubmatch(e.Name()); m != nil {
			ordinals = append(ordinals, m[1])
		}
	}
	sort.Strings(ordinals)

	var pending []PendingConfigRequest
	for _, ord := range ordinals {
		reqPath := filepath.Join(sessionFolderPath, ord+"_config_request.json")
		if _, err := os.Stat(ConfigResponsePathFor(reqPath)); errors.Is(err, fs.ErrNotExist) {
			pending = append(pending, PendingConfigRequest{Ordinal: ord, Path: reqPath})
		}
	}
	return pending, nil
}

// ConfigResponsePathFor maps a NNNNN_config_request.json path to its
// NNNNN_config_response.json path.
func ConfigResponsePathFor(configRequestFilePath string) string {
	dir := filepath.Dir(configRequestFilePath)
	name := filepath.Base(configRequestFilePath)
	m := configRequestFilePattern.FindStringSubmatch(name)
	if m == nil {
		return configRequestFilePath
	}
	return filepath.Join(dir, m[1]+"_config_response.json")
}

// StatusRequestCounters returns all status-request counters for the given ordinal
// present in sessionFolderPath, sorted in ascending order.
func StatusRequestCounters(sessionFolderPath, ordinal string) ([]int, error) {
	entries, err := os.ReadDir(sessionFolderPath)
	if err != nil {
		return nil, fmt.Errorf("read session folder %s: %w", sessionFolderPath, err)
	}
	var counters []int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := statusRequestFilePattern.FindStringSubmatch(e.Name())
		if m == nil || m[1] != ordinal {
			continue
		}
		c, err := strconv.Atoi(m[2])
		if err != nil {
			continue // should never happen due to regex
		}
		counters = append(counters, c)
	}
	sort.Ints(counters)
	return counters, nil
}

// StatusResponsePathFor returns the path for a status_response file for the given
// ordinal and counter within sessionFolderPath.
func StatusResponsePathFor(sessionFolderPath, ordinal string, counter int) string {
	return filepath.Join(sessionFolderPath, fmt.Sprintf("%s_status_response_%03d.json", ordinal, counter))
}
