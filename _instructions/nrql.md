# Query New Relic (NRQL)

To query New Relic (service logs, errors, latency, traffic), use the `imu-nrql` skill.
1. Find it with `skills.md` (the folder ending `imu-nrql`) and read its `SKILL.md` with exec.
2. Run its prereq check first, once per session: `python3 <skill folder>/scripts/check_prereqs.py`. Continue only if it prints `STATUS: OK`.
3. If it says not configured, never ask Izik to paste the API key and never print it. Tell him to run `newrelic profile add` himself in his own terminal, as the skill describes.
4. Given a correlation ID (a UUID) to trace a request, use the `imu-trace-explorer` skill instead.
