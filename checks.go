package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/golan2/claude-code-on-the-road/internal/config"
	"github.com/golan2/claude-code-on-the-road/internal/response"
	"github.com/golan2/claude-code-on-the-road/internal/session"
	"github.com/golan2/claude-code-on-the-road/internal/shellexec"
)

// checkTimeout bounds each pre-flight check.
const checkTimeout = 30 * time.Second

type checkRequestPayload struct {
	Env string `json:"env"`
}

// checkSpec is one check: a shell Command, or a Run function for checks done
// in Go. Run returns the output and whether the check passed.
type checkSpec struct {
	Name    string
	Command string
	Run     func() (string, bool)
	Fix     string
}

type checkResult struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
	Fix      string `json:"fix"`
}

// checkResponse carries the structured results and a Report: the text PC
// relays to Izik as is, one line per check, failures with their fix.
type checkResponse struct {
	Status  string        `json:"status"`
	Report  string        `json:"report"`
	Results []checkResult `json:"results"`
}

// shQuote single-quotes s for a POSIX shell.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// checksFor builds the pre-flight checks for one environment from its config
// entry. The command shapes are fixed here; only the profile, context, VPN
// host and skill paths come from the config. kube runs only when the
// environment has a kubeContext, and frontegg only when it asks for it.
func checksFor(cfg *config.Config, env config.Environment) []checkSpec {
	profile := shQuote(env.AwsProfile)
	specs := []checkSpec{
		{
			Name:    "vpn",
			Command: "curl -sS -m 5 -o /dev/null " + shQuote("https://"+cfg.VpnCheckHost+"/api/v4/version"),
			Fix:     "Connect the VPN",
		},
		{
			Name:    "aws-sso",
			Command: "aws sts get-caller-identity --profile " + profile + " --query Arn --output text",
			Fix:     "Izik runs in a terminal: aws sso login --profile " + env.AwsProfile,
		},
	}
	if env.KubeContext != "" {
		specs = append(specs, checkSpec{
			Name:    "kube",
			Command: "kubectl --context " + shQuote(env.KubeContext) + " --request-timeout=8s auth can-i get pods",
			Fix:     "Check the VPN, then Izik runs: aws sso login --profile " + env.AwsProfile,
		})
	}
	if env.Frontegg {
		specs = append(specs, checkSpec{
			Name: "frontegg",
			Run: func() (string, bool) {
				home, err := os.UserHomeDir()
				if err != nil {
					return err.Error(), false
				}
				out, err := fronteggTokenStatus(filepath.Join(home, ".frontegg"), time.Now())
				if err != nil {
					return err.Error(), false
				}
				return out, true
			},
			Fix: "Izik opens https://internal-proxy.apps.imubit.com/login, clicks Copy and saves the token to ~/.frontegg",
		})
	}
	if len(cfg.SkillPaths) > 0 {
		paths := make([]string, len(cfg.SkillPaths))
		for i, p := range cfg.SkillPaths {
			paths[i] = shQuote(p)
		}
		specs = append(specs, checkSpec{
			Name:    "skill-scripts",
			Command: "bad=$(find " + strings.Join(paths, " ") + " -path '*/scripts/*.sh' ! -perm -u+x 2>/dev/null); if [ -n \"$bad\" ]; then echo \"$bad\"; exit 1; fi; echo all executable",
			Fix:     "chmod +x the scripts listed in the output",
		})
	}
	return specs
}

// runChecks runs every spec in parallel and returns the results in spec order.
func runChecks(specs []checkSpec, ordinal string, timeout time.Duration) []checkResult {
	results := make([]checkResult, len(specs))
	var wg sync.WaitGroup
	for i, spec := range specs {
		results[i] = checkResult{Name: spec.Name, Status: "fail", ExitCode: -1, Fix: spec.Fix}
		wg.Add(1)
		go func(i int, spec checkSpec) {
			defer wg.Done()
			defer recoverPanic(fmt.Sprintf("runCheck[%s][%s]", ordinal, spec.Name))
			if spec.Run != nil {
				out, ok := spec.Run()
				results[i].Output = out
				if ok {
					results[i].Status, results[i].ExitCode = "pass", 0
				}
				return
			}
			result, err := shellexec.Invoke(shellexec.InvokeParams{Command: spec.Command, Workdir: fallbackWorkdir, Timeout: timeout, WaitDelay: 100 * time.Millisecond})
			if err != nil {
				results[i].Output = err.Error()
				return
			}
			results[i].Output = strings.TrimSpace(result.Output)
			results[i].ExitCode = result.ExitCode
			if result.TimedOut {
				results[i].Status = "timeout"
			} else if result.ExitCode == 0 {
				results[i].Status = "pass"
			}
		}(i, spec)
	}
	wg.Wait()
	return results
}

// buildReport renders results as the text PC relays: a header and one line per
// check. A failed line carries the first line of the output and the fix. When
// the VPN check failed, a failed kube line is marked as probably the same cause.
func buildReport(env string, results []checkResult) string {
	failed, vpnDown := 0, false
	for _, r := range results {
		if r.Status != "pass" {
			failed++
			vpnDown = vpnDown || r.Name == "vpn"
		}
	}
	var b strings.Builder
	if failed == 0 {
		fmt.Fprintf(&b, "Pre-flight %s: all %d checks passed", env, len(results))
	} else {
		fmt.Fprintf(&b, "Pre-flight %s: %d of %d checks need attention", env, failed, len(results))
	}
	for _, r := range results {
		if r.Status == "pass" {
			fmt.Fprintf(&b, "\n[ok] %s", r.Name)
			continue
		}
		detail, _, _ := strings.Cut(strings.TrimSpace(r.Output), "\n")
		if len(detail) > 120 {
			detail = detail[:120] + "..."
		}
		fmt.Fprintf(&b, "\n[%s] %s", r.Status, r.Name)
		if detail != "" {
			fmt.Fprintf(&b, ": %s", detail)
		}
		if r.Name == "kube" && vpnDown {
			b.WriteString(" (probably the VPN)")
		}
		if r.Fix != "" {
			fmt.Fprintf(&b, " -> %s", r.Fix)
		}
	}
	return b.String()
}

func processCheckRequest(cfg *config.Config, folder, ordinal, path string) {
	log.Printf("[%s][%s_check][REQ]", filepath.Base(folder), ordinal)
	resp := checkResponse{Status: "pass", Results: []checkResult{}}

	var payload checkRequestPayload
	data, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(data, &payload)
	}
	var env config.Environment
	if err == nil {
		var ok bool
		if env, ok = cfg.Environments[payload.Env]; !ok {
			err = fmt.Errorf("unknown env %q, known: %s", payload.Env, strings.Join(environmentNames(cfg), ", "))
		}
	}
	if err != nil {
		resp.Results = append(resp.Results, checkResult{Name: "request", Status: "fail", ExitCode: -1, Output: err.Error()})
	} else {
		resp.Results = runChecks(checksFor(cfg, env), ordinal, checkTimeout)
	}

	for _, result := range resp.Results {
		if result.Status == "timeout" {
			resp.Status = "timeout"
			break
		}
		if result.Status == "fail" {
			resp.Status = "fail"
		}
	}
	resp.Report = buildReport(payload.Env, resp.Results)
	if err := response.Write(session.CheckResponsePathFor(path), &resp); err != nil {
		log.Printf("ERROR: %v", err)
	}
	log.Printf("[%s][%s_check][RES] [%s] env %q, %d checks", filepath.Base(folder), ordinal, resp.Status, payload.Env, len(resp.Results))
}

func environmentNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Environments))
	for name := range cfg.Environments {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
