package session

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMidRequestCounters(t *testing.T) {
	tests := []struct {
		name         string
		files        []string
		queryOrdinal string
		want         []int
	}{
		{
			name: "when_mid_requests_exist_for_ordinal_then_returns_sorted_counters",
			files: []string{
				"00001_mid_request_003.json",
				"00001_mid_request_001.json",
				"00001_mid_request_002.json",
			},
			queryOrdinal: "00001",
			want:         []int{1, 2, 3},
		},
		{
			name: "when_querying_different_ordinal_then_returns_empty",
			files: []string{
				"00001_mid_request_001.json",
			},
			queryOrdinal: "00002",
			want:         []int{},
		},
		{
			name: "when_files_do_not_match_pattern_then_ignored",
			files: []string{
				"00001_mid_request_01.json",
				"00001_mid_request_1000.json",
				"00001_status.json",
				"00001_request.json",
				"00001_response.json",
				"00001_mid_response_001.json",
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

			got, err := MidRequestCounters(dir, tt.queryOrdinal)
			if err != nil {
				t.Fatalf("MidRequestCounters returned error: %v", err)
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

func TestMidResponsePathFor(t *testing.T) {
	dir := "/tmp/session"
	got := MidResponsePathFor(dir, "00001", 7)
	want := filepath.Join(dir, "00001_mid_response_007.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
