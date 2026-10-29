// Package instructions keeps PC's instructions folder in the Drive root in
// step with the _instructions folder in the repo. The folder holds a main file
// (instructions.md) and any number of other .md files; its single version line,
// the first line of the main file, covers the whole folder.
package instructions

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// FolderName is the folder name both in the repo (relative to the directory
	// GoApp runs from) and directly under the Drive root. The leading underscore
	// marks it as a reserved folder, like "_archived": it is never a session.
	FolderName = "_instructions"

	// MainFile is the file inside FolderName whose first line carries the version.
	MainFile = "instructions.md"
)

// versionPattern matches the "<!-- version: N -->" line that must be the first
// line of MainFile. The version is plain content metadata maintained by hand:
// whoever changes any file in the folder bumps it by 1.
var versionPattern = regexp.MustCompile(`^<!-- version: (\d+) -->`)

// ParseVersion extracts the version number from the first line of data. ok is
// false when the first line is not a version line.
func ParseVersion(data []byte) (version int, ok bool) {
	firstLine := data
	if i := bytes.IndexByte(data, '\n'); i != -1 {
		firstLine = data[:i]
	}
	m := versionPattern.FindSubmatch(firstLine)
	if m == nil {
		return 0, false
	}
	v, err := strconv.Atoi(string(m[1]))
	if err != nil {
		return 0, false
	}
	return v, true
}

// Sync replaces the Drive instructions folder with the local one when the local
// version is strictly greater than the Drive version (or Drive has none yet).
// Otherwise it does nothing, so an old binary can never overwrite a newer
// folder written by a newer one.
//
// "Replace" means: every local .md file is written into watchFolder/_instructions
// (in place, and only when its content differs, so Drive keeps each file's
// identity and syncs only real changes), and every regular file in that Drive
// folder that no longer exists locally is deleted, so no dead files are left
// behind. MainFile is written last: its version line is the marker, so an
// interrupted sync is simply redone at the next start. A sub-folder found inside
// the Drive folder is reported and left alone.
func Sync(localDir, watchFolder string, logf func(format string, args ...any)) {
	localMain, err := os.ReadFile(filepath.Join(localDir, MainFile))
	if err != nil {
		logf("ERROR: read local %s/%s: %v", localDir, MainFile, err)
		return
	}
	localVersion, ok := ParseVersion(localMain)
	if !ok {
		logf("WARNING: local %s/%s is missing a leading \"<!-- version: N -->\" line; skipping Drive sync", localDir, MainFile)
		return
	}

	driveDir := filepath.Join(watchFolder, FolderName)
	driveMain, err := os.ReadFile(filepath.Join(driveDir, MainFile))
	switch {
	case err == nil:
		if driveVersion, ok := ParseVersion(driveMain); ok && driveVersion >= localVersion {
			logf("Instructions in Drive %s are already up to date (version %d)", FolderName, driveVersion)
			return
		}
	case !errors.Is(err, fs.ErrNotExist):
		logf("ERROR: read drive %s/%s: %v", FolderName, MainFile, err)
		return
	}

	names, err := localMarkdownNames(localDir)
	if err != nil {
		logf("ERROR: list local %s: %v", localDir, err)
		return
	}
	if err := os.MkdirAll(driveDir, 0o755); err != nil {
		logf("ERROR: create drive %s: %v", FolderName, err)
		return
	}

	var written []string
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(localDir, name))
		if err != nil {
			logf("ERROR: read local %s: %v", name, err)
			return
		}
		existing, err := os.ReadFile(filepath.Join(driveDir, name))
		if err == nil && bytes.Equal(existing, data) {
			continue
		}
		if err := os.WriteFile(filepath.Join(driveDir, name), data, 0o644); err != nil {
			logf("ERROR: write drive %s/%s: %v", FolderName, name, err)
			return
		}
		written = append(written, name)
	}

	removed := removeDeadFiles(driveDir, names, logf)

	logf("Synced instructions to Drive %s (version %d): wrote %d, removed %d {%s}",
		FolderName, localVersion, len(written), len(removed), strings.Join(append(written, removed...), ","))
}

// localMarkdownNames returns the regular .md files directly in dir, sorted, with
// MainFile last so Sync writes it last.
func localMarkdownNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".md") && e.Name() != MainFile {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return append(names, MainFile), nil
}

// removeDeadFiles deletes the regular files in driveDir whose names are not in
// keep, and returns their names. A sub-folder is never removed, only reported.
func removeDeadFiles(driveDir string, keep []string, logf func(format string, args ...any)) []string {
	entries, err := os.ReadDir(driveDir)
	if err != nil {
		logf("ERROR: list drive %s: %v", FolderName, err)
		return nil
	}
	wanted := make(map[string]bool, len(keep))
	for _, n := range keep {
		wanted[n] = true
	}
	var removed []string
	for _, e := range entries {
		if wanted[e.Name()] {
			continue
		}
		if e.IsDir() {
			logf("WARNING: Drive %s contains a folder %q that is not part of the instructions; left alone", FolderName, e.Name())
			continue
		}
		if err := os.Remove(filepath.Join(driveDir, e.Name())); err != nil {
			logf("ERROR: remove dead file %s/%s: %v", FolderName, e.Name(), err)
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed
}
