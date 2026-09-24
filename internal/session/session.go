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
)

const sessionConfFileName = "__session_conf.json"

var (
	requestFilePattern    = regexp.MustCompile(`^(\d{5})_request\.json$`)
	midRequestFilePattern = regexp.MustCompile(`^(\d{5})_mid_request_(\d{3})\.json$`)
)

// SessionConf holds the per-session metadata stored in __session_conf.json.
type SessionConf struct {
	SessionID      string `json:"sessionId"`
	Workdir        string `json:"workdir"`
	PermissionMode string `json:"permissionMode"`
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

// DiscoverSessionFolders recursively walks root and returns the absolute paths
// of every directory that directly contains at least one NNNNN_request.json file.
// Unreadable directories are skipped silently.
func DiscoverSessionFolders(root string) ([]string, error) {
	var folders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		hasRequest, err := dirHasRequestFile(path)
		if err != nil {
			return nil
		}
		if hasRequest {
			folders = append(folders, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover session folders under %s: %w", root, err)
	}
	return folders, nil
}

func dirHasRequestFile(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !e.IsDir() && requestFilePattern.MatchString(e.Name()) {
			return true, nil
		}
	}
	return false, nil
}

// NextPendingRequest returns the lowest-ordinal NNNNN_request.json in
// sessionFolderPath that has no matching NNNNN_response.json yet.
func NextPendingRequest(sessionFolderPath string) (ordinal string, requestFilePath string, found bool, err error) {
	entries, err := os.ReadDir(sessionFolderPath)
	if err != nil {
		return "", "", false, fmt.Errorf("read session folder %s: %w", sessionFolderPath, err)
	}
	var ordinals []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if m := requestFilePattern.FindStringSubmatch(e.Name()); m != nil {
			ordinals = append(ordinals, m[1])
		}
	}
	sort.Strings(ordinals)
	for _, ord := range ordinals {
		reqPath := filepath.Join(sessionFolderPath, ord+"_request.json")
		if _, err := os.Stat(ResponsePathFor(reqPath)); errors.Is(err, fs.ErrNotExist) {
			return ord, reqPath, true, nil
		}
	}
	return "", "", false, nil
}

// ResponsePathFor maps a NNNNN_request.json path to its NNNNN_response.json path.
func ResponsePathFor(requestFilePath string) string {
	dir := filepath.Dir(requestFilePath)
	name := filepath.Base(requestFilePath)
	m := requestFilePattern.FindStringSubmatch(name)
	if m == nil {
		return requestFilePath
	}
	return filepath.Join(dir, m[1]+"_response.json")
}

// MidRequestCounters returns all mid-request counters for the given ordinal
// present in sessionFolderPath, sorted in ascending order.
func MidRequestCounters(sessionFolderPath, ordinal string) ([]int, error) {
	entries, err := os.ReadDir(sessionFolderPath)
	if err != nil {
		return nil, fmt.Errorf("read session folder %s: %w", sessionFolderPath, err)
	}
	var counters []int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := midRequestFilePattern.FindStringSubmatch(e.Name())
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

// MidResponsePathFor returns the path for a mid_response file for the given
// ordinal and counter within sessionFolderPath.
func MidResponsePathFor(sessionFolderPath, ordinal string, counter int) string {
	return filepath.Join(sessionFolderPath, fmt.Sprintf("%s_mid_response_%03d.json", ordinal, counter))
}
