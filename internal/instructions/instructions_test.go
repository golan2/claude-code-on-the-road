package instructions

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	t.Run("when_first_line_is_version_then_parsed", func(t *testing.T) {
		got, ok := ParseVersion([]byte("<!-- version: 41 -->\n# Title\n"))
		if !ok || got != 41 {
			t.Errorf("got %d, %v; want 41, true", got, ok)
		}
	})

	t.Run("when_no_version_line_then_not_ok", func(t *testing.T) {
		if _, ok := ParseVersion([]byte("# Title\n<!-- version: 3 -->\n")); ok {
			t.Error("a version line that is not the first line must not count")
		}
	})

	t.Run("when_version_is_not_a_number_then_not_ok", func(t *testing.T) {
		if _, ok := ParseVersion([]byte("<!-- version: x -->\n")); ok {
			t.Error("want not ok")
		}
	})
}

// writeFiles creates files (name -> content) under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string)
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(data)
	}
	return out
}

func TestSync(t *testing.T) {
	const (
		mainV2 = "<!-- version: 2 -->\n# main\n"
		mainV3 = "<!-- version: 3 -->\n# main\n"
	)

	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	t.Run("when_drive_has_no_folder_then_all_files_are_created", func(t *testing.T) {
		local, watch := t.TempDir(), t.TempDir()
		writeFiles(t, local, map[string]string{MainFile: mainV2, "a.md": "A", "notes.txt": "ignored"})

		Sync(local, watch, logf)

		got := readDir(t, filepath.Join(watch, FolderName))
		if len(got) != 2 || got[MainFile] != mainV2 || got["a.md"] != "A" {
			t.Errorf("got %v; want main + a.md only (txt is not an instruction file)", got)
		}
	})

	t.Run("when_local_is_newer_then_drive_folder_is_replaced_and_dead_files_removed", func(t *testing.T) {
		local, watch := t.TempDir(), t.TempDir()
		writeFiles(t, local, map[string]string{MainFile: mainV3, "a.md": "A2", "b.md": "B"})
		writeFiles(t, filepath.Join(watch, FolderName), map[string]string{MainFile: mainV2, "a.md": "A1", "dead.md": "old"})

		Sync(local, watch, logf)

		got := readDir(t, filepath.Join(watch, FolderName))
		want := map[string]string{MainFile: mainV3, "a.md": "A2", "b.md": "B"}
		if len(got) != len(want) {
			t.Fatalf("got %v; want %v", got, want)
		}
		for name, content := range want {
			if got[name] != content {
				t.Errorf("%s = %q; want %q", name, got[name], content)
			}
		}
		if _, ok := got["dead.md"]; ok {
			t.Error("dead.md must be removed")
		}
	})

	t.Run("when_versions_are_equal_then_nothing_changes", func(t *testing.T) {
		local, watch := t.TempDir(), t.TempDir()
		writeFiles(t, local, map[string]string{MainFile: mainV2, "a.md": "local"})
		writeFiles(t, filepath.Join(watch, FolderName), map[string]string{MainFile: mainV2, "a.md": "drive", "extra.md": "x"})

		Sync(local, watch, logf)

		got := readDir(t, filepath.Join(watch, FolderName))
		if got["a.md"] != "drive" || got["extra.md"] != "x" {
			t.Errorf("an up-to-date Drive folder must be left alone, got %v", got)
		}
	})

	t.Run("when_drive_is_newer_then_it_is_not_overwritten", func(t *testing.T) {
		local, watch := t.TempDir(), t.TempDir()
		writeFiles(t, local, map[string]string{MainFile: mainV2, "a.md": "stale binary"})
		writeFiles(t, filepath.Join(watch, FolderName), map[string]string{MainFile: mainV3, "a.md": "newer"})

		Sync(local, watch, logf)

		got := readDir(t, filepath.Join(watch, FolderName))
		if got[MainFile] != mainV3 || got["a.md"] != "newer" {
			t.Errorf("an older local folder must not overwrite a newer Drive one, got %v", got)
		}
	})

	t.Run("when_local_main_has_no_version_then_sync_is_skipped", func(t *testing.T) {
		local, watch := t.TempDir(), t.TempDir()
		writeFiles(t, local, map[string]string{MainFile: "# no version\n"})
		logs = nil

		Sync(local, watch, logf)

		if _, err := os.Stat(filepath.Join(watch, FolderName)); err == nil {
			t.Error("nothing should be created without a version line")
		}
		if len(logs) == 0 || !strings.Contains(logs[0], "WARNING") {
			t.Errorf("want a WARNING, got %v", logs)
		}
	})

	t.Run("when_drive_folder_has_a_subfolder_then_it_is_kept_and_reported", func(t *testing.T) {
		local, watch := t.TempDir(), t.TempDir()
		writeFiles(t, local, map[string]string{MainFile: mainV3})
		writeFiles(t, filepath.Join(watch, FolderName), map[string]string{MainFile: mainV2})
		if err := os.Mkdir(filepath.Join(watch, FolderName, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		logs = nil

		Sync(local, watch, logf)

		if _, err := os.Stat(filepath.Join(watch, FolderName, "sub")); err != nil {
			t.Error("a sub-folder must be left alone")
		}
		if !strings.Contains(strings.Join(logs, "\n"), "left alone") {
			t.Errorf("want a warning about the sub-folder, got %v", logs)
		}
	})
}

// TestRepoInstructionsAreConsistent checks the real _instructions folder in the
// repo: it has a valid version line, and every .md file the main file names
// exists next to it, so PC is never pointed at a missing file.
func TestRepoInstructionsAreConsistent(t *testing.T) {
	dir := filepath.Join("..", "..", FolderName)
	mainData, err := os.ReadFile(filepath.Join(dir, MainFile))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("when_main_is_read_then_it_has_a_version_line", func(t *testing.T) {
		if _, ok := ParseVersion(mainData); !ok {
			t.Error("the main file must start with <!-- version: N -->")
		}
	})

	t.Run("when_main_names_a_file_then_it_exists", func(t *testing.T) {
		for _, m := range regexp.MustCompile("`([a-z0-9-]+\\.md)`").FindAllStringSubmatch(string(mainData), -1) {
			if _, err := os.Stat(filepath.Join(dir, m[1])); err != nil {
				t.Errorf("%s is named in %s but missing: %v", m[1], MainFile, err)
			}
		}
	})
}
