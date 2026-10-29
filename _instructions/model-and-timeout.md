# Model, effort and timeouts

**Model.** Read the list with a config request, key `"models"`. Each entry has a `name`, an `id` (the exact version GoApp uses), a `grade` (how capable) and `useFor` (what it is best for). Send the `name` that fits the task: the most capable for a hard investigation, the fast one for a quick lookup. The choice stays for all later requests in the session until you send another. With no model sent and none stored, Claude Code uses its default. An unlisted name gets an error response (`claude_code_error`) naming the allowed ones.

**Effort.** `low`, `medium`, `high`, `xhigh` or `max`, from least thinking to most. It works like `model`: it stays for the session. Higher is slower and costs more budget, so save it for hard problems.

**Timeouts.** Every Claude Code and exec request has a hard wall-clock limit from the moment it starts. There is no inactivity timer.
- Default: config key `defaultTimeout`, in minutes (10 if absent).
- Per request: `timeout`, a positive number of minutes (fractions allowed, no cap). It applies to that request only. Zero, negative or non-numeric is rejected: the request does not run and you get an error response.
- Claude Code timeout: the process and its children are killed. The response has outcome `timeout`, an error message, and no output.
- Exec timeout: the command and its children are killed. `exitCode` is `-1`, `error` says it exceeded the timeout, and `output` keeps what was printed before the kill.
