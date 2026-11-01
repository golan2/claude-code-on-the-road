package archiver

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)

	write := func(t *testing.T, path, content string, mtime time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }
	nop := func(string, ...any) {}

	t.Run("when_session_is_idle_then_it_is_zipped_and_folder_removed", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "idle", "00001_cc_request.json"), "{}", old)
		write(t, filepath.Join(root, "idle", "sub", "notes.txt"), "hi", old)
		write(t, filepath.Join(root, "fresh", "00001_cc_request.json"), "{}", now)

		got := Run(root, 30, now, nop)

		if len(got) != 1 || got[0] != "idle" {
			t.Fatalf("archived = %v", got)
		}
		if exists(filepath.Join(root, "idle")) {
			t.Error("idle folder should be removed")
		}
		if !exists(filepath.Join(root, "fresh")) {
			t.Error("fresh folder should stay")
		}
		zr, err := zip.OpenReader(filepath.Join(root, "_archived", "idle.zip"))
		if err != nil {
			t.Fatal(err)
		}
		defer zr.Close()
		names := map[string]bool{}
		for _, f := range zr.File {
			names[f.Name] = true
		}
		if !names["idle/00001_cc_request.json"] || !names["idle/sub/notes.txt"] {
			t.Errorf("zip entries = %v", names)
		}
	})

	t.Run("when_folder_was_moved_to_archived_by_hand_then_it_is_zipped", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "_archived", "manual", "a.json"), "{}", old)

		got := Run(root, 30, now, nop)

		if len(got) != 1 || got[0] != "manual" {
			t.Fatalf("archived = %v", got)
		}
		if exists(filepath.Join(root, "_archived", "manual")) {
			t.Error("manual folder should be removed")
		}
		if !exists(filepath.Join(root, "_archived", "manual.zip")) {
			t.Error("manual.zip missing")
		}
	})

	t.Run("when_zip_already_exists_then_folder_is_kept", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "dup", "a.json"), "{}", old)
		write(t, filepath.Join(root, "_archived", "dup.zip"), "x", old)

		got := Run(root, 30, now, nop)

		if len(got) != 0 {
			t.Fatalf("archived = %v", got)
		}
		if !exists(filepath.Join(root, "dup", "a.json")) {
			t.Error("folder must be kept")
		}
	})

	t.Run("when_run_twice_then_second_run_does_nothing", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "idle", "a.json"), "{}", old)
		Run(root, 30, now, nop)

		if got := Run(root, 30, now, nop); len(got) != 0 {
			t.Fatalf("second run archived %v", got)
		}
	})
}
