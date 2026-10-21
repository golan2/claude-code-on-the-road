# CCOTR To-Do

## Open
1. TeamPlan repo with jira-team-plan skill auto-fetch (blocked on name decision)
2. Make `claudePath` a config.json value (GoApp tests login at startup)
3. Document/extract skills-copy sync feature for team use
4. every time the scanner runs it should update the [goapp_started.json] file with heartbeat field with current time (same format as startedAt).
5. On GoApp start it should guess when it last run by scanning the [goapp_started.json] file as well as the unanswered requests. Then take all the unanswered requests form the last hour since shutdown and answer them.
6. The "running shell command" and "sent to Claude Code" should be replaced with "EXEC" and "CC"
7. The [skillscopy] is reports (logs) the file names instead of the short skill name
8. Skills copy: skip check ignores missing destination files. skillscopy.go:128 skips a file when skills_state.json has a matching source mtime and dest path, without checking that the destination file exists. Files deleted from /tmp/ccotr-skills (likely tmp cleanup, last full copy Oct 13) are never restored: 589 of 610 state entries are dangling, 9 of 17 source skills are missing from the copy (imu-nrql, imu-backend-guide, imu-frontend-guide, imu-gl-grep, imu-go-api-sync, imu-jira-mr, imu-klog, imu-stg-rds-connect, imu-trace-explorer). Fix: add an os.Stat(destFile) check to the skip condition and the stale-entry logic so missing copies are re-copied; consider moving skillsCopyDir out of /tmp. Two commits if both are done.

## Done
- Process cancellation: PID file and cancel request kill Claude Code process group
- Brief summary in every request, merged into the log line
- Repository list moved to GoApp config
- Per-session "skills" config resolved by removal
- Startup recovery made to match poller
