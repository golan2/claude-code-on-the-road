package startupmarker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriter(t *testing.T) {
	startedAt := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	firstBeat := startedAt.Add(5 * time.Second)
	secondBeat := startedAt.Add(10 * time.Second)
	const pid = 4242

	readMarker := func(t *testing.T, path string) Marker {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var m Marker
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("parse marker: %v", err)
		}
		return m
	}

	t.Run("when_written_then_heartbeat_set_and_startup_fields_kept", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), FileName)
		if err := New(path, startedAt, pid).Write(firstBeat); err != nil {
			t.Fatal(err)
		}
		m := readMarker(t, path)
		if m.StartedAt != "2026-10-22T09:00:00Z" {
			t.Errorf("startedAt: got %q", m.StartedAt)
		}
		if m.PID != pid {
			t.Errorf("pid: got %d, want %d", m.PID, pid)
		}
		if m.Heartbeat != "2026-10-22T09:00:05Z" {
			t.Errorf("heartbeat: got %q", m.Heartbeat)
		}
	})

	t.Run("when_written_again_then_only_heartbeat_moves", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), FileName)
		w := New(path, startedAt, pid)
		if err := w.Write(firstBeat); err != nil {
			t.Fatal(err)
		}
		if err := w.Write(secondBeat); err != nil {
			t.Fatal(err)
		}
		m := readMarker(t, path)
		if m.Heartbeat != "2026-10-22T09:00:10Z" {
			t.Errorf("heartbeat: got %q", m.Heartbeat)
		}
		if m.StartedAt != "2026-10-22T09:00:00Z" || m.PID != pid {
			t.Errorf("startup fields changed: startedAt %q, pid %d", m.StartedAt, m.PID)
		}
	})

	t.Run("when_written_then_no_temp_file_left_behind", func(t *testing.T) {
		dir := t.TempDir()
		if err := New(filepath.Join(dir, FileName), startedAt, pid).Write(firstBeat); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != FileName {
			t.Errorf("want only %s in dir, got %d entries", FileName, len(entries))
		}
	})

	t.Run("when_directory_missing_then_error_returned", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing", FileName)
		if err := New(path, startedAt, pid).Write(firstBeat); err == nil {
			t.Error("want an error when the directory does not exist")
		}
	})
}
