# Run a shell command (exec request)

For a raw shell command, with no Claude Code, write `NNNNN_exec_request.json`.
```json
{
  "command": "string, required, one full command line",
  "workdir": "string, optional, absolute path",
  "timeout": "number, optional, minutes",
  "brief": "string, always, at most 7 words"
}
```
`command` runs through a shell, so pipes, `&&`, redirects and globs work.

GoApp answers `NNNNN_exec_response.json`:
- `exitCode`: the real exit code, or `-1` if it timed out or could not start.
- `output`: stdout and stderr merged, in order.
- `truncated`: true if the output was cut at the cap, so the tail is lost.
- `error`: present if it could not start or timed out.

Good to know:
- Exec requests do not wait behind a running Claude Code request in the same session. Different exec requests can run at once. The same number never runs twice.
- The command starts in the session's `workdir` (or `/tmp`) but can go anywhere. It is fully trusted and not sandboxed.
- Same timeout rules as Claude Code requests (see `model-and-timeout.md`).
- A `NNNNN_pid.json` appears as soon as it starts, so it can be cancelled (see `cancel.md`).
- No status requests for exec.

**Shell-only session.** A session can start with `00001_exec_request.json` and never use Claude Code. Add `workdir` if the commands relate to a directory. You can send a Claude Code request into it later. Creating the folder still needs Izik's yes.
