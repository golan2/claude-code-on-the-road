package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golan2/claude-code-on-the-road/internal/config"
	"github.com/golan2/claude-code-on-the-road/internal/session"
)

func TestChecksFor(t *testing.T) {
	cfg := &config.Config{VpnCheckHost: "git.example"}
	stg := config.Environment{AwsProfile: "imu-stage", KubeContext: "ips"}
	names := func(specs []checkSpec) string {
		var n []string
		for _, s := range specs {
			n = append(n, s.Name)
		}
		return strings.Join(n, ",")
	}
	t.Run("when_stg_then_vpn_aws_and_kube_use_its_values", func(t *testing.T) {
		specs := checksFor(cfg, stg)
		if names(specs) != "vpn,aws-sso,kube" {
			t.Fatalf("checks = %s", names(specs))
		}
		if !strings.Contains(specs[0].Command, "https://git.example/api/v4/version") {
			t.Errorf("vpn = %+v", specs[0])
		}
		if !strings.Contains(specs[1].Command, "--profile 'imu-stage'") || !strings.Contains(specs[1].Fix, "aws sso login --profile imu-stage") {
			t.Errorf("aws-sso = %+v", specs[1])
		}
		if !strings.Contains(specs[2].Command, "--context 'ips'") {
			t.Errorf("kube = %+v", specs[2])
		}
	})
	t.Run("when_env_has_no_kube_context_and_frontegg_then_no_kube_but_frontegg", func(t *testing.T) {
		specs := checksFor(cfg, config.Environment{AwsProfile: "ip", Frontegg: true})
		if names(specs) != "vpn,aws-sso,frontegg" {
			t.Fatalf("checks = %s", names(specs))
		}
	})
	t.Run("when_skill_paths_set_then_skill_scripts_check_is_last", func(t *testing.T) {
		specs := checksFor(&config.Config{VpnCheckHost: "h", SkillPaths: []string{"/a b"}}, stg)
		last := specs[len(specs)-1]
		if last.Name != "skill-scripts" || !strings.Contains(last.Command, "'/a b'") {
			t.Fatalf("specs = %+v", specs)
		}
	})
}

func TestRunChecks(t *testing.T) {
	specs := []checkSpec{
		{Name: "ok", Command: "printf ready"},
		{Name: "bad", Command: "printf broken; exit 7", Fix: "repair"},
		{Name: "slow", Command: "sleep 10 & wait"},
		{Name: "native-ok", Run: func() (string, bool) { return "fine", true }},
		{Name: "native-bad", Run: func() (string, bool) { return "nope", false }},
	}
	t.Run("when_checks_pass_fail_and_time_out_then_results_keep_order", func(t *testing.T) {
		start := time.Now()
		got := runChecks(specs, "00001", time.Second)
		if got[0].Status != "pass" || got[0].Output != "ready" ||
			got[1].Status != "fail" || got[1].ExitCode != 7 || got[1].Fix != "repair" ||
			got[2].Status != "timeout" || got[2].ExitCode != -1 ||
			got[3].Status != "pass" || got[3].Output != "fine" ||
			got[4].Status != "fail" || got[4].Output != "nope" {
			t.Fatalf("results = %+v", got)
		}
		if time.Since(start) > 4*time.Second {
			t.Errorf("took %s", time.Since(start))
		}
	})
}

func TestCheckRequest_Process(t *testing.T) {
	cfg := &config.Config{Environments: map[string]config.Environment{"stg": {AwsProfile: "p", KubeContext: "c"}}}
	run := func(t *testing.T, body string) (checkResponse, string) {
		t.Helper()
		folder := t.TempDir()
		path := filepath.Join(folder, "00001_check_request.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		processCheckRequest(cfg, folder, "00001", path)
		data, err := os.ReadFile(session.CheckResponsePathFor(path))
		if err != nil {
			t.Fatal(err)
		}
		var resp checkResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Fatal(err)
		}
		return resp, folder
	}
	t.Run("when_env_unknown_then_fails_listing_known", func(t *testing.T) {
		resp, _ := run(t, `{"env":"prod"}`)
		if resp.Status != "fail" || len(resp.Results) != 1 || !strings.Contains(resp.Results[0].Output, "known: stg") {
			t.Fatalf("response = %+v", resp)
		}
	})
	t.Run("when_env_missing_then_fails", func(t *testing.T) {
		resp, _ := run(t, `{}`)
		if resp.Status != "fail" {
			t.Fatalf("response = %+v", resp)
		}
	})
	t.Run("when_check_ordinal_cancelled_then_rejected", func(t *testing.T) {
		_, folder := run(t, `{"env":"nope"}`)
		if resp := cancelClaudeCode(folder, "00001"); resp.Message != "check requests cannot be cancelled" {
			t.Fatalf("response = %+v", resp)
		}
	})
}

func TestBuildReport(t *testing.T) {
	t.Run("when_all_pass_then_header_and_ok_lines", func(t *testing.T) {
		got := buildReport("stg", []checkResult{{Name: "vpn", Status: "pass"}, {Name: "kube", Status: "pass"}})
		if got != "Pre-flight stg: all 2 checks passed\n[ok] vpn\n[ok] kube" {
			t.Fatalf("report = %q", got)
		}
	})
	t.Run("when_vpn_and_kube_fail_then_kube_is_marked_and_fix_shown", func(t *testing.T) {
		got := buildReport("stg", []checkResult{
			{Name: "vpn", Status: "fail", Output: "curl: (28) timeout", Fix: "Connect the VPN"},
			{Name: "kube", Status: "fail", Output: "Unauthorized\nmore", Fix: "login"},
			{Name: "aws-sso", Status: "pass"},
		})
		for _, want := range []string{"2 of 3 checks need attention", "[fail] vpn: curl: (28) timeout -> Connect the VPN", "[fail] kube: Unauthorized (probably the VPN) -> login", "[ok] aws-sso"} {
			if !strings.Contains(got, want) {
				t.Errorf("report missing %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "more") {
			t.Errorf("only the first output line belongs in the report:\n%s", got)
		}
	})
	t.Run("when_kube_fails_but_vpn_is_up_then_not_marked", func(t *testing.T) {
		got := buildReport("stg", []checkResult{{Name: "vpn", Status: "pass"}, {Name: "kube", Status: "fail", Output: "x"}})
		if strings.Contains(got, "probably the VPN") {
			t.Fatalf("report = %q", got)
		}
	})
}
