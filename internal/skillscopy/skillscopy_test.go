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
