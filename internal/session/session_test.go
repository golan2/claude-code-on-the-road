package session

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverSessionFolders(t *testing.T) {
	writeRequest := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "00001_request.json"), []byte("{}"), 0o644); err != nil {
			t.Fatalf("write fixture file in %s: %v", dir, err)
		}
	}

	t.Run("when_normal_session_folder_exists_then_it_is_discovered", func(t *testing.T) {
		root := t.TempDir()
		sessionDir := filepath.Join(root, "my-session")
		writeRequest(t, sessionDir)

		got, err := DiscoverSessionFolders(root)
		if err != nil {
			t.Fatalf("DiscoverSessionFolders returned error: %v", err)
		}
		if !reflect.DeepEqual(got, []string{sessionDir}) {
			t.Fatalf("got %v, want %v", got, []string{sessionDir})
		}
	})

	t.Run("when_session_folder_is_under_root_archived_then_it_is_not_discovered", func(t *testing.T) {
		root := t.TempDir()
		writeRequest(t, filepath.Join(root, "_archived", "old-session"))

		got, err := DiscoverSessionFolders(root)
		if err != nil {
			t.Fatalf("DiscoverSessionFolders returned error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %v, want no folders", got)
		}
	})

	t.Run("when_root_archived_contains_request_file_then_it_is_not_a_session_folder", func(t *testing.T) {
		root := t.TempDir()
		writeRequest(t, filepath.Join(root, "_archived"))

		got, err := DiscoverSessionFolders(root)
		if err != nil {
			t.Fatalf("DiscoverSessionFolders returned error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %v, want no folders", got)
		}
	})

	t.Run("when_archived_is_nested_deeper_then_it_is_treated_normally", func(t *testing.T) {
		root := t.TempDir()
		sessionDir := filepath.Join(root, "group", "sub", "_archived")
		writeRequest(t, sessionDir)

		got, err := DiscoverSessionFolders(root)
		if err != nil {
			t.Fatalf("DiscoverSessionFolders returned error: %v", err)
		}
		if !reflect.DeepEqual(got, []string{sessionDir}) {
			t.Fatalf("got %v, want %v", got, []string{sessionDir})
		}
	})
}

func TestStatusRequestCounters(t *testing.T) {
	tests := []struct {
		name         string
		files        []string
		queryOrdinal string
		want         []int
	}{
		{
			name: "when_status_requests_exist_for_ordinal_then_returns_sorted_counters",
			files: []string{
				"00001_status_request_003.json",
				"00001_status_request_001.json",
				"00001_status_request_002.json",
			},
			queryOrdinal: "00001",
			want:         []int{1, 2, 3},
		},
		{
			name: "when_querying_different_ordinal_then_returns_empty",
			files: []string{
				"00001_status_request_001.json",
			},
			queryOrdinal: "00002",
			want:         []int{},
		},
		{
			name: "when_files_do_not_match_pattern_then_ignored",
			files: []string{
				"00001_status_request_01.json",
				"00001_status_request_1000.json",
				"00001_status.json",
				"00001_request.json",
				"00001_response.json",
				"00001_status_response_001.json",
			},
			queryOrdinal: "00001",
			want:         []int{},
		},
		{
			name:         "when_no_files_then_returns_empty",
			files:        []string{},
			queryOrdinal: "00001",
			want:         []int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o644); err != nil {
					t.Fatalf("write fixture file %s: %v", f, err)
				}
			}

			got, err := StatusRequestCounters(dir, tt.queryOrdinal)
			if err != nil {
				t.Fatalf("StatusRequestCounters returned error: %v", err)
			}
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatusResponsePathFor(t *testing.T) {
	dir := "/tmp/session"
	got := StatusResponsePathFor(dir, "00001", 7)
	want := filepath.Join(dir, "00001_status_response_007.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
