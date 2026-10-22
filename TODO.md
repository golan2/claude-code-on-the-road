# CCOTR To-Do

## Open
1. TeamPlan repo with jira-team-plan skill auto-fetch (blocked on name decision)
2. Document/extract skills-copy sync feature for team use
3. every time the scanner runs it should update the [goapp_started.json] file with heartbeat field with current time (same format as startedAt).
4. On GoApp start it should guess when it last run by scanning the [goapp_started.json] file as well as the unanswered requests. Then take all the unanswered requests form the last hour since shutdown and answer them.

## Done
- Process cancellation: PID file and cancel request kill Claude Code process group
- Brief summary in every request, merged into the log line
- Repository list moved to GoApp config
- Per-session "skills" config resolved by removal
- Startup recovery made to match poller
