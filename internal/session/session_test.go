package session

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
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

func TestLatestActivity(t *testing.T) {
	writeAt := func(t *testing.T, dir, name string, modTime time.Time) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatalf("write fixture file %s: %v", name, err)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("when_request_response_and_ack_files_exist_then_the_latest_modtime_wins", func(t *testing.T) {
		dir := t.TempDir()
		writeAt(t, dir, "00001_request.json", base)
		writeAt(t, dir, "00001_ack.json", base.Add(time.Hour))
		writeAt(t, dir, "00001_response.json", base.Add(2*time.Hour))

		got, ok, err := LatestActivity(dir)
		if err != nil {
			t.Fatalf("LatestActivity returned error: %v", err)
		}
		if !ok {
			t.Fatalf("got ok=false, want true")
		}
		if !got.Equal(base.Add(2 * time.Hour)) {
			t.Fatalf("got %v, want %v", got, base.Add(2*time.Hour))
		}
	})

	t.Run("when_exec_and_status_files_exist_then_they_also_count_as_activity", func(t *testing.T) {
		dir := t.TempDir()
		writeAt(t, dir, "00001_request.json", base)
		writeAt(t, dir, "00001_exec_request.json", base.Add(time.Hour))
		writeAt(t, dir, "00001_exec_response.json", base.Add(2*time.Hour))
		writeAt(t, dir, "00001_status_request_001.json", base.Add(3*time.Hour))
		writeAt(t, dir, "00001_status_response_001.json", base.Add(4*time.Hour))

		got, ok, err := LatestActivity(dir)
		if err != nil {
			t.Fatalf("LatestActivity returned error: %v", err)
		}
		if !ok {
			t.Fatalf("got ok=false, want true")
		}
		if !got.Equal(base.Add(4 * time.Hour)) {
			t.Fatalf("got %v, want %v (the status_response file, the latest of all of them)", got, base.Add(4*time.Hour))
		}
	})

	t.Run("when_no_matching_files_then_ok_is_false", func(t *testing.T) {
		dir := t.TempDir()
		writeAt(t, dir, "__session_conf.json", base)

		_, ok, err := LatestActivity(dir)
		if err != nil {
			t.Fatalf("LatestActivity returned error: %v", err)
		}
		if ok {
			t.Fatalf("got ok=true, want false")
		}
	})
}

func TestRequestOrdinals(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"00003_request.json", "00001_request.json", "00002_request.json", "00001_response.json", "00001_exec_request.json"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o644); err != nil {
			t.Fatalf("write fixture file %s: %v", f, err)
		}
	}

	got, err := RequestOrdinals(dir)
	if err != nil {
		t.Fatalf("RequestOrdinals returned error: %v", err)
	}
	want := []string{"00001", "00002", "00003"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFindStuckRequest(t *testing.T) {
	writeFiles := func(t *testing.T, dir string, files []string) {
		t.Helper()
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o644); err != nil {
				t.Fatalf("write fixture file %s: %v", f, err)
			}
		}
	}

	t.Run("when_no_request_files_then_not_found", func(t *testing.T) {
		dir := t.TempDir()

		got, err := FindStuckRequest(dir)
		if err != nil {
			t.Fatalf("FindStuckRequest returned error: %v", err)
		}
		if got.Found || got.Anomaly {
			t.Fatalf("got %+v, want Found=false Anomaly=false", got)
		}
	})

	t.Run("when_all_requests_are_answered_then_not_found", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, []string{"00001_request.json", "00001_response.json", "00002_request.json", "00002_response.json"})

		got, err := FindStuckRequest(dir)
		if err != nil {
			t.Fatalf("FindStuckRequest returned error: %v", err)
		}
		if got.Found || got.Anomaly {
			t.Fatalf("got %+v, want Found=false Anomaly=false", got)
		}
	})

	t.Run("when_highest_ordinal_request_has_no_response_then_it_is_found_stuck", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, []string{"00001_request.json", "00001_response.json", "00001_ack.json", "00002_request.json", "00002_ack.json"})

		got, err := FindStuckRequest(dir)
		if err != nil {
			t.Fatalf("FindStuckRequest returned error: %v", err)
		}
		if !got.Found || got.Anomaly {
			t.Fatalf("got %+v, want Found=true Anomaly=false", got)
		}
		if got.Ordinal != "00002" {
			t.Fatalf("got ordinal %q, want 00002", got.Ordinal)
		}
		if got.RequestFilePath != filepath.Join(dir, "00002_request.json") {
			t.Fatalf("got path %q, want %q", got.RequestFilePath, filepath.Join(dir, "00002_request.json"))
		}
	})

	t.Run("when_a_lower_ordinal_is_also_unanswered_then_it_is_flagged_as_an_anomaly", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, []string{"00001_request.json", "00002_request.json", "00003_request.json"})

		got, err := FindStuckRequest(dir)
		if err != nil {
			t.Fatalf("FindStuckRequest returned error: %v", err)
		}
		if got.Found || !got.Anomaly {
			t.Fatalf("got %+v, want Found=false Anomaly=true", got)
		}
		want := []string{"00001", "00002", "00003"}
		if !reflect.DeepEqual(got.Unanswered, want) {
			t.Fatalf("got Unanswered=%v, want %v", got.Unanswered, want)
		}
	})

	t.Run("when_the_unanswered_ordinal_is_not_the_highest_then_it_is_flagged_as_an_anomaly", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, []string{"00001_request.json", "00002_request.json", "00002_response.json"})

		got, err := FindStuckRequest(dir)
		if err != nil {
			t.Fatalf("FindStuckRequest returned error: %v", err)
		}
		if got.Found || !got.Anomaly {
			t.Fatalf("got %+v, want Found=false Anomaly=true", got)
		}
	})
}

func TestStatusResponsePathFor(t *testing.T) {
	dir := "/tmp/session"
	got := StatusResponsePathFor(dir, "00001", 7)
	want := filepath.Join(dir, "00001_status_response_007.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
