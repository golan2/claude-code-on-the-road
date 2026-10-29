# Cancel a running request

Write `NNNNN_cancel_request.json` with `{}`, using the number of the request to stop. It works for Claude Code and exec requests, and it takes no new number. Cancelling is a hard kill: GoApp sends SIGKILL to the process and everything it started. Work already done stays on disk. GoApp answers `NNNNN_cancel_response.json`.
- `{"status":"success","pid":N}`: killed. The cancelled request then gets its own response with outcome `claude_code_error`.
- `{"status":"error","message":"..."}`: nothing killed.
  - `PID file not found`: it never launched, or the number is wrong.
  - `request already finished`: nothing to stop.
  - `PID file is invalid` or `read PID file: ...`: report it to Izik.
  - `kill -9 -<pid> failed`: usually it just exited, so check the response.

A cancel request gets one response and is never reprocessed. To retry after an error, do not resend the file: report to Izik.
