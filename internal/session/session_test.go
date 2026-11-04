package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/golan2/claude-code-on-the-road/internal/instructions"
)

func TestSession_CheckRequests(t *testing.T) {
	const requestName = "00001_check_request.json"
	t.Run("when_only_check_request_then_session_discovered_and_pending", func(t *testing.T) {
		root := t.TempDir()
		folder := filepath.Join(root, "checks")
		if err := os.Mkdir(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(folder, requestName)
		if err := os.WriteFile(path, []byte(`{"names":["vpn"]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		folders, err := DiscoverSessionFolders(root)
		if err != nil || len(folders) != 1 {
			t.Fatalf("folders = %v, error = %v", folders, err)
		}
		pending, err := PendingCheckRequests(folder)
		if err != nil || len(pending) != 1 || pending[0].Ordinal != "00001" {
			t.Fatalf("pending = %v, error = %v", pending, err)
		}
		if _, ok, err := LatestActivity(folder); err != nil || !ok {
			t.Fatalf("activity = %v, error = %v", ok, err)
		}
		if err := os.WriteFile(CheckResponsePathFor(path), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		pending, err = PendingCheckRequests(folder)
		if err != nil || len(pending) != 0 {
			t.Fatalf("pending = %v, error = %v", pending, err)
		}
	})
}

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
