package skillscopy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSync(t *testing.T) {
	const skillBody = "---\nname: demo\ndisable-model-invocation: true\n---\n# demo\n"

	t.Run("when_source_permissions_change_then_copy_and_state_preserve_mode", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "skills-root")
		src := filepath.Join(root, "demo", "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(src, []byte(skillBody), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := Config{SkillPaths: []string{root}, CopyDir: filepath.Join(t.TempDir(), "copy")}
		dest := filepath.Join(cfg.CopyDir, claudeSkillsSubdir, "skills-root__demo", "SKILL.md")
		logf := func(string, ...any) {}
		for _, mode := range []os.FileMode{0o755, 0o640} {
			if err := os.Chmod(src, mode); err != nil {
				t.Fatal(err)
			}
			if err := Sync(cfg, logf); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(dest)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != mode {
				t.Fatalf("mode = %o, want %o", info.Mode().Perm(), mode)
			}
			st, err := loadState(cfg.CopyDir)
			if err != nil {
				t.Fatal(err)
			}
			if st.Files[src].Mode != mode {
				t.Fatalf("state mode = %o, want %o", st.Files[src].Mode, mode)
			}
		}
	})

	t.Run("when_dest_deleted_then_recopies", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "skills-root")
		skillFile := filepath.Join(root, "demo", "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(skillFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(skillFile, []byte(skillBody), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := Config{SkillPaths: []string{root}, CopyDir: filepath.Join(t.TempDir(), "copy")}
		destFile := filepath.Join(cfg.CopyDir, claudeSkillsSubdir, "skills-root__demo", "SKILL.md")

		var logs []string
		logf := func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		}

		if err := Sync(cfg, logf); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(destFile); err != nil {
			t.Fatalf("first pass: dest not copied: %v", err)
		}

		logs = nil
		if err := Sync(cfg, logf); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(logs, "\n"), "restored") {
			t.Errorf("second pass with dest present: unexpected restore log: %q", logs)
		}

		if err := os.Remove(destFile); err != nil {
			t.Fatal(err)
		}
		logs = nil
		if err := Sync(cfg, logf); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(destFile); err != nil {
			t.Fatalf("third pass after dest deleted: dest not re-copied: %v", err)
		}
		if !strings.Contains(strings.Join(logs, "\n"), "restored 1") {
			t.Errorf("third pass: want a 'restored 1' log line, got %q", logs)
		}
		got, err := os.ReadFile(destFile)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(got), "disable-model-invocation: true") {
			t.Errorf("re-copied SKILL.md still has disable-model-invocation: true")
		}
	})
}
