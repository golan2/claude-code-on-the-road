// Package archiver zips idle session folders into the "_archived" folder.
// It runs once, at GoApp startup and before polling begins, so no request is
// in flight while it works.
package archiver

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
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
// (any request, response or pid file) is older than days days: it is zipped to
// watchFolder/_archived/<name>.zip and the folder is removed. A folder with no
// activity files (its age is unknown), or whose zip already exists, is left
// alone and reported through logf. An old unanswered request does not protect
// a folder. It then zips any plain folder found inside _archived (a session
// moved there by hand). It returns the names it archived and logs one summary
// line when any were archived.
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

		if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
			logf("ERROR: archiver: create %s: %v", archivedFolderName, err)
			return archived
		}
		if err := zipAndRemove(folder, archiveRoot); err != nil {
			logf("archiver: skipped %s - %v", name, err)
			continue
		}
		archived = append(archived, name)
	}

	if len(archived) > 0 {
		logf("archiver: archived %d (idle > %d days) {%s}", len(archived), days, strings.Join(archived, ","))
	}

	// Plain folders already inside _archived were moved there by hand.
	var zipped []string
	entries, _ := os.ReadDir(archiveRoot)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := zipAndRemove(filepath.Join(archiveRoot, e.Name()), archiveRoot); err != nil {
			logf("archiver: skipped %s/%s - %v", archivedFolderName, e.Name(), err)
			continue
		}
		zipped = append(zipped, e.Name())
	}
	if len(zipped) > 0 {
		logf("archiver: zipped %d unzipped folders in %s {%s}", len(zipped), archivedFolderName, strings.Join(zipped, ","))
	}
	return append(archived, zipped...)
}

// zipAndRemove writes folder to destDir/<name>.zip (entries under "<name>/",
// so unzipping into the watch folder restores the session) and then removes
// the folder. The zip is built under a temporary name and renamed only when
// complete, and the folder is removed only after that, so a failure never
// loses data. It fails if the zip already exists.
func zipAndRemove(folder, destDir string) error {
	name := filepath.Base(folder)
	dest := filepath.Join(destDir, name+".zip")
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists", filepath.Join(archivedFolderName, name+".zip"))
	}
	tmp := dest + ".tmp"
	if err := writeZip(folder, tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("zip: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("zip: %w", err)
	}
	if err := os.RemoveAll(folder); err != nil {
		return fmt.Errorf("zipped but could not remove folder: %w", err)
	}
	return nil
}

func writeZip(folder, zipPath string) (err error) {
	out, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	zw := zip.NewWriter(out)
	base := filepath.Dir(folder)
	walkErr := filepath.WalkDir(folder, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		} else {
			hdr.Method = zip.Deflate
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil || d.IsDir() {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
	if walkErr != nil {
		zw.Close()
		return walkErr
	}
	return zw.Close()
}
