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

func TestAckPathFor(t *testing.T) {
	dir := "/tmp/session"
	got := AckPathFor(dir, "00001")
	want := filepath.Join(dir, "00001_ack.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExecResponsePathFor(t *testing.T) {
	dir := "/tmp/session"
	got := ExecResponsePathFor(filepath.Join(dir, "00001_exec_request.json"))
	want := filepath.Join(dir, "00001_exec_response.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPendingExecRequests(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  []PendingExecRequest
	}{
		{
			name: "when_exec_request_has_no_matching_response_then_it_is_returned",
			files: []string{
				"00001_exec_request.json",
			},
			want: []PendingExecRequest{
				{Ordinal: "00001", Path: "00001_exec_request.json"},
			},
		},
		{
			name: "when_exec_request_has_matching_response_then_it_is_not_returned",
			files: []string{
				"00001_exec_request.json",
				"00001_exec_response.json",
			},
			want: []PendingExecRequest{},
		},
		{
			name: "when_multiple_pending_exec_requests_then_returned_sorted_by_ordinal",
			files: []string{
				"00003_exec_request.json",
				"00001_exec_request.json",
				"00002_exec_request.json",
			},
			want: []PendingExecRequest{
				{Ordinal: "00001", Path: "00001_exec_request.json"},
				{Ordinal: "00002", Path: "00002_exec_request.json"},
				{Ordinal: "00003", Path: "00003_exec_request.json"},
			},
		},
		{
			name: "when_only_regular_request_files_exist_then_returns_empty",
			files: []string{
				"00001_request.json",
				"00001_response.json",
				"00002_request.json",
			},
			want: []PendingExecRequest{},
		},
		{
			name:  "when_no_files_then_returns_empty",
			files: []string{},
			want:  []PendingExecRequest{},
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

			got, err := PendingExecRequests(dir)
			if err != nil {
				t.Fatalf("PendingExecRequests returned error: %v", err)
			}

			want := make([]PendingExecRequest, len(tt.want))
			copy(want, tt.want)
			for i := range want {
				want[i].Path = filepath.Join(dir, want[i].Path)
			}

			if len(got) == 0 && len(want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v, want %v", got, want)
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
