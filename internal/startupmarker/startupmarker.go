// Package startupmarker maintains goapp_started.json, the file GoApp keeps in
// the watch folder root (not in any session folder) so PhoneClaude and the user
// can tell whether GoApp is alive and when it last started. startedAt and pid
// are written once at startup; heartbeat is rewritten on every poll tick.
package startupmarker

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// FileName is the marker's file name inside the watch folder.
const FileName = "goapp_started.json"

// Marker is the on-disk shape of goapp_started.json. Times are RFC 3339.
type Marker struct {
	StartedAt string `json:"startedAt"`
	PID       int    `json:"pid"`
	Heartbeat string `json:"heartbeat,omitempty"`
}

// Writer rewrites one marker file. It keeps startedAt and pid in memory, so a
// heartbeat never has to read the file back. It is not safe for concurrent use:
// GoApp calls it only from the main poll loop.
type Writer struct {
	path   string
	marker Marker
}

// New returns a Writer for the marker at path, recording startedAt and pid.
// It does not touch the file; call Write to create it.
func New(path string, startedAt time.Time, pid int) *Writer {
	return &Writer{
		path:   path,
		marker: Marker{StartedAt: startedAt.Format(time.RFC3339), PID: pid},
	}
}

// Write sets heartbeat to now and replaces the file atomically: it writes a
// temp file beside the marker and renames it over the old one, so a reader
// never sees a partial file. The temp file is removed if the rename fails.
func (w *Writer) Write(now time.Time) error {
	w.marker.Heartbeat = now.Format(time.RFC3339)
	data, err := json.MarshalIndent(w.marker, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", FileName, err)
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp %s: %w", FileName, err)
	}
	if err := os.Rename(tmp, w.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", FileName, err)
	}
	return nil
}
