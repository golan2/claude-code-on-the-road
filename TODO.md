# CCOTR To-Do

## Open
1. TeamPlan repo with jira-team-plan skill auto-fetch (blocked on name decision)
2. Document/extract skills-copy sync feature for team use
3. every time the scanner runs it should update the [goapp_started.json] file with heartbeat field with current time (same format as startedAt).
4. Break the instructions file down into several instruction files, so PC does not load all the minor details into its context at once; keep a short main file that points to the detail files.

## Done
- Process cancellation: PID file and cancel request kill Claude Code process group
- Brief summary in every request, merged into the log line
- Repository list moved to GoApp config
- Per-session "skills" config resolved by removal
- Startup recovery made to match poller
