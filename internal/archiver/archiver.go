// Package archiver moves idle session folders into the "_archived" folder.
// It runs once, at GoApp startup and before polling begins, so no request is
// in flight while it moves folders.
package archiver

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golan2/claude-code-on-the-road/internal/session"
)

// archivedFolderName is the folder, directly under the watch folder, that
// holds archived sessions. It must match the name the scanner skips.
const archivedFolderName = "_archived"

// Run archives every session folder under watchFolder whose newest activity
// (any request, response or pid file) is older than days days: it is moved to
// watchFolder/_archived/<name>. A folder with no activity files (its age is
// unknown), or whose destination already exists, is left alone and reported
// through logf. An old unanswered request does not protect a folder. It
// returns the names of the folders it archived and logs one summary line when
// any were archived.
func Run(watchFolder string, days int, now time.Time, logf func(format string, args ...any)) []string {
	folders, err := session.DiscoverSessionFolders(watchFolder)
	if err != nil {
		logf("ERROR: archiver: %v", err)
		return nil
	}

	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	archiveRoot := filepath.Join(watchFolder, archivedFolderName)
	var archived []string

	for _, folder := range folders {
		name := filepath.Base(folder)

		latest, ok, err := session.LatestActivity(folder)
		if err != nil {
			logf("ERROR: archiver: %v", err)
			continue
		}
		if !ok {
			logf("archiver: skipped %s - no activity files, age unknown", name)
			continue
		}
		if !latest.Before(cutoff) {
			continue
		}

		dest := filepath.Join(archiveRoot, name)
		if _, err := os.Stat(dest); err == nil {
			logf("archiver: skipped %s - %s already exists", name, filepath.Join(archivedFolderName, name))
			continue
		}
		if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
			logf("ERROR: archiver: create %s: %v", archivedFolderName, err)
			return archived
		}
		if err := os.Rename(folder, dest); err != nil {
			logf("ERROR: archiver: move %s: %v", name, err)
			continue
		}
		archived = append(archived, name)
	}

	if len(archived) > 0 {
		logf("archiver: archived %d (idle > %d days) {%s}", len(archived), days, strings.Join(archived, ","))
	}
	return archived
}
