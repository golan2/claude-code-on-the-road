package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/golan2/claude-code-on-the-road/internal/instructions"
)

func TestDiscoverSessionFolders(t *testing.T) {
	const request = "00001_config_request.json"

	root := t.TempDir()
	mkSession := func(parts ...string) {
		dir := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, request), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkSession("real")
	mkSession(archivedFolderName)
	mkSession(instructions.FolderName)
	mkSession("group", "nested")

	got, err := DiscoverSessionFolders(root)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("when_folder_is_a_direct_child_with_requests_then_it_is_a_session", func(t *testing.T) {
		if len(got) != 1 || filepath.Base(got[0]) != "real" {
			t.Errorf("got %v; want only the direct child \"real\"", got)
		}
	})

	t.Run("when_folder_is_reserved_then_it_is_never_a_session", func(t *testing.T) {
		for _, f := range got {
			if name := filepath.Base(f); name == archivedFolderName || name == instructions.FolderName {
				t.Errorf("reserved folder %q was returned as a session", name)
			}
		}
	})
}
